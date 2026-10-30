package edit

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceUsagesResolveMarkdownFrontmatterAndHTMLReferences(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "docs/article.md", "---\ntitle: Article\ncover: assets/old%20name.png\n---\n![image](../assets/old%20name.png?rev=1)\n[download](/assets/old%20name.png)\n<img src=\"../assets/old%20name.png\">\n")
	writeEditFile(t, vault, "page.html", `<a href="assets/old%20name.png">asset</a>`)
	writeEditFile(t, vault, "assets/old name.png", "image")

	usages, err := ResourceUsages(vault, "assets/old name.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 5 {
		t.Fatalf("usage count = %d, want 5: %#v", len(usages), usages)
	}
	fields := make(map[string]bool)
	for _, usage := range usages {
		fields[usage.Kind] = true
	}
	for _, kind := range []string{"frontmatter", "markdown-image", "markdown-link", "html-src", "html-href"} {
		if !fields[kind] {
			t.Fatalf("missing usage kind %q: %#v", kind, fields)
		}
	}
	updates, _, _, err := resourceRenameUpdates(vault, "assets/old name.png", "assets/new name.png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updates["docs/article.md"]), "../assets/new%20name.png") || !strings.Contains(string(updates["page.html"]), "assets/new%20name.png") {
		t.Fatalf("renamed references = %#v", updates)
	}
}

func TestResourceUsagesIncludeCSSUrls(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "styles/site.css", `body { background: url("../assets/bg.png"); }`)
	writeEditFile(t, vault, "assets/bg.png", "image")
	usages, err := ResourceUsages(vault, "assets/bg.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Kind != "css-url" {
		t.Fatalf("CSS usages = %#v", usages)
	}
}

func TestResourceRenameUpdatesSelfReferencingResources(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "styles/site.css", `body { background: url("site.css"); }`)
	writeEditFile(t, vault, "page.html", `<iframe src="page.html"></iframe>`)
	cssUpdates, _, _, err := resourceRenameUpdates(vault, "styles/site.css", "styles/renamed.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cssUpdates["styles/site.css"]), `url("renamed.css")`) {
		t.Fatalf("self-referencing CSS update = %q", cssUpdates["styles/site.css"])
	}
	htmlUpdates, _, _, err := resourceRenameUpdates(vault, "page.html", "renamed.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(htmlUpdates["page.html"]), `src="renamed.html"`) {
		t.Fatalf("self-referencing HTML update = %q", htmlUpdates["page.html"])
	}
}

func TestResourceUsagesIncludeReferenceDefinitionsAndWikiEmbeds(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "notes/article.md", "---\ntitle: Article\n---\n[download][poster]\n\n[poster]: ../assets/poster.png\n![[../assets/poster.png|Poster]]\n")
	writeEditFile(t, vault, "assets/poster.png", "image")
	usages, err := ResourceUsages(vault, "assets/poster.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 2 {
		t.Fatalf("reference-style usage count = %d, want 2: %#v", len(usages), usages)
	}
	updates, _, _, err := resourceRenameUpdates(vault, "assets/poster.png", "assets/renamed.png")
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updates["notes/article.md"])
	if !strings.Contains(updated, "![[../assets/renamed.png") || strings.Contains(updated, "![[assets/renamed.png") {
		t.Fatalf("renamed reference-style links = %q", updated)
	}
}

func TestResourceUsagesHandleBalancedParenthesesInMarkdownDestinations(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "article.md", "![hero](assets/hero(1).png)\n\n[hero]: assets/hero(1).png\n")
	writeEditFile(t, vault, "assets/hero(1).png", "image")
	usages, err := ResourceUsages(vault, "assets/hero(1).png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 2 || usages[0].Reference != "assets/hero(1).png" || usages[1].Reference != "assets/hero(1).png" {
		t.Fatalf("parenthesized Markdown usage = %#v", usages)
	}
	updates, _, _, err := resourceRenameUpdates(vault, "assets/hero(1).png", "assets/hero(2).png")
	if err != nil {
		t.Fatal(err)
	}
	updated := string(updates["article.md"])
	if strings.Count(updated, "assets/hero%282%29.png") != 2 {
		t.Fatalf("parenthesized Markdown rename = %q", updated)
	}
}

func TestResourceUsagesIncludeSrcsetInlineCSSImportsAndDefaultImage(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\ndefaultImg: assets/default.png\n")
	writeEditFile(t, vault, "page.html", `<img srcset="assets/a.png 1x, assets/b.png 2x" style="background-image: url('assets/c.png')">`)
	writeEditFile(t, vault, "styles/site.css", `@import "../assets/import.css"; body { background: url("../assets/bg.png"); }`)
	for _, name := range []string{"assets/a.png", "assets/b.png", "assets/c.png", "assets/default.png", "assets/bg.png", "assets/import.css"} {
		writeEditFile(t, vault, name, "resource")
	}
	usages, err := ResourceUsages(vault, "assets/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Kind != "html-srcset" {
		t.Fatalf("srcset usages = %#v", usages)
	}
	usages, err = ResourceUsages(vault, "assets/c.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Kind != "css-url" {
		t.Fatalf("inline CSS usages = %#v", usages)
	}
	usages, err = ResourceUsages(vault, "assets/import.css")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Kind != "css-import" {
		t.Fatalf("CSS import usages = %#v", usages)
	}
	usages, err = ResourceUsages(vault, "assets/default.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Document != "oxpio.yaml" || usages[0].Kind != "config" {
		t.Fatalf("default image usages = %#v", usages)
	}
}

func TestResourceUsagesUseConfiguredAttachmentDirectory(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\nattachments:\n  directory: assets/uploads\n")
	writeEditFile(t, vault, "notes/article.md", "---\ntitle: Article\n---\n![poster](poster.png)\n")
	writeEditFile(t, vault, "assets/uploads/poster.png", "image")
	usages, err := ResourceUsages(vault, "assets/uploads/poster.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].Document != "notes/article.md" {
		t.Fatalf("attachment usages = %#v, want one notes/article.md usage", usages)
	}
}

func TestReferenceUpdatesRollBackEarlierWritesWhenLaterPreconditionFails(t *testing.T) {
	vault := t.TempDir()
	writeEditFile(t, vault, "first.md", "first")
	writeEditFile(t, vault, "second.md", "second")
	firstBefore := []byte("first")
	updates := map[string][]byte{"first.md": []byte("updated first"), "second.md": []byte("updated second")}
	before := map[string]string{"first.md": hashResourceBytes(firstBefore), "second.md": "stale"}
	if _, err := applyFileManagerReferenceUpdates(vault, updates, before); err == nil {
		t.Fatal("reference update with a stale later precondition succeeded")
	}
	first, err := os.ReadFile(filepath.Join(vault, "first.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(firstBefore) {
		t.Fatalf("first reference document = %q, want original bytes", first)
	}
}

func TestResourceRenameUpdatesReferencesAndDeleteRejectsUsages(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeEditFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeEditFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeEditFile(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	writeEditFile(t, vault, "docs/article.md", "---\ntitle: Article\npublish: true\ntype: doc\ncover: assets/old.png\n---\n![old](../assets/old.png)\n")
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "assets", "old.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	built, err := buildCandidate(vault, output)
	if err != nil {
		t.Fatalf("%v result=%#v", err, built)
	}
	coordinator, err := NewCoordinator(vault, output, built.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	state, err := inspectFileManagerState(vault, "assets/old.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.DeletePath("assets/old.png", state.hash); err == nil {
		t.Fatal("delete of referenced resource succeeded")
	} else {
		var conflict *ResourceUsageConflictError
		if !errors.As(err, &conflict) || len(conflict.Usages) != 2 {
			t.Fatalf("delete error = %v, want usage conflict with two usages", err)
		}
	}
	if _, err := os.Stat(filepath.Join(vault, "assets", "old.png")); err != nil {
		t.Fatal(err)
	}

	renamed, err := coordinator.RenamePath("assets/old.png", "assets/new name.png", state.hash)
	if err != nil {
		var candidateErr *CandidateBuildError
		if errors.As(err, &candidateErr) {
			t.Fatalf("%v result=%#v", err, candidateErr.Result)
		}
		t.Fatal(err)
	}
	if renamed.Build == nil {
		t.Fatal("resource rename did not rebuild")
	}
	article, err := os.ReadFile(filepath.Join(vault, "docs", "article.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(article)
	if !strings.Contains(text, "cover: assets/new name.png") || !strings.Contains(text, "../assets/new%20name.png") {
		t.Fatalf("renamed references = %q", text)
	}
	if strings.Contains(text, "old.png") {
		t.Fatalf("old resource reference remains: %q", text)
	}
	if err := os.WriteFile(filepath.Join(vault, "assets", "unused.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	unused, err := inspectFileManagerState(vault, "assets/unused.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.DeletePath("assets/unused.png", unused.hash); err != nil {
		t.Fatalf("delete unused resource = %v", err)
	}
}
