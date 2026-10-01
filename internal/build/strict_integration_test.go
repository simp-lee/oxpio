package build

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/simp-lee/obsite/internal/model"
	xhtml "golang.org/x/net/html"
)

func copyFixtureVault(t *testing.T, fixtureName string) string {
	t.Helper()
	srcRoot := filepath.Join("..", "..", "test", "testdata", "e2e", filepath.FromSlash(fixtureName))
	dstRoot := t.TempDir()
	stamp := time.Date(2026, time.April, 6, 12, 0, 0, 0, time.UTC)
	if err := filepath.Walk(srcRoot, func(source string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcRoot, source)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		destination := filepath.Join(dstRoot, rel)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0o644); err != nil {
			return err
		}
		return os.Chtimes(destination, stamp, stamp)
	}); err != nil {
		t.Fatalf("copy fixture %q: %v", fixtureName, err)
	}
	return dstRoot
}

func readBuildOutputFile(t *testing.T, root, relPath string) []byte {
	t.Helper()
	filePath, err := url.PathUnescape(relPath)
	if err != nil {
		t.Fatalf("decode output URL path %q: %v", relPath, err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(filePath)))
	if err != nil {
		t.Fatalf("read output %q: %v", relPath, err)
	}
	return data
}

func TestStrictBuildPreservesAuthoredHTMLComments(t *testing.T) {
	vaultPath := t.TempDir()
	writeStrictFile(t, vaultPath, "obsite.yaml", "title: Comments\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vaultPath, "_index.md", "---\ntitle: Home\npublish: true\n---\n<!-- preserve this comment --><script><!-- preserve script comment --></script><style><!-- preserve style comment --></style><p>Body</p>\n")
	outputPath := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vaultPath, outputPath, Options{}); err != nil {
		t.Fatal(err)
	}
	output := string(readBuildOutputFile(t, outputPath, "index.html"))
	for _, comment := range []string{"<!-- preserve this comment -->", "<!-- preserve script comment -->", "<!-- preserve style comment -->"} {
		if !strings.Contains(output, comment) {
			t.Fatalf("authored HTML comment %q missing from output: %s", comment, output)
		}
	}
}

func TestStrictBuildUsesCanonicalSectionPlanAndRichMarkdown(t *testing.T) {
	vaultPath := copyFixtureVault(t, "feature-vault")
	outputPath := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vaultPath, outputPath, Options{})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v", err)
	}
	if result.NotePages != 2 {
		t.Fatalf("NotePages = %d, want 2", result.NotePages)
	}
	for _, rel := range []string{"index.html", "guide/index.html", "guide/article/index.html", "roadmap/index.html", "tags/feature/index.html", "updates/index.html", "404.html", "assets/custom.css", ".obsite-cache/manifest.json", "sitemap.xml", "index.xml"} {
		if _, err := os.Stat(filepath.Join(outputPath, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("missing output %q: %v", rel, err)
		}
	}
	article := readBuildOutputFile(t, outputPath, "guide/article/index.html")
	articleHTML := string(article)
	if strings.Count(articleHTML, "<h1") != 1 {
		t.Fatalf("article H1 count = %d, want one\n%s", strings.Count(articleHTML, "<h1"), articleHTML)
	}
	if !strings.Contains(articleHTML, `article-metadata`) {
		t.Fatalf("article missing structured metadata\n%s", articleHTML)
	}
	section := string(readBuildOutputFile(t, outputPath, "index.html"))
	if strings.Count(section, "<h1") != 1 {
		t.Fatalf("home H1 count = %d, want one\n%s", strings.Count(section, "<h1"), section)
	}
	for _, want := range []string{"Feature Article", "deprecated", "Alice Example", "Developers", "2.0", "Releases", "application/ld+json", "summary_large_image", "page-banner"} {
		if !bytes.Contains(article, []byte(want)) {
			t.Fatalf("article missing %q\n%s", want, article)
		}
	}
	if strings.Contains(string(article), "assets/social") == false {
		t.Fatal("article missing generated social image")
	}
	rss := string(readBuildOutputFile(t, outputPath, "index.xml"))
	for _, want := range []string{"<link>https://example.com/blog/</link>", "<description>Integration coverage for strict section features.</description>", "<obsite:status>deprecated</obsite:status>", "<obsite:audience>Developers</obsite:audience>", "<obsite:productVersion>2.0</obsite:productVersion>", "<obsite:series>Releases</obsite:series>"} {
		if !strings.Contains(rss, want) {
			t.Fatalf("RSS missing %q: %s", want, rss)
		}
	}
	if got := readBuildOutputFile(t, outputPath, "assets/custom.css"); len(bytes.TrimSpace(got)) == 0 {
		t.Fatal("custom CSS output is empty")
	}
}

func TestStrictBuildRendersCompleteHTMLSidebarTree(t *testing.T) {
	vaultPath := t.TempDir()
	writeStrictFile(t, vaultPath, "obsite.yaml", `title: Nested Sidebar
baseURL: https://example.test/blog/
navigation: []
sidebar:
  enabled: true
`)
	writeStrictFile(t, vaultPath, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vaultPath, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	writeStrictFile(t, vaultPath, "docs/nested/_index.md", "---\ntitle: Nested\npublish: true\n---\nNested\n")
	writeStrictFile(t, vaultPath, "docs/nested/leaf.md", "---\ntitle: Leaf\npublish: true\ntype: page\n---\nLeaf\n")
	writeStrictFile(t, vaultPath, "docs/nested/sibling.md", "---\ntitle: Sibling\npublish: true\ntype: page\n---\nSibling\n")
	writeStrictFile(t, vaultPath, "docs/nested/draft.md", "---\ntitle: Draft\npublish: false\ntype: page\n---\nDraft\n")
	writeStrictFile(t, vaultPath, "other/_index.md", "---\ntitle: Other\npublish: true\n---\nOther\n")
	writeStrictFile(t, vaultPath, "other/unrelated.md", "---\ntitle: Unrelated\npublish: true\ntype: page\n---\nUnrelated\n")

	outputPath := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vaultPath, outputPath, Options{}); err != nil {
		t.Fatalf("BuildWithOptions() error = %v", err)
	}
	fallback := func(route string) string {
		t.Helper()
		page := string(readBuildOutputFile(t, outputPath, route))
		start := strings.Index(page, "data-sidebar-root")
		if start < 0 {
			t.Fatalf("HTML page %q has no Sidebar fallback", route)
		}
		end := strings.Index(page[start:], "</nav>")
		if end < 0 {
			t.Fatalf("HTML page %q has an unterminated Sidebar fallback", route)
		}
		return page[start : start+end]
	}

	rootFallback := fallback("index.html")
	for _, want := range []string{"Docs", "Nested", "Leaf", "Sibling", "Other", "Unrelated"} {
		if !strings.Contains(rootFallback, want) {
			t.Fatalf("root Sidebar fallback missing complete-tree entry %q: %s", want, rootFallback)
		}
	}

	articleFallback := fallback("docs/nested/leaf/index.html")
	for _, want := range []string{"Docs", "Nested", "Leaf", "Sibling", "Other", "Unrelated", `aria-current=page`} {
		if !strings.Contains(articleFallback, want) {
			t.Fatalf("article Sidebar fallback missing complete-tree entry %q: %s", want, articleFallback)
		}
	}

	for _, route := range []string{"index.html", "docs/index.html", "docs/nested/leaf/index.html", "404.html"} {
		assertStrictHTMLSidebarMatchesPayload(t, outputPath, route, "", "/blog/")
	}
	if strings.Contains(articleFallback, "Draft") {
		t.Fatalf("article Sidebar contains unpublished entry: %s", articleFallback)
	}
}

func assertStrictHTMLSidebarMatchesPayload(t *testing.T, outputPath, route, versionID, basePath string) {
	t.Helper()
	var payload struct {
		Default  []model.SidebarNode            `json:"default"`
		Versions map[string][]model.SidebarNode `json:"versions"`
	}
	if err := json.Unmarshal(readBuildOutputFile(t, outputPath, "assets/obsite/sidebar.json"), &payload); err != nil {
		t.Fatalf("decode Sidebar payload: %v", err)
	}
	nodes := payload.Default
	if versionID != "" {
		nodes = payload.Versions[versionID]
	}
	type sidebarLink struct {
		name string
		href string
	}
	var expected []sidebarLink
	var flatten func([]model.SidebarNode)
	flatten = func(values []model.SidebarNode) {
		for _, node := range values {
			expected = append(expected, sidebarLink{name: node.Name, href: strings.TrimSuffix(basePath, "/") + node.URL})
			flatten(node.Children)
		}
	}
	flatten(nodes)

	document, err := xhtml.Parse(bytes.NewReader(readBuildOutputFile(t, outputPath, route)))
	if err != nil {
		t.Fatalf("parse Sidebar HTML %q: %v", route, err)
	}
	var sidebarRoot *xhtml.Node
	var visit func(*xhtml.Node)
	visit = func(node *xhtml.Node) {
		if sidebarRoot != nil {
			return
		}
		if node.Type == xhtml.ElementNode && node.Data == "nav" {
			for _, attribute := range node.Attr {
				if attribute.Key == "data-sidebar-root" {
					sidebarRoot = node
					return
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(document)
	if sidebarRoot == nil {
		t.Fatalf("HTML page %q has no Sidebar root", route)
	}
	var actual []sidebarLink
	var text func(*xhtml.Node) string
	text = func(node *xhtml.Node) string {
		if node.Type == xhtml.TextNode {
			return node.Data
		}
		var value strings.Builder
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			value.WriteString(text(child))
		}
		return value.String()
	}
	var collect func(*xhtml.Node)
	collect = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && node.Data == "a" {
			var href string
			for _, attribute := range node.Attr {
				if attribute.Key == "href" {
					href = attribute.Val
				}
			}
			actual = append(actual, sidebarLink{name: strings.TrimSpace(text(node)), href: href})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(sidebarRoot)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("HTML Sidebar %q differs from shared payload: HTML=%#v payload=%#v", route, actual, expected)
	}
}

func TestStrictBuildPreservesManagedOutputOnPlanningFailure(t *testing.T) {
	vaultPath := copyFixtureVault(t, "slug-conflict-vault")
	outputPath := filepath.Join(t.TempDir(), "site")
	if err := os.MkdirAll(outputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outputPath, managedOutputMarkerFilename)
	if err := os.WriteFile(marker, []byte(managedOutputMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(outputPath, "index.html")
	before := []byte("published output")
	if err := os.WriteFile(index, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildWithOptions(vaultPath, outputPath, Options{}); err == nil {
		t.Fatal("BuildWithOptions() error = nil, want route conflict")
	}
	if got, err := os.ReadFile(index); err != nil || !bytes.Equal(got, before) {
		t.Fatalf("managed output changed after planning failure: %q, %v", got, err)
	}
}
