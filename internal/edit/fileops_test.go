package edit

import (
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
	if _, err := coordinator.DeletePath("docs", state.hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "docs")); !os.IsNotExist(err) {
		t.Fatalf("deleted folder stat = %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || !strings.Contains(string(got), "Article") {
		t.Fatalf("unrelated source changed: %q, err=%v", got, err)
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
