package edit

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalbuild "github.com/simp-lee/obsite/internal/build"
)

func TestFileManagerOperationsUseCASAndCandidateBuilds(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nArticle\n")
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	beforeOutput, err := os.ReadFile(filepath.Join(output, "article", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CreateMarkdown("broken.md", AbsentSourceHash, []byte("---\ntitle: Broken\npublish: true\ntype: invalid\n---\nBroken\n")); err == nil {
		t.Fatal("invalid Markdown create succeeded")
	}
	if _, err := os.Stat(filepath.Join(vault, "broken.md")); !os.IsNotExist(err) {
		t.Fatalf("failed create stat = %v", err)
	}
	if afterOutput, err := os.ReadFile(filepath.Join(output, "article", "index.html")); err != nil || string(afterOutput) != string(beforeOutput) {
		t.Fatalf("failed create changed output: err=%v", err)
	}

	folder, err := coordinator.CreateFolder("docs", AbsentSourceHash)
	if err != nil {
		t.Fatal(err)
	}
	if folder.SourceHash == AbsentSourceHash {
		t.Fatal("created folder returned absent hash")
	}
	section, err := NewSectionSource("Docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CreateMarkdown("docs/_index.md", AbsentSourceHash, section); err != nil {
		t.Fatal(err)
	}
	article, err := NewArticleSource("Guide", "doc", "")
	if err != nil {
		t.Fatal(err)
	}
	created, err := coordinator.CreateMarkdown("docs/guide.md", AbsentSourceHash, article)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RenamePath("docs/guide.md", "docs/renamed.md", "stale"); err == nil {
		t.Fatal("stale rename succeeded")
	} else {
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale rename error = %v, want ConflictError", err)
		}
	}
	if _, err := coordinator.RenamePath("docs/guide.md", "docs/renamed.md", created.SourceHash); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.DeletePath("docs/renamed.md", created.SourceHash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "docs", "renamed.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted file stat = %v", err)
	}

	state, err := inspectFileManagerState(vault, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.DeletePath("docs", state.hash); err == nil || !strings.Contains(err.Error(), "section sources") {
		t.Fatalf("section-containing folder delete error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "docs", "_index.md")); err != nil {
		t.Fatalf("section source was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || !strings.Contains(string(got), "Article") {
		t.Fatalf("unrelated source changed: %q, err=%v", got, err)
	}
}

func TestUploadFileUsesExistingFolderAndAbsentCAS(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.UploadFile("docs/pixel.png", AbsentSourceHash, png)
	if err != nil {
		t.Fatal(err)
	}
	if result.RelPath != "docs/pixel.png" || result.SourceHash == AbsentSourceHash {
		t.Fatalf("upload result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(vault, "docs", "pixel.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.UploadFile("docs/pixel.png", AbsentSourceHash, png); err == nil {
		t.Fatal("duplicate upload succeeded")
	}
	if _, err := coordinator.UploadFile("missing/pixel.png", AbsentSourceHash, png); err == nil {
		t.Fatal("upload created a missing parent folder")
	}
	if _, err := coordinator.UploadFile("docs/bad.svg", AbsentSourceHash, []byte(`<svg><image href="https://example.test/x"/></svg>`)); err == nil {
		t.Fatal("unsafe SVG upload succeeded")
	}
	markdown := []byte("---\ntitle: Uploaded\npublish: false\ntype: doc\n---\nUploaded\n")
	if _, err := coordinator.UploadFile("docs/uploaded.md", AbsentSourceHash, markdown); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.UploadFile("docs/invalid.md", AbsentSourceHash, []byte{0xff}); err == nil {
		t.Fatal("invalid UTF-8 Markdown upload succeeded")
	}
}

func TestFileManagerCandidateExcludesFormalOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "public/stale.md", "not a source document")

	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CreateFolder("docs", AbsentSourceHash); err != nil {
		t.Fatalf("CreateFolder() error = %v", err)
	}
}

func TestCreatedPathRollbackRemovesOnlyCreatedIdentity(t *testing.T) {
	vault := t.TempDir()
	filename := filepath.Join(vault, "created.md")
	if err := createFileNoReplace(filename, []byte("created")); err != nil {
		t.Fatal(err)
	}
	state, err := inspectFileManagerState(vault, "created.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := removeCreatedPath(vault, "created.md", state.hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filename); !os.IsNotExist(err) {
		t.Fatalf("created path remains after rollback: %v", err)
	}
	entries, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rollback left temporary entries: %#v", entries)
	}
}

func TestRenameRollbackPreservesExternalDestinationAndSource(t *testing.T) {
	vault := t.TempDir()
	source := filepath.Join(vault, "source.md")
	if err := os.WriteFile(source, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := inspectFileManagerState(vault, "source.md")
	if err != nil {
		t.Fatal(err)
	}
	mutation := fileManagerMutation{operation: fileMutationRename, path: "source.md", destination: "renamed.md"}
	rollback, finalize, expected, err := applyFileManagerMutation(vault, mutation, current)
	if err != nil {
		t.Fatal(err)
	}
	if expected.hash != current.hash {
		t.Fatalf("rename hash = %q, want %q", expected.hash, current.hash)
	}
	if err := os.WriteFile(filepath.Join(vault, "renamed.md"), []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err == nil {
		t.Fatal("rollback unexpectedly ignored external destination")
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "original" {
		t.Fatalf("source after rollback = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "renamed.md")); err != nil || string(got) != "external" {
		t.Fatalf("external destination after rollback = %q, err=%v", got, err)
	}
	if err := finalize(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRollbackCleansDisplacedSourceOnExternalReplacement(t *testing.T) {
	vault := t.TempDir()
	source := filepath.Join(vault, "source.md")
	if err := os.WriteFile(source, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := inspectFileManagerState(vault, "source.md")
	if err != nil {
		t.Fatal(err)
	}
	mutation := fileManagerMutation{operation: fileMutationDelete, path: "source.md"}
	rollback, _, _, err := applyFileManagerMutation(vault, mutation, current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rollback(); err == nil {
		t.Fatal("rollback unexpectedly ignored external replacement")
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "external" {
		t.Fatalf("external source after rollback = %q, err=%v", got, err)
	}
	entries, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".obsite-displaced-") {
			t.Fatalf("displaced source leaked: %s", entry.Name())
		}
	}
}

func TestRenamePathNoReplacePreservesDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("destination"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renamePathNoReplace(source, destination); err == nil {
		t.Fatal("renamePathNoReplace replaced an existing destination")
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "destination" {
		t.Fatalf("destination = %q, err=%v", got, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source stat = %v", err)
	}
}

func TestValidateManagedPathsRejectReservedAndTraversal(t *testing.T) {
	for _, pathValue := range []string{"../escape.md", "/absolute.md", ".obsite/theme.css", "obsite.yaml", "OBSITE.YAML", "node_modules/pkg", "NODE_MODULES/pkg", "docs/../guide.md"} {
		if err := validateManagedRelPath(pathValue); err == nil {
			t.Fatalf("validateManagedRelPath(%q) succeeded", pathValue)
		}
	}
	if err := validateManagedMarkdownPath("docs/readme.txt"); err == nil {
		t.Fatal("non-Markdown path accepted")
	}
}
