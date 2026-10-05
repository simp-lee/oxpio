// Package siteplan builds the canonical, section-aware vault model consumed by
// validation and rendering. It contains no writer and never mutates the vault.
package siteplan

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	internalasset "github.com/simp-lee/oxpio/internal/asset"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	"github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/render"
	"github.com/simp-lee/oxpio/internal/slug"
	"github.com/simp-lee/oxpio/internal/vault"
	_ "golang.org/x/image/webp"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Result contains the normalized plan and diagnostics produced before any
// output is considered. Diagnostics are sorted and safe to pass to a later
// shared analyzer.
type Result struct {
	Plan            *model.SitePlan
	Scan            vault.ScanResult
	Sources         vault.StrictFrontmatterResult
	Index           *model.VaultIndex
	RelatedSemantic []model.RelatedSemanticDocument
	Diagnostics     []diag.Diagnostic
}

// Build loads the vault-local configuration and creates a strict site plan.
func Build(vaultPath string) (*Result, error) {
	return BuildWithConcurrency(vaultPath, 0)
}

// BuildWithConcurrency creates a strict plan with an optional bound on
// independent Markdown indexing workers.
func BuildWithConcurrency(vaultPath string, concurrency int) (*Result, error) {
	resolved, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	cfg, err := internalconfig.LoadForBuild(resolved)
	if err != nil {
		return nil, err
	}
	return buildWithConfigAndOutput(resolved, cfg, "", concurrency)
}

// BuildForOutput creates the same strict plan while excluding the resolved
// publication destination from vault discovery.
func BuildForOutput(vaultPath, outputPath string) (*Result, error) {
	return BuildForOutputWithConcurrency(vaultPath, outputPath, 0)
}

// BuildForOutputWithOverlay analyzes a candidate Markdown overlay without
// replacing any source file. It is the planning handoff used by edit CAS.
func BuildForOutputWithOverlay(vaultPath, outputPath string, overlay map[string][]byte, deleted map[string]bool, concurrency int) (*Result, error) {
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return nil, err
	}
	cfg, err := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return nil, err
	}
	return buildWithConfigAndOutputAndOverlay(boundary.VaultPath, cfg, boundary.OutputPath, overlay, deleted, concurrency)
}

// BuildForOutputWithConcurrency creates a strict plan with an optional bound
// on independent Markdown indexing workers. A non-positive value preserves the
// default worker selection.
func BuildForOutputWithConcurrency(vaultPath, outputPath string, concurrency int) (*Result, error) {
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return nil, err
	}
	cfg, err := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
	if err != nil {
		return nil, err
	}
	return buildWithConfigAndOutput(boundary.VaultPath, cfg, boundary.OutputPath, concurrency)
}

// BuildWithConfig creates a strict plan from an already loaded configuration.
// The configuration is normalized again so callers cannot bypass its contract.
func BuildWithConfig(vaultPath string, cfg model.SiteConfig) (*Result, error) {
	return buildWithConfigAndOutput(vaultPath, cfg, "", 0)
}

func buildWithConfigAndOutput(vaultPath string, cfg model.SiteConfig, outputPath string, concurrency int) (*Result, error) {
	return buildWithConfigAndOutputAndOverlay(vaultPath, cfg, outputPath, nil, nil, concurrency)
}

func buildWithConfigAndOutputAndOverlay(vaultPath string, cfg model.SiteConfig, outputPath string, overlay map[string][]byte, deleted map[string]bool, concurrency int) (*Result, error) {
	collector := diag.NewCollector()
	cfg, err := internalconfig.NormalizeSiteConfig(cfg)
	if err != nil {
		return &Result{Diagnostics: diagnosticsWithError(collector, diag.Location{Path: internalconfig.Filename}, diag.KindSchema, err)}, err
	}
	resolvedVault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	scan, err := vault.ScanWithOptions(resolvedVault, vault.ScanOptions{OutputPath: outputPath, OverlayMarkdown: overlay, OverlayDeleted: deleted})
	if err != nil {
		return &Result{Diagnostics: diagnosticsWithError(collector, diag.Location{Path: resolvedVault}, diag.KindSchema, err)}, err
	}
	sources, err := vault.ParseStrictFrontmatter(scan)
	if err != nil {
		diagnostic := strictParseDiagnostic(resolvedVault, err)
		collector.Add(diagnostic)
		return &Result{Diagnostics: collector.Diagnostics()}, err
	}

	plan := &model.SitePlan{
		VaultPath: resolvedVault, Config: cfg,
		Routes:           make(map[string]string),
		PublicPageRoutes: map[string]struct{}{routeKey("/404.html"): {}},
		ReservedRoutes:   reservedRoutes(),
	}
	sections := make(map[string]*model.Section, len(sources.Sections))
	for _, source := range sources.Sections {
		if source == nil {
			continue
		}
		if _, _, prefixErr := slug.NumericPrefix(path.Base(source.SectionPath)); prefixErr != nil {
			record(collector, diag.KindOrder, source.RelPath, "%v", prefixErr)
		}
		if _, exists := sections[source.SectionPath]; exists {
			record(collector, diag.KindSection, source.RelPath, "duplicate section source")
			continue
		}
		sections[source.SectionPath] = &model.Section{
			RelPath: source.SectionPath, SourcePath: source.RelPath,
			Title: source.Frontmatter.Title, Description: source.Frontmatter.Description,
			Publish: source.Frontmatter.Publish != nil && *source.Frontmatter.Publish,
			Order:   source.Frontmatter.Order, Banner: source.Frontmatter.Banner, BannerAlt: source.Frontmatter.BannerAlt,
			RawContent: append([]byte(nil), source.RawContent...), BodyStartLine: source.BodyStartLine,
		}
	}

	required := requiredSectionPaths(sources)
	for sectionPath := range required {
		if _, ok := sections[sectionPath]; !ok {
			record(collector, diag.KindSection, sectionSourcePath(sectionPath), "missing required _index.md for section %q", sectionPath)
		}
	}
	if _, ok := sections["."]; !ok {
		record(collector, diag.KindSection, "_index.md", "vault root must contain _index.md")
	}

	versions, versionByPath := planVersions(cfg.Versions, resolvedVault, sections, collector)
	for _, section := range sections {
		if versionID := versionByPath[section.RelPath]; versionID != "" {
			section.VersionID = versionID
		}
	}

	attachSections(plan, sections, versions, collector)
	for _, section := range sections {
		if section.VersionID == "" && section.RelPath != "." && !hasSectionParent(sections, section.RelPath) {
			record(collector, diag.KindSection, section.SourcePath, "section %q has no indexed parent section", section.RelPath)
		}
	}
	computeEffectivePublish(sections, versions, collector)
	assignSectionRoutes(plan, sections, versions, cfg.Versions, collector)
	assignArticles(plan, sections, versions, cfg.Versions, sources.AllArticles, collector)
	validateNavigation(sections, cfg.Navigation, cfg.BaseURL, cfg.FieldLines, collector)
	frontmatterAssets, assetOwners := validatePlannedAssets(resolvedVault, outputPath, plan, sections, sources, scan.ResourceFiles, collector)
	buildVersionCorrespondence(versions, collector)
	finalizeCollections(plan, sections, versions, collector)
	if cfg.Timeline.Enabled {
		plan.Timeline = model.NewTimelinePagePlan(cfg.Timeline.Path, cfg.Pagination.PageSize, len(plan.Posts))
	}
	indexResult, indexErr := vault.BuildStrictIndex(scan, sources, plan.Articles, plan.Sections, collector, vault.BuildIndexOptions{Concurrency: concurrency, CollectRelatedSemantic: cfg.Related.Enabled, ResourceSections: allSections(sections)})
	if indexErr != nil {
		record(collector, diag.KindSchema, resolvedVault, "index strict Markdown: %v", indexErr)
	}
	planVaultCSS(plan, scan, indexResult.Index, collector)
	validateStrictOptionalInputs(resolvedVault, outputPath, plan, scan, indexResult.Index, collector)
	if indexResult.Index != nil {
		for _, tag := range sortedStrictTags(indexResult.Index.Tags) {
			if tag != nil {
				claimPublicPageRoute(plan, "/"+encodePath(tag.Slug)+"/", "tag:"+tag.Name, collector)
			}
		}
		if plan.Timeline != nil {
			for _, page := range plan.Timeline.Pages {
				claimPublicPageRoute(plan, page.Route, "timeline", collector)
			}
		}
		validateStrictMarkdown(plan, indexResult.Index, collector)
	}

	plannedAssetOutputs := validateAssetDestinations(resolvedVault, plan, frontmatterAssets, assetOwners, indexResult.Index, scan.ResourceFiles, collector)
	validateThemeSlotAssets(plan, indexResult.Index, plannedAssetOutputs, collector)
	result := &Result{Plan: plan, Scan: scan, Sources: sources, Index: indexResult.Index, RelatedSemantic: indexResult.RelatedSemantic, Diagnostics: collector.Diagnostics()}
	if collector.HasErrors() {
		return result, fmt.Errorf("site plan has %d error(s)", collector.ErrorCount())
	}
	return result, nil
}

var (
	strictParsePathPattern   = regexp.MustCompile(`(?:config|article|section|frontmatter) "([^"]+)"`)
	strictParseLinePattern   = regexp.MustCompile(`\bline ([0-9]+)\b`)
	strictParseFieldPattern  = regexp.MustCompile(`(?:field|key) "([^"]+)"|(?:^| )([A-Za-z][A-Za-z0-9]*) at line|\b([A-Za-z][A-Za-z0-9_.\[\]]*) (?:is|required|must)`)
	strictParseTargetPattern = regexp.MustCompile(`(?:link|target|resource|asset|route) "([^"]+)"`)
)

func strictParseDiagnostic(vaultRoot string, err error) diag.Diagnostic {
	diagnostic := diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindSchema, Location: diag.Location{Path: vaultRoot}}
	if err == nil {
		return diagnostic
	}
	message := err.Error()
	if match := strictParsePathPattern.FindStringSubmatch(message); len(match) == 2 {
		diagnostic.Location.Path = filepath.Join(vaultRoot, filepath.FromSlash(match[1]))
	}
	if match := strictParseLinePattern.FindStringSubmatch(message); len(match) == 2 {
		diagnostic.Location.Line, _ = strconv.Atoi(match[1])
	}
	if match := strictParseFieldPattern.FindStringSubmatch(message); len(match) > 1 {
		for _, value := range match[1:] {
			if value != "" {
				diagnostic.Field = value
				break
			}
		}
	}
	if match := strictParseTargetPattern.FindStringSubmatch(message); len(match) == 2 {
		diagnostic.Target = match[1]
	}
	diagnostic.Message = message
	return diagnostic
}

func diagnosticsWithError(collector *diag.Collector, location diag.Location, kind diag.Kind, err error) []diag.Diagnostic {
	if err != nil {
		collector.Errorf(kind, location, "%v", err)
	}
	return collector.Diagnostics()
}

func record(collector *diag.Collector, kind diag.Kind, source string, format string, args ...any) {
	if collector == nil {
		return
	}
	item := diag.Diagnostic{Severity: diag.SeverityError, Kind: kind, Location: diag.Location{Path: source}, Message: fmt.Sprintf(format, args...)}
	switch kind {
	case diag.KindSection:
		item.Field = "_index.md"
	case diag.KindNavigation:
		item.Field = "navigation"
	case diag.KindVersion:
		item.Field = "versions"
	case diag.KindOrder:
		item.Field = "order"
	case diag.KindMetadata:
		item.Field = "metadata"
	case diag.KindSchema:
		item.Field = "frontmatter"
	case diag.KindRoute:
		if match := strictParseTargetPattern.FindStringSubmatch(item.Message); len(match) == 2 {
			item.Target = match[1]
		} else {
			item.Field = "route"
		}
	}
	collector.Add(item)
}

func allSections(sections map[string]*model.Section) []*model.Section {
	paths := make([]string, 0, len(sections))
	for sectionPath := range sections {
		paths = append(paths, sectionPath)
	}
	sort.Strings(paths)
	result := make([]*model.Section, 0, len(paths))
	for _, sectionPath := range paths {
		if sections[sectionPath] != nil {
			result = append(result, sections[sectionPath])
		}
	}
	return result
}

func requiredSectionPaths(sources vault.StrictFrontmatterResult) map[string]struct{} {
	paths := map[string]struct{}{".": {}}
	for _, source := range sources.Sources {
		if source.Section != nil {
			paths[source.Section.SectionPath] = struct{}{}
			addParentPaths(paths, source.Section.SectionPath)
			continue
		}
		if source.Article == nil {
			continue
		}
		sectionPath := path.Dir(source.RelPath)
		paths[sectionPath] = struct{}{}
		addParentPaths(paths, sectionPath)
	}
	return paths
}

func addParentPaths(paths map[string]struct{}, value string) {
	for current := value; current != "." && current != ""; current = path.Dir(current) {
		paths[current] = struct{}{}
	}
	paths["."] = struct{}{}
}

func sectionSourcePath(sectionPath string) string {
	if sectionPath == "." {
		return "_index.md"
	}
	return path.Join(sectionPath, "_index.md")
}

func hasSectionParent(sections map[string]*model.Section, sectionPath string) bool {
	parent := path.Dir(sectionPath)
	for {
		if _, ok := sections[parent]; ok {
			return true
		}
		if parent == "." {
			return false
		}
		parent = path.Dir(parent)
	}
}

func planVersions(config *model.VersionsConfig, vaultRoot string, sections map[string]*model.Section, collector *diag.Collector) ([]*model.Version, map[string]string) {
	if config == nil {
		return nil, map[string]string{}
	}
	root, err := normalizeVersionDirectory(config.Root)
	if err != nil {
		record(collector, diag.KindVersion, "oxpio.yaml", "versions.root: %v", err)
	}
	if root != "" {
		if _, _, statErr := internalfsutil.InspectContainedDirectory(vaultRoot, root); statErr != nil {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions.root %q: %v", root, statErr)
		}
		if _, ok := sections[root]; !ok {
			record(collector, diag.KindVersion, sectionSourcePath(root), "versions.root %q must contain _index.md", root)
		}
	}
	if strings.TrimSpace(config.Default) == "" {
		record(collector, diag.KindVersion, "oxpio.yaml", "versions.default is required")
	}
	if len(config.Entries) == 0 {
		record(collector, diag.KindVersion, "oxpio.yaml", "versions.entries must not be empty")
	}

	versions := make([]*model.Version, 0, len(config.Entries))
	byID := make(map[string]struct{}, len(config.Entries))
	bySource := make([]string, 0, len(config.Entries))
	for index, entry := range config.Entries {
		if entry.ID == "" || !validVersionID(entry.ID) {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions.entries[%d].id is not a valid ASCII path segment", index)
		}
		if entry.Label == "" {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions.entries[%d].label is required", index)
		}
		if _, exists := byID[entry.ID]; exists {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions entry id %q is duplicated", entry.ID)
		}
		byID[entry.ID] = struct{}{}
		source, sourceErr := normalizeVersionDirectory(entry.Source)
		if sourceErr != nil {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions entry %q source: %v", entry.ID, sourceErr)
			continue
		}
		for _, other := range bySource {
			if source == other || isDescendant(source, other) || isDescendant(other, source) {
				record(collector, diag.KindVersion, "oxpio.yaml", "version source %q overlaps %q", source, other)
			}
		}
		bySource = append(bySource, source)
		fullSource := path.Join(root, source)
		if _, _, statErr := internalfsutil.InspectContainedDirectory(vaultRoot, fullSource); statErr != nil {
			record(collector, diag.KindVersion, "oxpio.yaml", "version %q source %q: %v", entry.ID, fullSource, statErr)
		}
		if section, ok := sections[fullSource]; !ok {
			record(collector, diag.KindVersion, sectionSourcePath(fullSource), "version source %q must contain _index.md", fullSource)
		} else if !section.Publish {
			// A configured version root is the selector destination for that
			// entry, so it must be explicitly public even when its ancestor
			// tree is otherwise valid.
			record(collector, diag.KindVersion, section.SourcePath, "version source %q must set publish: true", fullSource)
		}
		versions = append(versions, &model.Version{ID: entry.ID, Label: entry.Label, Source: fullSource})
	}
	if config.Default != "" {
		if _, ok := byID[config.Default]; !ok {
			record(collector, diag.KindVersion, "oxpio.yaml", "versions.default %q does not identify an entry", config.Default)
		}
	}

	versionByPath := make(map[string]string)
	for _, version := range versions {
		if version == nil {
			continue
		}
		for sectionPath := range sections {
			if sectionPath == version.Source || isDescendant(sectionPath, version.Source) {
				if old := versionByPath[sectionPath]; old != "" && old != version.ID {
					record(collector, diag.KindVersion, sectionSourcePath(sectionPath), "section belongs to overlapping versions %q and %q", old, version.ID)
				}
				versionByPath[sectionPath] = version.ID
			}
		}
	}
	return versions, versionByPath
}

func normalizeVersionDirectory(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." || strings.Contains(raw, `\`) || strings.Contains(raw, "//") || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "../") || strings.Contains(raw, "/../") || strings.Contains(raw, "/./") {
		return "", fmt.Errorf("must be a normalized vault-relative directory path")
	}
	cleaned := path.Clean(raw)
	if cleaned != raw || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || !internalfsutil.IsPortableSitePath(cleaned) {
		return "", fmt.Errorf("must be a normalized vault-relative directory path")
	}
	return cleaned, nil
}

func validVersionID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

func isDescendant(value, parent string) bool {
	return value != parent && strings.HasPrefix(value, parent+"/")
}

func attachSections(plan *model.SitePlan, sections map[string]*model.Section, versions []*model.Version, collector *diag.Collector) {
	for _, section := range sections {
		section.Children = nil
		section.Parent = nil
	}
	for _, section := range sections {
		if section == nil || section.RelPath == "." {
			continue
		}
		parentPath := path.Dir(section.RelPath)
		parent := sections[parentPath]
		for parent == nil && parentPath != "." {
			parentPath = path.Dir(parentPath)
			parent = sections[parentPath]
		}
		if parent == nil {
			continue
		}
		if section.VersionID != "" && parent.VersionID != section.VersionID {
			continue
		}
		if section.VersionID == "" && parent.VersionID != "" {
			continue
		}
		section.Parent = parent
		parent.Children = append(parent.Children, section)
	}
	for _, section := range sections {
		sortSections(section.Children)
	}
	root := sections["."]
	plan.Root = root
	if root != nil && root.VersionID != "" {
		record(collector, diag.KindVersion, root.SourcePath, "vault root cannot be a version section")
	}
	for _, version := range versions {
		if version == nil {
			continue
		}
		version.Root = sections[version.Source]
		if version.Root == nil {
			continue
		}
		version.Root.Parent = nil
		version.Sections = collectSubsections(version.Root)
	}
}

func computeEffectivePublish(sections map[string]*model.Section, versions []*model.Version, collector *diag.Collector) {
	var visit func(*model.Section, bool, string)
	visit = func(section *model.Section, inherited bool, hiddenBy string) {
		if section == nil {
			return
		}
		if !inherited && section.Publish && hiddenBy != "" {
			record(collector, diag.KindSection, section.SourcePath, "published section %q is hidden by ancestor %q", section.RelPath, hiddenBy)
		}
		section.EffectivePublish = inherited && section.Publish
		section.HiddenBy = hiddenBy
		nextHiddenBy := hiddenBy
		if inherited && !section.Publish {
			nextHiddenBy = section.SourcePath
		}
		for _, child := range section.Children {
			visit(child, section.EffectivePublish, nextHiddenBy)
		}
	}
	roots := make([]*model.Section, 0, len(sections))
	for _, section := range sections {
		if section != nil && section.Parent == nil && section.VersionID == "" {
			roots = append(roots, section)
		}
	}
	sortSections(roots)
	for _, root := range roots {
		visit(root, true, "")
	}
	for _, version := range versions {
		if version == nil || version.Root == nil {
			continue
		}
		inherited, hiddenBy := true, ""
		for parentPath := path.Dir(version.Root.RelPath); parentPath != "." && parentPath != ""; parentPath = path.Dir(parentPath) {
			parent := sections[parentPath]
			if parent != nil && !parent.EffectivePublish {
				inherited = false
				if hiddenBy == "" {
					hiddenBy = parent.SourcePath
				}
			}
		}
		visit(version.Root, inherited, hiddenBy)
	}
}

func assignSectionRoutes(plan *model.SitePlan, sections map[string]*model.Section, versions []*model.Version, versionConfig *model.VersionsConfig, collector *diag.Collector) {
	sectionPaths := make([]string, 0, len(sections))
	for sectionPath := range sections {
		sectionPaths = append(sectionPaths, sectionPath)
	}
	sort.Strings(sectionPaths)
	for _, sectionPath := range sectionPaths {
		section := sections[sectionPath]
		if section == nil || !section.EffectivePublish {
			continue
		}
		var route string
		if section.VersionID == "" {
			route = sectionRoute(section.RelPath)
		} else {
			version := findVersion(versions, section.VersionID)
			if version == nil {
				continue
			}
			relative := strings.TrimPrefix(section.RelPath, version.Source)
			relative = strings.TrimPrefix(relative, "/")
			route = versionRoute(versionConfig.Root, version.ID, relative)
		}
		section.Route = route
		claimRoute(plan, route, section.SourcePath, collector)
		plan.Sections = append(plan.Sections, section)
	}
	// Keep the canonical section order independent of map traversal.
	sort.Slice(plan.Sections, func(i, j int) bool { return sectionPathKey(plan.Sections[i]) < sectionPathKey(plan.Sections[j]) })
	for _, section := range plan.Sections {
		section.Breadcrumbs = breadcrumbs(section)
	}
}

func assignArticles(plan *model.SitePlan, sections map[string]*model.Section, versions []*model.Version, versionConfig *model.VersionsConfig, articles []*model.Note, collector *diag.Collector) {
	for _, article := range articles {
		if article == nil {
			continue
		}
		sectionPath := path.Dir(article.RelPath)
		section := sections[sectionPath]
		if section == nil {
			record(collector, diag.KindSection, article.RelPath, "article directory %q has no _index.md", sectionPath)
			continue
		}
		article.SectionPath = section.RelPath
		article.VersionID = section.VersionID
		if _, _, prefixErr := slug.NumericPrefix(articleFileStem(article.RelPath)); prefixErr != nil {
			record(collector, diag.KindOrder, article.RelPath, "%v", prefixErr)
			continue
		}
		if article.Frontmatter.Publish == nil || !*article.Frontmatter.Publish {
			continue
		}
		if !section.EffectivePublish {
			hiddenBy := section.HiddenBy
			if hiddenBy == "" && !section.Publish {
				hiddenBy = section.SourcePath
			}
			if hiddenBy != "" {
				record(collector, diag.KindSection, article.RelPath, "published article %q is hidden by ancestor %q", article.RelPath, hiddenBy)
			}
			continue
		}
		explicit := (*string)(nil)
		if article.Frontmatter.Slug != "" {
			explicit = &article.Frontmatter.Slug
		}
		segment, err := slug.GenerateArticleSegment(explicit, article.RelPath)
		if err != nil {
			record(collector, diag.KindRoute, article.RelPath, "%v", err)
			continue
		}
		article.Slug = segment
		if section.VersionID == "" {
			article.Route = joinRoute(section.Route, segment)
		} else {
			version := findVersion(versions, section.VersionID)
			if version == nil {
				continue
			}
			relativeSection := strings.TrimPrefix(section.RelPath, version.Source)
			relativeSection = strings.TrimPrefix(relativeSection, "/")
			prefix := relativeSection
			if prefix != "" {
				prefix += "/"
			}
			article.Route = versionRoute(versionConfig.Root, version.ID, prefix+segment)
		}
		claimRoute(plan, article.Route, article.RelPath, collector)
		section.Articles = append(section.Articles, article)
		plan.Articles = append(plan.Articles, article)
		switch article.Frontmatter.Type {
		case "doc":
			section.Documents = append(section.Documents, article)
			plan.Documents = append(plan.Documents, article)
		case "post":
			section.Posts = append(section.Posts, article)
			plan.Posts = append(plan.Posts, article)
		case "page":
			section.Pages = append(section.Pages, article)
			plan.Pages = append(plan.Pages, article)
		}
	}
}

func validateNavigation(sections map[string]*model.Section, navigation []model.NavigationItem, baseURL string, lines map[string]int, collector *diag.Collector) {
	seen := make(map[string]int, len(navigation))
	for index, item := range navigation {
		field := fmt.Sprintf("navigation[%d].url", index)
		if item.Section != "" {
			field = fmt.Sprintf("navigation[%d].section", index)
		}
		report := func(format string, args ...any) {
			collector.Add(diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindNavigation, Location: diag.Location{Path: "oxpio.yaml", Line: lines[field]}, Field: field, Message: fmt.Sprintf(format, args...)})
		}
		targetKey := navigationTargetKey(item, sections, baseURL)
		if previous, exists := seen[targetKey]; exists {
			report("navigation[%d] duplicates navigation[%d] target", index, previous)
		} else {
			seen[targetKey] = index
		}
		if item.Section == "" {
			continue
		}
		section := sections[item.Section]
		if section == nil {
			report("navigation[%d] targets missing section %q", index, item.Section)
			continue
		}
		if !section.EffectivePublish {
			report("navigation[%d] targets unpublished section %q", index, item.Section)
		}
		if section.VersionID != "" {
			report("navigation[%d] cannot target version entry section %q", index, item.Section)
		}
	}
}

func navigationTargetKey(item model.NavigationItem, sections map[string]*model.Section, baseURL string) string {
	if item.Section != "" {
		if section := sections[item.Section]; section != nil && section.Route != "" {
			return "route:" + section.Route
		}
		return "section:" + item.Section
	}
	parsed, err := url.Parse(item.URL)
	if err != nil {
		return "url:" + item.URL
	}
	if !parsed.IsAbs() && parsed.Host == "" && strings.HasPrefix(item.URL, "/") && !strings.HasPrefix(item.URL, "//") {
		return navigationRouteTargetKey(navigationURLPath(parsed), parsed)
	}
	base, baseErr := url.Parse(baseURL)
	if baseErr == nil && parsed.IsAbs() && parsed.Host != "" && strings.EqualFold(parsed.Scheme, base.Scheme) && strings.EqualFold(parsed.Host, base.Host) {
		basePath, targetPath := navigationURLPath(base), navigationURLPath(parsed)
		if basePath == "/" {
			return navigationRouteTargetKey(targetPath, parsed)
		}
		switch {
		case targetPath == basePath:
			return navigationRouteTargetKey("/", parsed)
		case strings.HasPrefix(targetPath, basePath):
			return navigationRouteTargetKey("/"+strings.TrimPrefix(targetPath, basePath), parsed)
		}
	}
	normalized := *parsed
	normalized.Scheme = strings.ToLower(normalized.Scheme)
	normalized.Host = strings.ToLower(normalized.Host)
	return "url:" + normalized.String()
}

func navigationURLPath(parsed *url.URL) string {
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" || escapedPath == "/" {
		return "/"
	}
	return routeKey("/" + strings.Trim(escapedPath, "/") + "/")
}

func navigationRouteTargetKey(route string, parsed *url.URL) string {
	key := "route:" + route
	if parsed.RawQuery != "" {
		key += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		key += "#" + parsed.Fragment
	}
	return key
}

func validateStrictOptionalInputs(vaultRoot, outputPath string, plan *model.SitePlan, scan vault.ScanResult, index *model.VaultIndex, collector *diag.Collector) {
	if plan == nil || collector == nil {
		return
	}
	if plan.Config.CustomCSS != "" {
		if _, _, _, err := internalfsutil.ReadContainedRegularFile(vaultRoot, plan.Config.CustomCSS); err != nil {
			record(collector, diag.KindMetadata, plan.Config.CustomCSS, "custom CSS: %v", err)
		}
	}
	if plan.Config.ThemeDir == "" {
		return
	}
	relTheme, err := filepath.Rel(vaultRoot, plan.Config.ThemeDir)
	if err != nil || relTheme == ".." || strings.HasPrefix(relTheme, ".."+string(filepath.Separator)) || filepath.IsAbs(relTheme) {
		record(collector, diag.KindMetadata, plan.Config.ThemeDir, "theme directory must be inside the vault")
		return
	}
	inputs := make(map[string][]byte)
	assetSources := make(map[string]string)
	cssSource := ""
	_ = filepath.WalkDir(plan.Config.ThemeDir, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			record(collector, diag.KindMetadata, current, "theme entry: %v", walkErr)
			return nil
		}
		if outputPath != "" && internalfsutil.SamePath(current, outputPath) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(plan.Config.ThemeDir, current)
		if err != nil {
			record(collector, diag.KindMetadata, current, "theme entry path: %v", err)
			return nil
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			record(collector, diag.KindMetadata, current, "theme entry %q must not be a symbolic link", rel)
			return nil
		}
		if entry.IsDir() {
			if rel != "assets" && !strings.HasPrefix(rel, "assets/") {
				record(collector, diag.KindMetadata, current, "unsupported theme directory %q", rel)
			}
			return nil
		}
		_, data, _, readErr := internalfsutil.ReadContainedRegularFile(vaultRoot, current)
		if readErr != nil {
			record(collector, diag.KindMetadata, current, "theme entry %q: %v", rel, readErr)
			return nil
		}
		if rel == "slots.html" {
			if slotErr := render.ValidateThemeSlots(string(data)); slotErr != nil {
				record(collector, diag.KindMetadata, current, "theme slots: %v", slotErr)
			} else {
				plan.Config.ThemeSlots = string(data)
			}
			return nil
		}
		source := filepath.ToSlash(filepath.Join(relTheme, filepath.FromSlash(rel)))
		if rel == "theme.css" {
			cssSource = source
		} else if strings.HasPrefix(rel, "assets/") {
			assetSources[strings.TrimPrefix(rel, "assets/")] = source
		} else {
			record(collector, diag.KindMetadata, current, "unsupported theme entry %q", rel)
			return nil
		}
		inputs[source] = data
		return nil
	})
	planThemeAssets(plan, scan, index, inputs, assetSources, cssSource, collector)
}

func portableVaultAssetPath(value string) bool {
	return internalfsutil.IsPortableSitePath(strings.ReplaceAll(value, "%", "x"))
}

func assetRecord(collector *diag.Collector, owner string, line int, field, target, format string, args ...any) {
	if collector == nil {
		return
	}
	collector.Add(diag.Diagnostic{
		Severity: diag.SeverityError, Kind: diag.KindMetadata,
		Location: diag.Location{Path: owner, Line: line}, Field: field, Target: target,
		Message: fmt.Sprintf(format, args...),
	})
}

func validatePlannedAssets(vaultRoot, outputPath string, plan *model.SitePlan, sections map[string]*model.Section, sources vault.StrictFrontmatterResult, resourceFiles []string, collector *diag.Collector) (map[string]*model.Asset, map[string]assetDiagnostic) {
	seen := make(map[string]struct{})
	validated := make(map[string]*model.Asset)
	owners := make(map[string]assetDiagnostic)
	resourceVersions := make(map[string]string, len(resourceFiles))
	for _, resource := range resourceFiles {
		physical := path.Dir(resource)
		bestDepth := -1
		for sectionPath, section := range sections {
			if section == nil || section.VersionID == "" || physical != sectionPath && !strings.HasPrefix(physical, sectionPath+"/") {
				continue
			}
			depth := len(strings.Split(sectionPath, "/"))
			if depth > bestDepth {
				resourceVersions[resource] = section.VersionID
				bestDepth = depth
			}
		}
	}
	check := func(source, kind, owner, ownerVersion string, line int) {
		if source == "" {
			return
		}
		seenKey := kind + "\x00" + ownerVersion + "\x00" + owner + "\x00" + source
		if _, exists := seen[seenKey]; exists {
			return
		}
		seen[seenKey] = struct{}{}
		candidate := filepath.Join(vaultRoot, filepath.FromSlash(source))
		if outputPath != "" && internalfsutil.PathWithinRoot(outputPath, candidate) {
			assetRecord(collector, owner, line, kind, source, "%s must not be inside the generated output", kind)
			return
		}
		if resourceVersion := resourceVersions[source]; resourceVersion != "" && resourceVersion != ownerVersion {
			assetRecord(collector, owner, line, kind, source, "%s belongs to version %q and cannot be used from version %q", kind, resourceVersion, ownerVersion)
			return
		}
		if !internalasset.IsPublishableAssetPath(source) || strings.Contains(source, `\`) || strings.HasPrefix(source, "/") || strings.Contains(source, "?") || strings.Contains(source, "#") || path.Clean(source) != source || strings.HasPrefix(path.Clean(source), "../") || !portableVaultAssetPath(source) {
			assetRecord(collector, owner, line, kind, source, "%s must be a normalized vault-relative local asset", kind)
			return
		}
		lower := strings.ToLower(source)
		supported := strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".webp")
		if kind == "banner" || kind == "defaultImg" {
			supported = supported || strings.HasSuffix(lower, ".svg")
		}
		if !supported {
			assetRecord(collector, owner, line, kind, source, "%s has an unsupported format", kind)
			return
		}
		_, data, _, err := internalfsutil.ReadContainedRegularFile(vaultRoot, source)
		if err != nil {
			assetRecord(collector, owner, line, kind, source, "%s cannot be read: %v", kind, err)
			return
		}
		if strings.HasSuffix(lower, ".svg") {
			if err := internalasset.ValidateLocalSVG(data); err != nil {
				assetRecord(collector, owner, line, kind, source, "%s SVG: %v", kind, err)
				return
			}
			if plannedAssetReference(plan, kind, owner) {
				validated[source] = &model.Asset{SrcPath: source}
				if _, exists := owners[source]; !exists {
					owners[source] = assetDiagnostic{path: owner, line: line, field: kind}
				}
			}
			return
		}
		if _, format, err := image.Decode(bytes.NewReader(data)); err != nil {
			assetRecord(collector, owner, line, kind, source, "%s cannot be decoded: %v", kind, err)
		} else if format != "png" && format != "jpeg" && format != "webp" {
			assetRecord(collector, owner, line, kind, source, "%s decoded as unsupported format %q", kind, format)
		} else if plannedAssetReference(plan, kind, owner) {
			validated[source] = &model.Asset{SrcPath: source}
			if _, exists := owners[source]; !exists {
				owners[source] = assetDiagnostic{path: owner, line: line, field: kind}
			}
		}
	}
	if plan != nil && plan.Config.DefaultImg != "" && !plan.Config.DefaultImgExternal {
		check(plan.Config.DefaultImg, "defaultImg", internalconfig.Filename, "", plan.Config.FieldLines["defaultImg"])
	}
	for _, source := range sources.Sources {
		if source.Section != nil {
			sectionVersion := ""
			if section := sections[source.Section.SectionPath]; section != nil {
				sectionVersion = section.VersionID
			}
			check(source.Section.Frontmatter.Banner, "banner", source.Section.RelPath, sectionVersion, source.Section.FieldLines["banner"])
		}
		if source.Article != nil {
			articleVersion := ""
			if section := sections[path.Dir(source.Article.RelPath)]; section != nil {
				articleVersion = section.VersionID
			}
			check(source.Article.Frontmatter.Banner, "banner", source.Article.RelPath, articleVersion, source.Article.FieldLines["banner"])
			check(source.Article.Frontmatter.Cover, "cover", source.Article.RelPath, articleVersion, source.Article.FieldLines["cover"])
		}
	}
	return validated, owners
}

func plannedAssetReference(plan *model.SitePlan, kind, owner string) bool {
	if plan == nil {
		return false
	}
	if kind == "defaultImg" {
		return true
	}
	for _, section := range plan.Sections {
		if section != nil && kind == "banner" && section.SourcePath == owner {
			return true
		}
	}
	for _, article := range plan.Articles {
		if article == nil || article.RelPath != owner {
			continue
		}
		return kind == "banner" || kind == "cover"
	}
	return false
}

func validateAssetDestinations(vaultRoot string, plan *model.SitePlan, frontmatter map[string]*model.Asset, owners map[string]assetDiagnostic, index *model.VaultIndex, resourceFiles []string, collector *diag.Collector) map[string]struct{} {
	outputs := make(map[string]struct{})
	if plan == nil {
		return outputs
	}
	if plan.Config.CustomCSS != "" {
		outputs[customCSSDestination] = struct{}{}
	}

	assets := make(map[string]*model.Asset, len(frontmatter))
	for source := range frontmatter {
		assets[source] = &model.Asset{SrcPath: source}
	}
	if index != nil {
		for source, indexed := range index.Assets {
			if indexed == nil {
				continue
			}
			if existing := assets[source]; existing != nil {
				existing.RefCount += indexed.RefCount
				continue
			}
			clone := *indexed
			clone.DstPath = ""
			assets[source] = &clone
		}
	}
	if len(assets) == 0 && len(plan.ThemeAssets) == 0 && len(plan.VaultCSSAssets) == 0 {
		return outputs
	}

	allAssets := make(map[string]*model.Asset, len(assets)+len(plan.ThemeAssets)+len(plan.VaultCSSAssets))
	for source, plannedAsset := range assets {
		allAssets[source] = plannedAsset
	}
	overrides := make(map[string][]byte, len(plan.ThemeAssets)+len(plan.VaultCSSAssets))
	addPlanned := func(plannedAssets map[string]*model.PlannedAsset) {
		for source, planned := range plannedAssets {
			if planned == nil {
				continue
			}
			plannedAsset := planned.Asset
			allAssets[source] = &plannedAsset
			overrides[source] = planned.Data
		}
	}
	addPlanned(plan.ThemeAssets)
	addPlanned(plan.VaultCSSAssets)

	assetCollector, err := internalasset.NewCollectorWithOverrides(vaultRoot, allAssets, nil, resourceFiles, overrides)
	if err != nil {
		record(collector, diag.KindMetadata, vaultRoot, "plan asset destinations: %v", err)
		return outputs
	}
	destinations := assetCollector.PlanDestinations(allAssets)
	applyPlannedAssetDestinations(allAssets, destinations)
	for _, plannedAsset := range allAssets {
		if plannedAsset != nil && plannedAsset.DstPath != "" {
			outputs[plannedAsset.DstPath] = struct{}{}
		}
	}
	distinct := make(map[string]bool, len(allAssets))
	for source := range frontmatter {
		distinct[source] = true
	}
	if err := internalasset.ValidateDestinationCollisions(vaultRoot, allAssets, distinct, overrides); err != nil {
		var collision *internalasset.DestinationCollisionError
		if errors.As(err, &collision) {
			owner := owners[collision.FirstSource]
			if owner.path == "" {
				owner = owners[collision.SecondSource]
			}
			if owner.path == "" {
				owner = assetDiagnosticOwner(plan, collision.FirstSource)
			}
			assetRecord(collector, owner.path, owner.line, owner.field, collision.Destination, "%v", err)
			return outputs
		}
		record(collector, diag.KindMetadata, vaultRoot, "plan asset destinations: %v", err)
	}
	return outputs
}

type assetDiagnostic struct {
	path  string
	line  int
	field string
}

func assetDiagnosticOwner(plan *model.SitePlan, source string) assetDiagnostic {
	if plan != nil {
		if plan.Config.DefaultImg == source && !plan.Config.DefaultImgExternal {
			return assetDiagnostic{path: internalconfig.Filename, line: plan.Config.FieldLines["defaultImg"], field: "defaultImg"}
		}
		for _, section := range plan.Sections {
			if section != nil && section.Banner == source {
				return assetDiagnostic{path: section.SourcePath, field: "banner"}
			}
		}
		for _, article := range plan.Articles {
			if article == nil {
				continue
			}
			if article.Frontmatter.Banner == source {
				return assetDiagnostic{path: article.RelPath, field: "banner"}
			}
			if article.Frontmatter.Cover == source {
				return assetDiagnostic{path: article.RelPath, field: "cover"}
			}
		}
	}
	return assetDiagnostic{path: source, field: "asset"}
}

func applyPlannedAssetDestinations(assets map[string]*model.Asset, destinations map[string]string) {
	for source, destination := range destinations {
		if asset := assets[source]; asset != nil {
			asset.DstPath = destination
		}
	}
}

type validationAssetSink struct{}

func (validationAssetSink) Register(value string) string {
	return "assets/" + strings.TrimPrefix(value, "/")
}

func validateStrictMarkdown(plan *model.SitePlan, index *model.VaultIndex, collector *diag.Collector) {
	if plan == nil || index == nil || collector == nil {
		return
	}
	for _, article := range plan.Articles {
		if article == nil {
			continue
		}
		indexed := index.Notes[article.RelPath]
		if indexed == nil {
			continue
		}
		validateStrictMarkdownNote(index, indexed, plan.PublicPageRoutes, collector)
	}
	for _, section := range plan.Sections {
		if section == nil {
			continue
		}
		note := &model.Note{
			RelPath: section.SourcePath, BodyStartLine: section.BodyStartLine, RawContent: section.RawContent,
			Route: section.Route, VersionID: section.VersionID, Slug: strings.Trim(section.Route, "/"), Headings: section.Headings,
			HeadingSections: section.HeadingSections, OutLinks: section.OutLinks, Embeds: section.Embeds,
			ImageRefs: section.ImageRefs, HasMath: section.HasMath, HasMermaid: section.HasMermaid,
		}
		validateStrictMarkdownNote(index, note, plan.PublicPageRoutes, collector)
	}
}

func validateStrictMarkdownNote(index *model.VaultIndex, note *model.Note, publicPageRoutes map[string]struct{}, collector *diag.Collector) {
	md, _ := markdown.NewMarkdownWithPageRoutes(index, note, validationAssetSink{}, collector, publicPageRoutes)
	var rendered bytes.Buffer
	if err := md.Convert(note.RawContent, &rendered); err != nil {
		collector.Errorf(diag.KindSchema, diag.Location{Path: note.RelPath}, "render Markdown: %v", err)
		return
	}
}

func sortedStrictTags(tags map[string]*model.Tag) []*model.Tag {
	values := make([]*model.Tag, 0, len(tags))
	for _, tag := range tags {
		if tag != nil {
			values = append(values, tag)
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	return values
}

func buildVersionCorrespondence(versions []*model.Version, collector *diag.Collector) {
	byVersionPath := make(map[string]map[string]*model.Note)
	byVersionSectionPath := make(map[string]map[string]*model.Section)
	for _, version := range versions {
		if version == nil {
			continue
		}
		items := make(map[string]*model.Note)
		sections := make(map[string]*model.Section)
		for _, section := range version.Sections {
			if section == nil || !section.EffectivePublish || section.Route == "" {
				continue
			}
			relSection := versionRelativeSourcePath(section.RelPath, version.Source)
			sections[versionCorrespondenceKey(relSection)] = section
			for _, article := range section.Articles {
				// Cross-version article identity is the normalized Markdown source
				// path, not its independently configurable output slug.
				relSource := versionRelativeSourcePath(article.RelPath, version.Source)
				key := versionCorrespondenceKey(relSource)
				if previous := items[key]; previous != nil {
					record(collector, diag.KindVersion, article.RelPath, "version %q articles %q and %q have the same normalized source identity %q", version.ID, previous.RelPath, article.RelPath, key)
					continue
				}
				items[key] = article
			}
		}
		byVersionPath[version.ID] = items
		byVersionSectionPath[version.ID] = sections
	}
	for _, version := range versions {
		if version == nil {
			continue
		}
		for rel, section := range byVersionSectionPath[version.ID] {
			if section.VersionRoutes == nil {
				section.VersionRoutes = make(map[string]string, len(versions))
			}
			for _, otherVersion := range versions {
				if otherVersion == nil {
					continue
				}
				if other := byVersionSectionPath[otherVersion.ID][rel]; other != nil {
					section.VersionRoutes[otherVersion.ID] = other.Route
				} else if otherVersion.Root != nil && otherVersion.Root.Route != "" {
					section.VersionRoutes[otherVersion.ID] = otherVersion.Root.Route
				}
			}
		}
	}
	for _, version := range versions {
		if version == nil {
			continue
		}
		for rel, article := range byVersionPath[version.ID] {
			if article.VersionRoutes == nil {
				article.VersionRoutes = make(map[string]string, len(versions))
			}
			for _, otherVersion := range versions {
				if otherVersion == nil {
					continue
				}
				if otherArticle := byVersionPath[otherVersion.ID][rel]; otherArticle != nil && otherArticle.Frontmatter.Type == article.Frontmatter.Type {
					article.VersionRoutes[otherVersion.ID] = otherArticle.Route
				} else if otherVersion.Root != nil && otherVersion.Root.Route != "" {
					article.VersionRoutes[otherVersion.ID] = otherVersion.Root.Route
				}
			}
		}
	}
}

func versionRelativeSourcePath(sourcePath, versionSource string) string {
	relative := strings.TrimPrefix(sourcePath, versionSource)
	return strings.TrimPrefix(relative, "/")
}

// versionCorrespondenceKey is the cross-version page identity normalization:
// scanner-normalized slash separators followed by Unicode NFKC and case fold.
func versionCorrespondenceKey(relativeSourcePath string) string {
	return fold(relativeSourcePath)
}

func finalizeCollections(plan *model.SitePlan, sections map[string]*model.Section, versions []*model.Version, collector *diag.Collector) {
	for _, section := range sections {
		if section == nil {
			continue
		}
		sortArticles(section.Articles)
		sortArticles(section.Documents)
		sortArticles(section.Posts)
		sortArticles(section.Pages)
	}
	sortArticles(plan.Articles)
	sortArticles(plan.Documents)
	sortArticles(plan.Posts)
	sortArticles(plan.Pages)
	validateCollectionTies(plan, collector)
	for _, version := range versions {
		if version == nil {
			continue
		}
		sort.Slice(version.Sections, func(i, j int) bool { return sectionPathKey(version.Sections[i]) < sectionPathKey(version.Sections[j]) })
	}
	plan.Versions = versions
	for _, section := range plan.Sections {
		if section == nil {
			continue
		}
		section.Breadcrumbs = breadcrumbs(section)
	}
}

func validateCollectionTies(plan *model.SitePlan, collector *diag.Collector) {
	if plan == nil || collector == nil {
		return
	}
	for _, collection := range []struct {
		name  string
		notes []*model.Note
	}{
		{name: "doc", notes: plan.Documents},
		{name: "post", notes: plan.Posts},
		{name: "page", notes: plan.Pages},
	} {
		for start := 0; start < len(collection.notes); {
			end := start + 1
			for end < len(collection.notes) && model.CollectionNotesTie(collection.notes[start], collection.notes[end]) {
				end++
			}
			if end-start > 1 {
				tied := append([]*model.Note(nil), collection.notes[start:end]...)
				sort.Slice(tied, func(i, j int) bool { return tied[i].RelPath < tied[j].RelPath })
				left, right := tied[0], tied[1]
				collector.Add(diag.Diagnostic{
					Severity: diag.SeverityError,
					Kind:     diag.KindOrder,
					Location: diag.Location{Path: left.RelPath},
					Field:    "collection",
					Target:   right.RelPath,
					Message: fmt.Sprintf(
						"%s collection has a complete sort-key tie between %q and %q; change one of the collection's prescribed sort keys",
						collection.name, left.RelPath, right.RelPath,
					),
				})
			}
			start = end
		}
	}
}

func sortArticles(items []*model.Note) {
	sort.SliceStable(items, func(i, j int) bool { return model.LessCollectionNote(items[i], items[j]) })
}

func articleFileStem(relPath string) string {
	filename := path.Base(relPath)
	return strings.TrimSuffix(filename, path.Ext(filename))
}

func numericPrefixValue(prefix string) int64 {
	prefix = strings.TrimRight(prefix, "-_ .")
	value, _ := strconv.ParseInt(prefix, 10, 32)
	return value
}

func sortSections(items []*model.Section) {
	sort.SliceStable(items, func(i, j int) bool { return sectionLess(items[i], items[j]) })
}

func sectionLess(left, right *model.Section) bool {
	if left == nil || right == nil {
		return left != nil
	}
	if (left.Order != nil) != (right.Order != nil) {
		return left.Order != nil
	}
	if left.Order != nil && *left.Order != *right.Order {
		return *left.Order < *right.Order
	}
	lp, lh, _ := slug.NumericPrefix(path.Base(left.RelPath))
	rp, rh, _ := slug.NumericPrefix(path.Base(right.RelPath))
	if lh != rh {
		return lh
	}
	if lh && numericPrefixValue(lp) != numericPrefixValue(rp) {
		return numericPrefixValue(lp) < numericPrefixValue(rp)
	}
	if fold(left.Title) != fold(right.Title) {
		return fold(left.Title) < fold(right.Title)
	}
	leftPath, rightPath := norm.NFKC.String(left.RelPath), norm.NFKC.String(right.RelPath)
	if leftPath != rightPath {
		return leftPath < rightPath
	}
	return left.RelPath < right.RelPath
}

func sectionPathKey(section *model.Section) string {
	if section == nil {
		return ""
	}
	return fold(section.RelPath) + "\x00" + norm.NFKC.String(section.RelPath) + "\x00" + section.RelPath
}
func fold(value string) string { return cases.Fold().String(norm.NFKC.String(value)) }

func collectSubsections(root *model.Section) []*model.Section {
	if root == nil {
		return nil
	}
	result := []*model.Section{root}
	for _, child := range root.Children {
		result = append(result, collectSubsections(child)...)
	}
	return result
}

func findVersion(versions []*model.Version, id string) *model.Version {
	for _, v := range versions {
		if v != nil && v.ID == id {
			return v
		}
	}
	return nil
}

func breadcrumbs(section *model.Section) []model.Breadcrumb {
	var reversed []model.Breadcrumb
	for current := section; current != nil; current = current.Parent {
		if current.Route != "" {
			reversed = append(reversed, model.Breadcrumb{Name: current.Title, URL: current.Route})
		}
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed
}

func reservedRoutes() map[string]struct{} {
	values := []string{"/assets/", "/style.css", "/sitemap.xml", "/robots.txt", "/index.xml", "/404.html", "/.oxpio-output", "/.oxpio-cache/", "/_popover/", "/_oxpio/"}
	result := make(map[string]struct{}, len(values))
	for _, v := range values {
		result[v] = struct{}{}
	}
	return result
}

func initializeRouteOutputIndex(plan *model.SitePlan) {
	if plan.RouteIndex == nil {
		plan.RouteIndex = &model.RouteOutputIndex{}
	}
	index := plan.RouteIndex
	if index.Exact == nil {
		index.Exact = make(map[string]string)
	}
	if index.Prefixes == nil {
		index.Prefixes = make(map[string]string)
	}
	if index.FoldedExact == nil {
		index.FoldedExact = make(map[string]string)
	}
	if index.FoldedPrefix == nil {
		index.FoldedPrefix = make(map[string]string)
	}
	if len(index.Exact) == 0 && len(plan.Routes) > 0 {
		routes := make([]string, 0, len(plan.Routes))
		for route := range plan.Routes {
			routes = append(routes, route)
		}
		sort.Strings(routes)
		for _, route := range routes {
			addRouteOutputClaim(index, route)
		}
	}
}

func addRouteOutputClaim(index *model.RouteOutputIndex, route string) {
	if index == nil {
		return
	}
	destination := physicalOutputPath(routeDestination(route))
	setRouteOutputOwner(index.Exact, destination, route)
	for _, prefix := range routeOutputPrefixes(destination) {
		setRouteOutputOwner(index.Prefixes, prefix, route)
	}
	folded := fold(destination)
	setRouteOutputOwner(index.FoldedExact, folded, route)
	for _, prefix := range routeOutputPrefixes(folded) {
		setRouteOutputOwner(index.FoldedPrefix, prefix, route)
	}
}

func routeOutputConflict(index *model.RouteOutputIndex, destination string) string {
	if index == nil {
		return ""
	}
	find := func(exact, prefixes map[string]string, value string) string {
		best := ""
		for _, prefix := range routeOutputPrefixes(value) {
			if route := exact[prefix]; route != "" && (best == "" || route < best) {
				best = route
			}
		}
		if route := prefixes[value]; route != "" && (best == "" || route < best) {
			best = route
		}
		return best
	}
	physical := physicalOutputPath(destination)
	best := find(index.Exact, index.Prefixes, physical)
	folded := find(index.FoldedExact, index.FoldedPrefix, fold(physical))
	if folded != "" && (best == "" || folded < best) {
		best = folded
	}
	return best
}

func routeOutputPrefixes(value string) []string {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	prefixes := make([]string, 0, len(parts))
	for index := range parts {
		prefix := strings.Join(parts[:index+1], "/")
		if prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func setRouteOutputOwner(owners map[string]string, outputPath, route string) {
	if current := owners[outputPath]; current == "" || route < current {
		owners[outputPath] = route
	}
}

func claimRoute(plan *model.SitePlan, route, owner string, collector *diag.Collector) {
	key := routeKey(route)
	destination := routeDestination(key)
	if forbiddenPhysicalSegment(key) {
		record(collector, diag.KindRoute, owner, "route %q contains a Windows-reserved path segment", route)
		return
	}
	if !portableRoute(key) {
		record(collector, diag.KindRoute, owner, "route %q contains a filesystem-invalid path segment", route)
		return
	}
	for reserved := range plan.ReservedRoutes {
		if outputPathsConflict(destination, reservedDestination(reserved)) {
			record(collector, diag.KindRoute, owner, "route %q conflicts with reserved output", route)
			return
		}
	}
	if plan.Routes == nil {
		plan.Routes = make(map[string]string)
	}
	initializeRouteOutputIndex(plan)
	if existing := routeOutputConflict(plan.RouteIndex, destination); existing != "" {
		record(collector, diag.KindRoute, owner, "route %q conflicts with %q", route, plan.Routes[existing])
		return
	}
	plan.Routes[key] = owner
	addRouteOutputClaim(plan.RouteIndex, key)
}

func claimPublicPageRoute(plan *model.SitePlan, route, owner string, collector *diag.Collector) {
	claimRoute(plan, route, owner, collector)
	key := routeKey(route)
	if plan.Routes[key] == owner {
		plan.PublicPageRoutes[key] = struct{}{}
	}
}

// routeKey normalizes source Unicode before percent encoding, so composed and
// decomposed names claim the same URL. It also canonicalizes percent escapes.
func routeKey(route string) string {
	return slug.CanonicalRoute(route)
}

func routeDestination(route string) string {
	trimmed := strings.Trim(route, "/")
	if trimmed == "" {
		return "index.html"
	}
	return trimmed + "/index.html"
}
func reservedDestination(route string) string { return strings.Trim(route, "/") }
func outputPathsConflict(left, right string) bool {
	left, right = physicalOutputPath(left), physicalOutputPath(right)
	return physicalPathConflict(left, right) || physicalPathConflict(fold(left), fold(right))
}

func physicalOutputPath(value string) string {
	value = strings.Trim(value, "/")
	if decoded, err := url.PathUnescape(value); err == nil {
		return strings.Trim(decoded, "/")
	}
	return value
}

func physicalPathConflict(left, right string) bool {
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func portableRoute(route string) bool {
	for _, segment := range strings.Split(strings.Trim(route, "/"), "/") {
		if segment == "" {
			continue
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, `/\\`) {
			return false
		}
		// Percent and fragment characters are safe in decoded filesystem names;
		// their generated URL spellings remain escaped.
		portableValue := strings.NewReplacer("%", "x", "#", "x").Replace(decoded)
		if !internalfsutil.IsPortableSitePath(portableValue) {
			return false
		}
	}
	return true
}

func forbiddenPhysicalSegment(route string) bool {
	for _, segment := range strings.Split(strings.Trim(route, "/"), "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			decoded = segment
		}
		base := strings.ToUpper(strings.SplitN(decoded, ".", 2)[0])
		switch {
		case base == "CON", base == "PRN", base == "AUX", base == "NUL":
			return true
		case len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9':
			return true
		}
	}
	return false
}

func sectionRoute(rel string) string {
	if rel == "." {
		return "/"
	}
	return "/" + encodePath(rel) + "/"
}

func versionRoute(root, id, relative string) string {
	base := "/" + encodePath(root) + "/" + encodePath(id) + "/"
	if relative == "" {
		return base
	}
	return base + encodePath(relative) + "/"
}

func joinRoute(section, segment string) string {
	if section == "/" {
		return "/" + encodePath(segment) + "/"
	}
	return strings.TrimSuffix(section, "/") + "/" + encodePath(segment) + "/"
}

func encodePath(value string) string {
	parts := strings.Split(value, "/")
	for i, part := range parts {
		parts[i] = encodeSegment(norm.NFKC.String(part))
	}
	return strings.Join(parts, "/")
}

func encodeSegment(value string) string {
	const hex = "0123456789ABCDEF"
	var builder strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '.' || b == '_' || b == '~' {
			builder.WriteByte(b)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hex[b>>4])
		builder.WriteByte(hex[b&0x0f])
	}
	return builder.String()
}
