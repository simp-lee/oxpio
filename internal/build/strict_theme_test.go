package build

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/analyze"
	"github.com/simp-lee/oxpio/internal/asset"
)

func TestStrictBuildRewritesThemeCSSAndInvalidatesDependentURLs(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/docs/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, ".oxpio/theme/theme.css", `@import /* keep */ "styles/nested.css" screen;
.logo { background: url("My%20Logo.svg?v=1#mark"); }
/* url(not-a-resource) */ .label::after { content: "url(not-a-resource)"; }`)
	writeStrictFile(t, vault, ".oxpio/theme/assets/styles/nested.css", `@font-face { src: url('../fonts/site.woff2') format('woff2'); }
.logo { background-image: image-set("../My Logo.svg" 1x, url(../My\ Logo.svg) 2x); }
.external { background: url(https://example.test/remote.png); mask: url(#local); }`)
	writeStrictFile(t, vault, ".oxpio/theme/assets/fonts/site.woff2", "font bytes")
	writeStrictFile(t, vault, ".oxpio/theme/assets/My Logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"><path id="mark"/></svg>`)
	writeStrictFile(t, vault, ".oxpio/theme/slots.html", `{{define "oxpio-head-end"}}<link rel="stylesheet" href="{{themeAssetURL .SiteRootRel "styles/nested.css"}}">{{end}}`)
	output := filepath.Join(t.TempDir(), "site")
	first, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	css := first.Assets[".oxpio/theme/theme.css"]
	nested := first.Assets[".oxpio/theme/assets/styles/nested.css"]
	logo := first.Assets[".oxpio/theme/assets/My Logo.svg"]
	font := first.Assets[".oxpio/theme/assets/fonts/site.woff2"]
	if !strings.HasSuffix(css.DstPath, ".css") || !strings.HasSuffix(logo.DstPath, ".svg") {
		t.Fatalf("theme destinations lost source extensions: CSS=%q logo=%q", css.DstPath, logo.DstPath)
	}
	rootCSS := string(readBuildOutputFile(t, output, css.DstPath))
	for _, want := range []string{path.Base(nested.DstPath), path.Base(logo.DstPath) + "?v=1#mark", `/* url(not-a-resource) */`, `content: "url(not-a-resource)"`} {
		if !strings.Contains(rootCSS, want) {
			t.Fatalf("theme CSS missing %q: %s", want, rootCSS)
		}
	}
	nestedCSS := string(readBuildOutputFile(t, output, nested.DstPath))
	for _, want := range []string{path.Base(font.DstPath), path.Base(logo.DstPath), "https://example.test/remote.png", "url(#local)"} {
		if !strings.Contains(nestedCSS, want) {
			t.Fatalf("nested CSS missing %q: %s", want, nestedCSS)
		}
	}
	// Resolve every emitted local CSS URL and verify that it names published bytes.
	for _, stylesheet := range []string{css.DstPath, nested.DstPath} {
		data := readBuildOutputFile(t, output, stylesheet)
		if !strings.Contains(stylesheet, fmt.Sprintf(".%x.", sha256.Sum256(data))) {
			t.Fatalf("CSS path does not hash emitted bytes: %s", stylesheet)
		}
		_, err := asset.RewriteCSSURLs(data, func(raw string) (string, error) {
			if strings.HasPrefix(raw, "https:") || strings.HasPrefix(raw, "#") {
				return raw, nil
			}
			resource := strings.Split(strings.Split(raw, "?")[0], "#")[0]
			readBuildOutputFile(t, output, path.Join(path.Dir(stylesheet), resource))
			return raw, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	firstBytes := strictOutputBytes(t, output)
	firstURLs := strictOutputURLs(first)
	again, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatalf("unchanged theme rebuild failed: %v", err)
	}
	if !reflect.DeepEqual(first.Assets, again.Assets) {
		t.Fatal("unchanged theme plan is not stable")
	}
	compareStrictOutputBytes(t, firstBytes, strictOutputBytes(t, output))
	compareStrictURLValues(t, firstURLs, strictOutputURLs(again))
	writeStrictFile(t, vault, ".oxpio/theme/assets/My Logo.svg", `<svg xmlns="http://www.w3.org/2000/svg"><circle id="mark"/></svg>`)
	changed, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{css.SrcPath, nested.SrcPath, logo.SrcPath} {
		if changed.Assets[old].DstPath == first.Assets[old].DstPath {
			t.Fatalf("changed dependency did not invalidate %s", old)
		}
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(first.Assets[old].DstPath))); !os.IsNotExist(err) {
			t.Fatalf("stale theme output retained for %s: %v", old, err)
		}
	}
	if changed.Assets[font.SrcPath].DstPath != font.DstPath {
		t.Fatal("unrelated theme font URL changed")
	}
}

func TestStrictBuildPlansThemeCSSVaultRootResources(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/docs/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, ".oxpio/theme/theme.css", `@import "/styles/vault.css";
.hero { background: url("/images/hero.bin?v=1#mark"); }
.external { background: url("//cdn.example.test/image.png"); }`)
	writeStrictFile(t, vault, "styles/vault.css", `.font { src: url("../fonts/site.woff2"); }`)
	writeStrictFile(t, vault, "images/hero.bin", "hero bytes")
	writeStrictFile(t, vault, "fonts/site.woff2", "font bytes")

	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"images/hero.bin", "styles/vault.css", "fonts/site.woff2"} {
		planned := result.Assets[source]
		if planned == nil || planned.DstPath == "" {
			t.Fatalf("theme root resource %q was not planned: %#v", source, result.Assets)
		}
		readBuildOutputFile(t, output, planned.DstPath)
	}

	rootCSS := string(readBuildOutputFile(t, output, result.Assets[".oxpio/theme/theme.css"].DstPath))
	for _, want := range []string{
		path.Base(result.Assets["styles/vault.css"].DstPath),
		path.Base(result.Assets["images/hero.bin"].DstPath) + "?v=1#mark",
		"//cdn.example.test/image.png",
	} {
		if !strings.Contains(rootCSS, want) {
			t.Fatalf("theme CSS missing %q: %s", want, rootCSS)
		}
	}
	if strings.Contains(rootCSS, `url("/images/hero.bin`) {
		t.Fatalf("theme CSS retained vault-root URL under a subpath baseURL: %s", rootCSS)
	}
	vaultCSS := string(readBuildOutputFile(t, output, result.Assets["styles/vault.css"].DstPath))
	if want := path.Base(result.Assets["fonts/site.woff2"].DstPath); !strings.Contains(vaultCSS, want) {
		t.Fatalf("vault CSS imported by theme missing planned dependency %q: %s", want, vaultCSS)
	}
}

func TestStrictThemeSlotLiteralsMayReferenceExactPlannedOutputs(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/docs/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	const source = ".oxpio/theme/assets/logo.bin"
	data := []byte("logo bytes")
	writeStrictFile(t, vault, source, string(data))
	planned := asset.PlanData(source, data)
	resourceURL := "/docs/" + planned.DstPath + "?v=1#logo"
	writeStrictFile(t, vault, ".oxpio/theme/slots.html", fmt.Sprintf(`{{define "oxpio-footer-end"}}<img src=%q><img src="https://cdn.example.test/logo.png">{{end}}`, resourceURL))

	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatalf("planned and external slot resources must pass validation: %v", err)
	}
	if page := string(readBuildOutputFile(t, output, "index.html")); !strings.Contains(page, resourceURL) {
		t.Fatalf("published slot is missing planned literal resource %q: %s", resourceURL, page)
	}
}

func TestStrictThemeSlotsMayReferenceBuiltInStyleCSS(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/docs/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, ".oxpio/theme/slots.html", `{{define "oxpio-head-end"}}<link rel="preload" href="./style.css" as="style">{{end}}`)

	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatalf("built-in style.css should be allowed in theme slots: %v", err)
	}
	if page := string(readBuildOutputFile(t, output, "index.html")); !strings.Contains(page, `href=./style.css`) {
		t.Fatalf("published slot is missing built-in style.css preload: %s", page)
	}
}

func TestStrictThemeFailuresHaveSharedReadOnlyDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, slots, css, want string
	}{
		{name: "missing slot resource", slots: `{{define "oxpio-footer-end"}}<img src="{{themeAssetURL .SiteRootRel "missing.svg"}}">{{end}}`, want: "missing.svg"},
		{name: "conditional slot resource", slots: `{{define "oxpio-footer-end"}}{{if eq .RelPath "/guide/"}}<img src="{{themeAssetURL .SiteRootRel "missing.svg"}}">{{end}}{{end}}`, want: "missing.svg"},
		{name: "literal slot resource", slots: `{{define "oxpio-footer-end"}}<img src="/logo.png">{{end}}`, want: "themeAssetURL"},
		{name: "conditional literal slot resource", slots: `{{define "oxpio-footer-end"}}{{if eq .RelPath "/guide/"}}<img srcset="/logo.png 1x, https://cdn.example.test/logo.png 2x">{{end}}{{end}}`, want: "themeAssetURL"},
		{name: "literal slot attachment", slots: `{{define "oxpio-footer-end"}}<a href="/manual.pdf">Manual</a>{{end}}`, want: "themeAssetURL"},
		{name: "missing CSS resource", css: `.logo { background: url(missing.svg); }`, want: "missing.svg"},
		{name: "missing root CSS resource", css: `.logo { background: url("/images/missing.svg"); }`, want: "/images/missing.svg"},
		{name: "escaping root CSS resource", css: `.logo { background: url("/../outside.svg"); }`, want: "escapes the vault"},
		{name: "cyclic CSS", css: `@import "theme.css";`, want: "cyclic theme CSS"},
	} {
		t.Run(test.name, func(t *testing.T) {
			vault := t.TempDir()
			writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
			writeStrictFile(t, vault, "logo.png", "unplanned logo")
			writeStrictFile(t, vault, "manual.pdf", "unplanned manual")
			writeStrictFile(t, vault, ".oxpio/theme/slots.html", test.slots)
			writeStrictFile(t, vault, ".oxpio/theme/theme.css", test.css)
			output := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, output, managedOutputMarkerFilename, managedOutputMarkerContents)
			writeStrictFile(t, output, "index.html", "previous site")
			analysis, err := analyze.AnalyzeWithOutput(vault, output)
			if err == nil || !strings.Contains(fmt.Sprint(analysis.Diagnostics), test.want) {
				t.Fatalf("analyze: %v; diagnostics=%v", err, analysis.Diagnostics)
			}
			built, err := BuildWithOptions(vault, output, Options{Strict: true})
			if err == nil || !reflect.DeepEqual(analysis.Diagnostics, built.Diagnostics) {
				t.Fatalf("build did not share analysis diagnostics: %v; diagnostics=%v", err, built.Diagnostics)
			}
			if got := string(readBuildOutputFile(t, output, "index.html")); got != "previous site" {
				t.Fatalf("failed theme planning changed published site: %q", got)
			}
		})
	}
}

func TestStrictBuildPlansThemeAssets(t *testing.T) {
	for _, sameContent := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-content=%t", sameContent), func(t *testing.T) {
			vault := t.TempDir()
			writeStrictFile(t, vault, "oxpio.yaml", "title: Theme\nbaseURL: https://example.test/docs/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n![shared](../images/café.txt)\n")
			first, second := "first asset", "second asset"
			if sameContent {
				second = first
			}
			sources := map[string]string{
				".oxpio/theme/assets/café.txt":       first,
				".oxpio/theme/assets/cafe\u0301.txt": second,
				".oxpio/theme/theme.css":             ":root { color: red; }",
				".oxpio/theme/assets/theme.css":      ":root { color: blue; }",
				"images/café.txt":                     first,
			}
			for source, data := range sources {
				writeStrictFile(t, vault, source, data)
			}
			writeStrictFile(t, vault, ".oxpio/theme/slots.html", `{{define "oxpio-footer-end"}}<a href="{{themeAssetURL .SiteRootRel "café.txt"}}">first</a><a href="{{themeAssetURL .SiteRootRel "café.txt"}}">second</a>{{end}}`)
			output := filepath.Join(t.TempDir(), "site")
			if result, err := analyze.AnalyzeWithOutput(vault, output); err != nil || len(result.Diagnostics) != 0 {
				t.Fatalf("analyze: %v; diagnostics=%v", err, result.Diagnostics)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("analysis touched output: %v", err)
			}
			result, err := BuildWithOptions(vault, output, Options{Strict: true})
			if err != nil {
				t.Fatal(err)
			}
			for source, data := range sources {
				asset := result.Assets[source]
				if asset == nil {
					t.Fatalf("theme source not in shared asset result: %q", source)
				}
				if !strings.Contains(asset.DstPath, fmt.Sprintf(".%x.", sha256.Sum256([]byte(data)))) {
					t.Fatalf("non-content-addressed destination: %q", asset.DstPath)
				}
				if got := string(readBuildOutputFile(t, output, asset.DstPath)); got != data {
					t.Fatalf("asset %q = %q, want %q", source, got, data)
				}
			}
			if result.Assets["images/café.txt"].DstPath != result.Assets[".oxpio/theme/assets/café.txt"].DstPath {
				t.Fatal("theme and Markdown assets did not share allocation/deduplication")
			}
			for page, prefix := range map[string]string{"index.html": "./", "guide/index.html": "../"} {
				html := string(readBuildOutputFile(t, output, page))
				for _, source := range []string{".oxpio/theme/assets/café.txt", ".oxpio/theme/assets/cafe\u0301.txt"} {
					if !strings.Contains(html, prefix+result.Assets[source].DstPath) {
						t.Fatalf("%s missing planned slot URL for %s", page, source)
					}
				}
				if !strings.Contains(html, "/docs/"+result.Assets[".oxpio/theme/theme.css"].DstPath) {
					t.Fatalf("%s missing planned theme CSS URL", page)
				}
			}
			if _, err := os.Stat(filepath.Join(output, "assets", "theme")); !os.IsNotExist(err) {
				t.Fatalf("legacy unhashed theme output exists: %v", err)
			}
		})
	}
}
