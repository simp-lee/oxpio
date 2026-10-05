package build

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"
)

func TestStrictBuildPublishesIndependentVersionTreesAndEscapedSources(t *testing.T) {
	vault := makeStrictVersionedMetadataVault(t, false, false)
	writeStrictFile(t, vault, "docs/v1/Only V1.md", `---
title: Only V1
publish: true
type: page
slug: only-v1
---
Only in version 1.
`)
	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatal(err)
	}
	docs := readBuildOutputFile(t, output, "docs/index.html")
	if !bytes.Contains(docs, []byte("Version 1")) || !bytes.Contains(docs, []byte("Version 2")) {
		t.Fatalf("version entry points missing from docs landing:\n%s", docs)
	}
	v1 := readBuildOutputFile(t, output, "docs/v1/start-v1/index.html")
	v2 := readBuildOutputFile(t, output, "docs/v2/start-v2/index.html")
	for _, page := range [][]byte{v1, v2} {
		if !bytes.Contains(page, []byte("Edit this page")) || !bytes.Contains(page, []byte("View source")) {
			t.Fatalf("source metadata missing:\n%s", page)
		}
	}
	if !strings.Contains(string(v1), `https://git.example/edit/docs/v1/Start%20Here.md?ref=main`) {
		t.Fatalf("v1 source URL not segment-escaped:\n%s", v1)
	}
	assertStrictVersionDocument(t, v1, "https://example.test/docs/docs/v1/start-v1/", []strictExpectedVersionLink{
		{label: "Version 1", href: "/docs/docs/v1/start-v1/", current: true},
		{label: "Version 2", href: "/docs/docs/v2/start-v2/"},
	})
	assertStrictVersionDocument(t, v2, "https://example.test/docs/docs/v2/start-v2/", []strictExpectedVersionLink{
		{label: "Version 1", href: "/docs/docs/v1/start-v1/"},
		{label: "Version 2", href: "/docs/docs/v2/start-v2/", current: true},
	})
	assertStrictHTMLSidebarMatchesPayload(t, output, "docs/v1/start-v1/index.html", "v1", "/docs/")
	assertStrictHTMLSidebarMatchesPayload(t, output, "docs/v2/start-v2/index.html", "v2", "/docs/")
	v1Only := readBuildOutputFile(t, output, "docs/v1/only-v1/index.html")
	assertStrictVersionDocument(t, v1Only, "https://example.test/docs/docs/v1/only-v1/", []strictExpectedVersionLink{
		{label: "Version 1", href: "/docs/docs/v1/only-v1/", current: true},
		{label: "Version 2", href: "/docs/docs/v2/"},
	})
	sitemap := string(readBuildOutputFile(t, output, "sitemap.xml"))
	if !strings.Contains(sitemap, "https://example.test/docs/docs/v1/start-v1/") || !strings.Contains(sitemap, "https://example.test/docs/docs/v2/start-v2/") {
		t.Fatalf("version routes missing from sitemap: %s", sitemap)
	}
}

type strictExpectedVersionLink struct {
	label   string
	href    string
	current bool
}

func assertStrictVersionDocument(t *testing.T, page []byte, wantCanonical string, wantLinks []strictExpectedVersionLink) {
	t.Helper()
	document, err := xhtml.Parse(bytes.NewReader(page))
	if err != nil {
		t.Fatalf("parse generated version page: %v", err)
	}

	heads := strictHTMLNodes(document, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "head"
	})
	if len(heads) != 1 {
		t.Fatalf("head element count = %d, want 1:\n%s", len(heads), page)
	}
	canonicals := strictHTMLNodes(heads[0], func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "link" && strictHTMLTokenAttribute(node, "rel", "canonical")
	})
	if len(canonicals) != 1 {
		t.Fatalf("canonical link count = %d, want 1:\n%s", len(canonicals), page)
	}
	if href, _ := strictHTMLAttribute(canonicals[0], "href"); href != wantCanonical {
		t.Fatalf("canonical href = %q, want %q", href, wantCanonical)
	}

	selectors := strictHTMLNodes(document, func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "nav" && strictHTMLTokenAttribute(node, "class", "version-selector")
	})
	if len(selectors) != 1 {
		t.Fatalf("version selector count = %d, want 1:\n%s", len(selectors), page)
	}
	links := strictHTMLNodes(selectors[0], func(node *xhtml.Node) bool {
		return node.Type == xhtml.ElementNode && node.Data == "a"
	})
	if len(links) != len(wantLinks) {
		t.Fatalf("version selector link count = %d, want %d", len(links), len(wantLinks))
	}
	for index, want := range wantLinks {
		link := links[index]
		if label := strings.TrimSpace(strictHTMLText(link)); label != want.label {
			t.Errorf("version selector link %d label = %q, want %q", index, label, want.label)
		}
		if href, _ := strictHTMLAttribute(link, "href"); href != want.href {
			t.Errorf("version selector link %q href = %q, want %q", want.label, href, want.href)
		}
		ariaCurrent, hasAriaCurrent := strictHTMLAttribute(link, "aria-current")
		if want.current && (!hasAriaCurrent || ariaCurrent != "page") {
			t.Errorf("current version link %q aria-current = %q, want %q", want.label, ariaCurrent, "page")
		}
		if !want.current && hasAriaCurrent {
			t.Errorf("non-current version link %q unexpectedly has aria-current=%q", want.label, ariaCurrent)
		}
	}
}

func strictHTMLNodes(root *xhtml.Node, match func(*xhtml.Node) bool) []*xhtml.Node {
	var result []*xhtml.Node
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if match(node) {
			result = append(result, node)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	return result
}

func strictHTMLAttribute(node *xhtml.Node, name string) (string, bool) {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val, true
		}
	}
	return "", false
}

func strictHTMLTokenAttribute(node *xhtml.Node, name, token string) bool {
	value, ok := strictHTMLAttribute(node, name)
	if !ok {
		return false
	}
	for _, field := range strings.Fields(value) {
		if field == token {
			return true
		}
	}
	return false
}

func strictHTMLText(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		text.WriteString(strictHTMLText(child))
	}
	return text.String()
}

func makeStrictVersionedMetadataVault(t *testing.T, reverse, precursor bool) string {
	t.Helper()
	vault := t.TempDir()
	writeStrictVersionedMetadataVault(t, vault, reverse, precursor)
	return vault
}

func writeStrictVersionedMetadataVault(t *testing.T, vault string, reverse, precursor bool) {
	t.Helper()
	config := `title: Versioned Determinism
baseURL: https://example.test/docs/
author: Alice Example
description: Deterministic versioned metadata fixture.
navigation:
  - name: Docs
    section: docs
source:
  editURL: https://git.example/edit/:path?ref=main
  viewURL: https://git.example/view/:path
sidebar:
  enabled: true
popover:
  enabled: true
related:
  enabled: true
  count: 1
rss:
  enabled: true
timeline:
  enabled: false
versions:
  root: docs
  default: v1
  entries:
    - id: v1
      label: Version 1
      source: v1
    - id: v2
      label: Version 2
      source: v2
`
	if reverse {
		config = `versions:
  entries:
    - source: v1
      label: Version 1
      id: v1
    - source: v2
      label: Version 2
      id: v2
  default: v1
  root: docs
timeline:
  enabled: false
rss:
  enabled: true
related:
  count: 1
  enabled: true
popover:
  enabled: true
sidebar:
  enabled: true
source:
  viewURL: https://git.example/view/:path
  editURL: https://git.example/edit/:path?ref=main
navigation:
  - section: docs
    name: Docs
description: Deterministic versioned metadata fixture.
author: Alice Example
baseURL: https://example.test/docs/
title: Versioned Determinism
`
	}

	section := func(title, description string) string {
		if reverse {
			return fmt.Sprintf("---\nbannerAlt: Shared banner\nbanner: images/banner.png\npublish: true\ndescription: %s\ntitle: %s\n---\n%s\n", description, title, description)
		}
		return fmt.Sprintf("---\ntitle: %s\ndescription: %s\npublish: true\nbanner: images/banner.png\nbannerAlt: Shared banner\n---\n%s\n", title, description, description)
	}
	article := func(version string) string {
		title, slug, status, productVersion, body := "Start "+strings.ToUpper(version), "start-"+version, "stable", strings.TrimPrefix(version, "v")+".0", "Published "+version+" content."
		if precursor && version == "v1" {
			title, slug, status, productVersion, body = "Start Preview", "start-preview", "experimental", "0.9", "Preview content."
		}
		if reverse {
			return fmt.Sprintf(`---
cover: images/banner.png
bannerAlt: Article banner
banner: images/banner.png
series: Releases
productVersion: %q
audience: Developers
status: %s
reviewed: 2026-04-09
author: Alice Example
order: 1
aliases:
  - %s-alias
tags:
  - deterministic
  - versioned
updated: 2026-04-08
date: 2026-04-06
slug: %s
type: post
publish: true
description: Rich metadata for %s.
title: %s
---
# %s

%s
`, productVersion, status, version, slug, version, title, title, body)
		}
		return fmt.Sprintf(`---
title: %s
description: Rich metadata for %s.
publish: true
type: post
slug: %s
date: 2026-04-06
updated: 2026-04-08
tags:
  - deterministic
  - versioned
aliases:
  - %s-alias
order: 1
author: Alice Example
reviewed: 2026-04-09
status: %s
audience: Developers
productVersion: %q
series: Releases
banner: images/banner.png
bannerAlt: Article banner
cover: images/banner.png
---
# %s

%s
`, title, version, slug, version, status, productVersion, title, body)
	}

	files := []struct {
		name    string
		content string
	}{
		{"oxpio.yaml", config},
		{"_index.md", section("Home", "Home landing")},
		{"docs/_index.md", section("Docs", "Documentation landing")},
		{"docs/v1/_index.md", section("Version 1", "Version 1 landing")},
		{"docs/v1/Start Here.md", article("v1")},
		{"docs/v2/_index.md", section("Version 2", "Version 2 landing")},
		{"docs/v2/Start Here.md", article("v2")},
		{"images/banner.png", string(acceptanceBannerPNG(t))},
	}
	if reverse {
		for left, right := 0, len(files)-1; left < right; left, right = left+1, right-1 {
			files[left], files[right] = files[right], files[left]
		}
	}
	for _, file := range files {
		writeStrictFile(t, vault, file.name, file.content)
	}

	stamp := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	if err := filepath.Walk(vault, func(name string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return os.Chtimes(name, stamp, stamp)
	}); err != nil {
		t.Fatalf("set deterministic fixture times: %v", err)
	}
}

func writeStrictFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
