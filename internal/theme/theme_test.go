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
