package build

import (
	"path/filepath"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestBuildResultCatalogRetainsVersionIdentityForDraftSources(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeStrictFile(t, vault, "oxpio.yaml", `title: Site
baseURL: https://example.test/docs/
navigation: []
versions:
  root: docs
  default: v1
  entries:
    - id: v1
      label: Version 1
      source: v1
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeStrictFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\n")
	writeStrictFile(t, vault, "docs/v1/_index.md", "---\ntitle: Version 1\npublish: true\n---\n")
	writeStrictFile(t, vault, "docs/v1/guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n")
	writeStrictFile(t, vault, "docs/v1/guide/文章.md", "---\ntitle: Article\npublish: true\ntype: doc\nslug: readme\n---\nArticle\n")
	writeStrictFile(t, vault, "docs/v1/guide/draft.md", "---\ntitle: Draft\npublish: false\ntype: doc\n---\nDraft\n")
	writeStrictFile(t, vault, "docs/v1/hidden/_index.md", "---\ntitle: Hidden\npublish: false\n---\n")
	writeStrictFile(t, vault, "docs/v1/hidden/draft.md", "---\ntitle: Hidden draft\npublish: true\ntype: doc\n---\nDraft\n")

	result, err := BuildWithOptions(vault, output, Options{})
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]model.SourceCatalogEntry, len(result.Catalog.Entries))
	for _, entry := range result.Catalog.Entries {
		byPath[entry.RelPath] = entry
	}
	published := byPath["docs/v1/guide/文章.md"]
	if published.Route != "/docs/v1/guide/readme/" || published.SectionPath != "docs/v1/guide" || published.VersionID != "v1" || !published.EffectivePublish {
		t.Fatalf("published version entry = %#v", published)
	}
	draft := byPath["docs/v1/guide/draft.md"]
	if draft.Route != "" || draft.SectionPath != "docs/v1/guide" || draft.VersionID != "v1" || draft.EffectivePublish {
		t.Fatalf("draft version entry = %#v", draft)
	}
	hiddenDraft := byPath["docs/v1/hidden/draft.md"]
	if hiddenDraft.Route != "" || hiddenDraft.SectionPath != "docs/v1/hidden" || hiddenDraft.VersionID != "v1" || hiddenDraft.EffectivePublish {
		t.Fatalf("hidden version entry = %#v", hiddenDraft)
	}
}

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
