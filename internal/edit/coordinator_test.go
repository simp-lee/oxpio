package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	internalbuild "github.com/simp-lee/obsite/internal/build"
	internalfsutil "github.com/simp-lee/obsite/internal/fsutil"
)

func TestCoordinatorPreservesCASAndPublishesSourceAndOutputTogether(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeEditFile(t, vault, "article.md", original)
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}

	oldHash := sourceHash([]byte(original))
	updated := "---\ntitle: Article\npublish: true\ntype: doc\n---\nUpdated\n"
	result, err := coordinator.Save("article.md", oldHash, []byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceHash != sourceHash([]byte(updated)) {
		t.Fatalf("source hash = %q", result.SourceHash)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "article.md")); err != nil || string(got) != updated {
		t.Fatalf("saved source = %q, err=%v", got, err)
	}
	page, err := os.ReadFile(filepath.Join(output, "article", "index.html"))
	if err != nil || !strings.Contains(string(page), "Updated") {
		t.Fatalf("published page = %q, err=%v", page, err)
	}

	beforeSource, err := os.ReadFile(filepath.Join(vault, "article.md"))
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotEditOutput(t, output)
	if _, err := coordinator.Save("article.md", sourceHash([]byte(updated)), []byte("---\ntitle: Article\npublish: true\ntype: invalid\n---\nRejected\n")); err == nil {
		t.Fatal("invalid save error = nil")
	}
	if afterSource, readErr := os.ReadFile(filepath.Join(vault, "article.md")); readErr != nil || string(afterSource) != string(beforeSource) {
		t.Fatalf("invalid save changed source: %q, err=%v", afterSource, readErr)
	}
	if after := snapshotEditOutput(t, output); !reflect.DeepEqual(after, before) {
		t.Fatal("invalid save changed formal output")
	}
	if _, err := coordinator.Save("article.md", oldHash, []byte("---\ntitle: Article\npublish: true\ntype: doc\n---\nLost\n")); err == nil {
		t.Fatal("stale save error = nil")
	} else {
		var conflict *ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale save error = %v, want ConflictError", err)
		}
	}
	if after := snapshotEditOutput(t, output); !reflect.DeepEqual(after, before) {
		t.Fatal("stale save changed formal output")
	}

	newSource, err := NewArticleSource("Draft", "doc", "")
	if err != nil {
		t.Fatal(err)
	}
	created, err := coordinator.Create("draft.md", AbsentSourceHash, newSource)
	if err != nil {
		t.Fatal(err)
	}
	if created.SourceHash == AbsentSourceHash {
		t.Fatal("create returned absent source hash")
	}
	if got, err := os.ReadFile(filepath.Join(vault, "draft.md")); err != nil || string(got) != string(newSource) {
		t.Fatalf("created source = %q, err=%v", got, err)
	}

	deleted, err := coordinator.Delete("draft.md", created.SourceHash)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.SourceHash != AbsentSourceHash {
		t.Fatalf("delete source hash = %q", deleted.SourceHash)
	}
	if _, err := os.Stat(filepath.Join(vault, "draft.md")); !os.IsNotExist(err) {
		t.Fatalf("deleted source stat error = %v", err)
	}
}

func TestCoordinatorCandidateExcludesFormalOutput(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeEditFile(t, vault, "article.md", original)
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeEditFile(t, vault, "public/stale.md", "not a source document")

	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	updated := "---\ntitle: Article\npublish: true\ntype: doc\n---\nUpdated\n"
	if _, err := coordinator.Save("article.md", sourceHash([]byte(original)), []byte(updated)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
}

func TestCoordinatorEditsScannerAcceptedUppercaseMarkdownRelPath(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeEditFile(t, vault, "Article.MD", original)

	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range built.Catalog.Entries {
		if entry.RelPath == "Article.MD" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("catalog does not contain scanner path Article.MD: %#v", built.Catalog.Entries)
	}

	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	updated := "---\ntitle: Article\npublish: true\ntype: doc\n---\nUpdated\n"
	result, err := coordinator.Save("Article.MD", sourceHash([]byte(original)), []byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceHash != sourceHash([]byte(updated)) {
		t.Fatalf("saved source hash = %q", result.SourceHash)
	}
	if got, err := os.ReadFile(filepath.Join(vault, "Article.MD")); err != nil || string(got) != updated {
		t.Fatalf("saved uppercase source = %q, err=%v", got, err)
	}

	result, err = coordinator.Delete("Article.MD", result.SourceHash)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceHash != AbsentSourceHash {
		t.Fatalf("deleted source hash = %q", result.SourceHash)
	}
	if _, err := os.Stat(filepath.Join(vault, "Article.MD")); !os.IsNotExist(err) {
		t.Fatalf("deleted uppercase source stat error = %v", err)
	}
}

func TestCoordinatorKeepsCommittedOutputWhenBackupCleanupFails(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "obsite.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	original := "---\ntitle: Article\npublish: true\ntype: doc\n---\nOriginal\n"
	writeEditFile(t, vault, "article.md", original)
	built, err := internalbuild.BuildWithOptions(vault, output, internalbuild.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}

	cleanupFailure := errors.New("simulated partial backup cleanup failure")
	originalRemove := editOutputRemoveAll
	editOutputRemoveAll = func(name string) error {
		if strings.Contains(filepath.Base(name), ".obsite-output-backup-") {
			if err := os.RemoveAll(name); err != nil {
				return err
			}
			return cleanupFailure
		}
		return originalRemove(name)
	}
	defer func() { editOutputRemoveAll = originalRemove }()

	updated := "---\ntitle: Article\npublish: true\ntype: doc\n---\nUpdated\n"
	result, err := coordinator.Save("article.md", sourceHash([]byte(original)), []byte(updated))
	if err != nil {
		t.Fatal(err)
	}
	if result.Build == nil || !errors.Is(result.Build.OutputCleanupError, cleanupFailure) {
		t.Fatalf("build cleanup error = %#v, want %v", result.Build, cleanupFailure)
	}
	if got, err := os.ReadFile(filepath.Join(output, "article", "index.html")); err != nil || !strings.Contains(string(got), "Updated") {
		t.Fatalf("committed output = %q, err=%v", got, err)
	}
}

func sourceHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func snapshotEditOutput(t *testing.T, root string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		_, data, _, readErr := internalfsutil.ReadContainedRegularFile(root, rel)
		if readErr != nil {
			return readErr
		}
		result[filepath.ToSlash(rel)] = data
		return nil
	}); err != nil {
		t.Fatalf("walk edit output: %v", err)
	}
	return result
}
