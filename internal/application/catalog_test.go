package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestCatalogResolvesExactSourceAndPublicRouteIdentities(t *testing.T) {
	raw := []byte("---\ntitle: 文章\npublish: true\ntype: doc\n---\nbody\n")
	catalog := NewCatalog(&model.SourceCatalog{BasePath: "/docs/", Entries: []model.SourceCatalogEntry{
		{RelPath: "guide/文章.md", Route: "/guide/%E6%96%87%E7%AB%A0/", Title: "文章", Kind: "article", Type: "doc", Publish: true, EffectivePublish: true, SectionPath: "guide", VersionID: "v2", RawSource: raw},
		{RelPath: "guide/draft.md", Route: "", Title: "Draft", Kind: "article", Type: "doc", Publish: false, EffectivePublish: false, SectionPath: "guide", VersionID: "v2", RawSource: []byte("draft")},
	}})
	entry, err := catalog.EntryByRelPath("guide/文章.md", false)
	if err != nil || entry.RelPath != "guide/文章.md" || entry.VersionID != "v2" || string(entry.RawSource) != string(raw) {
		t.Fatalf("published source lookup = %#v, %v", entry, err)
	}
	entry.RawSource[0] = 'X'
	fresh, err := catalog.EntryByRelPath("guide/文章.md", false)
	if err != nil || string(fresh.RawSource) != string(raw) {
		t.Fatalf("catalog source bytes were aliased: %#v, %v", fresh, err)
	}
	if _, err := catalog.EntryByRelPath("guide/draft.md", false); !errors.Is(err, ErrHiddenSource) {
		t.Fatalf("hidden source lookup = %v, want ErrHiddenSource", err)
	}
	mapped, ok := catalog.EntryForPublicRequest("/docs/guide/%E6%96%87%E7%AB%A0/")
	if !ok || mapped.RelPath != "guide/文章.md" {
		t.Fatalf("base-path route mapping = %#v, %v", mapped, ok)
	}
	mapped, ok = catalog.EntryForPublicRequest("/docs/guide/%e6%96%87%e7%ab%a0/")
	if !ok || mapped.RelPath != "guide/文章.md" {
		t.Fatalf("non-canonical escaped route mapping = %#v, %v", mapped, ok)
	}
	if _, ok := catalog.EntryForPublicRequest("/docs/guide/draft/"); ok {
		t.Fatal("hidden source unexpectedly mapped to a public route")
	}
}

func TestListDirectoryHidesCaseVariantManagementPaths(t *testing.T) {
	vault := t.TempDir()
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeApplicationFile(t, vault, ".OXPIO/secret.txt", "secret")
	snapshot, err := OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := snapshot.ListDirectory(".", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Path, ".OXPIO/secret.txt") {
			t.Fatalf("case-variant management entry exposed: %#v", entries)
		}
	}
}

func TestListDirectoryHidesExistingOutputDirectory(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "site-output")
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	writeApplicationFile(t, vault, "site-output/index.html", "output")

	snapshot, err := OpenSnapshot(vault, output)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := snapshot.ListDirectory(".", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Path == "site-output" {
			t.Fatalf("existing output directory exposed: %#v", entries)
		}
	}
}

func TestListDirectoryPreservesLeadingSpacePathIdentity(t *testing.T) {
	vault := t.TempDir()
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeApplicationFile(t, vault, " guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n")
	snapshot, err := OpenSnapshot(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ListDirectory(" guide", false); err != nil {
		t.Fatalf("ListDirectory() error = %v, want leading-space directory identity", err)
	}
}

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
	original, err := os.ReadFile(filepath.Join(vault, "hidden/draft.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "hidden/draft.md"), []byte("external change"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, snapshotData, err := snapshot.ReadSource("hidden/draft.md", true)
	if err != nil || string(snapshotData) != string(original) {
		t.Fatalf("snapshot source = %q, %v", snapshotData, err)
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
