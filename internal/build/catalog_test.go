package build

import (
	"path/filepath"
	"testing"
)

func TestBuildResultExposesExactPublishedAndDraftSourceCatalog(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/base/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n")
	writeStrictFile(t, vault, "guide/article.md", "---\ntitle: Article\npublish: true\ntype: doc\nslug: custom\n---\nArticle\n")
	writeStrictFile(t, vault, "guide/draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\nDraft\n")

	result, err := BuildWithOptions(vault, output, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Catalog == nil || result.Catalog.BasePath != "/base/" || len(result.Catalog.Entries) != 4 {
		t.Fatalf("catalog = %#v", result.Catalog)
	}
	entries := make(map[string]struct {
		route, kind, title string
		publish, effective bool
	})
	for _, entry := range result.Catalog.Entries {
		entries[entry.RelPath] = struct {
			route, kind, title string
			publish, effective bool
		}{entry.Route, entry.Kind, entry.Title, entry.Publish, entry.EffectivePublish}
	}
	if got := entries["guide/article.md"]; got.route != "/guide/custom/" || got.kind != "article" || !got.publish || !got.effective {
		t.Fatalf("published article catalog entry = %#v", got)
	}
	if got := entries["guide/draft.md"]; got.route != "" || got.kind != "article" || got.publish || got.effective {
		t.Fatalf("draft catalog entry = %#v", got)
	}
	if got := entries["guide/_index.md"]; got.route != "/guide/" || got.kind != "section" || !got.publish || !got.effective {
		t.Fatalf("section catalog entry = %#v", got)
	}
}
