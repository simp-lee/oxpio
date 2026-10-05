package build

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/render"
)

func TestStrictBuildSidebarFallbackHTMLContainsCompleteTree(t *testing.T) {
	const articleCount = 8
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", `title: Sidebar Scale
baseURL: https://scale.example/
navigation: []
sidebar:
  enabled: true
popover:
  enabled: false
related:
  enabled: false
rss:
  enabled: false
timeline:
  enabled: false
`)
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	for index := 0; index < articleCount; index++ {
		name := fmt.Sprintf("article-%03d", index)
		writeStrictFile(t, vault, name+".md", fmt.Sprintf("---\ntitle: Article %03d\npublish: true\ntype: page\n---\nBody\n", index))
	}

	output := filepath.Join(t.TempDir(), "site")
	if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
		t.Fatalf("BuildWithOptions() error = %v", err)
	}
	for index := -1; index < articleCount; index++ {
		route := "index.html"
		if index >= 0 {
			route = fmt.Sprintf("article-%03d/index.html", index)
		}
		page := string(readBuildOutputFile(t, output, route))
		start := strings.Index(page, "data-sidebar-root")
		if start < 0 {
			t.Fatalf("page %q has no Sidebar fallback", route)
		}
		end := strings.Index(page[start:], "</nav>")
		if end < 0 {
			t.Fatalf("page %q has an unterminated Sidebar fallback", route)
		}
		fallback := page[start : start+end]
		for article := 0; article < articleCount; article++ {
			want := fmt.Sprintf("Article %03d", article)
			if !strings.Contains(fallback, want) {
				t.Fatalf("page %q Sidebar fallback missing complete-tree entry %q: %s", route, want, fallback)
			}
		}
	}
}

func TestStrictBuildScaleKeepsSharedOutputsSingleOwner(t *testing.T) {
	const (
		versionCount       = 3
		sectionsPerVersion = 3
		articlesPerSection = 2
	)

	vault := t.TempDir()
	var config strings.Builder
	config.WriteString(`title: Scale Garden
baseURL: https://scale.example/
navigation:
  - name: Home
    section: .
  - name: Docs
    section: docs
sidebar:
  enabled: true
popover:
  enabled: false
related:
  enabled: false
rss:
  enabled: false
timeline:
  enabled: false
versions:
  root: docs
  default: v1
  entries:
`)
	for version := 1; version <= versionCount; version++ {
		_, _ = fmt.Fprintf(&config, "    - id: v%d\n      label: Version %d\n      source: v%d\n", version, version, version)
	}
	writeStrictFile(t, vault, "oxpio.yaml", config.String())

	sectionIndex := "---\ntitle: %s\npublish: true\nbanner: images/shared-banner.png\nbannerAlt: Shared section banner\n---\nSection landing\n"
	writeStrictFile(t, vault, "_index.md", fmt.Sprintf(sectionIndex, "Home"))
	writeStrictFile(t, vault, "docs/_index.md", fmt.Sprintf(sectionIndex, "Documentation"))
	for version := 1; version <= versionCount; version++ {
		versionRoot := fmt.Sprintf("docs/v%d", version)
		writeStrictFile(t, vault, filepath.Join(versionRoot, "_index.md"), fmt.Sprintf(sectionIndex, fmt.Sprintf("Version %d", version)))
		for section := 1; section <= sectionsPerVersion; section++ {
			sectionPath := filepath.Join(versionRoot, fmt.Sprintf("section-%d", section))
			writeStrictFile(t, vault, filepath.Join(sectionPath, "_index.md"), fmt.Sprintf(sectionIndex, fmt.Sprintf("Version %d Section %d", version, section)))
			for article := 1; article <= articlesPerSection; article++ {
				articlePath := filepath.Join(sectionPath, fmt.Sprintf("%02d-topic-%d.md", article, article))
				articleFrontmatter := fmt.Sprintf(`---
title: Version %d Section %d Article %d
publish: true
type: doc
banner: images/shared-banner.png
bannerAlt: Shared article banner
cover: images/shared-cover.png
---
Shared article body.

![Shared resource](../../../images/shared-inline.png)
`, version, section, article)
				writeStrictFile(t, vault, articlePath, articleFrontmatter)
			}
		}
	}
	writeStrictFile(t, vault, "images/shared-banner.png", string(acceptanceBannerPNG(t)))
	writeStrictFile(t, vault, "images/shared-cover.png", string(acceptanceBannerPNG(t)))
	writeStrictFile(t, vault, "images/shared-inline.png", string(acceptanceBannerPNG(t)))

	output := filepath.Join(t.TempDir(), "site")
	runtimeAssets, err := render.RuntimeAssetData()
	if err != nil {
		t.Fatalf("render.RuntimeAssetData(): %v", err)
	}
	result, err := BuildWithOptions(vault, output, Options{Concurrency: 4})
	if err != nil {
		if result == nil {
			t.Fatalf("%v", err)
		}
		t.Fatalf("%v; diagnostics = %#v", err, result.Diagnostics)
	}

	expectedSections := 2 + versionCount*(1+sectionsPerVersion)
	expectedArticles := versionCount * sectionsPerVersion * articlesPerSection
	if result.NotePages != expectedArticles {
		t.Fatalf("NotePages = %d, want %d", result.NotePages, expectedArticles)
	}
	if len(result.Index.Sections) != expectedSections {
		t.Fatalf("published sections = %d, want %d", len(result.Index.Sections), expectedSections)
	}
	if len(result.Index.Notes) != expectedArticles {
		t.Fatalf("published articles = %d, want %d", len(result.Index.Notes), expectedArticles)
	}
	for version := 1; version <= versionCount; version++ {
		prefix := fmt.Sprintf("/docs/v%d/", version)
		sectionCount, articleCount := 0, 0
		for _, section := range result.Index.Sections {
			if section != nil && strings.HasPrefix(section.Route, prefix) {
				sectionCount++
			}
		}
		for _, article := range result.Index.Notes {
			if article != nil && strings.HasPrefix(article.Route, prefix) {
				articleCount++
			}
		}
		if sectionCount != 1+sectionsPerVersion || articleCount != sectionsPerVersion*articlesPerSection {
			t.Fatalf("version %d tree = %d sections/%d articles, want %d/%d", version, sectionCount, articleCount, 1+sectionsPerVersion, sectionsPerVersion*articlesPerSection)
		}
	}

	manifest := loadStrictCacheManifest(output)
	if manifest == nil {
		t.Fatal("strict cache manifest is missing")
	}
	ownerOutputs := make(map[string][]strictCacheOutput)
	routes := make(map[string]struct{}, len(manifest.Outputs))
	for _, record := range manifest.Outputs {
		if _, exists := routes[record.Route]; exists {
			t.Fatalf("output route %q has more than one owner record", record.Route)
		}
		routes[record.Route] = struct{}{}
		ownerOutputs[record.Owner] = append(ownerOutputs[record.Owner], record)
	}
	assertScaleOwnerCount := func(owner string, want int) {
		t.Helper()
		if got := len(ownerOutputs[owner]); got != want {
			t.Fatalf("owner %q output count = %d, want %d: %#v", owner, got, want, ownerOutputs[owner])
		}
	}

	assertScaleOwnerCount("output marker", 1)
	assertScaleOwnerCount("sidebar", 1)
	assertScaleOwnerCount("built-in CSS", 1)
	assertScaleOwnerCount("sitemap", 1)
	assertScaleOwnerCount("robots", 1)
	assertScaleOwnerCount("404", 1)
	assertScaleOwnerCount("runtime", len(runtimeAssets))
	for _, runtimeAsset := range runtimeAssets {
		found := false
		for _, record := range ownerOutputs["runtime"] {
			if record.Route == runtimeAsset.OutputPath {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("runtime output %q is not owned by the shared runtime writer", runtimeAsset.OutputPath)
		}
	}

	assetSources := []string{"images/shared-banner.png", "images/shared-cover.png", "images/shared-inline.png"}
	if len(result.Assets) != len(assetSources) {
		t.Fatalf("planned asset count = %d, want %d: %#v", len(result.Assets), len(assetSources), result.Assets)
	}
	for _, source := range assetSources {
		asset := result.Assets[source]
		if asset == nil || asset.DstPath == "" {
			t.Fatalf("shared asset %q has no planned destination: %#v", source, asset)
		}
		assertScaleOwnerCount("asset:"+source, 1)
		if got := ownerOutputs["asset:"+source][0].Route; got != asset.DstPath {
			t.Fatalf("asset %q output route = %q, want planned route %q", source, got, asset.DstPath)
		}
	}
	sharedBannerRoute := result.Assets["images/shared-banner.png"].DstPath

	for relPath, section := range result.Index.Sections {
		if section == nil {
			continue
		}
		assertScaleOwnerCount("section:"+section.SourcePath, 1)
		page := string(readBuildOutputFile(t, output, render.StrictRouteOutputPath(section.Route)))
		if strings.Count(page, `class=page-banner`) != 1 || !strings.Contains(page, sharedBannerRoute) || strings.Contains(page, "og:image") {
			t.Fatalf("section %q did not keep its single shared banner without an article card", relPath)
		}
	}

	socialRoutes := make(map[string]string, expectedArticles)
	for relPath, article := range result.Index.Notes {
		if article == nil {
			continue
		}
		articleOwner := "article:" + relPath
		socialOwner := "social:" + relPath
		assertScaleOwnerCount(articleOwner, 1)
		assertScaleOwnerCount(socialOwner, 1)
		cardRoute := ownerOutputs[socialOwner][0].Route
		if _, exists := socialRoutes[cardRoute]; exists {
			t.Fatalf("social card route %q is shared by multiple articles", cardRoute)
		}
		socialRoutes[cardRoute] = relPath
		page := string(readBuildOutputFile(t, output, render.StrictRouteOutputPath(article.Route)))
		if strings.Count(page, `class=page-banner`) != 1 || !strings.Contains(page, sharedBannerRoute) {
			t.Fatalf("article %q did not keep its single shared banner", relPath)
		}
		wantCardURL := "https://scale.example/" + cardRoute
		if got := strictMetaContent(page, `property="og:image"`); got != wantCardURL {
			t.Fatalf("article %q og:image = %q, want %q", relPath, got, wantCardURL)
		}
	}
	if len(socialRoutes) != expectedArticles {
		t.Fatalf("independent social card URLs = %d, want %d", len(socialRoutes), expectedArticles)
	}

	// The manifest excludes its own cache record, so the one extra physical
	// file is the manifest itself. Every other generated file must have one
	// owner record, preventing page-count growth from duplicating shared writers.
	expectedManifestOutputs := 1 + 1 + expectedSections + expectedArticles*2 + len(assetSources) + 1 + len(runtimeAssets) + 3
	if len(manifest.Outputs) != expectedManifestOutputs {
		t.Fatalf("manifest output count = %d, want %d", len(manifest.Outputs), expectedManifestOutputs)
	}
	if files := strictOutputBytes(t, output); len(files) != expectedManifestOutputs+1 {
		t.Fatalf("physical output file count = %d, want %d", len(files), expectedManifestOutputs+1)
	}
}
