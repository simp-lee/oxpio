package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotSharesNormalizedCatalogAndHiddenReadBoundary(t *testing.T) {
	vault := t.TempDir()
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeApplicationFile(t, vault, "hidden/_index.md", "---\ntitle: Hidden\npublish: false\n---\n")
	writeApplicationFile(t, vault, "hidden/draft.md", "---\ntitle: Draft\npublish: true\ntype: doc\ntags: [secret]\n---\nsecret\n")
	writeApplicationFile(t, vault, "hidden/assets/private.txt", "private")

	snapshot, err := OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatalf("OpenSnapshot() error = %v", err)
	}
	if got := len(snapshot.Entries(false)); got != 1 {
		t.Fatalf("public entry count = %d, want 1", got)
	}
	if _, err := snapshot.Entry("hidden/draft.md", false); !errors.Is(err, ErrHiddenSource) {
		t.Fatalf("hidden Entry() error = %v, want ErrHiddenSource", err)
	}
	if entry, data, err := snapshot.ReadSource("hidden/draft.md", true); err != nil || entry.Title != "Draft" || string(data) == "" {
		t.Fatalf("authorized ReadSource() = %#v, %q, %v", entry, data, err)
	}
	if _, err := snapshot.ListDirectory(".", false); err != nil {
		t.Fatalf("root ListDirectory() error = %v", err)
	}
	if _, err := snapshot.ListDirectory("hidden/assets", false); !errors.Is(err, ErrHiddenSource) {
		t.Fatalf("hidden directory ListDirectory() error = %v, want ErrHiddenSource", err)
	}
	items, err := snapshot.ListDirectory("hidden/assets", true)
	if err != nil || len(items) != 1 || items[0].Path != "hidden/assets/private.txt" {
		t.Fatalf("authorized hidden directory = %#v, %v", items, err)
	}
	publicContext, err := LoadContext(vault, "", false)
	if err != nil || publicContext.Sources != 1 || publicContext.Sections != 1 || publicContext.Vault == "" {
		t.Fatalf("public context = %#v, %v", publicContext, err)
	}
	allContext, err := LoadContext(vault, "", true)
	if err != nil || allContext.Sources != 3 || allContext.Sections != 2 {
		t.Fatalf("unpublished context = %#v, %v", allContext, err)
	}
}

func TestOpenSnapshotDoesNotExposeMalformedSourcePath(t *testing.T) {
	vault := t.TempDir()
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeApplicationFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeApplicationFile(t, vault, "private/draft.md", "not frontmatter\n")
	_, err := OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err == nil || strings.Contains(err.Error(), "private/draft.md") || strings.Contains(err.Error(), vault) {
		t.Fatalf("OpenSnapshot() error = %v", err)
	}
}

func writeApplicationFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
