package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestStrictBuildPreservesUnicodeSourcePaths(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", `title: Unicode
baseURL: https://example.test/
navigation:
  - name: Home
    section: .
source:
  viewURL: https://git.example/view/:path
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "Ａ.md", "---\ntitle: Fullwidth\npublish: true\ntype: page\n---\nContent\n")
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	page := string(readBuildOutputFile(t, output, "A/index.html"))
	if !strings.Contains(page, `https://git.example/view/%EF%BC%A1.md`) {
		t.Fatalf("source link did not preserve the source filename:\n%s", page)
	}
	if strings.Contains(page, `https://git.example/view/A.md`) {
		t.Fatalf("source link normalized the source filename:\n%s", page)
	}
}

func TestStrictBuildMarksSameSiteAbsoluteNavigationCurrent(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", `title: Navigation
baseURL: https://example.test/sub/
navigation:
  - name: Current
    url: https://example.test/sub/article/
  - name: Outside Base
    url: https://example.test/article/
  - name: External
    url: https://other.test/sub/article/
  - name: Encoded Separator
    url: https://example.test/sub%2Farticle/
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\n---\nContent\n")
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}

	page := string(readBuildOutputFile(t, output, "article/index.html"))
	start := strings.Index(page, `aria-label="Global navigation"`)
	if start < 0 {
		t.Fatalf("article has no global navigation:\n%s", page)
	}
	end := strings.Index(page[start:], "</nav>")
	if end < 0 {
		t.Fatalf("article has unterminated global navigation:\n%s", page)
	}
	navigation := page[start : start+end]
	if !strings.Contains(navigation, `href=https://example.test/sub/article/ aria-current=page>Current</a>`) {
		t.Fatalf("same-site absolute navigation target is not current or its href changed: %s", navigation)
	}
	if strings.Count(navigation, `aria-current=page`) != 1 {
		t.Fatalf("global navigation current-page count = %d, want 1: %s", strings.Count(navigation, `aria-current=page`), navigation)
	}
	for _, href := range []string{"https://example.test/article/", "https://other.test/sub/article/", "https://example.test/sub%2Farticle/"} {
		if !strings.Contains(navigation, "href="+href) {
			t.Fatalf("absolute navigation href %q was not preserved: %s", href, navigation)
		}
	}
}

func TestStrictBuildMarksPathlessAbsoluteHomeNavigationCurrent(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation:\n  - name: Home\n    url: https://example.test\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	page := string(readBuildOutputFile(t, output, "index.html"))
	if !strings.Contains(page, `href=https://example.test aria-current=page>Home</a>`) {
		t.Fatalf("pathless homepage navigation is not current or its href changed: %s", page)
	}
}

func TestStrictBuildResolvesLinksToGeneratedTagPages(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Tags\nbaseURL: https://example.test/\nnavigation:\n  - name: Home\n    section: .\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: page\ntags: [foo]\n---\n[Foo](/tags/foo/)\n[Foo without slash](/tags/foo)\n")
	output := filepath.Join(t.TempDir(), "site")
	if result, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatalf("BuildWithOptions() error = %v; diagnostics = %#v", err, result.Diagnostics)
	} else if result.WarningCount != 0 {
		t.Fatalf("WarningCount = %d, want no warning for generated tag link; diagnostics = %#v", result.WarningCount, result.Diagnostics)
	}
	page := string(readBuildOutputFile(t, output, "article/index.html"))
	if !strings.Contains(page, `href=/tags/foo/>Foo</a>`) || !strings.Contains(page, `href=/tags/foo/>Foo without slash</a>`) {
		t.Fatalf("tag link was not preserved as a generated route:\n%s", page)
	}
	if _, err := os.Stat(filepath.Join(output, "tags", "foo", "index.html")); err != nil {
		t.Fatalf("generated tag page is missing: %v", err)
	}
}

func TestStrictBuildResolvesLinksToGeneratedTimelineAndNotFoundPages(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", `title: Generated pages
baseURL: https://example.test/docs/
navigation: []
pagination:
  pageSize: 1
timeline:
  enabled: true
  path: updates
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, "links.md", `---
title: Links
publish: true
type: page
---
[Timeline](/updates/)
[Encoded timeline](/%75pdates/)
[Timeline page 2](/updates/page/2/)
[Not found](/404.html)
[Relative timeline](../updates/)
[Relative timeline page 2](../updates/page/2/)
[Relative not found](../404.html)
`)
	writeStrictFile(t, vault, "first.md", "---\ntitle: First\npublish: true\ntype: post\ndate: 2026-04-05\n---\nFirst\n")
	writeStrictFile(t, vault, "second.md", "---\ntitle: Second\npublish: true\ntype: post\ndate: 2026-04-06\n---\nSecond\n")

	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{Strict: true})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v; diagnostics = %#v", err, result.Diagnostics)
	}
	if result.WarningCount != 0 || result.ErrorCount != 0 {
		t.Fatalf("diagnostic counts = %d warning(s), %d error(s), want none; diagnostics = %#v", result.WarningCount, result.ErrorCount, result.Diagnostics)
	}

	page := string(readBuildOutputFile(t, output, "links/index.html"))
	for _, href := range []string{
		"/docs/updates/",
		"/docs/updates/page/2/",
		"/docs/404.html",
		"../updates/",
		"../updates/page/2/",
		"../404.html",
	} {
		if !strings.Contains(page, "href="+href) {
			t.Fatalf("generated page link %q is missing:\n%s", href, page)
		}
	}
	if !strings.Contains(page, "href=/docs/updates/>Encoded timeline</a>") {
		t.Fatalf("encoded generated route was not rendered canonically:\n%s", page)
	}
	for _, relPath := range []string{"updates/index.html", "updates/page/2/index.html", "404.html"} {
		if _, err := os.Stat(filepath.Join(output, filepath.FromSlash(relPath))); err != nil {
			t.Fatalf("generated page %q is missing: %v", relPath, err)
		}
	}
}

func TestStrictBuildUsesCanonicalRoutesForNestedMarkdownLinks(t *testing.T) {
	vault := copyFixtureVault(t, "runtime-vault")
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "manual file.pdf", "manual attachment")
	writeStrictFile(t, vault, "child/child.md", "---\ntitle: Child Article\npublish: true\ntype: doc\n---\n[Reference](../reference.md#reference) [Manual](../manual%20file.pdf)\n")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	page := readBuildOutputFile(t, output, "child/child/index.html")
	if !bytes.Contains(page, []byte(`href=../../reference/`)) {
		t.Fatalf("nested link did not use canonical target route:\n%s", page)
	}
	if !bytes.Contains(page, []byte(`href=../../assets/manual%20file.`)) {
		t.Fatalf("nested attachment link did not use the asset planner:\n%s", page)
	}
	if entries, err := filepath.Glob(filepath.Join(output, "assets", "manual file.*.pdf")); err != nil || len(entries) != 1 {
		t.Fatalf("missing published content-addressed attachment: %v, err=%v", entries, err)
	}
	if !bytes.Contains(page, []byte(`data-popover-path=reference.md`)) {
		t.Fatalf("nested link did not receive its popover target:\n%s", page)
	}
	if _, err := os.Stat(filepath.Join(output, "_popover", "reference.md", "index.json")); err != nil {
		t.Fatalf("missing popover payload: %v", err)
	}
}

func TestStrictPopoverPathsArePrefixFree(t *testing.T) {
	output := t.TempDir()
	index := &model.VaultIndex{Notes: map[string]*model.Note{
		"A.md":               {RelPath: "A.md"},
		"foo.md":             {RelPath: "foo.md"},
		"foo.md.json/bar.md": {RelPath: "foo.md.json/bar.md"},
		"Ａ.md":               {RelPath: "Ａ.md"},
	}}

	if err := writeStrictPopoverPayloads(output, index, newStrictOutputRegistry("", nil)); err != nil {
		t.Fatalf("writeStrictPopoverPayloads() error = %v", err)
	}
	for _, relPath := range []string{
		filepath.Join("_popover", "A.md", "index.json"),
		filepath.Join("_popover", "foo.md", "index.json"),
		filepath.Join("_popover", "foo.md.json", "bar.md", "index.json"),
		filepath.Join("_popover", "Ａ.md", "index.json"),
	} {
		if _, err := os.Stat(filepath.Join(output, relPath)); err != nil {
			t.Fatalf("missing popover payload %q: %v", relPath, err)
		}
	}
}
