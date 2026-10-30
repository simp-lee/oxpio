package theme

import (
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/render/viewmodel"
)

func TestCanonicalJSONLDRejectsNonCoreValues(t *testing.T) {
	canonical := `{"@context":"https://schema.org","@type":"Article","headline":"Title","url":"https://example.test/article"}`
	if got, err := canonicalJSONLD(canonical); err != nil || string(got) != canonical {
		t.Fatalf("canonicalJSONLD(core) = %q, %v", got, err)
	}
	for _, value := range []string{
		`{"foo":"bar"}`,
		`{"@context":"https://schema.org","@type":"Article","headline":"Title","url":"https://example.test/article" }`,
	} {
		if _, err := canonicalJSONLD(value); err == nil {
			t.Fatalf("canonicalJSONLD(%q) error = nil", value)
		}
	}
}

func TestThemeRejectsPortablePathCollisions(t *testing.T) {
	source, err := BaseSource()
	if err != nil {
		t.Fatal(err)
	}
	source.Files["assets/A.css"] = []byte("a")
	source.Files["assets/a.css"] = []byte("b")
	if err := Validate(source); err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("collision validation error = %v", err)
	}
}

func TestThemeRuntimeContractRequiresEnabledMounts(t *testing.T) {
	source, err := BaseSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeContract(source, ValidationAssets(source), "https://example.test/", true, true); err != nil {
		t.Fatalf("base runtime contract = %v", err)
	}
	if err := ValidateRuntimeContract(source, ValidationAssets(source), "https://example.test/", false, false); err != nil {
		t.Fatalf("base optional theme toggle contract = %v", err)
	}
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/base.html"] = []byte(`{{define "base"}}<html></html>{{end}}`)
	minimal := sourceFromFiles("minimal", files, false)
	if err := ValidateRuntimeContract(minimal, ValidationAssets(minimal), "https://example.test/", false, false); err != nil {
		t.Fatalf("disabled runtime contract = %v", err)
	}
	if err := ValidateRuntimeContract(minimal, ValidationAssets(minimal), "https://example.test/", true, false); err == nil || !strings.Contains(err.Error(), "data-oxpio-sidebar") {
		t.Fatalf("missing Sidebar mount error = %v", err)
	}
	if err := ValidateRuntimeContract(minimal, ValidationAssets(minimal), "https://example.test/", false, true); err == nil || !strings.Contains(err.Error(), "data-oxpio-popover") {
		t.Fatalf("missing Popover mount error = %v", err)
	}
}

func TestBaseThemeRendersArticleTagsAsTagLinks(t *testing.T) {
	source, err := BaseSource()
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := Compile(source, nil, "https://example.test/docs/")
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("article", viewmodel.Page{
		Schema: viewmodel.SchemaVersion,
		Kind:   "article",
		Site:   viewmodel.Site{Title: "Site", Language: "en"},
		Title:  "Article",
		Article: &viewmodel.Article{
			Title: "Article",
			Tags:  []string{"go", "parent/child"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<li><a href="https://example.test/docs/tags/go/">go</a></li>`,
		`<li><a href="https://example.test/docs/tags/parent/child/">parent/child</a></li>`,
	} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("article tag link missing %q: %s", want, output)
		}
	}
}

func TestBaseThemeRendersVersionedArticleTagsUnderVersionRoot(t *testing.T) {
	source, err := BaseSource()
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := Compile(source, nil, "https://example.test/docs/")
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("article", viewmodel.Page{
		Schema:      viewmodel.SchemaVersion,
		Kind:        "article",
		VersionID:   "v2",
		VersionRoot: "/v2/",
		Site:        viewmodel.Site{Title: "Site", Language: "en"},
		Title:       "Article",
		Article:     &viewmodel.Article{Title: "Article", Tags: []string{"Go Tools"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `href="https://example.test/docs/v2/tags/go-tools/">Go Tools</a>`) {
		t.Fatalf("versioned article tag link missing: %s", output)
	}
}

func TestBaseThemeHasStableContractAndRendersAllPageKinds(t *testing.T) {
	source, err := BaseSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(source); err != nil {
		t.Fatal(err)
	}
	manifest := ManifestOf(source)
	if manifest.APIVersion != 1 || manifest.Name != BaseID || manifest.Hash == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	renderer, err := Compile(source, nil, "https://example.test/docs/")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"section", "article", "tag", "timeline", "404"} {
		output, renderErr := renderer.Render(kind, viewmodel.Page{Schema: viewmodel.SchemaVersion, Kind: kind, Site: viewmodel.Site{Title: "Site", Language: "en"}, Title: strings.ToUpper(kind[:1]) + kind[1:]})
		if renderErr != nil {
			t.Fatalf("render %s: %v", kind, renderErr)
		}
		if !strings.Contains(string(output), "<!doctype html>") {
			t.Fatalf("render %s = %q", kind, output)
		}
	}
	emptySidebar, err := renderer.Render("article", viewmodel.Page{Schema: viewmodel.SchemaVersion, Kind: "article", Site: viewmodel.Site{Title: "Site", Language: "en"}, Title: "Article"})
	if err != nil || strings.Contains(string(emptySidebar), "data-sidebar-toggle") || strings.Contains(string(emptySidebar), "data-oxpio-sidebar") {
		t.Fatalf("empty Sidebar render = %q, %v", emptySidebar, err)
	}
}

func TestThemeCompilerRejectsTrailingManifestAndMissingBaseTemplate(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["theme.yaml"] = append(files["theme.yaml"], []byte("\n---\napiVersion: 1\nname: minimal\n")...)
	if err := ValidateWithAssets(sourceFromFiles("minimal", files, false), ValidationAssets(sourceFromFiles("minimal", files, false)), "https://example.test/"); err == nil || !strings.Contains(err.Error(), "one document") {
		t.Fatalf("trailing manifest error = %v", err)
	}

	files, err = InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/base.html"] = []byte(`{{define "other"}}base{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, ValidationAssets(source), "https://example.test/"); err == nil || !strings.Contains(err.Error(), "define template") {
		t.Fatalf("missing base template error = %v", err)
	}
}

func TestThemeCompilerAllowsFullShellReplacementAndViewModelFields(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/article.html"] = []byte(`{{define "article"}}CUSTOM {{.Kind}} {{.Article.Title}} {{.Content}}{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, ValidationAssets(source), "https://example.test/"); err != nil {
		t.Fatal(err)
	}
	renderer, err := Compile(source, ValidationAssets(source), "https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	output, err := renderer.Render("article", samplePage("article"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "CUSTOM article Introduction") {
		t.Fatalf("output = %q", output)
	}
}

func TestThemeCompilerRejectsForbiddenAndUnplannedTemplateActions(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/article.html"] = []byte(`{{define "article"}}{{.Title | printf "%s"}}{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, ValidationAssets(source), "https://example.test/"); err == nil || !strings.Contains(err.Error(), `forbidden function "printf"`) {
		t.Fatalf("forbidden template function error = %v", err)
	}

	files, err = InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["partials/example.html"] = []byte(`{{assetURL ` + "`assets/missing.css`" + `}}`)
	source = sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, ValidationAssets(source), "https://example.test/"); err == nil || !strings.Contains(err.Error(), "unplanned asset") {
		t.Fatalf("unused partial asset error = %v", err)
	}
}

func TestThemeCompilerRejectsUnplannedSiteAsset(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/article.html"] = []byte(`{{define "article"}}{{siteURL "/assets/app.js"}}{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, map[string]string{}, "https://example.test/"); err == nil || !strings.Contains(err.Error(), "assetURL") {
		t.Fatalf("ValidateWithAssets error = %v", err)
	}
}

func TestThemeCompilerAllowsExternalNavigationAndReportsExternalResources(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/article.html"] = []byte(`{{define "article"}}<a href="https://github.com/example/project">Project</a>{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := ValidateWithAssets(source, ValidationAssets(source), "https://example.test/"); err != nil {
		t.Fatalf("external navigation validation error = %v", err)
	}

	files["layouts/article.html"] = []byte("{{define \"article\"}}\n<img src=\"https://images.example.test/hero.png\">\n{{end}}")
	source = sourceFromFiles("minimal", files, false)
	err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
	if err == nil || !strings.Contains(err.Error(), "line 2, column 11") || !strings.Contains(err.Error(), "resource attribute") || !strings.Contains(err.Error(), "external navigation href attributes are allowed") {
		t.Fatalf("external resource validation error = %v", err)
	}

	files["layouts/article.html"] = []byte(`{{define "article"}}<img srcset="https://images.example.test/hero.png 1x">{{end}}`)
	source = sourceFromFiles("minimal", files, false)
	err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
	if err == nil || !strings.Contains(err.Error(), "responsive image resource") || !strings.Contains(err.Error(), "https://images.example.test/hero.png") {
		t.Fatalf("external srcset validation error = %v", err)
	}

	files["layouts/article.html"] = []byte(`{{define "article"}}<style>@import "https://cdn.example.test/theme.css";</style>{{end}}`)
	source = sourceFromFiles("minimal", files, false)
	err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
	if err == nil || !strings.Contains(err.Error(), "CSS @import") || !strings.Contains(err.Error(), "https://cdn.example.test/theme.css") {
		t.Fatalf("external CSS import validation error = %v", err)
	}

	files["layouts/article.html"] = []byte(`{{define "article"}}<svg><use href="https://cdn.example.test/icon.svg#mark"></use></svg>{{end}}`)
	source = sourceFromFiles("minimal", files, false)
	err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
	if err == nil || !strings.Contains(err.Error(), "SVG resource href") || !strings.Contains(err.Error(), "https://cdn.example.test/icon.svg#mark") {
		t.Fatalf("external SVG resource validation error = %v", err)
	}

	for _, body := range []string{
		`{{define "article"}}<img src=https://images.example.test/hero.png>{{end}}`,
		`{{define "article"}}<link rel=stylesheet href=https://cdn.example.test/theme.css>{{end}}`,
		`{{define "article"}}<img srcset=/local.png,https://images.example.test/hero.png>{{end}}`,
	} {
		files["layouts/article.html"] = []byte(body)
		source = sourceFromFiles("minimal", files, false)
		err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
		if err == nil || !strings.Contains(err.Error(), "external") {
			t.Fatalf("unquoted external resource validation error = %v", err)
		}
	}
}

func TestThemeCompilerRejectsLocalSrcsetAssets(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{{define "article"}}<img srcset="/images/hero.png 1x, /assets/hero.png?rev=1#banner 2x">{{end}}`,
		`{{define "article"}}<link rel="preload" as="image" imagesrcset="assets/hero.png 1x">{{end}}`,
		`{{define "article"}}<img srcset="./assets/hero.png 1x">{{end}}`,
		`{{define "article"}}<img srcset="../assets/hero.png 1x">{{end}}`,
	} {
		files["layouts/article.html"] = []byte(body)
		source := sourceFromFiles("minimal", files, false)
		err = ValidateWithAssets(source, ValidationAssets(source), "https://example.test/")
		if err == nil || !strings.Contains(err.Error(), "must use assetURL") || !strings.Contains(err.Error(), "assets/hero.png") {
			t.Fatalf("local srcset validation error = %v", err)
		}
	}
}

func TestThemeCompilerRejectsUnknownFunctionAndMissingAsset(t *testing.T) {
	files, err := InitFiles("minimal")
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/article.html"] = []byte(`{{define "article"}}{{assetURL "assets/missing.css"}}{{end}}`)
	source := sourceFromFiles("minimal", files, false)
	if err := Validate(source); err != nil {
		t.Fatal(err)
	}
	if err := ValidateWithAssets(source, map[string]string{}, "https://example.test/"); err == nil || !strings.Contains(err.Error(), "asset") {
		t.Fatalf("ValidateWithAssets error = %v", err)
	}
}
