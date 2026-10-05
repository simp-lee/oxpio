package siteplan

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestBuildWithConfigRejectsAmbiguousVersionSourceIdentity(t *testing.T) {
	vault := t.TempDir()
	for _, dir := range []string{"", "docs/", "docs/v1/", "docs/v2/"} {
		writePlanFile(t, vault, dir+"_index.md", "---\ntitle: Section\npublish: true\n---\n")
	}
	for source, articleSlug := range map[string]string{
		"docs/v1/Intro.md": "first", "docs/v1/intro.md": "second", "docs/v2/intro.md": "intro",
	} {
		writePlanFile(t, vault, source, "---\ntitle: Article\npublish: true\ntype: doc\nslug: "+articleSlug+"\n---\n")
	}
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{
			{ID: "v1", Label: "One", Source: "v1"}, {ID: "v2", Label: "Two", Source: "v2"},
		},
	}}
	result, err := BuildWithConfig(vault, cfg)
	if err == nil {
		t.Fatal("ambiguous version source identity was accepted")
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Kind == diag.KindVersion && strings.Contains(diagnostic.Message, "same normalized source identity") && strings.Contains(diagnostic.Message, "docs/v1/Intro.md") && strings.Contains(diagnostic.Message, "docs/v1/intro.md") {
			return
		}
	}
	t.Fatalf("missing source identity collision diagnostic: %v", result.Diagnostics)
}

func TestBuildWithConfigVersionCorrespondenceIgnoresHiddenSectionIdentity(t *testing.T) {
	vault := t.TempDir()
	for _, dir := range []string{"", "docs/", "docs/v1/", "docs/v2/", "docs/v1/Guide/", "docs/v2/Guide/"} {
		writePlanFile(t, vault, dir+"_index.md", "---\ntitle: A\npublish: true\n---\n")
	}
	writePlanFile(t, vault, "docs/v1/guide/_index.md", "---\ntitle: Z\npublish: false\n---\n")
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{
			{ID: "v1", Label: "One", Source: "v1"}, {ID: "v2", Label: "Two", Source: "v2"},
		},
	}}
	result, err := BuildWithConfig(vault, cfg)
	if err != nil {
		t.Fatalf("BuildWithConfig: %v; diagnostics=%v", err, result.Diagnostics)
	}
	matched := 0
	for _, section := range result.Plan.Sections {
		if section.RelPath != "docs/v1/Guide" && section.RelPath != "docs/v2/Guide" {
			continue
		}
		matched++
		for _, versionID := range []string{"v1", "v2"} {
			want := "/docs/" + versionID + "/Guide/"
			if got := section.VersionRoutes[versionID]; got != want {
				t.Errorf("%s selector to %s = %q, want published counterpart %q", section.RelPath, versionID, got, want)
			}
		}
	}
	if matched != 2 {
		t.Fatalf("published matching section count = %d", matched)
	}
}

func TestBuildWithConfigRejectsHiddenVersionRoot(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/v1/_index.md", "---\ntitle: Version 1\npublish: false\n---\n")

	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{{ID: "v1", Label: "Version 1", Source: "v1"}},
	}}
	result, err := BuildWithConfig(vault, cfg)
	if err == nil {
		t.Fatal("BuildWithConfig() error = nil, want hidden version root rejection")
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Kind == diag.KindVersion && strings.Contains(diagnostic.Message, `version source "docs/v1" must set publish: true`) {
			return
		}
	}
	t.Fatalf("missing hidden version root diagnostic: %v", result.Diagnostics)
}

func TestBuildWithConfigRequiresIntermediateVersionContainerIndex(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/v1/guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/v1/guide/article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\n")
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{{ID: "v1", Label: "Version 1", Source: "v1/guide"}},
	}}

	result, err := BuildWithConfig(vault, cfg)
	if err == nil {
		t.Fatal("BuildWithConfig() error = nil, want missing intermediate section index rejection")
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Kind == diag.KindSection && diagnostic.Location.Path == "docs/v1/_index.md" && strings.Contains(diagnostic.Message, "missing required _index.md") {
			return
		}
	}
	t.Fatalf("missing intermediate section index diagnostic: %v", result.Diagnostics)
}

func TestBuildWithConfigPlansSectionsAndDocumentOrder(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\norder: 0\n---\nHome\n")
	writePlanFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\norder: 1\n---\nGuide\n")
	writePlanFile(t, vault, "guide/01-First.md", "---\ntitle: Zulu\npublish: true\ntype: doc\n---\nFirst\n")
	writePlanFile(t, vault, "guide/02 second.md", "---\ntitle: Alpha\npublish: true\ntype: doc\n---\nSecond\n")

	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/docs/", Navigation: []model.NavigationItem{{Name: "Guide", Section: "guide"}}}
	result, err := BuildWithConfig(vault, cfg)
	if err != nil {
		t.Fatalf("BuildWithConfig() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	if result.Plan.Root == nil || result.Plan.Root.Route != "/" {
		t.Fatalf("root = %#v, want root route", result.Plan.Root)
	}
	if _, ok := result.Plan.PublicPageRoutes["/404.html"]; !ok {
		t.Fatal("PublicPageRoutes is missing the generated 404 page")
	}
	if _, ok := result.Plan.PublicPageRoutes["/"]; ok {
		t.Fatal("PublicPageRoutes contains an indexed section route")
	}
	guide := result.Plan.Root.Children[0]
	if guide.Route != "/guide/" || guide.Banner != "" {
		t.Fatalf("guide = %#v", guide)
	}
	if len(guide.Documents) != 2 || guide.Documents[0].Frontmatter.Title != "Zulu" || guide.Documents[1].Frontmatter.Title != "Alpha" {
		t.Fatalf("documents = %#v, want filename-prefix order", guide.Documents)
	}
	if result.Plan.Documents[0].Route != "/guide/First/" || result.Plan.Documents[1].Route != "/guide/second/" {
		t.Fatalf("routes = %q, %q", result.Plan.Documents[0].Route, result.Plan.Documents[1].Route)
	}
	if got := guide.Breadcrumbs; len(got) != 2 || got[0].URL != "/" || got[1].URL != "/guide/" {
		t.Fatalf("breadcrumbs = %#v", got)
	}
}

func TestBuildWithConfigRejectsCompleteCollectionSortTies(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		typeName    string
		frontmatter string
	}{
		{name: "doc", typeName: "doc"},
		{name: "post", typeName: "post", frontmatter: "date: 2026-04-05\n"},
		{name: "page", typeName: "page"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			vault := t.TempDir()
			writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
			for _, article := range []struct {
				path string
				slug string
			}{
				{path: "A.md", slug: "first"},
				{path: "Ａ.md", slug: "second"},
			} {
				writePlanFile(t, vault, article.path, "---\ntitle: Same\npublish: true\ntype: "+testCase.typeName+"\n"+testCase.frontmatter+"slug: "+article.slug+"\n---\n")
			}

			result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
			if err == nil {
				t.Fatalf("BuildWithConfig() error = nil, want complete %s collection tie rejection; diagnostics=%v", testCase.name, result.Diagnostics)
			}
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Kind == diag.KindOrder && diagnostic.Field == "collection" && strings.Contains(diagnostic.Message, testCase.name+" collection has a complete sort-key tie") && strings.Contains(diagnostic.Message, "A.md") && strings.Contains(diagnostic.Message, "Ａ.md") {
					return
				}
			}
			t.Fatalf("missing %s collection tie diagnostic: %v", testCase.name, result.Diagnostics)
		})
	}
}

func TestBuildWithConfigRejectsSameSiteAbsoluteNavigationDuplicate(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		baseURL string
		url     string
	}{
		{name: "subpath", baseURL: "https://example.test/sub/", url: "https://example.test/sub/"},
		{name: "root", baseURL: "https://example.test/", url: "https://example.test/guide/"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			vault := t.TempDir()
			writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			if testCase.name == "root" {
				writePlanFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
			}
			cfg := model.SiteConfig{
				Title:   "Site",
				BaseURL: testCase.baseURL,
				Navigation: []model.NavigationItem{
					{Name: "Target", Section: func() string {
						if testCase.name == "root" {
							return "guide"
						}
						return "."
					}()},
					{Name: "Absolute target", URL: testCase.url},
				},
			}

			result, err := BuildWithConfig(vault, cfg)
			if err == nil {
				t.Fatalf("BuildWithConfig() error = nil, want duplicate navigation target; diagnostics=%v", result.Diagnostics)
			}
			if got := result.Plan.Config.Navigation[1].URL; got != testCase.url {
				t.Fatalf("absolute navigation href = %q, want unchanged %q", got, testCase.url)
			}
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Kind == diag.KindNavigation && diagnostic.Field == "navigation[1].url" && strings.Contains(diagnostic.Message, "duplicates navigation[0] target") {
					return
				}
			}
			t.Fatalf("missing duplicate navigation diagnostic: %v", result.Diagnostics)
		})
	}
}

func TestBuildWithConfigTreatsNumericMarkdownStemsAsSlugs(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	for _, article := range []struct {
		path  string
		title string
	}{
		{path: "alpha.md", title: "Alpha"},
		{path: "2147483648.md", title: "Huge"},
		{path: "-1.md", title: "Negative"},
		{path: "123.md", title: "Numeric"},
	} {
		writePlanFile(t, vault, article.path, "---\ntitle: "+article.title+"\npublish: true\ntype: doc\n---\n")
	}

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err != nil {
		t.Fatalf("BuildWithConfig() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	wantTitles := []string{"Alpha", "Huge", "Negative", "Numeric"}
	wantRoutes := []string{"/alpha/", "/2147483648/", "/-1/", "/123/"}
	if len(result.Plan.Documents) != len(wantTitles) {
		t.Fatalf("documents = %#v, want %d documents", result.Plan.Documents, len(wantTitles))
	}
	for i, note := range result.Plan.Documents {
		if note.Frontmatter.Title != wantTitles[i] || note.Route != wantRoutes[i] {
			t.Fatalf("documents[%d] = (%q, %q), want (%q, %q)", i, note.Frontmatter.Title, note.Route, wantTitles[i], wantRoutes[i])
		}
	}
}

func TestBuildWithConfigNFKCNormalizesExplicitSlugBeforeValidation(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\nslug: Ｃafe\u0301\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err != nil {
		t.Fatalf("BuildWithConfig() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	if len(result.Plan.Articles) != 1 {
		t.Fatalf("articles = %#v, want one article", result.Plan.Articles)
	}
	note := result.Plan.Articles[0]
	if note.Frontmatter.Slug != "Café" || note.Slug != "Café" || note.Route != "/Caf%C3%A9/" {
		t.Fatalf("article slug and route = (%q, %q, %q), want normalized Café route", note.Frontmatter.Slug, note.Slug, note.Route)
	}
}

func TestBuildWithConfigRejectsDefaultArticleSlugThatNormalizesToPath(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "a／b.md", "---\ntitle: Article\npublish: true\ntype: page\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil {
		t.Fatalf("BuildWithConfig() error = nil, want normalized path separator rejection; diagnostics=%v", result.Diagnostics)
	}
	if joined := diagnosticMessages(result.Diagnostics); !strings.Contains(joined, `normalized basename "a/b" contains a path separator`) {
		t.Fatalf("diagnostics = %q, want normalized path separator error", joined)
	}
}

func TestBuildWithConfigRejectsNFKCDotSectionRoute(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "．/_index.md", "---\ntitle: Dot\npublish: true\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil {
		t.Fatalf("BuildWithConfig() error = nil, want invalid normalized dot route; diagnostics=%v", result.Diagnostics)
	}
	if joined := diagnosticMessages(result.Diagnostics); !strings.Contains(joined, "filesystem-invalid path segment") {
		t.Fatalf("diagnostics = %q, want filesystem-invalid route error", joined)
	}
}

func TestBuildWithConfigRejectsMissingIndexAndHiddenOverride(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writePlanFile(t, vault, "private/public/_index.md", "---\ntitle: Public\npublish: true\n---\n")
	writePlanFile(t, vault, "private/public/visible.md", "---\ntitle: Visible\npublish: true\ntype: page\n---\n")
	writePlanFile(t, vault, "private/direct.md", "---\ntitle: Direct\npublish: true\ntype: page\n---\n")
	writePlanFile(t, vault, "missing/article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil {
		t.Fatal("BuildWithConfig() error = nil, want structural errors")
	}
	joined := diagnosticMessages(result.Diagnostics)
	for _, want := range []string{"missing required _index.md", "private", "public"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("diagnostics = %q, want %q", joined, want)
		}
	}
}

func TestBuildWithConfigPlansVersionCorrespondenceBySourcePathAndFallbacks(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\n")
	for _, version := range []string{"v1", "v2"} {
		writePlanFile(t, vault, "docs/"+version+"/_index.md", "---\ntitle: "+version+"\npublish: true\n---\n")
	}
	writePlanFile(t, vault, "docs/v1/intro.md", "---\ntitle: Intro\npublish: true\ntype: doc\nslug: intro-v1\n---\n")
	writePlanFile(t, vault, "docs/v1/only-v1.md", "---\ntitle: Only v1\npublish: true\ntype: doc\n---\n")
	writePlanFile(t, vault, "docs/v2/intro.md", "---\ntitle: Intro\npublish: true\ntype: doc\nslug: intro-v2\n---\n")
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{{ID: "v1", Label: "Version 1", Source: "v1"}, {ID: "v2", Label: "Version 2", Source: "v2"}},
	}}
	result, err := BuildWithConfig(vault, cfg)
	if err != nil {
		t.Fatalf("BuildWithConfig() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	if len(result.Plan.Versions) != 2 || result.Plan.Versions[0].Root == nil {
		t.Fatalf("versions = %#v", result.Plan.Versions)
	}
	var v1Only *model.Note
	for _, note := range result.Plan.Documents {
		if note.Frontmatter.Title == "Only v1" {
			v1Only = note
		}
	}
	if v1Only == nil || v1Only.VersionRoutes["v2"] != "/docs/v2/" {
		t.Fatalf("v1 fallback routes = %#v", v1Only)
	}
	var intro *model.Note
	for _, note := range result.Plan.Documents {
		if note.Frontmatter.Title == "Intro" && note.VersionID == "v1" {
			intro = note
		}
	}
	if intro == nil || intro.VersionRoutes["v2"] != "/docs/v2/intro-v2/" {
		t.Fatalf("same-source version routes = %#v, want target version's independent slug", intro)
	}
}

func TestBuildWithConfigVersionCorrespondenceRequiresSameArticleType(t *testing.T) {
	vault := t.TempDir()
	for _, dir := range []string{"", "docs/", "docs/v1/", "docs/v2/"} {
		writePlanFile(t, vault, dir+"_index.md", "---\ntitle: Section\npublish: true\n---\n")
	}
	writePlanFile(t, vault, "docs/v1/intro.md", "---\ntitle: Intro doc\npublish: true\ntype: doc\nslug: intro-doc\n---\n")
	writePlanFile(t, vault, "docs/v2/intro.md", "---\ntitle: Intro page\npublish: true\ntype: page\nslug: intro-page\n---\n")
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{
			{ID: "v1", Label: "One", Source: "v1"}, {ID: "v2", Label: "Two", Source: "v2"},
		},
	}}
	result, err := BuildWithConfig(vault, cfg)
	if err != nil {
		t.Fatalf("BuildWithConfig: %v; diagnostics=%v", err, result.Diagnostics)
	}
	articles := make(map[string]*model.Note, len(result.Plan.Articles))
	for _, article := range result.Plan.Articles {
		articles[article.VersionID] = article
	}
	wantRoutes := map[string]map[string]string{
		"v1": {"v1": "/docs/v1/intro-doc/", "v2": "/docs/v2/"},
		"v2": {"v1": "/docs/v1/", "v2": "/docs/v2/intro-page/"},
	}
	for sourceVersion, routes := range wantRoutes {
		article := articles[sourceVersion]
		if article == nil {
			t.Fatalf("missing %s article", sourceVersion)
		}
		for targetVersion, want := range routes {
			if got := article.VersionRoutes[targetVersion]; got != want {
				t.Errorf("%s selector to %s = %q, want %q", sourceVersion, targetVersion, got, want)
			}
		}
	}
}

func TestBuildWithConfigAllowsVersionContainerSectionsAndTheirBanner(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\nbanner: docs/banner.svg\nbannerAlt: Docs banner\n---\n")
	writePlanFile(t, vault, "docs/banner.svg", `<svg xmlns="http://www.w3.org/2000/svg"><path id="banner"/></svg>`)
	writePlanFile(t, vault, "docs/sources/_index.md", "---\ntitle: Sources\npublish: true\n---\n")
	writePlanFile(t, vault, "docs/sources/v1/_index.md", "---\ntitle: Version 1\npublish: true\n---\n")
	cfg := model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Versions: &model.VersionsConfig{
		Root: "docs", Default: "v1", Entries: []model.VersionEntry{{ID: "v1", Label: "Version 1", Source: "sources/v1"}},
	}}

	result, err := BuildWithConfig(vault, cfg)
	if err != nil {
		t.Fatalf("BuildWithConfig() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	sections := make(map[string]*model.Section, len(result.Plan.Sections))
	for _, section := range result.Plan.Sections {
		sections[section.RelPath] = section
	}
	if docs := sections["docs"]; docs == nil || docs.Route != "/docs/" || docs.Banner != "docs/banner.svg" {
		t.Fatalf("docs container = %#v, want landing with banner", docs)
	}
	if container := sections["docs/sources"]; container == nil || container.Route != "/docs/sources/" || container.VersionID != "" {
		t.Fatalf("intermediate container = %#v, want non-version landing", container)
	}
	if version := sections["docs/sources/v1"]; version == nil || version.Route != "/docs/v1/" || version.VersionID != "v1" {
		t.Fatalf("version source = %#v, want v1 landing", version)
	}
}

func TestBuildWithConfigNormalizesUnicodeRoutesAndClaimsReservedOutputs(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "Cafe\u0301/_index.md", "---\ntitle: Decomposed\npublish: true\n---\n")
	writePlanFile(t, vault, "Café/_index.md", "---\ntitle: Composed\npublish: true\n---\n")
	writePlanFile(t, vault, "assets/_index.md", "---\ntitle: Assets\npublish: true\n---\n")
	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil {
		t.Fatal("BuildWithConfig() error = nil, want route collisions")
	}
	messages := diagnosticMessages(result.Diagnostics)
	if !strings.Contains(messages, "conflicts") || !strings.Contains(messages, "reserved output") {
		t.Fatalf("diagnostics = %q", messages)
	}
}

func TestBuildWithConfigRejectsCaseInsensitiveAndReservedPhysicalRoutes(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "Foo/_index.md", "---\ntitle: Upper\npublish: true\n---\n")
	writePlanFile(t, vault, "foo/_index.md", "---\ntitle: Lower\npublish: true\n---\n")
	writePlanFile(t, vault, "CON.md", "---\ntitle: Device\npublish: true\ntype: page\n---\n")
	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil || !strings.Contains(diagnosticMessages(result.Diagnostics), "Windows-reserved") {
		t.Fatalf("BuildWithConfig() error=%v diagnostics=%v, want physical route rejection", err, result.Diagnostics)
	}
	if !strings.Contains(diagnosticMessages(result.Diagnostics), "conflicts") {
		t.Fatalf("diagnostics=%v, want case-insensitive route collision", result.Diagnostics)
	}
}

func TestBuildWithConfigRejectsUnicodeCaseFoldedPhysicalRouteCollisions(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "upper.md", "---\ntitle: Upper\npublish: true\ntype: page\nslug: Ä\n---\n")
	writePlanFile(t, vault, "lower.md", "---\ntitle: Lower\npublish: true\ntype: page\nslug: ä\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil || !strings.Contains(diagnosticMessages(result.Diagnostics), "conflicts") {
		t.Fatalf("BuildWithConfig() error=%v diagnostics=%v, want decoded case-insensitive route collision", err, result.Diagnostics)
	}
}

func TestBuildWithConfigRejectsUnknownAndImplicitArticleMetadata(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\nlayout: legacy\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil || !strings.Contains(diagnosticMessages(result.Diagnostics), "unknown frontmatter field") {
		t.Fatalf("BuildWithConfig() error=%v diagnostics=%v, want unknown-field rejection", err, result.Diagnostics)
	}
}

func TestBuildWithConfigRejectsAssetDestinationCollisions(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"images/one/banner.png", "images/two/banner.png"} {
		writePlanFile(t, vault, source, imageData.String())
	}
	writePlanFile(t, vault, "one.md", "---\ntitle: One\npublish: true\ntype: page\nbanner: images/one/banner.png\nbannerAlt: Banner\n---\n")
	writePlanFile(t, vault, "two.md", "---\ntitle: Two\npublish: true\ntype: page\nbanner: images/two/banner.png\nbannerAlt: Banner\n---\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil || !strings.Contains(diagnosticMessages(result.Diagnostics), "asset destination") || !strings.Contains(diagnosticMessages(result.Diagnostics), "distinct sources") {
		t.Fatalf("BuildWithConfig() error=%v diagnostics=%v, want asset destination collision", err, result.Diagnostics)
	}
}

func TestBuildWithConfigRejectsAssetCollisionAcrossFrontmatterAndMarkdown(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\nbanner: images/one/banner.png\nbannerAlt: Banner\n---\n[Inline](images/two/banner.png)\n")
	writePlanFile(t, vault, "images/one/banner.png", imageData.String())
	writePlanFile(t, vault, "images/two/banner.png", imageData.String())

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil || !strings.Contains(diagnosticMessages(result.Diagnostics), "asset destination") || !strings.Contains(diagnosticMessages(result.Diagnostics), "distinct sources") {
		t.Fatalf("BuildWithConfig() error=%v diagnostics=%v, want cross-pass asset collision", err, result.Diagnostics)
	}
}

func TestBuildWithConfigRejectsMissingLocalMarkdownAssets(t *testing.T) {
	vault := t.TempDir()
	writePlanFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writePlanFile(t, vault, "images/Cafe\u0301.png", "decomposed")
	writePlanFile(t, vault, "images/Café.png", "composed")
	writePlanFile(t, vault, "files/Cafe\u0301", "decomposed")
	writePlanFile(t, vault, "files/Café", "composed")
	writePlanFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\n![Missing](missing.png)\n\n[Attachment](missing.pdf)\n\n![[missing-embed.png]]\n\n![[missing%2Epng]]\n\n![Outside](../outside.png)\n\n[Root attachment](/missing-root.pdf)\n\n[Fragment attachment](missing-fragment.pdf#page=2)\n\n![[images/CAF%C3%89%2Epng]]\n\n![[images/CAFÉ.png]]\n\n![Exact](images/Café.png)\n\n[Ambiguous attachment](/files/CAFÉ#part)\n\n[Windows attachment](C:/assets/manual.pdf)\n\n[Malformed attachment](missing%ZZ.pdf)\n\n![Remote](https://images.example.test/remote.png)\n\n[Remote attachment](https://files.example.test/manual.pdf)\n\n![[https://cdn.example.test/embed.png]]\n\n[Missing route](missing/)\n\n[Missing extensionless route](missing-page)\n\n[[Missing Note]]\n")

	result, err := BuildWithConfig(vault, model.SiteConfig{Title: "Site", BaseURL: "https://example.test/"})
	if err == nil {
		t.Fatal("BuildWithConfig() error = nil, want unresolved local asset errors")
	}

	wantErrors := map[string]int{
		"missing.png":                 6,
		"missing.pdf":                 8,
		"missing-embed.png":           10,
		"missing%2Epng":               12,
		"../outside.png":              14,
		"/missing-root.pdf":           16,
		"missing-fragment.pdf#page=2": 18,
		"images/CAF%C3%89%2Epng":      20,
		"images/CAFÉ.png":             22,
		"/files/CAFÉ#part":            26,
		"C:/assets/manual.pdf":        28,
		"missing%ZZ.pdf":              30,
	}
	for target, line := range wantErrors {
		found := false
		for _, item := range result.Diagnostics {
			if item.Target != target {
				continue
			}
			found = true
			if item.Severity != diag.SeverityError || item.Kind != diag.KindUnresolvedAsset || item.Location.Path != "article.md" || item.Location.Line != line {
				t.Fatalf("diagnostic for %q = %#v, want unresolved_asset error at article.md:%d", target, item, line)
			}
		}
		if !found {
			t.Fatalf("diagnostics = %#v, want error for %q", result.Diagnostics, target)
		}
	}
	for _, item := range result.Diagnostics {
		if item.Target == "https://images.example.test/remote.png" || item.Target == "https://files.example.test/manual.pdf" {
			t.Fatalf("standard external target produced diagnostic: %#v", item)
		}
	}
	wantWarnings := map[string]diag.Kind{
		"https://cdn.example.test/embed.png": diag.KindUnresolvedAsset,
		"missing/":                           diag.KindDeadLink,
		"missing-page":                       diag.KindDeadLink,
		"Missing Note":                       diag.KindDeadLink,
	}
	for target, kind := range wantWarnings {
		found := false
		for _, item := range result.Diagnostics {
			if item.Target == target && item.Severity == diag.SeverityWarning && item.Kind == kind {
				found = true
			}
		}
		if !found {
			t.Fatalf("diagnostics = %#v, want %s warning for %q", result.Diagnostics, kind, target)
		}
	}
}

func diagnosticMessages(diagnostics []diag.Diagnostic) string {
	var values []string
	for _, diagnostic := range diagnostics {
		values = append(values, diagnostic.Message)
	}
	return strings.Join(values, "\n")
}

func writePlanFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
