package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	internalasset "github.com/simp-lee/oxpio/internal/asset"
	d "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/link"
	"github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/recommend"
	"github.com/simp-lee/oxpio/internal/render"
	"github.com/simp-lee/oxpio/internal/siteplan"
	"github.com/simp-lee/oxpio/internal/slug"
	"github.com/simp-lee/oxpio/internal/social"
	"github.com/tdewolff/minify/v2"
	minhtml "github.com/tdewolff/minify/v2/html"
	xhtml "golang.org/x/net/html"
)

// buildStrictSite publishes the normalized section model through the same
// managed staging publisher used by the existing build foundation.
func buildStrictSite(planned *siteplan.Result, vaultPath, outputPath string, diagnosticsWriter io.Writer, concurrency ...int) (result *BuildResult, err error) {
	return buildStrictSiteWithTransactionTracking(planned, vaultPath, outputPath, diagnosticsWriter, nil, concurrency...)
}

func buildStrictSiteWithTransactionTracking(planned *siteplan.Result, vaultPath, outputPath string, diagnosticsWriter io.Writer, trackTransactionPath func(string), concurrency ...int) (result *BuildResult, err error) {
	workerConcurrency := 0
	if len(concurrency) > 0 {
		workerConcurrency = concurrency[0]
	}
	result = &BuildResult{}
	if planned == nil || planned.Plan == nil {
		return result, fmt.Errorf("strict site plan is required")
	}
	plan := planned.Plan
	graph, relatedByPath, relationErr := buildStrictRelations(planned, workerConcurrency)
	if relationErr != nil {
		return result, relationErr
	}
	result.Graph = graph
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return result, err
	}
	result.OutputPath = boundary.OutputPath
	publisher, err := prepareStagedOutputPublisherWithTransactionTracking(boundary.VaultPath, boundary.OutputPath, trackTransactionPath)
	if err != nil {
		return result, err
	}
	previousCache := loadStrictCacheManifest(boundary.OutputPath)
	defer func() {
		if finalizeErr := publisher.Finalize(err == nil); finalizeErr != nil {
			if err == nil {
				err = finalizeErr
			} else {
				err = fmt.Errorf("publish strict site: %w; cleanup: %v", err, finalizeErr)
			}
		}
		if publisher.cleanupErr != nil {
			result.OutputCleanupError = publisher.cleanupErr
			if diagnosticsWriter != nil {
				_, _ = fmt.Fprintf(diagnosticsWriter, "cleanup %s: %s\n", d.KindOutputCleanup, publisher.cleanupErr)
			}
		}
	}()
	staging := publisher.OutputPath()
	outputs := newStrictOutputRegistry(boundary.OutputPath, previousCache)
	if err := outputs.write(staging, managedOutputMarkerFilename, "output marker", []byte(managedOutputMarkerContents)); err != nil {
		return result, err
	}
	if err := writeStrictConfiguredAssets(boundary.VaultPath, staging, plan, outputs); err != nil {
		return result, err
	}
	if planned.Index == nil {
		return result, fmt.Errorf("strict site index is required")
	}
	result.Index = planned.Index
	rebindStrictPlanNotes(plan, planned.Index)
	assets, err := prepareStrictAssets(boundary.VaultPath, plan)
	if err != nil {
		return result, err
	}
	overrides := strictPlannedAssetOverrides(plan)
	for source, planned := range strictPlannedAssets(plan) {
		plannedAsset := planned.Asset
		assets[source] = &plannedAsset
	}
	reservedAssetOutputs := strictReservedAssetOutputs(plan)
	assetCollector, err := internalasset.NewCollectorWithOverrides(boundary.VaultPath, assets, reservedAssetOutputs, planned.Scan.ResourceFiles, overrides)
	if err != nil {
		return result, fmt.Errorf("plan strict assets: %w", err)
	}
	applyStrictPlannedDestinations(assets, assetCollector.PlanDestinations(assets))
	applyStrictAssetURLs(plan, assets)
	lookupDigests, err := strictCacheLookupDigests(planned.Index)
	if err != nil {
		return result, err
	}
	if plan.Config.Sidebar.Enabled {
		payload := strictSidebarPayload{Default: render.StrictSidebarNodes(plan, ""), Versions: make(map[string][]model.SidebarNode)}
		for _, version := range plan.Versions {
			if version != nil && version.Root != nil && version.Root.EffectivePublish && version.Root.Route != "" {
				payload.Versions[version.ID] = render.StrictSidebarNodes(plan, version.ID)
			}
		}
		data, sidebarErr := json.Marshal(payload)
		if sidebarErr != nil {
			return result, sidebarErr
		}
		if err := outputs.dependency("sidebar", "oxpio.yaml", payload); err != nil {
			return result, err
		}
		if writeErr := outputs.write(staging, "assets/oxpio/sidebar.json", "sidebar", data); writeErr != nil {
			return result, writeErr
		}
	}
	for _, section := range plan.Sections {
		if section == nil || section.Route == "" {
			continue
		}
		pageAssets := newStrictCacheAssetSink(assetCollector)
		data, renderErr := render.RenderStrictSection(plan, section, planned.Index, pageAssets)
		if renderErr != nil {
			return result, fmt.Errorf("render section %q: %w", section.RelPath, renderErr)
		}
		owner := "section:" + section.SourcePath
		if dependencyErr := outputs.dependency(owner, section.SourcePath, strictCacheSectionPageInput(plan, section, lookupDigests.forEmbeds(len(section.Embeds) > 0), pageAssets.destinations)); dependencyErr != nil {
			return result, dependencyErr
		}
		if writeErr := writeStrictHTML(outputs, staging, render.StrictRouteOutputPath(section.Route), owner, data); writeErr != nil {
			return result, writeErr
		}
	}
	socialCards, cardErr := generateStrictSocialCards(plan, boundary.VaultPath, workerConcurrency)
	if cardErr != nil {
		return result, cardErr
	}
	for _, article := range plan.Articles {
		if article == nil || article.Route == "" {
			continue
		}
		section := strictArticleSection(plan, article)
		card, ok := socialCards[article.RelPath]
		if !ok {
			return result, fmt.Errorf("social card for %q was not generated", article.RelPath)
		}
		if err := outputs.dependencyBytes("social:"+article.RelPath, article.RelPath, card.CanonicalJSON); err != nil {
			return result, err
		}
		article.SocialImage = card.Path
		if writeErr := outputs.write(staging, card.Path, "social:"+article.RelPath, card.PNG); writeErr != nil {
			return result, writeErr
		}
		previous, next, position, total := strictReadingFlow(section, article)
		backlinks := strictBacklinks(planned.Index, graph, article)
		related := relatedByPath[article.RelPath]
		pageAssets := newStrictCacheAssetSink(assetCollector)
		data, renderErr := render.RenderStrictArticle(plan, article, previous, next, position, total, backlinks, related, planned.Index, pageAssets)
		if renderErr != nil {
			return result, fmt.Errorf("render article %q: %w", article.RelPath, renderErr)
		}
		owner := "article:" + article.RelPath
		if dependencyErr := outputs.dependency(owner, article.RelPath, strictCacheArticlePageInput(plan, article, section, previous, next, position, total, backlinks, related, lookupDigests.forEmbeds(len(article.Embeds) > 0), pageAssets.destinations)); dependencyErr != nil {
			return result, dependencyErr
		}
		if writeErr := writeStrictHTML(outputs, staging, render.StrictRouteOutputPath(article.Route), owner, data); writeErr != nil {
			return result, writeErr
		}
		result.NotePages++
	}
	if plan.Config.Popover.Enabled {
		if err := writeStrictPopoverPayloads(staging, planned.Index, outputs); err != nil {
			return result, err
		}
	}
	for _, tag := range strictBuildTags(planned.Index.Tags) {
		notes := make([]*model.Note, 0, len(tag.Notes))
		for _, relPath := range tag.Notes {
			if note := planned.Index.Notes[relPath]; note != nil {
				notes = append(notes, note)
			}
		}
		data, renderErr := render.RenderStrictTag(plan, tag, notes)
		if renderErr != nil {
			return result, fmt.Errorf("render tag %q: %w", tag.Name, renderErr)
		}
		route := "/" + slug.EncodePath(tag.Slug) + "/"
		owner := "tag:" + tag.Name
		dependency := strictCacheHTMLBase(plan, route, "Tag: "+tag.Name, "", "", "", true)
		dependency.Entries = strictCachePageEntries(notes)
		if dependencyErr := outputs.dependency(owner, tag.Name, dependency); dependencyErr != nil {
			return result, dependencyErr
		}
		if writeErr := writeStrictHTML(outputs, staging, render.StrictRouteOutputPath(route), owner, data); writeErr != nil {
			return result, writeErr
		}
		result.TagPages++
	}
	if plan.Timeline != nil {
		for _, page := range plan.Timeline.Pages {
			owner := "timeline"
			if page.Number > 1 {
				owner += ":" + strconv.Itoa(page.Number)
			}
			pagePosts := plan.Posts[page.Start:page.End]
			data, renderErr := render.RenderStrictTimeline(plan, page.Route, pagePosts)
			if renderErr != nil {
				return result, fmt.Errorf("render timeline: %w", renderErr)
			}
			dependency := strictCacheHTMLBase(plan, page.Route, "Recent articles", "", "", "", true)
			dependency.Entries = strictCachePageEntries(pagePosts)
			dependency.TimelinePageCount = len(plan.Timeline.Pages)
			if dependencyErr := outputs.dependency(owner, "oxpio.yaml", dependency); dependencyErr != nil {
				return result, dependencyErr
			}
			if writeErr := writeStrictHTML(outputs, staging, render.StrictRouteOutputPath(page.Route), owner, data); writeErr != nil {
				return result, writeErr
			}
		}
	}
	allAssets := assetCollector.Snapshot()
	for source, seed := range assets {
		if allAssets[source] == nil {
			clone := *seed
			allAssets[source] = &clone
		}
	}
	applyStrictPlannedDestinations(allAssets, assetCollector.PlanDestinations(allAssets))
	for source, planned := range strictPlannedAssets(plan) {
		plannedAsset := planned.Asset
		allAssets[source] = &plannedAsset
	}
	assetSources := make([]string, 0, len(allAssets))
	for source := range allAssets {
		assetSources = append(assetSources, source)
	}
	sort.Strings(assetSources)
	distinct := make(map[string]bool, len(assetSources))
	for _, source := range assetSources {
		distinct[source] = strictDistinctAssetSource(plan, source)
	}
	if err := internalasset.ValidateDestinationCollisions(boundary.VaultPath, allAssets, distinct, overrides); err != nil {
		return result, err
	}
	writtenAssetHashes := make(map[string]string, len(assetSources))
	for _, source := range assetSources {
		asset := allAssets[source]
		if asset == nil || asset.DstPath == "" {
			continue
		}
		var data []byte
		if planned := strictPlannedAsset(plan, source); planned != nil {
			data = planned.Data
		} else {
			_, sourceData, _, readErr := internalfsutil.ReadContainedRegularFile(boundary.VaultPath, source)
			if readErr != nil {
				return result, fmt.Errorf("read asset %q: %w", source, readErr)
			}
			data = sourceData
		}
		hash := sha256.Sum256(data)
		hashValue := fmt.Sprintf("%x", hash)
		if existing, ok := writtenAssetHashes[asset.DstPath]; ok && existing == hashValue {
			continue
		}
		if err := outputs.write(staging, asset.DstPath, "asset:"+source, data); err != nil {
			return result, err
		}
		writtenAssetHashes[asset.DstPath] = hashValue
	}
	applyStrictAssetURLs(plan, allAssets)
	result.Assets = allAssets
	styleData, err := render.StyleCSSData()
	if err != nil {
		return result, fmt.Errorf("read style.css: %w", err)
	}
	if err := outputs.dependencyBytes("built-in CSS", "style.css", styleData); err != nil {
		return result, err
	}
	if err := outputs.write(staging, "style.css", "built-in CSS", styleData); err != nil {
		return result, err
	}
	runtimeAssets, err := render.RuntimeAssetData()
	if err != nil {
		return result, fmt.Errorf("read runtime assets: %w", err)
	}
	for _, runtimeAsset := range runtimeAssets {
		if err := outputs.dependencyBytes("runtime", runtimeAsset.OutputPath, runtimeAsset.Data); err != nil {
			return result, err
		}
		if err := outputs.write(staging, runtimeAsset.OutputPath, "runtime", runtimeAsset.Data); err != nil {
			return result, err
		}
	}
	if err := writeStrictMetadataOutputs(staging, plan, planned.Index, outputs); err != nil {
		return result, err
	}
	if err := writeStrictCacheManifest(staging, plan, planned.Index, allAssets, outputs); err != nil {
		return result, err
	}
	if diagnosticsWriter != nil {
		_ = diagnosticsWriter
	}
	return result, nil
}

func strictPlannedAssets(plan *model.SitePlan) map[string]*model.PlannedAsset {
	if plan == nil {
		return nil
	}
	planned := make(map[string]*model.PlannedAsset, len(plan.ThemeAssets)+len(plan.VaultCSSAssets))
	for source, item := range plan.ThemeAssets {
		if item != nil {
			planned[source] = item
		}
	}
	for source, item := range plan.VaultCSSAssets {
		if item != nil {
			planned[source] = item
		}
	}
	return planned
}

func strictPlannedAsset(plan *model.SitePlan, source string) *model.PlannedAsset {
	if plan == nil {
		return nil
	}
	if planned := plan.VaultCSSAssets[source]; planned != nil {
		return planned
	}
	return plan.ThemeAssets[source]
}

func strictPlannedAssetOverrides(plan *model.SitePlan) map[string][]byte {
	overrides := make(map[string][]byte)
	for source, planned := range strictPlannedAssets(plan) {
		overrides[source] = planned.Data
	}
	return overrides
}

func strictDistinctAssetSource(plan *model.SitePlan, source string) bool {
	if plan == nil || source == "" {
		return false
	}
	for _, section := range plan.Sections {
		if section != nil && section.Banner == source {
			return true
		}
	}
	for _, article := range plan.Articles {
		if article != nil && (article.Frontmatter.Banner == source || article.Frontmatter.Cover == source) {
			return true
		}
	}
	return false
}

type strictCacheAssetSink struct {
	delegate     markdown.AssetSink
	destinations map[string]string
}

func newStrictCacheAssetSink(delegate markdown.AssetSink) *strictCacheAssetSink {
	return &strictCacheAssetSink{delegate: delegate, destinations: make(map[string]string)}
}

func (sink *strictCacheAssetSink) Register(source string) string {
	if sink == nil || sink.delegate == nil {
		return ""
	}
	destination := sink.delegate.Register(source)
	if destination != "" {
		sink.destinations[source] = destination
	}
	return destination
}

type strictCachePageEntry struct {
	RelPath     string `json:"relPath"`
	Route       string `json:"route"`
	Title       string `json:"title"`
	Summary     string `json:"summary,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	VersionID   string `json:"versionID,omitempty"`
}

type strictCacheVersionEntry struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	RootRoute string `json:"rootRoute"`
}

type strictCacheLookupEntry struct {
	RelPath   string            `json:"relPath"`
	Route     string            `json:"route"`
	Title     string            `json:"title"`
	VersionID string            `json:"versionID,omitempty"`
	Aliases   []string          `json:"aliases,omitempty"`
	Headings  []model.Heading   `json:"headings,omitempty"`
	Metadata  model.Frontmatter `json:"metadata,omitempty"`
	Content   []byte            `json:"content,omitempty"`
}

type strictCacheLookupDigest struct {
	base     string
	embedded string
}

func (digest strictCacheLookupDigest) forEmbeds(includeContent bool) string {
	if includeContent {
		return digest.embedded
	}
	return digest.base
}

type strictCacheSectionInput struct {
	RelPath       string                 `json:"relPath"`
	SourcePath    string                 `json:"sourcePath"`
	Route         string                 `json:"route"`
	Title         string                 `json:"title"`
	Description   string                 `json:"description,omitempty"`
	Order         *int                   `json:"order,omitempty"`
	Banner        string                 `json:"banner,omitempty"`
	BannerURL     string                 `json:"bannerURL,omitempty"`
	BannerAlt     string                 `json:"bannerAlt,omitempty"`
	RawContent    []byte                 `json:"rawContent"`
	Children      []strictCachePageEntry `json:"children,omitempty"`
	Articles      []strictCachePageEntry `json:"articles,omitempty"`
	VersionID     string                 `json:"versionID,omitempty"`
	VersionRoutes map[string]string      `json:"versionRoutes,omitempty"`
	Breadcrumbs   []model.Breadcrumb     `json:"breadcrumbs,omitempty"`
}

type strictCacheArticleInput struct {
	RelPath       string            `json:"relPath"`
	Route         string            `json:"route"`
	SectionPath   string            `json:"sectionPath"`
	VersionID     string            `json:"versionID,omitempty"`
	VersionRoutes map[string]string `json:"versionRoutes,omitempty"`
	SocialImage   string            `json:"socialImage"`
	BannerURL     string            `json:"bannerURL,omitempty"`
	CoverURL      string            `json:"coverURL,omitempty"`
	Frontmatter   model.Frontmatter `json:"frontmatter"`
	Aliases       []string          `json:"aliases,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	RawContent    []byte            `json:"rawContent"`
}

// strictCacheHTMLConfig is the shared shell projection. Page-specific
// features such as source links, version selectors, and timeline pagination
// are added to strictCacheHTMLInput only for pages that render them.
type strictCacheHTMLConfig struct {
	Title              string                 `json:"title"`
	BaseURL            string                 `json:"baseURL"`
	Language           string                 `json:"language"`
	Navigation         []model.NavigationItem `json:"navigation,omitempty"`
	DefaultImgURL      string                 `json:"defaultImgURL,omitempty"`
	DefaultImgExternal bool                   `json:"defaultImgExternal,omitempty"`
	CustomCSS          bool                   `json:"customCSS,omitempty"`
	ThemeCSS           string                 `json:"themeCSS,omitempty"`
	ThemeSlots         string                 `json:"themeSlots,omitempty"`
	ThemeAuthor        string                 `json:"themeAuthor,omitempty"`
	ThemeDescription   string                 `json:"themeDescription,omitempty"`
	Popover            bool                   `json:"popover,omitempty"`
}

type strictCacheSourceInput struct {
	EditURL string `json:"editURL,omitempty"`
	ViewURL string `json:"viewURL,omitempty"`
}

type strictCacheHTMLInput struct {
	Config            strictCacheHTMLConfig     `json:"config"`
	ThemeAssetURLs    map[string]string         `json:"themeAssetURLs,omitempty"`
	Route             string                    `json:"route"`
	Title             string                    `json:"title"`
	Description       string                    `json:"description,omitempty"`
	SourcePath        string                    `json:"sourcePath,omitempty"`
	VersionID         string                    `json:"versionID,omitempty"`
	Source            *strictCacheSourceInput   `json:"source,omitempty"`
	Versions          []strictCacheVersionEntry `json:"versions,omitempty"`
	Sidebar           []model.SidebarNode       `json:"sidebar,omitempty"`
	Section           *strictCacheSectionInput  `json:"section,omitempty"`
	Article           *strictCacheArticleInput  `json:"article,omitempty"`
	Entries           []strictCachePageEntry    `json:"entries,omitempty"`
	Previous          *strictCachePageEntry     `json:"previous,omitempty"`
	Next              *strictCachePageEntry     `json:"next,omitempty"`
	Position          int                       `json:"position,omitempty"`
	Total             int                       `json:"total,omitempty"`
	TimelinePageCount int                       `json:"timelinePageCount,omitempty"`
	Backlinks         []strictCachePageEntry    `json:"backlinks,omitempty"`
	Related           []strictCachePageEntry    `json:"related,omitempty"`
	MarkdownAssets    map[string]string         `json:"markdownAssets,omitempty"`
	LookupDigest      string                    `json:"lookupDigest,omitempty"`
}

func strictCacheHTMLBase(plan *model.SitePlan, route, title, description, sourcePath, versionID string, includeDefaultImage bool) strictCacheHTMLInput {
	input := strictCacheHTMLInput{
		Config:      strictCacheConfig(plan),
		Route:       route,
		Title:       title,
		Description: description,
		SourcePath:  sourcePath,
		VersionID:   versionID,
	}
	if plan == nil {
		return input
	}
	if !includeDefaultImage || input.Config.DefaultImgURL == "" {
		input.Config.DefaultImgURL = ""
		input.Config.DefaultImgExternal = false
	}
	if sourcePath != "" && (plan.Config.Source.EditURL != "" || plan.Config.Source.ViewURL != "") {
		input.Source = &strictCacheSourceInput{EditURL: plan.Config.Source.EditURL, ViewURL: plan.Config.Source.ViewURL}
	}
	if versionID != "" {
		input.Versions = strictCacheVersionEntries(plan)
	}
	if strings.TrimSpace(plan.Config.ThemeSlots) != "" {
		input.ThemeAssetURLs = make(map[string]string, len(plan.ThemeAssetURLs))
		for name, assetRoute := range plan.ThemeAssetURLs {
			input.ThemeAssetURLs[name] = assetRoute
		}
	}
	if plan.Config.Sidebar.Enabled {
		nodes := render.StrictSidebarNodes(plan, versionID)
		if len(nodes) > 0 {
			input.Sidebar = nodes
		}
	}
	return input
}

func strictCacheConfig(plan *model.SitePlan) strictCacheHTMLConfig {
	if plan == nil {
		return strictCacheHTMLConfig{}
	}
	config := strictCacheHTMLConfig{
		Title:              plan.Config.Title,
		BaseURL:            plan.Config.BaseURL,
		Language:           plan.Config.Language,
		DefaultImgURL:      plan.Config.DefaultImgURL,
		DefaultImgExternal: plan.Config.DefaultImgExternal,
		CustomCSS:          plan.Config.CustomCSS != "",
		ThemeCSS:           plan.Config.ThemeCSS,
		Popover:            plan.Config.Popover.Enabled,
	}
	config.Navigation = append([]model.NavigationItem(nil), plan.Config.Navigation...)
	if strings.TrimSpace(plan.Config.ThemeSlots) != "" {
		config.ThemeSlots = plan.Config.ThemeSlots
		config.ThemeAuthor = plan.Config.Author
		config.ThemeDescription = plan.Config.Description
	}
	return config
}

func strictCacheVersionEntries(plan *model.SitePlan) []strictCacheVersionEntry {
	if plan == nil {
		return nil
	}
	entries := make([]strictCacheVersionEntry, 0, len(plan.Versions))
	for _, version := range plan.Versions {
		if version == nil || version.Root == nil || !version.Root.EffectivePublish || version.Root.Route == "" {
			continue
		}
		entries = append(entries, strictCacheVersionEntry{ID: version.ID, Label: version.Label, RootRoute: version.Root.Route})
	}
	return entries
}

func strictCacheSectionVersionEntries(plan *model.SitePlan, section *model.Section) []strictCacheVersionEntry {
	if plan == nil || section == nil {
		return nil
	}
	entries := make([]strictCacheVersionEntry, 0)
	for _, version := range plan.Versions {
		if version == nil || version.Root == nil || !version.Root.EffectivePublish || version.Root.Route == "" {
			continue
		}
		if path.Dir(version.Root.RelPath) != section.RelPath && !strings.HasPrefix(version.Root.RelPath, section.RelPath+"/") {
			continue
		}
		entries = append(entries, strictCacheVersionEntry{ID: version.ID, Label: version.Label, RootRoute: version.Root.Route})
	}
	return entries
}

func strictCachePageEntries(notes []*model.Note) []strictCachePageEntry {
	entries := make([]strictCachePageEntry, 0, len(notes))
	for _, note := range notes {
		if note != nil {
			entries = append(entries, *strictCacheNoteEntry(note))
		}
	}
	return entries
}

func strictCacheSectionEntries(sections []*model.Section) []strictCachePageEntry {
	entries := make([]strictCachePageEntry, 0, len(sections))
	for _, section := range sections {
		if section != nil && section.EffectivePublish {
			entries = append(entries, strictCachePageEntry{RelPath: section.SourcePath, Route: section.Route, Title: section.Title, VersionID: section.VersionID})
		}
	}
	return entries
}

func strictCacheNoteEntry(note *model.Note) *strictCachePageEntry {
	if note == nil {
		return nil
	}
	summary := strings.TrimSpace(note.Frontmatter.Description)
	if summary == "" {
		summary = strings.TrimSpace(note.Summary)
	}
	publishedAt := ""
	if published := note.Frontmatter.Date; !published.IsZero() {
		publishedAt = published.UTC().Format(time.RFC3339)
	}
	return &strictCachePageEntry{
		RelPath: note.RelPath, Route: note.Route, Title: note.Frontmatter.Title,
		Summary: summary, PublishedAt: publishedAt, VersionID: note.VersionID,
	}
}

// strictCacheLookupDigests hashes the shared lookup once per build. The base
// digest excludes embed-only content so pages retain their existing dependency
// granularity without copying and serializing the full lookup per page.
func strictCacheLookupDigests(index *model.VaultIndex) (strictCacheLookupDigest, error) {
	baseHash := sha256.New()
	embeddedHash := sha256.New()
	baseEncoder := json.NewEncoder(baseHash)
	embeddedEncoder := json.NewEncoder(embeddedHash)
	type digestEntry struct {
		Kind  string                 `json:"kind"`
		Entry strictCacheLookupEntry `json:"entry"`
	}
	encode := func(kind string, base, embedded strictCacheLookupEntry) error {
		if err := baseEncoder.Encode(digestEntry{Kind: kind, Entry: base}); err != nil {
			return err
		}
		return embeddedEncoder.Encode(digestEntry{Kind: kind, Entry: embedded})
	}
	if index != nil {
		paths := make([]string, 0, len(index.Notes))
		for relPath := range index.Notes {
			paths = append(paths, relPath)
		}
		sort.Strings(paths)
		for _, relPath := range paths {
			note := index.Notes[relPath]
			if note == nil {
				continue
			}
			base := strictCacheLookupEntry{RelPath: note.RelPath, Route: note.Route, Title: note.Frontmatter.Title, VersionID: note.VersionID, Aliases: note.Aliases, Headings: note.Headings}
			embedded := base
			embedded.Metadata = note.Frontmatter
			embedded.Content = note.RawContent
			if err := encode("note", base, embedded); err != nil {
				return strictCacheLookupDigest{}, fmt.Errorf("hash cache note lookup %q: %w", relPath, err)
			}
		}
		sectionPaths := make([]string, 0, len(index.Sections))
		for relPath := range index.Sections {
			sectionPaths = append(sectionPaths, relPath)
		}
		sort.Strings(sectionPaths)
		for _, relPath := range sectionPaths {
			section := index.Sections[relPath]
			if section == nil {
				continue
			}
			base := strictCacheLookupEntry{RelPath: section.SourcePath, Route: section.Route, Title: section.Title, VersionID: section.VersionID, Headings: section.Headings}
			embedded := base
			embedded.Content = section.RawContent
			if err := encode("section", base, embedded); err != nil {
				return strictCacheLookupDigest{}, fmt.Errorf("hash cache section lookup %q: %w", relPath, err)
			}
		}
	}
	return strictCacheLookupDigest{base: fmt.Sprintf("%x", baseHash.Sum(nil)), embedded: fmt.Sprintf("%x", embeddedHash.Sum(nil))}, nil
}

func strictCacheSectionPageInput(plan *model.SitePlan, section *model.Section, lookupDigest string, markdownAssets map[string]string) strictCacheHTMLInput {
	input := strictCacheHTMLBase(plan, section.Route, section.Title, section.Description, section.SourcePath, section.VersionID, true)
	if section.VersionID == "" {
		input.Versions = strictCacheSectionVersionEntries(plan, section)
	}
	input.Section = &strictCacheSectionInput{
		RelPath: section.RelPath, SourcePath: section.SourcePath, Route: section.Route,
		Title: section.Title, Description: section.Description, Order: section.Order,
		Banner: section.Banner, BannerURL: section.BannerURL, BannerAlt: section.BannerAlt,
		RawContent: append([]byte(nil), section.RawContent...), Children: strictCacheSectionEntries(section.Children),
		Articles: strictCachePageEntries(section.Articles), VersionID: section.VersionID,
		VersionRoutes: section.VersionRoutes, Breadcrumbs: section.Breadcrumbs,
	}
	input.MarkdownAssets = markdownAssets
	input.LookupDigest = lookupDigest
	return input
}

func strictCacheArticlePageInput(plan *model.SitePlan, article *model.Note, section *model.Section, previous, next *model.Note, position, total int, backlinks, related []*model.Note, lookupDigest string, markdownAssets map[string]string) strictCacheHTMLInput {
	input := strictCacheHTMLBase(plan, article.Route, article.Frontmatter.Title, article.Frontmatter.Description, article.RelPath, article.VersionID, false)
	input.Article = &strictCacheArticleInput{
		RelPath: article.RelPath, Route: article.Route, SectionPath: article.SectionPath,
		VersionID: article.VersionID, VersionRoutes: article.VersionRoutes, SocialImage: article.SocialImage,
		BannerURL: article.BannerURL, CoverURL: article.CoverURL, Frontmatter: article.Frontmatter,
		Aliases: append([]string(nil), article.Aliases...), Tags: append([]string(nil), article.Tags...), RawContent: append([]byte(nil), article.RawContent...),
	}
	if section != nil {
		sectionInput := strictCacheSectionInput{
			RelPath: section.RelPath, SourcePath: section.SourcePath, Route: section.Route,
			Title: section.Title, VersionID: section.VersionID, VersionRoutes: section.VersionRoutes,
			Breadcrumbs: section.Breadcrumbs,
		}
		input.Section = &sectionInput
	}
	input.Previous = strictCacheNoteEntry(previous)
	input.Next = strictCacheNoteEntry(next)
	input.Position = position
	input.Total = total
	input.Backlinks = strictCachePageEntries(backlinks)
	input.Related = strictCachePageEntries(related)
	input.MarkdownAssets = markdownAssets
	input.LookupDigest = lookupDigest
	return input
}

func writeStrictHTML(outputs *strictOutputRegistry, outputRoot, relPath, owner string, data []byte) error {
	protected, comments, err := protectStrictHTMLComments(data)
	if err != nil {
		return fmt.Errorf("protect HTML comments in %s: %w", relPath, err)
	}
	minifier := minify.New()
	minifier.AddFunc("text/html", minhtml.Minify)
	compact, err := minifier.Bytes("text/html", protected)
	if err != nil {
		return fmt.Errorf("minify %s: %w", relPath, err)
	}
	for _, comment := range comments {
		compact = bytes.ReplaceAll(compact, comment.placeholder, comment.content)
	}
	return outputs.write(outputRoot, relPath, owner, compact)
}

type strictHTMLComment struct {
	placeholder []byte
	content     []byte
}

type strictHTMLProtectionSpan struct {
	start, end int
	rawComment bool
}

func protectStrictHTMLComments(data []byte) ([]byte, []strictHTMLComment, error) {
	spans := make([]strictHTMLProtectionSpan, 0)
	rawTextTag := ""
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(data))
	offset := 0
	for {
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		start := offset
		offset += len(raw)
		if tokenType == xhtml.ErrorToken {
			if tokenizer.Err() == io.EOF {
				break
			}
			return nil, nil, tokenizer.Err()
		}
		if rawTextTag != "" {
			appendStrictRawTextCommentSpans(&spans, raw, start)
			if tokenType == xhtml.EndTagToken {
				tagName, _ := tokenizer.TagName()
				if strings.EqualFold(string(tagName), rawTextTag) {
					rawTextTag = ""
				}
			}
			continue
		}
		if tokenType == xhtml.CommentToken {
			spans = append(spans, strictHTMLProtectionSpan{start: start, end: offset})
			continue
		}
		if tokenType == xhtml.StartTagToken {
			tagName, _ := tokenizer.TagName()
			if strings.EqualFold(string(tagName), "script") || strings.EqualFold(string(tagName), "style") {
				rawTextTag = strings.ToLower(string(tagName))
			}
		}
	}
	if len(spans) == 0 {
		return data, nil, nil
	}
	digest := sha256.Sum256(data)
	comments := make([]strictHTMLComment, 0, len(spans))
	protected := make([]byte, 0, len(data))
	previous := 0
	for index, current := range spans {
		protected = append(protected, data[previous:current.start]...)
		token := []byte(fmt.Sprintf("oxpio-comment-%x-%08d", digest[:8], index))
		for bytes.Contains(data, token) {
			token = append(token, 'x')
		}
		placeholder := token
		if current.rawComment {
			placeholder = []byte(`/*!` + string(token) + `*/`)
		}
		comments = append(comments, strictHTMLComment{placeholder: placeholder, content: append([]byte(nil), data[current.start:current.end]...)})
		protected = append(protected, placeholder...)
		previous = current.end
	}
	protected = append(protected, data[previous:]...)
	return protected, comments, nil
}

func appendStrictRawTextCommentSpans(spans *[]strictHTMLProtectionSpan, raw []byte, offset int) {
	for cursor := 0; cursor < len(raw); {
		start := bytes.Index(raw[cursor:], []byte("<!--"))
		if start < 0 {
			return
		}
		start += cursor
		end := bytes.Index(raw[start+4:], []byte("-->"))
		if end < 0 {
			return
		}
		end += start + 7
		*spans = append(*spans, strictHTMLProtectionSpan{start: offset + start, end: offset + end, rawComment: true})
		cursor = end
	}
}

func generateStrictSocialCards(plan *model.SitePlan, vaultPath string, workerCount int) (map[string]social.Result, error) {
	cards := make(map[string]social.Result)
	if plan == nil {
		return cards, nil
	}
	articles := make([]*model.Note, 0, len(plan.Articles))
	for _, article := range plan.Articles {
		if article != nil && article.Route != "" {
			articles = append(articles, article)
		}
	}
	if len(articles) == 0 {
		return cards, nil
	}
	if workerCount <= 0 {
		workerCount = runtime.GOMAXPROCS(0)
		if workerCount > 8 {
			workerCount = 8
		}
		if workerCount < 1 {
			workerCount = 1
		}
	}
	if workerCount > len(articles) {
		workerCount = len(articles)
	}

	results := make([]social.Result, len(articles))
	errorsByArticle := make([]error, len(articles))
	coverErrors := make([]bool, len(articles))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				article := articles[index]
				cover, err := strictCoverBytes(vaultPath, article.Frontmatter.Cover)
				if err == nil {
					var input social.Input
					input, err = strictSocialCardInput(plan, article, cover)
					if err == nil {
						results[index], err = social.Generate(input)
					}
				}
				if err != nil {
					coverErrors[index] = cover == nil && article.Frontmatter.Cover != ""
					errorsByArticle[index] = err
				}
			}
		}()
	}
	for index := range articles {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	for index, article := range articles {
		if err := errorsByArticle[index]; err != nil {
			if coverErrors[index] {
				return nil, fmt.Errorf("read cover for %q: %w", article.RelPath, err)
			}
			return nil, fmt.Errorf("generate social card for %q: %w", article.RelPath, err)
		}
		cards[article.RelPath] = results[index]
	}
	return cards, nil
}

func strictSocialCardInput(plan *model.SitePlan, article *model.Note, cover []byte) (social.Input, error) {
	if plan == nil || article == nil {
		return social.Input{}, fmt.Errorf("social card requires a plan and article")
	}
	section := strictArticleSection(plan, article)
	context := ""
	if section != nil {
		context = section.Title
	}
	if article.VersionID != "" {
		for _, version := range plan.Versions {
			if version != nil && version.ID == article.VersionID {
				if context != "" {
					context += " / "
				}
				context += version.Label
			}
		}
	}
	return social.Input{
		CanonicalURL: strictBuildCanonicalURL(plan.Config.BaseURL, article.Route), SiteTitle: plan.Config.Title,
		Title: article.Frontmatter.Title, Context: context, Description: article.Frontmatter.Description,
		Date: strictCardDate(article.Frontmatter.Date), Updated: strictCardDate(article.Frontmatter.Updated),
		Tags: append([]string(nil), article.Tags...), Aliases: append([]string(nil), article.Frontmatter.Aliases...),
		Slug: article.Frontmatter.Slug, Type: article.Frontmatter.Type, Order: article.Frontmatter.Order,
		Author: article.Frontmatter.Author, Reviewed: strictCardDate(article.Frontmatter.Reviewed),
		Status: article.Frontmatter.Status, Audience: article.Frontmatter.Audience,
		ProductVersion: article.Frontmatter.ProductVersion, Series: article.Frontmatter.Series, Cover: cover,
	}, nil
}

func buildStrictRelations(planned *siteplan.Result, concurrency int) (*model.LinkGraph, map[string][]*model.Note, error) {
	related := make(map[string][]*model.Note)
	if planned == nil || planned.Plan == nil || planned.Index == nil {
		return &model.LinkGraph{Forward: map[string][]string{}, Backward: map[string][]string{}}, related, nil
	}
	if planned.Plan.Config.Related.Enabled {
		// Recommendations intentionally use only source-declared edges and consume
		// their semantic owner before the render-equivalent backlinks pass.
		recommendationGraph := link.BuildSourceGraph(planned.Index)
		groups := make(map[string][]model.RelatedSemanticDocument)
		for index := range planned.RelatedSemantic {
			semantic := planned.RelatedSemantic[index]
			versionID := ""
			if note := planned.Index.Notes[semantic.RelPath]; note != nil {
				versionID = note.VersionID
			}
			groups[versionID] = append(groups[versionID], semantic)
			planned.RelatedSemantic[index] = model.RelatedSemanticDocument{}
		}
		planned.RelatedSemantic = nil
		groupKeys := make([]string, 0, len(groups))
		for key := range groups {
			groupKeys = append(groupKeys, key)
		}
		sort.Strings(groupKeys)
		for _, key := range groupKeys {
			semanticOwner := groups[key]
			delete(groups, key)
			engine, err := recommend.BuildEngineFromSemanticOwner(&semanticOwner, planned.Index, recommendationGraph, recommend.ProductionEngineParameters(planned.Plan.Config.Related.Count, concurrency))
			if err != nil {
				return nil, nil, fmt.Errorf("build related articles: %w", err)
			}
			for _, document := range engine.Documents {
				items := make([]*model.Note, 0, len(document.Related))
				for _, candidate := range document.Related {
					if candidate.DocID < 0 || candidate.DocID >= len(engine.Documents) {
						continue
					}
					target := planned.Index.Notes[engine.Documents[candidate.DocID].RelPath]
					if target != nil {
						items = append(items, target)
					}
				}
				related[document.RelPath] = items
			}
		}
	}

	// Backlinks describe visible page content and therefore include links
	// contributed by embeds from the render-local pass-2 results.
	graph, err := buildStrictRenderGraph(planned.Index)
	if err != nil {
		return nil, nil, err
	}
	return graph, related, nil
}

// buildStrictRenderGraph performs the same markdown expansion used for pages,
// but keeps only the render-local link ledger. It uses a sink that does not
// publish assets because planning has already validated them.
func buildStrictRenderGraph(index *model.VaultIndex) (*model.LinkGraph, error) {
	resolved := make(map[string][]model.LinkRef, len(index.Notes))
	for relPath, note := range index.Notes {
		if note == nil {
			continue
		}
		md, result := markdown.NewMarkdown(index, note, strictGraphAssetSink{}, nil)
		if err := md.Convert(note.RawContent, io.Discard); err != nil {
			return nil, fmt.Errorf("expand links for %q: %w", relPath, err)
		}
		resolved[relPath] = result.OutLinks()
	}
	return link.BuildGraph(index, resolved), nil
}

type strictGraphAssetSink struct{}

func (strictGraphAssetSink) Register(value string) string {
	return "assets/" + strings.TrimPrefix(value, "/")
}

func strictBacklinks(index *model.VaultIndex, graph *model.LinkGraph, article *model.Note) []*model.Note {
	if index == nil || graph == nil || article == nil {
		return nil
	}
	paths := graph.Backward[article.RelPath]
	result := make([]*model.Note, 0, len(paths))
	for _, source := range paths {
		if note := index.Notes[source]; note != nil {
			result = append(result, note)
		}
	}
	return result
}

type strictPopoverPayload struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags"`
}

func writeStrictPopoverPayloads(outputRoot string, index *model.VaultIndex, outputs *strictOutputRegistry) error {
	if index == nil || len(index.Notes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(index.Notes))
	for relPath := range index.Notes {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	for _, relPath := range paths {
		note := index.Notes[relPath]
		if note == nil {
			continue
		}
		data, err := json.Marshal(strictPopoverPayload{Title: note.Frontmatter.Title, Summary: note.Frontmatter.Description, Tags: append([]string{}, note.Tags...)})
		if err != nil {
			return fmt.Errorf("marshal popover payload %q: %w", relPath, err)
		}
		if err := outputs.dependency("popover:"+relPath, relPath, struct {
			Frontmatter model.Frontmatter `json:"frontmatter"`
			Tags        []string          `json:"tags"`
		}{note.Frontmatter, note.Tags}); err != nil {
			return err
		}
		if err := outputs.write(outputRoot, path.Join("_popover", slug.EncodeSourcePath(relPath), "index.json"), "popover:"+relPath, data); err != nil {
			return err
		}
	}
	return nil
}

func strictBuildTags(tags map[string]*model.Tag) []*model.Tag {
	values := make([]*model.Tag, 0, len(tags))
	for _, tag := range tags {
		if tag != nil {
			values = append(values, tag)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}

// strictCacheDependency describes the canonical inputs owned by a build
// producer. It is intentionally separate from strictCacheOutput: an output
// hash proves what was emitted, while this signature records why it was
// emitted and which source owns the input.
type strictCacheDependency struct {
	Owner          string `json:"owner"`
	Source         string `json:"source"`
	InputSignature string `json:"inputSignature"`
}

type strictCacheOutput struct {
	Owner      string `json:"owner"`
	Route      string `json:"route"`
	OutputHash string `json:"outputHash"`
}

type strictCacheManifest struct {
	Version      int                     `json:"version"`
	Dependencies []strictCacheDependency `json:"dependencies"`
	Outputs      []strictCacheOutput     `json:"outputs"`
}

func strictCacheRelativePath(vaultRoot, value string) string {
	if value == "" || !filepath.IsAbs(value) {
		return filepath.ToSlash(value)
	}
	relative, err := filepath.Rel(vaultRoot, value)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(relative)
}

func loadStrictCacheManifest(outputRoot string) *strictCacheManifest {
	if strings.TrimSpace(outputRoot) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(outputRoot, ".oxpio-cache", "manifest.json"))
	if err != nil {
		return nil
	}
	var manifest strictCacheManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != 2 {
		return nil
	}
	return &manifest
}

func writeStrictCacheManifest(outputRoot string, plan *model.SitePlan, index *model.VaultIndex, assets map[string]*model.Asset, outputs *strictOutputRegistry) error {
	manifest := strictCacheManifest{Version: 2, Dependencies: make([]strictCacheDependency, 0), Outputs: make([]strictCacheOutput, 0)}
	if outputs != nil {
		manifest.Dependencies = append(manifest.Dependencies, outputs.dependencies...)
	}
	addDependency := func(owner, source string, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshal cache dependency %q: %w", source, err)
		}
		hash := sha256.Sum256(data)
		manifest.Dependencies = append(manifest.Dependencies, strictCacheDependency{
			Owner: owner, Source: source, InputSignature: fmt.Sprintf("%x", hash),
		})
		return nil
	}
	if plan != nil {
		cacheConfig := plan.Config
		cacheConfig.CustomCSS = strictCacheRelativePath(plan.VaultPath, cacheConfig.CustomCSS)
		cacheConfig.ThemeDir = strictCacheRelativePath(plan.VaultPath, cacheConfig.ThemeDir)
		cacheConfig.Edit = nil
		// These fields are assigned from planned assets during the build and
		// therefore belong to output state, not the canonical config input.
		cacheConfig.ThemeCSS = ""
		cacheConfig.DefaultImgURL = ""
		if err := addDependency("site", "oxpio.yaml", struct {
			Config model.SiteConfig `json:"config"`
		}{Config: cacheConfig}); err != nil {
			return err
		}
	}
	assetSources := make([]string, 0, len(assets))
	for source := range assets {
		assetSources = append(assetSources, source)
	}
	sort.Strings(assetSources)
	for _, source := range assetSources {
		asset := assets[source]
		if asset == nil {
			continue
		}
		if plan == nil {
			return fmt.Errorf("site plan is required for asset dependency %q", source)
		}
		if planned := strictPlannedAsset(plan, source); planned != nil {
			if err := addDependency("asset:"+source, source, struct {
				Content []byte `json:"content"`
			}{planned.Data}); err != nil {
				return err
			}
			continue
		}
		_, content, _, err := internalfsutil.ReadContainedRegularFile(plan.VaultPath, source)
		if err != nil {
			return fmt.Errorf("read cache dependency asset %q: %w", source, err)
		}
		if err := addDependency("asset:"+source, source, struct {
			Content []byte `json:"content"`
		}{content}); err != nil {
			return err
		}
	}
	if outputs != nil {
		manifest.Outputs = append(manifest.Outputs, outputs.records...)
	}
	sort.Slice(manifest.Dependencies, func(i, j int) bool {
		left, right := manifest.Dependencies[i], manifest.Dependencies[j]
		if left.Owner != right.Owner {
			return left.Owner < right.Owner
		}
		return left.Source < right.Source
	})
	sort.Slice(manifest.Outputs, func(i, j int) bool {
		left, right := manifest.Outputs[i], manifest.Outputs[j]
		if left.Owner != right.Owner {
			return left.Owner < right.Owner
		}
		return left.Route < right.Route
	})
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return outputs.write(outputRoot, ".oxpio-cache/manifest.json", "cache", data)
}

func writeStrictConfiguredAssets(vaultRoot, outputRoot string, plan *model.SitePlan, outputs *strictOutputRegistry) error {
	if plan == nil {
		return nil
	}
	if plan.Config.CustomCSS != "" {
		data := plan.CustomCSSData
		if err := outputs.dependencyBytes("custom CSS", strictCacheRelativePath(vaultRoot, plan.Config.CustomCSS), data); err != nil {
			return err
		}
		if err := outputs.write(outputRoot, customCSSOutputPath, "custom CSS", data); err != nil {
			return err
		}
	}
	return nil
}

func strictReservedAssetOutputs(plan *model.SitePlan) []string {
	reserved := []string{"style.css", "assets/oxpio-runtime", "assets/oxpio", "assets/oxpio", "assets/social"}
	if plan != nil && plan.Config.CustomCSS != "" {
		reserved = append(reserved, "assets/custom.css")
	}
	return reserved
}

type strictSidebarPayload struct {
	Default  []model.SidebarNode            `json:"default"`
	Versions map[string][]model.SidebarNode `json:"versions,omitempty"`
}

func writeStrictMetadataOutputs(outputRoot string, plan *model.SitePlan, index *model.VaultIndex, outputs *strictOutputRegistry) error {
	var sitemap strings.Builder
	sitemapTimeline := make([]string, 0)
	sitemap.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, section := range plan.Sections {
		if section != nil && section.Route != "" {
			_, _ = fmt.Fprintf(&sitemap, `<url><loc>%s</loc>%s</url>`, strictXMLEscape(strictBuildCanonicalURL(plan.Config.BaseURL, section.Route)), strictLastMod(section.LastModified))
		}
	}
	for _, article := range plan.Articles {
		if article != nil && article.Route != "" {
			_, _ = fmt.Fprintf(&sitemap, `<url><loc>%s</loc>%s</url>`, strictXMLEscape(strictBuildCanonicalURL(plan.Config.BaseURL, article.Route)), strictLastMod(article.LastModified))
		}
	}
	if index != nil {
		for _, tag := range strictBuildTags(index.Tags) {
			if tag != nil {
				_, _ = fmt.Fprintf(&sitemap, `<url><loc>%s</loc></url>`, strictXMLEscape(strictBuildCanonicalURL(plan.Config.BaseURL, "/"+slug.EncodePath(tag.Slug)+"/")))
			}
		}
	}
	if plan.Timeline != nil {
		for _, page := range plan.Timeline.Pages {
			sitemapTimeline = append(sitemapTimeline, page.Route)
			_, _ = fmt.Fprintf(&sitemap, `<url><loc>%s</loc></url>`, strictXMLEscape(strictBuildCanonicalURL(plan.Config.BaseURL, page.Route)))
		}
	}
	sitemap.WriteString(`</urlset>`)
	type sitemapEntry struct {
		Route        string    `json:"route"`
		LastModified time.Time `json:"lastModified,omitempty"`
	}
	sitemapSections := make([]sitemapEntry, 0, len(plan.Sections))
	for _, section := range plan.Sections {
		if section != nil && section.Route != "" {
			sitemapSections = append(sitemapSections, sitemapEntry{Route: section.Route, LastModified: section.LastModified})
		}
	}
	sitemapArticles := make([]sitemapEntry, 0, len(plan.Articles))
	for _, article := range plan.Articles {
		if article != nil && article.Route != "" {
			sitemapArticles = append(sitemapArticles, sitemapEntry{Route: article.Route, LastModified: article.LastModified})
		}
	}
	sitemapTags := make([]string, 0)
	if index != nil {
		for _, tag := range strictBuildTags(index.Tags) {
			if tag != nil {
				sitemapTags = append(sitemapTags, tag.Slug)
			}
		}
	}
	if err := outputs.dependency("sitemap", "oxpio.yaml", struct {
		BaseURL  string         `json:"baseURL"`
		Sections []sitemapEntry `json:"sections"`
		Articles []sitemapEntry `json:"articles"`
		Tags     []string       `json:"tags,omitempty"`
		Timeline []string       `json:"timeline,omitempty"`
	}{plan.Config.BaseURL, sitemapSections, sitemapArticles, sitemapTags, sitemapTimeline}); err != nil {
		return err
	}
	sitemapData := []byte(sitemap.String())
	if err := outputs.write(outputRoot, "sitemap.xml", "sitemap", sitemapData); err != nil {
		return err
	}
	if err := outputs.dependency("robots", "oxpio.yaml", struct {
		BaseURL string `json:"baseURL"`
	}{plan.Config.BaseURL}); err != nil {
		return err
	}
	if err := outputs.write(outputRoot, "robots.txt", "robots", []byte(strictBuildRobots(plan.Config.BaseURL))); err != nil {
		return err
	}
	if plan.Config.RSS.Enabled {
		var rss strings.Builder
		_, _ = fmt.Fprintf(&rss, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:oxpio="https://oxpio.dev/ns/rss"><channel><title>%s</title><link>%s</link>`, strictXMLEscape(plan.Config.Title), strictXMLEscape(strings.TrimSuffix(plan.Config.BaseURL, "/")+"/"))
		if plan.Config.Description != "" {
			_, _ = fmt.Fprintf(&rss, `<description>%s</description>`, strictXMLEscape(plan.Config.Description))
		}
		if plan.Config.Language != "" {
			_, _ = fmt.Fprintf(&rss, `<language>%s</language>`, strictXMLEscape(plan.Config.Language))
		}
		if latest := strictLatestPostTime(plan.Posts); !latest.IsZero() {
			_, _ = fmt.Fprintf(&rss, `<lastBuildDate>%s</lastBuildDate>`, latest.UTC().Format(time.RFC1123Z))
		}
		for _, article := range plan.Posts {
			if article == nil {
				continue
			}
			link := strictBuildCanonicalURL(plan.Config.BaseURL, article.Route)
			_, _ = fmt.Fprintf(&rss, `<item><title>%s</title><link>%s</link><guid>%s</guid>`, strictXMLEscape(article.Frontmatter.Title), strictXMLEscape(link), strictXMLEscape(link))
			if article.Frontmatter.Description != "" {
				_, _ = fmt.Fprintf(&rss, `<description>%s</description>`, strictXMLEscape(article.Frontmatter.Description))
			}
			if article.Frontmatter.Author != "" {
				_, _ = fmt.Fprintf(&rss, `<dc:creator>%s</dc:creator>`, strictXMLEscape(article.Frontmatter.Author))
			}
			for _, tag := range article.Tags {
				_, _ = fmt.Fprintf(&rss, `<category>%s</category>`, strictXMLEscape(tag))
			}
			if !article.Frontmatter.Reviewed.IsZero() {
				_, _ = fmt.Fprintf(&rss, `<oxpio:reviewed>%s</oxpio:reviewed>`, strictXMLEscape(article.Frontmatter.Reviewed.UTC().Format(time.RFC3339)))
			}
			if article.Frontmatter.Status != "" {
				_, _ = fmt.Fprintf(&rss, `<oxpio:status>%s</oxpio:status>`, strictXMLEscape(article.Frontmatter.Status))
			}
			if article.Frontmatter.Audience != "" {
				_, _ = fmt.Fprintf(&rss, `<oxpio:audience>%s</oxpio:audience>`, strictXMLEscape(article.Frontmatter.Audience))
			}
			if article.Frontmatter.ProductVersion != "" {
				_, _ = fmt.Fprintf(&rss, `<oxpio:productVersion>%s</oxpio:productVersion>`, strictXMLEscape(article.Frontmatter.ProductVersion))
			}
			if article.Frontmatter.Series != "" {
				_, _ = fmt.Fprintf(&rss, `<oxpio:series>%s</oxpio:series>`, strictXMLEscape(article.Frontmatter.Series))
			}
			if !article.Frontmatter.Date.IsZero() {
				_, _ = fmt.Fprintf(&rss, `<pubDate>%s</pubDate>`, article.Frontmatter.Date.UTC().Format(time.RFC1123Z))
			}
			if !article.Frontmatter.Updated.IsZero() {
				_, _ = fmt.Fprintf(&rss, `<oxpio:updated>%s</oxpio:updated>`, article.Frontmatter.Updated.UTC().Format(time.RFC3339))
			}
			rss.WriteString(`</item>`)
		}
		rss.WriteString(`</channel></rss>`)
		type rssPostInput struct {
			Route          string    `json:"route"`
			Title          string    `json:"title"`
			Description    string    `json:"description,omitempty"`
			Author         string    `json:"author,omitempty"`
			Tags           []string  `json:"tags,omitempty"`
			Reviewed       time.Time `json:"reviewed,omitempty"`
			Status         string    `json:"status,omitempty"`
			Audience       string    `json:"audience,omitempty"`
			ProductVersion string    `json:"productVersion,omitempty"`
			Series         string    `json:"series,omitempty"`
			Date           time.Time `json:"date,omitempty"`
			Updated        time.Time `json:"updated,omitempty"`
		}
		posts := make([]rssPostInput, 0, len(plan.Posts))
		for _, article := range plan.Posts {
			if article == nil {
				continue
			}
			posts = append(posts, rssPostInput{
				Route: article.Route, Title: article.Frontmatter.Title, Description: article.Frontmatter.Description,
				Author: article.Frontmatter.Author, Tags: append([]string(nil), article.Tags...), Reviewed: article.Frontmatter.Reviewed,
				Status: article.Frontmatter.Status, Audience: article.Frontmatter.Audience, ProductVersion: article.Frontmatter.ProductVersion,
				Series: article.Frontmatter.Series, Date: article.Frontmatter.Date, Updated: article.Frontmatter.Updated,
			})
		}
		if err := outputs.dependency("rss", "oxpio.yaml", struct {
			Title       string         `json:"title"`
			BaseURL     string         `json:"baseURL"`
			Description string         `json:"description,omitempty"`
			Language    string         `json:"language,omitempty"`
			Posts       []rssPostInput `json:"posts"`
		}{plan.Config.Title, plan.Config.BaseURL, plan.Config.Description, plan.Config.Language, posts}); err != nil {
			return err
		}
		rssData := []byte(rss.String())
		if err := outputs.write(outputRoot, "index.xml", "rss", rssData); err != nil {
			return err
		}
	}
	notFound, err := render.RenderStrictNotFound(plan)
	if err != nil {
		return err
	}
	if err := outputs.dependency("404", "oxpio.yaml", strictCacheHTMLBase(plan, "/404.html", "Not found", "", "", "", true)); err != nil {
		return err
	}
	return writeStrictHTML(outputs, outputRoot, "404.html", "404", notFound)
}

func strictLatestPostTime(posts []*model.Note) time.Time {
	var latest time.Time
	for _, article := range posts {
		if article == nil {
			continue
		}
		value := article.Frontmatter.Updated
		if value.IsZero() {
			value = article.Frontmatter.Date
		}
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func strictBuildRobots(baseURL string) string {
	return "User-agent: *\nAllow: /\nSitemap: " + strings.TrimSuffix(baseURL, "/") + "/sitemap.xml\n"
}

func strictLastMod(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return `<lastmod>` + value.UTC().Format(time.RFC3339) + `</lastmod>`
}

func strictXMLEscape(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func prepareStrictAssets(vaultRoot string, plan *model.SitePlan) (map[string]*model.Asset, error) {
	assets := make(map[string]*model.Asset)
	add := func(source, kind string) error {
		if strings.TrimSpace(source) == "" {
			return nil
		}
		data, err := validateStrictAsset(vaultRoot, source, kind)
		if err != nil {
			return err
		}
		_ = data
		if assets[source] == nil {
			assets[source] = &model.Asset{SrcPath: source}
		}
		return nil
	}
	for _, section := range plan.Sections {
		if section != nil && section.Banner != "" {
			if err := add(section.Banner, "banner"); err != nil {
				return nil, err
			}
		}
	}
	if plan.Config.DefaultImg != "" && !plan.Config.DefaultImgExternal {
		if err := add(plan.Config.DefaultImg, "defaultImg"); err != nil {
			return nil, err
		}
	}
	for _, article := range plan.Articles {
		if article != nil {
			if err := add(article.Frontmatter.Banner, "banner"); err != nil {
				return nil, err
			}
			if err := add(article.Frontmatter.Cover, "cover"); err != nil {
				return nil, err
			}
		}
	}
	if len(assets) == 0 {
		return assets, nil
	}
	return assets, nil
}

func applyStrictPlannedDestinations(assets map[string]*model.Asset, destinations map[string]string) {
	for source, destination := range destinations {
		if asset := assets[source]; asset != nil {
			asset.DstPath = destination
		}
	}
}

func applyStrictAssetURLs(plan *model.SitePlan, assets map[string]*model.Asset) {
	if plan != nil {
		if plan.Config.DefaultImgExternal {
			plan.Config.DefaultImgURL = plan.Config.DefaultImg
		} else if asset := assets[plan.Config.DefaultImg]; asset != nil {
			plan.Config.DefaultImgURL = asset.DstPath
		}
	}
	for _, section := range plan.Sections {
		if section != nil && section.Banner != "" && assets[section.Banner] != nil {
			section.BannerURL = assets[section.Banner].DstPath
		}
	}
	for _, article := range plan.Articles {
		if article != nil {
			if asset := assets[article.Frontmatter.Banner]; asset != nil {
				article.BannerURL = asset.DstPath
			}
			if asset := assets[article.Frontmatter.Cover]; asset != nil {
				article.CoverURL = asset.DstPath
			}
		}
	}
}

func validateStrictAsset(vaultRoot, raw, kind string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !internalasset.IsPublishableAssetPath(raw) || strings.Contains(raw, `\`) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || path.Clean(raw) != raw || strings.HasPrefix(path.Clean(raw), "../") || !internalfsutil.IsPortableSitePath(strings.ReplaceAll(raw, "%", "x")) {
		return nil, fmt.Errorf("%s %q must be a normalized vault-relative local asset", kind, raw)
	}
	lower := strings.ToLower(raw)
	allowed := strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".webp")
	if kind == "banner" || kind == "defaultImg" {
		allowed = allowed || strings.HasSuffix(lower, ".svg")
	}
	if !allowed {
		return nil, fmt.Errorf("%s %q has an unsupported format", kind, raw)
	}
	_, data, _, err := internalfsutil.ReadContainedRegularFile(vaultRoot, raw)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", kind, raw, err)
	}
	if strings.HasSuffix(lower, ".svg") {
		if err := internalasset.ValidateLocalSVG(data); err != nil {
			return nil, fmt.Errorf("%s %q SVG: %w", kind, raw, err)
		}
		return data, nil
	}
	if _, format, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("%s %q cannot be decoded: %w", kind, raw, err)
	} else if format != "png" && format != "jpeg" && format != "webp" {
		return nil, fmt.Errorf("%s %q decoded as unsupported format %q", kind, raw, format)
	}
	return data, nil
}

func strictCoverBytes(vaultRoot, raw string) ([]byte, error) {
	return validateStrictAsset(vaultRoot, raw, "cover")
}

func strictBuildCanonicalURL(baseURL, route string) string {
	return strings.TrimSuffix(baseURL, "/") + route
}

func strictCardDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func strictArticleSection(plan *model.SitePlan, article *model.Note) *model.Section {
	if plan == nil || article == nil {
		return nil
	}
	for _, section := range plan.Sections {
		if section != nil && section.RelPath == article.SectionPath && section.VersionID == article.VersionID {
			return section
		}
	}
	return nil
}

func rebindStrictPlanNotes(plan *model.SitePlan, index *model.VaultIndex) {
	if plan == nil || index == nil {
		return
	}
	for _, note := range index.Notes {
		if note == nil {
			continue
		}
		original := strictPlanNote(plan, note.RelPath)
		if original == nil {
			continue
		}
		note.Route = original.Route
		note.SectionPath = original.SectionPath
		note.VersionID = original.VersionID
		note.VersionRoutes = original.VersionRoutes
		note.Slug = original.Slug
	}
	index.NoteBySlug = make(map[string]*model.Note, len(index.Notes))
	for _, note := range index.Notes {
		if note != nil && note.Slug != "" {
			index.NoteBySlug[slug.Canonicalize(note.Slug)] = note
		}
	}
	rebind := func(notes []*model.Note) []*model.Note {
		for i, note := range notes {
			if note != nil {
				if replacement := index.Notes[note.RelPath]; replacement != nil {
					notes[i] = replacement
				}
			}
		}
		return notes
	}
	plan.Articles = rebind(plan.Articles)
	plan.Documents = rebind(plan.Documents)
	plan.Posts = rebind(plan.Posts)
	plan.Pages = rebind(plan.Pages)
	for _, section := range plan.Sections {
		if section == nil {
			continue
		}
		section.Articles = rebind(section.Articles)
		section.Documents = rebind(section.Documents)
		section.Posts = rebind(section.Posts)
		section.Pages = rebind(section.Pages)
	}
}

func strictPlanNote(plan *model.SitePlan, relPath string) *model.Note {
	for _, note := range plan.Articles {
		if note != nil && note.RelPath == relPath {
			return note
		}
	}
	return nil
}

func strictReadingFlow(section *model.Section, article *model.Note) (previous, next *model.Note, position, total int) {
	if section == nil || article == nil || article.Frontmatter.Type != "doc" {
		return nil, nil, 0, 0
	}
	total = len(section.Documents)
	for index, candidate := range section.Documents {
		if candidate == article {
			position = index + 1
			if index > 0 {
				previous = section.Documents[index-1]
			}
			if index+1 < total {
				next = section.Documents[index+1]
			}
			break
		}
	}
	return previous, next, position, total
}
