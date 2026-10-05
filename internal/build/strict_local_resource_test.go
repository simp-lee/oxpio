package build

import (
	"bytes"
	"path"
	"path/filepath"
	"strings"
	"testing"

	xhtml "golang.org/x/net/html"
)

func TestStrictBuildPlansRawHTMLAndCustomCSSLocalResources(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Resources\nbaseURL: https://example.test/docs/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\nGuide\n")
	writeStrictFile(t, vault, "guide/article.md", `---
title: Article
publish: true
type: page
---
Inline <img src="../images/inline.bin?v=1#icon" data-keep="yes"> resource.

<figure><img src='../images/block.bin' srcset="../images/one.bin, ../images/two-local.bin 2x, https://cdn.example.test/two.bin 3x" style="background-image: url('../images/style.bin')"></figure>

<style>.hero { background: url("../images/sheet.bin#hero") }</style>

Inline style <style>.inline-style { background: url("../images/inline-style.bin") }</style> text.

<link rel="canonical" href="/canonical/"><link rel="stylesheet" href="../styles/content.css"><link rel="stylesheet" href="../custom.css">
`)
	writeStrictFile(t, vault, "custom.css", `@import "styles/nested.css" screen;
body { background: url("images/custom-bg.bin?v=2#main"); }
.remote { background: url(https://cdn.example.test/remote.png); }
`)
	writeStrictFile(t, vault, "styles/nested.css", `@font-face { src: url('../fonts/site.woff2'); }`)
	writeStrictFile(t, vault, "styles/content.css", `.content { background: url('../images/content-bg.bin'); }`)
	for source, content := range map[string]string{
		"images/inline.bin":       "inline",
		"images/block.bin":        "block",
		"images/one.bin":          "one",
		"images/two-local.bin":    "two",
		"images/style.bin":        "style",
		"images/sheet.bin":        "sheet",
		"images/inline-style.bin": "inline style",
		"images/content-bg.bin":   "content background",
		"images/custom-bg.bin":    "background",
		"fonts/site.woff2":        "font",
	} {
		writeStrictFile(t, vault, source, content)
	}

	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"images/inline.bin", "images/block.bin", "images/one.bin", "images/two-local.bin", "images/style.bin", "images/sheet.bin", "images/inline-style.bin",
		"styles/content.css", "images/content-bg.bin", "custom.css", "styles/nested.css", "images/custom-bg.bin", "fonts/site.woff2",
	} {
		planned := result.Assets[source]
		if planned == nil || planned.DstPath == "" {
			t.Fatalf("asset %q was not planned: %#v", source, result.Assets)
		}
		if got := string(readBuildOutputFile(t, output, planned.DstPath)); got == "" {
			t.Fatalf("asset %q published empty bytes", source)
		}
	}

	html := string(readBuildOutputFile(t, output, "guide/article/index.html"))
	for source, suffix := range map[string]string{
		"images/inline.bin":       "?v=1#icon",
		"images/block.bin":        "",
		"images/one.bin":          "",
		"images/two-local.bin":    "",
		"images/style.bin":        "",
		"images/sheet.bin":        "#hero",
		"images/inline-style.bin": "",
		"styles/content.css":      "",
		"custom.css":              "",
	} {
		want := "../../" + result.Assets[source].DstPath + suffix
		if !strings.Contains(html, want) {
			t.Fatalf("article HTML missing planned raw resource %q: %s", want, html)
		}
	}
	if !strings.Contains(html, "https://cdn.example.test/two.bin") || !strings.Contains(html, `data-keep=yes`) || !strings.Contains(html, `/canonical/`) {
		t.Fatalf("raw HTML unrelated content changed: %s", html)
	}

	customCSS := string(readBuildOutputFile(t, output, customCSSOutputPath))
	for _, want := range []string{
		path.Base(result.Assets["styles/nested.css"].DstPath),
		path.Base(result.Assets["images/custom-bg.bin"].DstPath) + "?v=2#main",
		"https://cdn.example.test/remote.png",
	} {
		if !strings.Contains(customCSS, want) {
			t.Fatalf("custom CSS missing %q: %s", want, customCSS)
		}
	}
	nestedCSS := string(readBuildOutputFile(t, output, result.Assets["styles/nested.css"].DstPath))
	if want := path.Base(result.Assets["fonts/site.woff2"].DstPath); !strings.Contains(nestedCSS, want) {
		t.Fatalf("nested custom CSS missing %q: %s", want, nestedCSS)
	}
	contentCSS := string(readBuildOutputFile(t, output, result.Assets["styles/content.css"].DstPath))
	if want := path.Base(result.Assets["images/content-bg.bin"].DstPath); !strings.Contains(contentCSS, want) {
		t.Fatalf("raw HTML stylesheet missing planned dependency %q: %s", want, contentCSS)
	}
}

func TestStrictBuildPreservesRawTextAndUnquotedResourceAttributes(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", `---
title: Home
publish: true
---
Example: <textarea><img src="missing.bin"><style>url(missing.css)</style></textarea> after.

Example: <textarea><img src="pic.png"></textarea> after.

Example: <script>const example = '<img src="missing.bin">';</script> after.

<div id="background" style=background:&#32;url(pic.png)></div>
`)
	writeStrictFile(t, vault, "pic.png", "picture")
	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatalf("raw-text literals must not require assets: %v", err)
	}
	document, err := xhtml.Parse(bytes.NewReader(readBuildOutputFile(t, output, "index.html")))
	if err != nil {
		t.Fatal(err)
	}
	textareas := strictHTMLNodes(document, func(n *xhtml.Node) bool { return n.Type == xhtml.ElementNode && n.Data == "textarea" })
	for index, want := range []string{`<img src="missing.bin"><style>url(missing.css)</style>`, `<img src="pic.png">`} {
		if len(textareas) <= index || textareas[index].FirstChild == nil || textareas[index].FirstChild.Data != want {
			t.Fatalf("textarea %d literal was changed", index)
		}
	}
	backgrounds := strictHTMLNodes(document, func(n *xhtml.Node) bool {
		id, _ := strictHTMLAttribute(n, "id")
		return id == "background"
	})
	if len(backgrounds) != 1 {
		t.Fatalf("background element count = %d", len(backgrounds))
	}
	style, _ := strictHTMLAttribute(backgrounds[0], "style")
	if !strings.Contains(style, result.Assets["pic.png"].DstPath) || !strings.HasPrefix(style, "background:") {
		t.Fatalf("unquoted style lost its planned background: %q", style)
	}
}

func TestStrictBuildPlansNoScriptFallbackResources(t *testing.T) {
	for _, prefix := range []string{"", "Inline "} {
		t.Run(prefix, func(t *testing.T) {
			vault := t.TempDir()
			output := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n"+prefix+`<noscript><img src="pic.png"></noscript>`+"\n")
			writeStrictFile(t, vault, "pic.png", "fallback picture")
			result, err := BuildWithOptions(vault, output, Options{Strict: true})
			if err != nil {
				t.Fatal(err)
			}
			planned := result.Assets["pic.png"]
			if planned == nil {
				t.Fatal("noscript fallback image was not planned")
			}
			if got := string(readBuildOutputFile(t, output, planned.DstPath)); got != "fallback picture" {
				t.Fatalf("fallback image bytes = %q", got)
			}
			document, err := xhtml.ParseWithOptions(bytes.NewReader(readBuildOutputFile(t, output, "index.html")), xhtml.ParseOptionEnableScripting(false))
			if err != nil {
				t.Fatal(err)
			}
			images := strictHTMLNodes(document, func(n *xhtml.Node) bool {
				if n.Type != xhtml.ElementNode || n.Data != "img" {
					return false
				}
			class, _ := strictHTMLAttribute(n, "class")
				return !strings.Contains(class, "site-logo")
			})
			if len(images) != 1 {
				t.Fatalf("no-JavaScript image count = %d", len(images))
			}
			if src, _ := strictHTMLAttribute(images[0], "src"); src != planned.DstPath {
				t.Fatalf("fallback image src = %q, want %q", src, planned.DstPath)
			}
		})
	}
}

func TestLocalResourcePlanningFailuresPreservePublishedOutput(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, vault string)
		want   string
	}{
		{
			name: "raw HTML",
			mutate: func(t *testing.T, vault string) {
				writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\n<img src=\"missing.bin\">\n")
			},
			want: "raw HTML resource",
		},
		{
			name: "noscript fallback",
			mutate: func(t *testing.T, vault string) {
				writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\n<noscript><img src=\"missing.bin\"></noscript>\n")
			},
			want: "raw HTML resource",
		},
		{
			name: "custom CSS",
			mutate: func(t *testing.T, vault string) {
				writeStrictFile(t, vault, "custom.css", "body { background: url(missing.bin); }\n")
			},
			want: "CSS asset",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			vault := t.TempDir()
			output := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nPublished body\n")
			if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
				t.Fatal(err)
			}
			before := strictOutputBytes(t, output)
			test.mutate(t, vault)
			result, err := BuildWithOptions(vault, output, Options{Strict: true})
			if err == nil || result == nil || result.ErrorCount == 0 || !strings.Contains(result.Diagnostics[0].Message, test.want) {
				t.Fatalf("BuildWithOptions() = (%#v, %v), want pre-publication %s failure", result, err, test.name)
			}
			after := strictOutputBytes(t, output)
			if len(after) != len(before) {
				t.Fatalf("published file count changed: %d -> %d", len(before), len(after))
			}
			for name, data := range before {
				if !bytes.Equal(data, after[name]) {
					t.Fatalf("published output %q changed after %s planning failure", name, test.name)
				}
			}
		})
	}
}
