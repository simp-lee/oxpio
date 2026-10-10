package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
)

func TestReadPreservesOriginalBytesAndHash(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\ntags: [Public]\n---\nBody\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Read(snapshot, "article.md", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == "" || result.Hash == "" || result.TotalLines != 7 || !result.EOF {
		t.Fatalf("read result = %#v", result)
	}
}

func TestReadRejectsHiddenSourceWithoutLeakingItsIdentity(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeQueryFile(t, vault, "private/article.md", "---\ntitle: Secret\npublish: true\ntype: doc\ntags: [Secret]\n---\nsecret\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Read(snapshot, "private/article.md", false, 0, 0)
	if !errors.Is(err, internalapplication.ErrHiddenSource) {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestFindRejectsUnsafeFiltersAndProtectsHiddenPaths(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Find(snapshot, false, "../", "", "", nil); err == nil {
		t.Fatal("Find() accepted traversal path")
	}
	if _, err := Find(snapshot, false, "", "unknown", "", nil); err == nil {
		t.Fatal("Find() accepted unknown kind")
	}
	if _, err := Find(snapshot, false, "private", "", "", nil); !errors.Is(err, internalapplication.ErrHiddenSource) {
		t.Fatalf("Find() hidden path error = %v", err)
	}
}

func TestTagsHonorUnpublishedScope(t *testing.T) {
	vault := t.TempDir()
	writeQueryFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeQueryFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeQueryFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\ntags: [Public]\n---\n")
	writeQueryFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeQueryFile(t, vault, "private/article.md", "---\ntitle: Secret\npublish: true\ntype: doc\ntags: [Secret]\n---\n")
	snapshot, err := internalapplication.OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	public := Tags(snapshot, false)
	if len(public) != 1 || public[0].Name != "public" {
		t.Fatalf("public tags = %#v", public)
	}
	all := Tags(snapshot, true)
	if len(all) != 2 || all[0].Name != "public" || all[1].Name != "secret" {
		t.Fatalf("all tags = %#v", all)
	}
}

func writeQueryFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	writeApplicationFileForAgent(t, root, relPath, content)
}

func writeApplicationFileForAgent(t *testing.T, root, relPath, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
