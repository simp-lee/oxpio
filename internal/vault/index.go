package vault

import (
	"fmt"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gohugoio/hugo-goldmark-extensions/passthrough"
	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	gmhashtag "go.abhg.dev/goldmark/hashtag"
	gmwikilink "go.abhg.dev/goldmark/wikilink"
)

// BuildIndexOptions controls optional pass-1 outputs and bounded concurrency.
type BuildIndexOptions struct {
	Concurrency                 int
	CollectRelatedSemantic      bool
	ResourceSections            []*model.Section
	IncludeUnpublishedResources bool
}

// IndexResult owns the immutable public index and any optional build-only sidecars.
// ValidationIndex is retained only for internal full-source validation; it must
// never be handed to public rendering or response layers.
type IndexResult struct {
	Index           *model.VaultIndex
	ValidationIndex *model.VaultIndex
	RelatedSemantic []model.RelatedSemanticDocument
}

type indexBuildOptions struct {
	concurrency            int
	collectRelatedSemantic bool
	resourceVersions       map[string]string
	fullResourceScan       ScanResult
	onNoteStart            func(*model.Note)
	onNoteDone             func(*model.Note)
}

type indexedNoteResult struct {
	note            *model.Note
	assets          map[string]*model.Asset
	relatedSemantic *model.RelatedSemanticDocument
}

// BuildStrictIndex builds the public projection selected by the canonical site
// plan and a separate full-source index used only for strict validation. It
// deliberately does not assign fallback slugs or read a second
// frontmatter/configuration contract.
func BuildStrictIndex(scanResult ScanResult, sources StrictFrontmatterResult, public []*model.Note, publicSections []*model.Section, diagCollector *diag.Collector, options BuildIndexOptions) (IndexResult, error) {
	unpublished := model.UnpublishedLookup{
		Notes:       make(map[string]*model.Note),
		NoteByName:  make(map[string][]*model.Note),
		AliasByName: make(map[string][]*model.Note),
		Sections:    make(map[string]*model.Section),
	}
	publicPaths := make(map[string]struct{}, len(public))
	for _, note := range public {
		if note != nil {
			publicPaths[note.RelPath] = struct{}{}
		}
	}
	for _, note := range sources.AllArticles {
		if note == nil {
			continue
		}
		if _, ok := publicPaths[note.RelPath]; ok {
			continue
		}
		unpublished.Notes[note.RelPath] = note
		key := noteLookupName(note.RelPath)
		unpublished.NoteByName[key] = append(unpublished.NoteByName[key], note)
		for _, alias := range note.Aliases {
			if key := aliasLookupName(alias); key != "" {
				unpublished.AliasByName[key] = append(unpublished.AliasByName[key], note)
			}
		}
	}
	idx := &model.VaultIndex{
		AttachmentFolderPath: scanResult.AttachmentFolderPath,
		Notes:                make(map[string]*model.Note, len(public)),
		Sections:             make(map[string]*model.Section, len(publicSections)),
		SectionsBySource:     make(map[string]*model.Section, len(publicSections)),
		SectionsByRoute:      make(map[string]*model.Section, len(publicSections)),
		NoteBySlug:           make(map[string]*model.Note, len(public)),
		NoteByName:           make(map[string][]*model.Note),
		AliasByName:          make(map[string][]*model.Note),
		Tags:                 make(map[string]*model.Tag),
		Assets:               make(map[string]*model.Asset),
		Unpublished:          unpublished,
	}
	parser := markdown.NewParser(diagCollector)
	scopeSections := options.ResourceSections
	if len(scopeSections) == 0 {
		scopeSections = publicSections
	}
	resourceFiles := scanResult.ResourceFiles
	if !options.IncludeUnpublishedResources {
		resourceFiles = PublishableResourceFiles(resourceFiles, scopeSections)
	}
	publicScanResult := scanResult.WithResourceFiles(resourceFiles)
	resourceVersions := resourceVersionMap(resourceFiles, scopeSections)
	idx.ResourceVersions = resourceVersions
	indexedNotes := indexNotes(public, publicScanResult, parser, diagCollector, indexBuildOptions{
		concurrency:            options.Concurrency,
		collectRelatedSemantic: options.CollectRelatedSemantic,
		resourceVersions:       resourceVersions,
		fullResourceScan:       scanResult,
	})
	var relatedSemantic []model.RelatedSemanticDocument
	if options.CollectRelatedSemantic && len(indexedNotes) > 0 {
		relatedSemantic = newRelatedSemanticOwner(len(indexedNotes))
	}
	for _, indexed := range indexedNotes {
		note := indexed.note
		if note == nil {
			continue
		}
		idx.Notes[note.RelPath] = note
		if note.Slug != "" {
			idx.NoteBySlug[note.Slug] = note
		}
		key := noteLookupName(note.RelPath)
		idx.NoteByName[key] = append(idx.NoteByName[key], note)
		for _, alias := range note.Aliases {
			if key := aliasLookupName(alias); key != "" {
				idx.AliasByName[key] = append(idx.AliasByName[key], note)
			}
		}
		mergeIndexedAssets(idx.Assets, indexed.assets)
		if indexed.relatedSemantic != nil {
			relatedSemantic = append(relatedSemantic, *indexed.relatedSemantic)
		}
	}
	idx.SetAssets(idx.Assets)
	idx.SetResources(resourceFiles)
	sectionNotes := make([]*model.Note, 0, len(publicSections))
	for _, section := range publicSections {
		if section != nil {
			idx.Sections[section.RelPath] = section
			idx.SectionsBySource[section.SourcePath] = section
			if section.Route != "" {
				idx.SectionsByRoute[section.Route] = section
			}
			sectionNotes = append(sectionNotes, sectionIndexNote(section))
		}
	}
	for _, indexed := range indexNotes(sectionNotes, publicScanResult, parser, diagCollector, indexBuildOptions{concurrency: options.Concurrency, resourceVersions: resourceVersions, fullResourceScan: scanResult}) {
		if indexed.note == nil {
			continue
		}
		mergeIndexedAssets(idx.Assets, indexed.assets)
		for _, section := range publicSections {
			if section == nil || section.SourcePath != indexed.note.RelPath {
				continue
			}
			applyIndexedSection(section, indexed.note)
			break
		}
	}
	idx.SetAssets(idx.Assets)
	idx.Tags = buildTagIndex(public)

	// The public index intentionally omits hidden Markdown and resources. Parse
	// the omitted source with the same analyzer against the full scan so hidden
	// bodies cannot bypass resource/link validation.
	sourceSections := sourceSectionModels(sources, scopeSections, publicSections)
	sourceResourceVersions := resourceVersionMap(scanResult.ResourceFiles, sourceSections)
	hiddenArticles := make([]*model.Note, 0, len(sources.AllArticles))
	for _, note := range sources.AllArticles {
		if note == nil {
			continue
		}
		if _, ok := publicPaths[note.RelPath]; !ok {
			hiddenArticles = append(hiddenArticles, note)
		}
	}
	hiddenIndexedNotes := indexNotes(hiddenArticles, scanResult, parser, diagCollector, indexBuildOptions{
		concurrency:      options.Concurrency,
		resourceVersions: sourceResourceVersions,
		fullResourceScan: scanResult,
	})
	publicSectionPaths := make(map[string]struct{}, len(publicSections))
	for _, section := range publicSections {
		if section != nil {
			publicSectionPaths[section.SourcePath] = struct{}{}
		}
	}
	for _, section := range sourceSections {
		if section == nil {
			continue
		}
		if _, ok := publicSectionPaths[section.SourcePath]; !ok {
			idx.Unpublished.Sections[section.SourcePath] = section
		}
	}
	hiddenSectionNotes := make([]*model.Note, 0, len(sourceSections))
	for _, section := range sourceSections {
		if section == nil {
			continue
		}
		if _, ok := publicSectionPaths[section.SourcePath]; !ok {
			hiddenSectionNotes = append(hiddenSectionNotes, sectionIndexNote(section))
		}
	}
	hiddenIndexedSections := indexNotes(hiddenSectionNotes, scanResult, parser, diagCollector, indexBuildOptions{
		concurrency:      options.Concurrency,
		resourceVersions: sourceResourceVersions,
		fullResourceScan: scanResult,
	})
	for _, indexed := range hiddenIndexedSections {
		if indexed.note == nil {
			continue
		}
		for _, section := range sourceSections {
			if section != nil && section.SourcePath == indexed.note.RelPath {
				applyIndexedSection(section, indexed.note)
				break
			}
		}
	}

	return IndexResult{
		Index:           idx,
		ValidationIndex: buildValidationIndex(idx, sourceSections, hiddenIndexedNotes, hiddenIndexedSections, scanResult, sourceResourceVersions),
		RelatedSemantic: relatedSemantic,
	}, nil
}

func sectionIndexNote(section *model.Section) *model.Note {
	if section == nil {
		return nil
	}
	return &model.Note{
		RelPath: section.SourcePath, SectionPath: section.RelPath,
		RawContent: cloneBytes(section.RawContent), BodyStartLine: section.BodyStartLine,
		Route: section.Route, VersionID: section.VersionID, Slug: strings.Trim(section.Route, "/"),
		Frontmatter: model.Frontmatter{Title: section.Title},
	}
}

func sourceSectionModels(sources StrictFrontmatterResult, scoped, public []*model.Section) []*model.Section {
	bySource := make(map[string]*model.Section, len(scoped)+len(public))
	for _, section := range scoped {
		if section != nil {
			bySource[section.SourcePath] = section
		}
	}
	for _, section := range public {
		if section != nil {
			bySource[section.SourcePath] = section
		}
	}

	result := make([]*model.Section, 0, len(sources.Sections))
	for _, source := range sources.Sections {
		if source == nil {
			continue
		}
		if section := bySource[source.RelPath]; section != nil {
			result = append(result, section)
			continue
		}
		result = append(result, &model.Section{
			RelPath: source.SectionPath, SourcePath: source.RelPath,
			Title: source.Frontmatter.Title, Description: source.Frontmatter.Description,
			Publish: source.Frontmatter.Publish != nil && *source.Frontmatter.Publish,
			Order:   source.Frontmatter.Order, Banner: source.Frontmatter.Banner, BannerAlt: source.Frontmatter.BannerAlt,
			RawContent: cloneBytes(source.RawContent), BodyStartLine: source.BodyStartLine,
		})
	}
	return result
}

func applyIndexedSection(section *model.Section, note *model.Note) {
	if section == nil || note == nil {
		return
	}
	section.RawContent = cloneBytes(note.RawContent)
	section.Headings = append([]model.Heading(nil), note.Headings...)
	if note.HeadingSections != nil {
		section.HeadingSections = make(map[string]model.SectionRange, len(note.HeadingSections))
		for key, value := range note.HeadingSections {
			section.HeadingSections[key] = value
		}
	} else {
		section.HeadingSections = nil
	}
	section.OutLinks = append([]model.LinkRef(nil), note.OutLinks...)
	section.Embeds = append([]model.EmbedRef(nil), note.Embeds...)
	section.ImageRefs = append([]model.ImageRef(nil), note.ImageRefs...)
	section.HasMath = note.HasMath
	section.HasMermaid = note.HasMermaid
}

func buildValidationIndex(public *model.VaultIndex, sections []*model.Section, hidden, hiddenSections []indexedNoteResult, scan ScanResult, resourceVersions map[string]string) *model.VaultIndex {
	if public == nil {
		return nil
	}
	result := &model.VaultIndex{
		AttachmentFolderPath: public.AttachmentFolderPath,
		Notes:                make(map[string]*model.Note, len(public.Notes)+len(hidden)),
		Sections:             make(map[string]*model.Section, len(sections)),
		SectionsBySource:     make(map[string]*model.Section, len(sections)),
		SectionsByRoute:      make(map[string]*model.Section, len(public.SectionsByRoute)),
		NoteBySlug:           make(map[string]*model.Note, len(public.NoteBySlug)),
		NoteByName:           make(map[string][]*model.Note, len(public.NoteByName)),
		AliasByName:          make(map[string][]*model.Note, len(public.AliasByName)),
		Tags:                 make(map[string]*model.Tag),
		Assets:               make(map[string]*model.Asset),
		Unpublished:          public.Unpublished,
		ResourceVersions:     resourceVersions,
	}
	for relPath, note := range public.Notes {
		result.Notes[relPath] = note
	}
	for slug, note := range public.NoteBySlug {
		result.NoteBySlug[slug] = note
	}
	for name, notes := range public.NoteByName {
		result.NoteByName[name] = append([]*model.Note(nil), notes...)
	}
	for alias, notes := range public.AliasByName {
		result.AliasByName[alias] = append([]*model.Note(nil), notes...)
	}
	for route, section := range public.SectionsByRoute {
		result.SectionsByRoute[route] = section
	}
	for _, section := range sections {
		if section == nil {
			continue
		}
		result.Sections[section.RelPath] = section
		result.SectionsBySource[section.SourcePath] = section
		if section.Route != "" {
			result.SectionsByRoute[section.Route] = section
		}
	}
	mergeIndexedAssets(result.Assets, public.Assets)
	for _, indexed := range hidden {
		if indexed.note == nil {
			continue
		}
		result.Notes[indexed.note.RelPath] = indexed.note
		if indexed.note.Slug != "" {
			result.NoteBySlug[indexed.note.Slug] = indexed.note
		}
		key := noteLookupName(indexed.note.RelPath)
		result.NoteByName[key] = append(result.NoteByName[key], indexed.note)
		for _, alias := range indexed.note.Aliases {
			if key := aliasLookupName(alias); key != "" {
				result.AliasByName[key] = append(result.AliasByName[key], indexed.note)
			}
		}
		mergeIndexedAssets(result.Assets, indexed.assets)
	}
	for _, indexed := range hiddenSections {
		mergeIndexedAssets(result.Assets, indexed.assets)
	}
	allNotes := make([]*model.Note, 0, len(result.Notes))
	for _, note := range result.Notes {
		allNotes = append(allNotes, note)
	}
	result.Tags = buildTagIndex(allNotes)
	result.SetAssets(result.Assets)
	result.SetResources(scan.ResourceFiles)
	return result
}

func resourceVersionMap(resourceFiles []string, sections []*model.Section) map[string]string {
	result := make(map[string]string)
	for _, resource := range resourceFiles {
		physical := path.Dir(resource)
		bestDepth := -1
		for _, section := range sections {
			if section == nil || section.VersionID == "" || physical != section.RelPath && !strings.HasPrefix(physical, section.RelPath+"/") {
				continue
			}
			depth := len(strings.Split(section.RelPath, "/"))
			if depth > bestDepth {
				result[resource] = section.VersionID
				bestDepth = depth
			}
		}
	}
	return result
}

func indexNotes(
	notes []*model.Note,
	scanResult ScanResult,
	parser goldmark.Markdown,
	diagCollector *diag.Collector,
	options indexBuildOptions,
) []indexedNoteResult {
	if len(notes) == 0 {
		return nil
	}

	results := make([]indexedNoteResult, len(notes))
	workerCount := normalizeIndexConcurrency(options.concurrency, len(notes))
	if workerCount <= 1 {
		for index, note := range notes {
			results[index] = buildIndexedNoteResult(note, scanResult, parser, diagCollector, options)
		}
		return results
	}

	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = buildIndexedNoteResult(notes[index], scanResult, parser, diagCollector, options)
			}
		}()
	}

	for index := range notes {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	return results
}

func buildIndexedNoteResult(
	note *model.Note,
	scanResult ScanResult,
	parser goldmark.Markdown,
	diagCollector *diag.Collector,
	options indexBuildOptions,
) indexedNoteResult {
	if note == nil {
		return indexedNoteResult{}
	}

	if options.onNoteStart != nil {
		options.onNoteStart(note)
	}
	if options.onNoteDone != nil {
		defer options.onNoteDone(note)
	}

	note.RawContent = cloneBytes(note.RawContent)
	note.HTMLContent = ""
	note.Summary = ""
	note.Headings = nil
	note.HeadingSections = nil
	note.OutLinks = nil
	note.Embeds = nil
	note.ImageRefs = nil
	note.HasMath = false
	note.HasMermaid = false

	assets := make(map[string]*model.Asset)
	root := parser.Parser().Parse(text.NewReader(note.RawContent))
	lineStarts := lineStartOffsets(note.RawContent)
	fullResourceScan := options.fullResourceScan
	if fullResourceScan.ResourceFiles == nil && len(scanResult.ResourceFiles) > 0 {
		fullResourceScan = scanResult
	}
	inlineTags := extractNoteMetadata(note, scanResult, fullResourceScan, assets, diagCollector, root, note.RawContent, lineStarts, options.resourceVersions)
	note.Tags = mergeNoteTags(note.Tags, inlineTags)

	var relatedSemantic *model.RelatedSemanticDocument
	if options.collectRelatedSemantic {
		headings, body := markdown.RelatedSemanticText(root, note.RawContent)
		relatedSemantic = &model.RelatedSemanticDocument{
			RelPath:  note.RelPath,
			Title:    note.Frontmatter.Title,
			Aliases:  append([]string(nil), note.Aliases...),
			Headings: headings,
			Body:     body,
		}
	}

	return indexedNoteResult{note: note, assets: assets, relatedSemantic: relatedSemantic}
}

//go:noinline
func newRelatedSemanticOwner(capacity int) []model.RelatedSemanticDocument {
	return make([]model.RelatedSemanticDocument, 0, capacity)
}

func normalizeIndexConcurrency(concurrency int, total int) int {
	if total <= 0 {
		return 0
	}
	if concurrency <= 0 {
		concurrency = runtime.NumCPU()
		if concurrency <= 0 {
			concurrency = 1
		}
	}
	if concurrency > total {
		return total
	}
	return concurrency
}

func mergeIndexedAssets(dst map[string]*model.Asset, src map[string]*model.Asset) {
	if len(src) == 0 {
		return
	}

	for srcPath, asset := range src {
		if srcPath == "" || asset == nil {
			continue
		}

		existing := dst[srcPath]
		if existing == nil {
			dst[srcPath] = &model.Asset{
				SrcPath:  srcPath,
				DstPath:  asset.DstPath,
				RefCount: asset.RefCount,
			}
			continue
		}

		existing.RefCount += asset.RefCount
		if existing.DstPath == "" {
			existing.DstPath = asset.DstPath
		}
	}
}

func extractNoteMetadata(
	note *model.Note,
	scanResult ScanResult,
	fullResourceScan ScanResult,
	assets map[string]*model.Asset,
	diagCollector *diag.Collector,
	root gast.Node,
	source []byte,
	lineStarts []int,
	resourceVersions map[string]string,
) []string {
	inlineTags := make([]string, 0)
	var rawHTMLContext markdown.RawHTMLResourceContext
	lineOffset := 0
	if note != nil && note.BodyStartLine > 1 {
		lineOffset = note.BodyStartLine - 1
	}

	_ = gast.Walk(root, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}

		switch current := node.(type) {
		case *gast.Heading:
			note.Headings = append(note.Headings, model.Heading{
				Level: current.Level,
				Text:  markdown.VisibleHeadingText(current, source),
				ID:    headingID(current),
			})
		case *gmhashtag.Node:
			if tagName := normalizeTag(string(current.Tag)); tagName != "" {
				inlineTags = append(inlineTags, tagName)
			}
		case *gast.Link:
			if ref := extractStandardLinkRef(current, source, lineStarts, lineOffset); ref.RawTarget != "" || ref.Fragment != "" {
				note.OutLinks = append(note.OutLinks, ref)
				resourceTarget := markdown.NormalizeDestination(ref.RawTarget)
				resource := resourcepath.LookupPath(note, scanResult.AttachmentFolderPath, resourceTarget, scanResult.LookupResourcePath)
				if resource.Path != "" && resourceVersionAllowed(note, resourceVersions, resource.Path) {
					registerAsset(assets, resource.Path)
				} else if len(resource.Ambiguous) > 0 {
					registerAmbiguousAssets(assets, resource.Ambiguous)
				} else if isHiddenResourceTarget(note, scanResult, fullResourceScan, resourceTarget, false) {
					recordHiddenResourcePermission(diagCollector, note, ref.RawTarget, current, lineStarts, lineOffset)
				}
			}
		case *gmwikilink.Node:
			if current.Embed {
				embedRef := extractEmbedRef(note, fullResourceScan, current, source, lineStarts, lineOffset)
				note.Embeds = append(note.Embeds, embedRef)
				imageLookup := lookupImageEmbedAssetPath(note, scanResult, embedRef.Target)
				if embedRef.IsImage {
					if imageLookup.Path != "" && resourceVersionAllowed(note, resourceVersions, imageLookup.Path) {
						registerAsset(assets, imageLookup.Path)
					} else if len(imageLookup.Ambiguous) > 0 {
						registerAmbiguousAssets(assets, imageLookup.Ambiguous)
						recordAmbiguousImageEmbed(diagCollector, note, embedRef.Target, imageLookup.Ambiguous, current, lineStarts, lineOffset)
					} else if isHiddenResourceTarget(note, scanResult, fullResourceScan, embedRef.Target, true) {
						recordHiddenResourcePermission(diagCollector, note, embedRef.Target, current, lineStarts, lineOffset)
					}
				} else if resourcepath.LooksLikeImage(embedRef.Target) && len(imageLookup.Ambiguous) > 0 {
					registerAmbiguousAssets(assets, imageLookup.Ambiguous)
					recordAmbiguousImageEmbed(diagCollector, note, embedRef.Target, imageLookup.Ambiguous, current, lineStarts, lineOffset)
				} else if isHiddenResourceTarget(note, scanResult, fullResourceScan, embedRef.Target, true) {
					recordHiddenResourcePermission(diagCollector, note, embedRef.Target, current, lineStarts, lineOffset)
				}
			} else {
				note.OutLinks = append(note.OutLinks, extractLinkRef(current, source, lineStarts, lineOffset))
			}
		case *gast.Image:
			imageRef := extractImageRef(current, lineStarts, lineOffset)
			note.ImageRefs = append(note.ImageRefs, imageRef)
			rawDestination := imageRef.RawTarget
			lookup := lookupImageAssetPath(note, scanResult, markdown.NormalizeDestination(rawDestination))
			if lookup.Path != "" && resourceVersionAllowed(note, resourceVersions, lookup.Path) {
				registerAsset(assets, lookup.Path)
			} else if len(lookup.Ambiguous) > 0 {
				registerAmbiguousAssets(assets, lookup.Ambiguous)
				recordAmbiguousMarkdownImage(diagCollector, note, rawDestination, lookup.Ambiguous, current, lineStarts, lineOffset)
			} else if isHiddenResourceTarget(note, scanResult, fullResourceScan, markdown.NormalizeDestination(rawDestination), true) {
				recordHiddenResourcePermission(diagCollector, note, rawDestination, current, lineStarts, lineOffset)
			} else {
				recordUnresolvedMarkdownImage(diagCollector, note, rawDestination, current, lineStarts, lineOffset)
			}
			return gast.WalkSkipChildren, nil
		case *gast.HTMLBlock:
			raw := make([]byte, 0)
			for index := 0; index < current.Lines().Len(); index++ {
				line := current.Lines().At(index)
				raw = append(raw, line.Value(source)...)
			}
			if current.HasClosure() {
				closure := current.ClosureLine
				raw = append(raw, closure.Value(source)...)
			}
			extractRawHTMLAssets(note, scanResult, fullResourceScan, assets, diagCollector, current, raw, lineStarts, lineOffset, resourceVersions, &rawHTMLContext)
		case *gast.RawHTML:
			raw := current.Segments.Value(source)
			extractRawHTMLAssets(note, scanResult, fullResourceScan, assets, diagCollector, current, raw, lineStarts, lineOffset, resourceVersions, &rawHTMLContext)
		case *gast.Text:
			if rawHTMLContext.InStyle() {
				targets, err := markdown.CSSResourceTargets(current.Segment.Value(source))
				if err != nil {
					recordRawHTMLAssetError(diagCollector, note, "", current, lineStarts, lineOffset, "inspect raw HTML style resources: %v", err)
				} else {
					extractRawHTMLAssetTargets(note, scanResult, fullResourceScan, assets, diagCollector, current, targets, lineStarts, lineOffset, resourceVersions)
				}
			}
		case *gast.FencedCodeBlock:
			if isMermaidFence(current.Language(source)) {
				note.HasMermaid = true
			}
		case *passthrough.PassthroughInline, *passthrough.PassthroughBlock:
			note.HasMath = true
		}

		return gast.WalkContinue, nil
	})

	note.HeadingSections = buildHeadingSections(root, source)

	return inlineTags
}

func extractLinkRef(node *gmwikilink.Node, source []byte, lineStarts []int, lineOffset int) model.LinkRef {
	offset, _ := nodeStartOffset(node)

	return model.LinkRef{
		RawTarget: composeRawTarget(string(node.Target), string(node.Fragment)),
		Display:   normalizeInlineText(wikilinkNodeText(source, node)),
		Fragment:  strings.TrimSpace(string(node.Fragment)),
		Line:      lineNumberForNode(node, lineStarts, lineOffset),
		Offset:    offset,
	}
}

func extractStandardLinkRef(node *gast.Link, source []byte, lineStarts []int, lineOffset int) model.LinkRef {
	if node == nil {
		return model.LinkRef{}
	}
	rawTarget := strings.TrimSpace(string(node.Destination))
	fragment := ""
	if _, after, ok := strings.Cut(markdown.NormalizeDestination(rawTarget), "#"); ok {
		fragment = strings.TrimSpace(after)
	}
	offset, _ := nodeStartOffset(node)
	return model.LinkRef{RawTarget: rawTarget, Display: normalizeInlineText(wikilinkNodeText(source, node)), Fragment: fragment, Standard: true, Line: lineNumberForNode(node, lineStarts, lineOffset), Offset: offset}
}

func extractImageRef(node *gast.Image, lineStarts []int, lineOffset int) model.ImageRef {
	offset, _ := nodeStartOffset(node)

	return model.ImageRef{
		RawTarget: strings.TrimSpace(string(node.Destination)),
		Line:      lineNumberForNode(node, lineStarts, lineOffset),
		Offset:    offset,
	}
}

func extractEmbedRef(note *model.Note, scanResult ScanResult, node *gmwikilink.Node, source []byte, lineStarts []int, lineOffset int) model.EmbedRef {
	label := normalizeInlineText(wikilinkNodeText(source, node))
	target := strings.TrimSpace(string(node.Target))
	fragment := strings.TrimSpace(string(node.Fragment))
	isImage := looksLikeImageEmbed(note, scanResult, target)
	offset, _ := nodeStartOffset(node)

	width := 0
	if isImage {
		if parsed, err := strconv.Atoi(strings.TrimSpace(label)); err == nil && parsed > 0 {
			width = parsed
		}
	}

	return model.EmbedRef{
		Target:   target,
		Fragment: fragment,
		IsImage:  isImage,
		Width:    width,
		Line:     lineNumberForNode(node, lineStarts, lineOffset),
		Offset:   offset,
	}
}

func resourceVersionAllowed(note *model.Note, resourceVersions map[string]string, resource string) bool {
	versionID := resourceVersions[resource]
	return versionID == "" || note == nil || note.VersionID == versionID
}

func extractRawHTMLAssets(note *model.Note, scanResult ScanResult, fullResourceScan ScanResult, assets map[string]*model.Asset, diagCollector *diag.Collector, node gast.Node, raw []byte, lineStarts []int, lineOffset int, resourceVersions map[string]string, context *markdown.RawHTMLResourceContext) {
	targets, err := context.Targets(raw)
	if err != nil {
		recordRawHTMLAssetError(diagCollector, note, "", node, lineStarts, lineOffset, "inspect raw HTML resources: %v", err)
		return
	}
	extractRawHTMLAssetTargets(note, scanResult, fullResourceScan, assets, diagCollector, node, targets, lineStarts, lineOffset, resourceVersions)
}

func extractRawHTMLAssetTargets(note *model.Note, scanResult ScanResult, fullResourceScan ScanResult, assets map[string]*model.Asset, diagCollector *diag.Collector, node gast.Node, targets []string, lineStarts []int, lineOffset int, resourceVersions map[string]string) {
	for _, target := range targets {
		if !resourcepath.IsLocalTarget(target) {
			continue
		}
		targetPath := target
		if position := strings.IndexAny(targetPath, "?#"); position >= 0 {
			targetPath = targetPath[:position]
		}
		if targetPath == "" {
			continue
		}
		lookup := resourcepath.LookupPath(note, scanResult.AttachmentFolderPath, targetPath, scanResult.LookupResourcePath)
		if len(lookup.Ambiguous) > 0 {
			registerAmbiguousAssets(assets, lookup.Ambiguous)
			recordRawHTMLAssetError(diagCollector, note, target, node, lineStarts, lineOffset, "raw HTML resource %q matched multiple publishable vault assets after canonical path normalization (%s); refusing canonical fallback", target, strings.Join(lookup.Ambiguous, ", "))
			continue
		}
		if lookup.Path == "" {
			if isHiddenResourceTarget(note, scanResult, fullResourceScan, targetPath, false) {
				recordHiddenResourcePermission(diagCollector, note, target, node, lineStarts, lineOffset)
			} else {
				recordRawHTMLAssetError(diagCollector, note, target, node, lineStarts, lineOffset, "raw HTML resource %q could not be resolved to a publishable vault asset", target)
			}
			continue
		}
		if !resourceVersionAllowed(note, resourceVersions, lookup.Path) {
			recordRawHTMLAssetError(diagCollector, note, target, node, lineStarts, lineOffset, "raw HTML resource %q is outside the current version resource scope", target)
			continue
		}
		registerAsset(assets, lookup.Path)
	}
}

func recordRawHTMLAssetError(diagCollector *diag.Collector, note *model.Note, target string, node gast.Node, lineStarts []int, lineOffset int, format string, args ...any) {
	if diagCollector == nil || note == nil {
		return
	}
	diagCollector.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Kind:     diag.KindUnresolvedAsset,
		Location: diag.Location{Path: note.RelPath, Line: lineNumberForNode(node, lineStarts, lineOffset)},
		Target:   target,
		Message:  fmt.Sprintf(format, args...),
	})
}

func isHiddenResourceTarget(note *model.Note, publicScan, fullScan ScanResult, target string, imageEmbed bool) bool {
	if len(fullScan.ResourceFiles) == 0 {
		return false
	}
	publicLookup := resourcepath.LookupPath(note, publicScan.AttachmentFolderPath, target, publicScan.LookupResourcePath)
	if imageEmbed {
		publicLookup = resourcepath.LookupImageEmbedPath(note, publicScan.AttachmentFolderPath, target, publicScan.LookupResourcePath)
	}
	if publicLookup.Path != "" || len(publicLookup.Ambiguous) > 0 {
		return false
	}
	fullLookup := resourcepath.LookupPath(note, fullScan.AttachmentFolderPath, target, fullScan.LookupResourcePath)
	if imageEmbed {
		fullLookup = resourcepath.LookupImageEmbedPath(note, fullScan.AttachmentFolderPath, target, fullScan.LookupResourcePath)
	}
	return fullLookup.Path != "" || len(fullLookup.Ambiguous) > 0
}

func recordHiddenResourcePermission(diagCollector *diag.Collector, note *model.Note, target string, node gast.Node, lineStarts []int, lineOffset int) {
	if diagCollector == nil || note == nil {
		return
	}
	diagCollector.Add(diag.Diagnostic{
		Severity: diag.SeverityError,
		Kind:     diag.KindUnresolvedAsset,
		Location: diag.Location{Path: note.RelPath, Line: lineNumberForNode(node, lineStarts, lineOffset)},
		Target:   target,
		Message:  "referenced resource is not available to public sources",
	})
}

func registerAsset(assets map[string]*model.Asset, vaultRelPath string) {
	if vaultRelPath == "" {
		return
	}

	asset := assets[vaultRelPath]
	if asset == nil {
		asset = &model.Asset{SrcPath: vaultRelPath}
		assets[vaultRelPath] = asset
	}
	asset.RefCount++
}

func registerAmbiguousAssets(assets map[string]*model.Asset, candidates []string) {
	for _, candidate := range candidates {
		registerAsset(assets, candidate)
	}
}

func headingID(heading *gast.Heading) string {
	if heading == nil {
		return ""
	}

	value, ok := heading.AttributeString("id")
	if !ok {
		return ""
	}

	switch current := value.(type) {
	case []byte:
		return string(current)
	case string:
		return current
	default:
		return fmt.Sprint(current)
	}
}

type headingSectionEntry struct {
	level int
	id    string
	start int
}

func buildHeadingSections(root gast.Node, source []byte) map[string]model.SectionRange {
	entries := make([]headingSectionEntry, 0)

	_ = gast.Walk(root, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}

		heading, ok := node.(*gast.Heading)
		if !ok {
			return gast.WalkContinue, nil
		}

		id := headingID(heading)
		start, ok := nodeStartOffset(heading)
		if !ok || id == "" {
			return gast.WalkContinue, nil
		}

		entries = append(entries, headingSectionEntry{
			level: heading.Level,
			id:    id,
			start: start,
		})

		return gast.WalkContinue, nil
	})

	if len(entries) == 0 {
		return nil
	}

	sections := make(map[string]model.SectionRange, len(entries))
	for i, entry := range entries {
		end := len(source)
		for j := i + 1; j < len(entries); j++ {
			if entries[j].level <= entry.level {
				end = entries[j].start
				break
			}
		}
		if end < entry.start {
			end = entry.start
		}

		sections[entry.id] = model.SectionRange{
			StartOffset: entry.start,
			EndOffset:   end,
		}
	}

	return sections
}

func composeRawTarget(target string, fragment string) string {
	target = strings.TrimSpace(target)
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return target
	}
	if target == "" {
		return "#" + fragment
	}
	return target + "#" + fragment
}

func normalizeInlineText(value string) string {
	return normalizeSummaryWhitespace(value)
}

func wikilinkNodeText(source []byte, node gast.Node) string {
	var builder strings.Builder
	appendWikilinkNodeText(&builder, source, node)
	return builder.String()
}

func appendWikilinkNodeText(builder *strings.Builder, source []byte, node gast.Node) {
	if builder == nil || node == nil {
		return
	}

	switch current := node.(type) {
	case *gast.Text:
		_, _ = builder.Write(current.Value(source))
		if current.SoftLineBreak() || current.HardLineBreak() {
			_ = builder.WriteByte('\n')
		}
	case *gast.String:
		_, _ = builder.Write(current.Value)
	case *gast.RawHTML:
		_, _ = builder.Write(current.Segments.Value(source))
	case *gast.AutoLink:
		_, _ = builder.Write(current.Label(source))
	default:
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			appendWikilinkNodeText(builder, source, child)
		}
	}
}

func normalizeSummaryWhitespace(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

func looksLikeImageEmbed(note *model.Note, scanResult ScanResult, target string) bool {
	lookup := lookupImageEmbedAssetPath(note, scanResult, target)
	return lookup.Path != "" && resourcepath.LooksLikeImage(lookup.Path)
}

func lookupImageEmbedAssetPath(note *model.Note, scanResult ScanResult, target string) model.PathLookupResult {
	return resourcepath.LookupImageEmbedPath(note, scanResult.AttachmentFolderPath, target, scanResult.LookupResourcePath)
}

func lookupImageAssetPath(note *model.Note, scanResult ScanResult, target string) model.PathLookupResult {
	return resourcepath.LookupPath(note, scanResult.AttachmentFolderPath, target, scanResult.LookupResourcePath)
}

func recordUnresolvedMarkdownImage(
	diagCollector *diag.Collector,
	note *model.Note,
	rawTarget string,
	node gast.Node,
	lineStarts []int,
	lineOffset int,
) {
	if diagCollector == nil || note == nil || !resourcepath.IsLocalTarget(markdown.NormalizeDestination(rawTarget)) {
		return
	}

	diagCollector.Add(diag.Diagnostic{
		Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset,
		Location: diag.Location{Path: note.RelPath, Line: lineNumberForNode(node, lineStarts, lineOffset)},
		Target:   rawTarget,
		Message:  fmt.Sprintf("markdown image %q could not be resolved to a publishable vault asset", strings.TrimSpace(rawTarget)),
	})
}

func recordAmbiguousImageEmbed(
	diagCollector *diag.Collector,
	note *model.Note,
	rawTarget string,
	ambiguous []string,
	node gast.Node,
	lineStarts []int,
	lineOffset int,
) {
	if diagCollector == nil || note == nil || len(ambiguous) == 0 {
		return
	}

	diagCollector.Add(diag.Diagnostic{
		Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset,
		Location: diag.Location{Path: note.RelPath, Line: lineNumberForNode(node, lineStarts, lineOffset)},
		Target:   rawTarget,
		Message:  fmt.Sprintf("image embed %q matched multiple publishable vault assets after canonical path normalization (%s); refusing canonical fallback", strings.TrimSpace(rawTarget), strings.Join(ambiguous, ", ")),
	})
}

func recordAmbiguousMarkdownImage(
	diagCollector *diag.Collector,
	note *model.Note,
	rawTarget string,
	ambiguous []string,
	node gast.Node,
	lineStarts []int,
	lineOffset int,
) {
	if diagCollector == nil || note == nil || len(ambiguous) == 0 {
		return
	}

	diagCollector.Add(diag.Diagnostic{
		Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset,
		Location: diag.Location{Path: note.RelPath, Line: lineNumberForNode(node, lineStarts, lineOffset)},
		Target:   rawTarget,
		Message:  fmt.Sprintf("markdown image %q matched multiple publishable vault assets after canonical path normalization (%s); refusing canonical fallback", strings.TrimSpace(rawTarget), strings.Join(ambiguous, ", ")),
	})
}

func lineStartOffsets(source []byte) []int {
	starts := []int{0}
	for index, b := range source {
		if b == '\n' && index+1 <= len(source) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func lineNumberForNode(node gast.Node, lineStarts []int, lineOffset int) int {
	start, ok := nodeStartOffset(node)
	if !ok {
		return 0
	}

	line := sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > start
	})
	if line == 0 {
		return 1 + lineOffset
	}
	return line + lineOffset
}

func nodeStartOffset(node gast.Node) (int, bool) {
	if node == nil {
		return 0, false
	}

	switch current := node.(type) {
	case *gast.Text:
		return current.Segment.Start, true
	}

	if pos := node.Pos(); pos >= 0 {
		return pos, true
	}

	if node.Type() == gast.TypeBlock {
		if lines := node.Lines(); lines != nil && lines.Len() > 0 {
			return lines.At(0).Start, true
		}
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if start, ok := nodeStartOffset(child); ok {
			return start, true
		}
	}
	return 0, false
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	cloned := make([]byte, len(src))
	copy(cloned, src)
	return cloned
}

func isMermaidFence(language []byte) bool {
	return strings.EqualFold(strings.TrimSpace(string(language)), "mermaid")
}
