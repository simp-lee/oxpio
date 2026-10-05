package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestStrictCacheManifestSeparatesInputsFromOutputs(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Cache\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "custom.css", "body { color: red; }\n")
	output := t.TempDir()
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	first := loadStrictCacheManifest(output)
	if first == nil || len(first.Dependencies) == 0 || len(first.Outputs) == 0 {
		t.Fatal("cache manifest does not contain separate dependency and output records")
	}
	firstDependency, firstOutput := cacheDependencyByOwner(first, "custom CSS"), cacheOutputByOwner(first, "custom CSS")
	if firstDependency.InputSignature == "" || firstOutput.OutputHash == "" {
		t.Fatal("cache records have empty signatures")
	}

	writeStrictFile(t, vault, "custom.css", "body { color: blue; }\n")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	second := loadStrictCacheManifest(output)
	secondDependency, secondOutput := cacheDependencyByOwner(second, "custom CSS"), cacheOutputByOwner(second, "custom CSS")
	if firstDependency.InputSignature == secondDependency.InputSignature {
		t.Fatal("custom CSS input signature did not change")
	}
	if firstOutput.OutputHash == secondOutput.OutputHash {
		t.Fatal("custom CSS output hash did not change")
	}
}

func TestStrictCacheInvalidatesListingMetadata(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Cache\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: post\ndate: 2026-01-02\ndescription: Initial summary\n---\nArticle\n")
	output := t.TempDir()
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "Initial summary") || !strings.Contains(string(first), "Jan 2, 2026") {
		t.Fatalf("initial listing metadata missing: %s", first)
	}

	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: post\ndate: 2026-01-03\ndescription: Updated summary\n---\nArticle\n")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(second), "Updated summary") || !strings.Contains(string(second), "Jan 3, 2026") {
		t.Fatalf("updated listing metadata missing: %s", second)
	}
	if strings.Contains(string(second), "Initial summary") || strings.Contains(string(second), "Jan 2, 2026") {
		t.Fatalf("stale listing metadata survived rebuild: %s", second)
	}
}

func TestStrictCacheTracksRecursiveCustomCSSDependencies(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Cache\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "custom.css", `@import "styles/nested.css";`)
	writeStrictFile(t, vault, "styles/nested.css", `@font-face { src: url("../fonts/site.woff2"); }`)
	writeStrictFile(t, vault, "fonts/site.woff2", "first font")
	output := filepath.Join(t.TempDir(), "site")
	firstResult, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	first := loadStrictCacheManifest(output)
	firstDependency, firstOutput := cacheDependencyByOwner(first, "custom CSS"), cacheOutputByOwner(first, "custom CSS")
	oldNested := firstResult.Assets["styles/nested.css"].DstPath

	writeStrictFile(t, vault, "fonts/site.woff2", "second font")
	secondResult, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	second := loadStrictCacheManifest(output)
	secondDependency, secondOutput := cacheDependencyByOwner(second, "custom CSS"), cacheOutputByOwner(second, "custom CSS")
	if firstDependency.InputSignature == secondDependency.InputSignature || firstOutput.OutputHash == secondOutput.OutputHash {
		t.Fatal("recursive CSS change did not invalidate the fixed custom CSS output")
	}
	if secondResult.Assets["styles/nested.css"].DstPath == oldNested {
		t.Fatal("recursive CSS change did not invalidate the transformed stylesheet")
	}
	if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(oldNested))); !os.IsNotExist(err) {
		t.Fatalf("stale transformed stylesheet remains after rebuild: %v", err)
	}
}

func TestStrictCacheHTMLConfigScopesPageDependencies(t *testing.T) {
	plan := &model.SitePlan{
		VaultPath: t.TempDir(),
		Config: model.SiteConfig{
			Title:    "Cache scope",
			BaseURL:  "https://example.test/",
			Language: "en",
			Source:   model.SourceConfig{EditURL: "https://edit.example/{path}"},
			Related:  model.RelatedConfig{Enabled: true, Count: 3},
			RSS:      model.RSSConfig{Enabled: true},
		},
	}
	section := &model.Section{
		RelPath: "other", SourcePath: "other/_index.md", Route: "/other/", Title: "Other",
		EffectivePublish: true,
	}
	article := &model.Note{
		RelPath: "other/article.md", Route: "/other/article/", SectionPath: "other",
		Frontmatter: model.Frontmatter{Title: "Article"},
	}
	sectionInput := func() strictCacheHTMLInput {
		return strictCacheSectionPageInput(plan, section, "lookup", nil)
	}
	articleInput := func() strictCacheHTMLInput {
		return strictCacheArticlePageInput(plan, article, section, nil, nil, 1, 1, nil, nil, "lookup", nil)
	}
	tagInput := func() strictCacheHTMLInput {
		return strictCacheHTMLBase(plan, "/tags/topic/", "Tag: topic", "", "", "", true)
	}
	marshal := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal cache input: %v", err)
		}
		return data
	}

	beforeSection, beforeArticle, beforeTag := marshal(sectionInput()), marshal(articleInput()), marshal(tagInput())
	plan.Config.Source.ViewURL = "https://view.example/{path}"
	afterSection, afterArticle, afterTag := marshal(sectionInput()), marshal(articleInput()), marshal(tagInput())
	if string(beforeSection) == string(afterSection) || string(beforeArticle) == string(afterArticle) {
		t.Fatal("source URL change did not invalidate source-bearing pages")
	}
	if string(beforeTag) != string(afterTag) {
		t.Fatal("source URL change invalidated a page without source links")
	}

	versionRoot := &model.Section{RelPath: "docs/v1", Route: "/docs/v1/", EffectivePublish: true}
	plan.Versions = []*model.Version{{ID: "v1", Label: "Version 1", Root: versionRoot}}
	versionArticle := *article
	versionArticle.VersionID = "v1"
	versionArticleInput := func() strictCacheHTMLInput {
		return strictCacheArticlePageInput(plan, &versionArticle, section, nil, nil, 1, 1, nil, nil, "lookup", nil)
	}
	unrelatedSectionBefore := marshal(sectionInput())
	versionArticleBefore := marshal(versionArticleInput())
	plan.Versions[0].Label = "Renamed version"
	if string(unrelatedSectionBefore) != string(marshal(sectionInput())) {
		t.Fatal("unrelated version change invalidated a section without version output")
	}
	if string(versionArticleBefore) == string(marshal(versionArticleInput())) {
		t.Fatal("version change did not invalidate a version page")
	}

	beforeSection = marshal(sectionInput())
	beforeArticle = marshal(articleInput())
	beforeTag = marshal(tagInput())
	plan.Config.Related.Count++
	plan.Config.RSS.Enabled = !plan.Config.RSS.Enabled
	if string(beforeSection) != string(marshal(sectionInput())) || string(beforeTag) != string(marshal(tagInput())) {
		t.Fatal("related or RSS config invalidated an unrelated HTML page")
	}
	if string(beforeArticle) != string(marshal(articleInput())) {
		t.Fatal("related or RSS config changed an article without changed rendered related content")
	}
}

func cacheDependencyByOwner(manifest *strictCacheManifest, owner string) strictCacheDependency {
	for _, dependency := range manifest.Dependencies {
		if dependency.Owner == owner {
			return dependency
		}
	}
	return strictCacheDependency{}
}

func cacheOutputByOwner(manifest *strictCacheManifest, owner string) strictCacheOutput {
	for _, output := range manifest.Outputs {
		if output.Owner == owner {
			return output
		}
	}
	return strictCacheOutput{}
}
