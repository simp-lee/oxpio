package render

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/markdown"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/slug"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"
)

// RenderStrictSection renders a normalized section through the selected Go
// html/template theme and immutable page View Model.
func RenderStrictSection(plan *model.SitePlan, section *model.Section, index *model.VaultIndex, assets markdown.AssetSink) ([]byte, error) {
	if section == nil {
		return nil, fmt.Errorf("section page requires a plan and section")
	}
	return RenderStrictSectionPage(plan, section, section.Route, section.Articles, index, assets)
}

// RenderStrictSectionPage renders one canonical page of a section article list.
func RenderStrictSectionPage(plan *model.SitePlan, section *model.Section, route string, notes []*model.Note, index *model.VaultIndex, assets markdown.AssetSink, themeRenderers ...ThemeRenderer) ([]byte, error) {
	if plan == nil || section == nil {
		return nil, fmt.Errorf("section page requires a plan and section")
	}
	sectionNote := &model.Note{
		RelPath: section.SourcePath, Route: section.Route, VersionID: section.VersionID, BasePath: strictBasePath(plan), Slug: strings.Trim(section.Route, "/"),
		RawContent: section.RawContent, Headings: section.Headings, HeadingSections: section.HeadingSections,
		OutLinks: section.OutLinks, Embeds: section.Embeds, ImageRefs: section.ImageRefs,
		HasMath: section.HasMath, HasMermaid: section.HasMermaid,
	}
	content, err := strictMarkdown(plan, index, sectionNote, assets)
	if err != nil {
		return nil, err
	}
	content, _, leadingTitleID, err := strictDropLeadingTitleHeading(content, section.Title)
	if err != nil {
		return nil, err
	}
	headingID, _ := strictLeadingTitlePlacement(content, sectionNote, leadingTitleID)
	if section.Banner != "" && section.BannerURL == "" {
		return nil, fmt.Errorf("section banner %q has no planned asset destination", section.Banner)
	}
	return strictDocument(plan, route, section.Title, section.Description, section.Breadcrumbs, section.VersionID, section.VersionRoutes, "", nil, section.SourcePath, StrictSidebarNodes(plan, section.VersionID), content, themedPageExtras{index: index, entries: notes, titleID: headingID}, firstThemeRenderer(themeRenderers))
}

// RenderStrictArticleWithRecommendations renders a normalized article page with
// the same recommendation DTO that the Agent adapter returns, preserving score
// and signal evidence for themes.
func RenderStrictArticleWithRecommendations(plan *model.SitePlan, article *model.Note, previous, next *model.Note, position, total int, backlinks, related []*model.Note, recommendations []model.RelatedRecommendation, index *model.VaultIndex, assets markdown.AssetSink, themeRenderers ...ThemeRenderer) ([]byte, error) {
	if plan == nil || article == nil {
		return nil, fmt.Errorf("article page requires a plan and article")
	}
	renderArticle := *article
	renderArticle.BasePath = strictBasePath(plan)
	content, err := strictMarkdown(plan, index, &renderArticle, assets)
	if err != nil {
		return nil, err
	}
	content, _, leadingTitleID, err := strictDropLeadingTitleHeading(content, article.Frontmatter.Title)
	if err != nil {
		return nil, err
	}
	headingID, tocHeadingID := strictLeadingTitlePlacement(content, article, leadingTitleID)
	if plan.Config.Popover.Enabled {
		content, err = annotateStrictPopovers(content, article, index, plan)
		if err != nil {
			return nil, err
		}
	}
	section := findStrictSection(plan, article.SectionPath, article.VersionID)
	breadcrumbs := []model.Breadcrumb(nil)
	if section != nil {
		breadcrumbs = section.Breadcrumbs
	}
	if article.Frontmatter.Banner != "" && article.BannerURL == "" {
		return nil, fmt.Errorf("article banner %q has no planned asset destination", article.Frontmatter.Banner)
	}
	var metadataHTML, tocHTML strings.Builder
	writeStrictArticleMetadata(&metadataHTML, article)
	writeStrictTOC(&tocHTML, article, tocHeadingID)
	return strictDocument(plan, article.Route, article.Frontmatter.Title, article.Frontmatter.Description, breadcrumbs, article.VersionID, article.VersionRoutes, article.SocialImage, article, article.RelPath, StrictSidebarNodes(plan, article.VersionID), content, themedPageExtras{index: index, previous: previous, next: next, position: position, total: total, backlinks: backlinks, related: related, relatedRecommendations: recommendations, titleID: headingID, metadataHTML: metadataHTML.String(), tocHTML: tocHTML.String()}, firstThemeRenderer(themeRenderers))
}

// RenderStrictNotFound renders the fixed static 404 page.
func RenderStrictNotFound(plan *model.SitePlan, themeRenderers ...ThemeRenderer) ([]byte, error) {
	if plan == nil {
		return nil, fmt.Errorf("404 page requires a plan")
	}
	return strictDocument(plan, "/404.html", "Not found", "", nil, "", nil, "", nil, "", StrictSidebarNodes(plan, ""), `The requested page could not be found.`, themedPageExtras{}, firstThemeRenderer(themeRenderers))
}

// RenderStrictTag renders a deterministic unversioned tag archive from the
// normalized index.
func RenderStrictTag(plan *model.SitePlan, tag *model.Tag, notes []*model.Note) ([]byte, error) {
	if tag == nil {
		return nil, fmt.Errorf("tag page requires a plan and tag")
	}
	return RenderStrictTagPageForVersion(plan, tag, "/"+slug.EncodePath(tag.Slug)+"/", "", notes)
}

// RenderStrictTagPage renders one canonical page of an unversioned tag list.
func RenderStrictTagPage(plan *model.SitePlan, tag *model.Tag, route string, notes []*model.Note) ([]byte, error) {
	return RenderStrictTagPageForVersion(plan, tag, route, "", notes)
}

// RenderStrictTagPageForVersion renders a tag page in the selected partition.
func RenderStrictTagPageForVersion(plan *model.SitePlan, tag *model.Tag, route, versionID string, notes []*model.Note, themeRenderers ...ThemeRenderer) ([]byte, error) {
	if plan == nil || tag == nil {
		return nil, fmt.Errorf("tag page requires a plan and tag")
	}
	return strictDocument(plan, route, "Tag: "+tag.Name, "", nil, versionID, nil, "", nil, "", StrictSidebarNodes(plan, versionID), "", themedPageExtras{tag: tag, entries: notes}, firstThemeRenderer(themeRenderers))
}

// RenderStrictTimeline renders the optional unversioned recent-article archive.
func RenderStrictTimeline(plan *model.SitePlan, route string, notes []*model.Note) ([]byte, error) {
	return RenderStrictTimelineForVersion(plan, route, "", notes)
}

// RenderStrictTimelineForVersion renders a timeline in the selected partition.
func RenderStrictTimelineForVersion(plan *model.SitePlan, route, versionID string, notes []*model.Note, themeRenderers ...ThemeRenderer) ([]byte, error) {
	if plan == nil || strings.Trim(route, "/") == "" {
		return nil, fmt.Errorf("timeline page requires a plan and route")
	}
	return strictDocument(plan, route, "Recent articles", "", nil, versionID, nil, "", nil, "", StrictSidebarNodes(plan, versionID), "", themedPageExtras{entries: notes}, firstThemeRenderer(themeRenderers))
}

func timelinePageInfo(plan *model.SitePlan, route string) (page, total int) {
	if plan == nil {
		return 1, 1
	}
	var timeline *model.TimelinePagePlan
	for _, partition := range plan.Partitions {
		if partition != nil && partition.Timeline != nil {
			if _, ok := partition.Timeline.PageForRoute(route); ok {
				timeline = partition.Timeline
				break
			}
		}
	}
	if timeline == nil {
		timeline = plan.Timeline
	}
	if timeline == nil || len(timeline.Pages) == 0 {
		return 1, 1
	}
	total = len(timeline.Pages)
	planned, ok := timeline.PageForRoute(route)
	if !ok {
		return 1, total
	}
	return planned.Number, total
}

func annotateStrictPopovers(content string, article *model.Note, index *model.VaultIndex, plan *model.SitePlan) (string, error) {
	if strings.TrimSpace(content) == "" || article == nil || index == nil {
		return content, nil
	}
	basePath := "/"
	if plan != nil {
		basePath = strictBasePath(plan)
	}
	base, _ := url.Parse("https://oxpio.invalid" + strings.TrimSuffix(basePath, "/") + article.Route)
	type replacement struct {
		start, end int
		value      []byte
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	replacements := make([]replacement, 0)
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
			return "", fmt.Errorf("scan article HTML for popovers: %w", tokenizer.Err())
		}
		if tokenType != xhtml.StartTagToken {
			continue
		}
		raw = append([]byte(nil), raw...)
		token := tokenizer.Token()
		if !strings.EqualFold(token.Data, "a") {
			continue
		}
		href := ""
		for _, attribute := range token.Attr {
			if strings.EqualFold(attribute.Key, "href") {
				href = attribute.Val
				break
			}
		}
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		target := strictPopoverTarget(base, href, index, basePath, article.VersionID)
		if target == nil {
			continue
		}
		insertion := len(raw) - 1
		if insertion > 0 && raw[insertion-1] == '/' {
			insertion--
		}
		value := make([]byte, 0, len(raw)+len(target.RelPath)+22)
		value = append(value, raw[:insertion]...)
		value = append(value, ` data-popover-path="`...)
		value = append(value, esc(target.RelPath)...)
		value = append(value, `"`...)
		value = append(value, raw[insertion:]...)
		replacements = append(replacements, replacement{start: start, end: offset, value: value})
	}
	if len(replacements) == 0 {
		return content, nil
	}
	var output bytes.Buffer
	previous := 0
	for _, current := range replacements {
		output.WriteString(content[previous:current.start])
		output.Write(current.value)
		previous = current.end
	}
	output.WriteString(content[previous:])
	return output.String(), nil
}

func strictPopoverTarget(base *url.URL, href string, index *model.VaultIndex, basePath, versionID string) *model.Note {
	if base == nil || index == nil {
		return nil
	}
	targetURL, err := url.Parse(href)
	if err != nil || targetURL.IsAbs() || targetURL.Host != "" {
		return nil
	}
	resolved := base.ResolveReference(targetURL)
	cleaned := strings.TrimSuffix(resolved.EscapedPath(), "/index.html")
	prefix := strings.TrimSuffix(basePath, "/")
	if prefix != "" && (cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/")) {
		cleaned = strings.TrimPrefix(cleaned, prefix)
	}
	if cleaned == "" {
		cleaned = "/"
	}
	if !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	for _, note := range index.Notes {
		if note != nil && note.Route == cleaned && (note.VersionID == "" || note.VersionID == versionID) {
			return note
		}
	}
	return nil
}

func writeStrictTOC(body *strings.Builder, article *model.Note, omitHeadingID string) {
	if body == nil || article == nil || len(article.Headings) == 0 {
		return
	}
	items := 0
	skippedHeading := false
	for _, heading := range article.Headings {
		if omitHeadingID != "" && !skippedHeading && heading.ID == omitHeadingID {
			skippedHeading = true
			continue
		}
		if heading.ID != "" && strings.TrimSpace(heading.Text) != "" {
			items++
		}
	}
	if items == 0 {
		return
	}
	body.WriteString(`<nav class="table-of-contents" aria-label="Table of contents"><ol>`)
	skippedHeading = false
	for _, heading := range article.Headings {
		if omitHeadingID != "" && !skippedHeading && heading.ID == omitHeadingID {
			skippedHeading = true
			continue
		}
		if heading.ID != "" && strings.TrimSpace(heading.Text) != "" {
			_, _ = fmt.Fprintf(body, `<li class="toc-level-%d"><a href="#%s">%s</a></li>`, heading.Level, esc(heading.ID), esc(heading.Text))
		}
	}
	body.WriteString(`</ol></nav>`)
}

func writeStrictArticleMetadata(body *strings.Builder, article *model.Note) {
	if body == nil || article == nil {
		return
	}
	values := []struct{ name, value string }{
		{"Author", article.Frontmatter.Author},
		{"Published", strictMetadataTime(article.Frontmatter.Date)},
		{"Updated", strictMetadataTime(article.Frontmatter.Updated)},
		{"Reviewed", strictMetadataTime(article.Frontmatter.Reviewed)},
		{"Status", article.Frontmatter.Status},
		{"Audience", article.Frontmatter.Audience},
		{"Product version", article.Frontmatter.ProductVersion},
		{"Series", article.Frontmatter.Series},
	}
	written := false
	for _, item := range values {
		if item.value != "" {
			if !written {
				body.WriteString(`<dl class="article-metadata">`)
				written = true
			}
			_, _ = fmt.Fprintf(body, `<div><dt>%s</dt><dd>%s</dd></div>`, esc(item.name), esc(item.value))
		}
	}
	if written {
		body.WriteString(`</dl>`)
	}
}

func strictMetadataTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("Jan 2, 2006")
}

func firstThemeRenderer(renderers []ThemeRenderer) ThemeRenderer {
	if len(renderers) == 0 {
		return nil
	}
	return renderers[0]
}

func strictDocument(plan *model.SitePlan, currentRoute, title, description string, breadcrumbs []model.Breadcrumb, versionID string, versionRoutes map[string]string, socialImage string, metadata *model.Note, sourcePath string, sidebarNodes []model.SidebarNode, body string, extras themedPageExtras, renderer ThemeRenderer) ([]byte, error) {
	return renderThemedDocument(plan, currentRoute, title, description, breadcrumbs, versionID, versionRoutes, socialImage, metadata, sourcePath, sidebarNodes, body, extras, renderer)
}

func strictBasePath(plan *model.SitePlan) string {
	if plan == nil {
		return "/"
	}
	parsed, err := url.Parse(plan.Config.BaseURL)
	if err != nil || parsed.EscapedPath() == "" {
		return "/"
	}
	return parsed.EscapedPath()
}

func strictSidebarRoot(plan *model.SitePlan, versionID string) *model.Section {
	if plan == nil {
		return nil
	}
	if versionID != "" {
		for _, version := range plan.Versions {
			if version != nil && version.ID == versionID {
				return version.Root
			}
		}
	}
	return plan.Root
}

// StrictSidebarNodes returns the complete sidebar tree for the selected
// version. The same normalized section tree is used for the shared runtime
// payload and for the server-rendered no-JavaScript sidebar.
func StrictSidebarNodes(plan *model.SitePlan, versionID string) []model.SidebarNode {
	if plan == nil || !plan.Config.Sidebar.Enabled {
		return nil
	}
	return strictSidebarChildren(strictSidebarRoot(plan, versionID))
}

func strictSidebarChildren(section *model.Section) []model.SidebarNode {
	if section == nil {
		return nil
	}
	result := make([]model.SidebarNode, 0, len(section.Children)+len(section.Articles))
	for _, child := range section.Children {
		if child != nil && child.EffectivePublish {
			result = append(result, model.SidebarNode{
				Name: child.Title, URL: child.Route, IsDir: true,
				Children: strictSidebarChildren(child),
			})
		}
	}
	for _, article := range section.Articles {
		if article != nil {
			result = append(result, model.SidebarNode{
				Name: article.Frontmatter.Title, URL: article.Route, Source: article.RelPath,
			})
		}
	}
	return result
}

func strictPageKind(route string, metadata *model.Note, sourcePath string) string {
	if metadata != nil {
		return "article"
	}
	if route == "/404.html" {
		return "404"
	}
	if strings.HasPrefix(route, "/tags/") {
		return "tag"
	}
	if sourcePath != "" {
		return "section"
	}
	return "timeline"
}

func strictMarkdown(plan *model.SitePlan, index *model.VaultIndex, note *model.Note, assets markdown.AssetSink) (string, error) {
	var output bytes.Buffer
	collector := diag.NewCollector()
	md, _ := markdown.NewMarkdownWithPageRoutes(index, note, assets, collector, plan.PublicPageRoutes)
	if err := md.Convert(note.RawContent, &output); err != nil {
		return "", err
	}
	return output.String(), nil
}

func strictLeadingTitlePlacement(content string, note *model.Note, leadingID string) (shellID, tocHeadingID string) {
	if leadingID == "" {
		return "", ""
	}
	retainedIDs := strictRenderedHTMLIDs(content)
	matchingHeadings := 0
	for _, heading := range noteHeadings(note) {
		if heading.ID == leadingID {
			matchingHeadings++
		}
	}
	if _, collision := retainedIDs[leadingID]; collision {
		if !strictSourceStartsWithRawHTMLHeading(note) && matchingHeadings > 0 {
			return "", leadingID
		}
		return "", ""
	}
	if matchingHeadings > 0 && !strictSourceStartsWithRawHTMLHeading(note) {
		return leadingID, leadingID
	}
	return leadingID, ""
}

func noteHeadings(note *model.Note) []model.Heading {
	if note == nil {
		return nil
	}
	return note.Headings
}

func strictSourceStartsWithRawHTMLHeading(note *model.Note) bool {
	if note == nil || len(note.RawContent) == 0 {
		return false
	}
	source := note.RawContent
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(source))
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case xhtml.ErrorToken:
			return false
		case xhtml.CommentToken:
			continue
		case xhtml.TextToken:
			if strings.TrimSpace(string(tokenizer.Text())) == "" {
				continue
			}
			return false
		case xhtml.StartTagToken:
			tagName, _ := tokenizer.TagName()
			return strings.EqualFold(string(tagName), "h1")
		default:
			return false
		}
	}
}

func strictRenderedHTMLComments(content string) []byte {
	var comments bytes.Buffer
	rawTextTag := ""
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	for {
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		if tokenType == xhtml.ErrorToken {
			return comments.Bytes()
		}
		if tokenType == xhtml.CommentToken {
			comments.Write(raw)
			continue
		}
		if rawTextTag != "" {
			switch tokenType {
			case xhtml.TextToken:
				strictAppendHTMLCommentLexemes(&comments, raw)
			case xhtml.EndTagToken:
				tagName, _ := tokenizer.TagName()
				if strings.EqualFold(string(tagName), rawTextTag) {
					rawTextTag = ""
				}
			}
			continue
		}
		if tokenType == xhtml.StartTagToken {
			tagName, _ := tokenizer.TagName()
			if strings.EqualFold(string(tagName), "script") || strings.EqualFold(string(tagName), "style") {
				rawTextTag = strings.ToLower(string(tagName))
			}
		}
	}
}

func strictAppendHTMLCommentLexemes(output *bytes.Buffer, content []byte) {
	for offset := 0; offset < len(content); {
		start := bytes.Index(content[offset:], []byte("<!--"))
		if start < 0 {
			return
		}
		start += offset
		end := bytes.Index(content[start+4:], []byte("-->"))
		if end < 0 {
			return
		}
		end += start + 7
		output.Write(content[start:end])
		offset = end
	}
}

func strictRenderedHTMLIDs(content string) map[string]struct{} {
	ids := make(map[string]struct{})
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	for {
		tokenType := tokenizer.Next()
		if tokenType == xhtml.ErrorToken {
			return ids
		}
		if tokenType != xhtml.StartTagToken && tokenType != xhtml.SelfClosingTagToken {
			continue
		}
		for _, attribute := range tokenizer.Token().Attr {
			if attribute.Key == "id" && attribute.Val != "" {
				ids[attribute.Val] = struct{}{}
			}
		}
	}
}

// strictDropLeadingTitleHeading keeps the authored source intact while avoiding
// a duplicate visible H1 when the shell already renders the frontmatter title.
// Only a leading, exact title match is removed; authored headings elsewhere are
// preserved.
func strictDropLeadingTitleHeading(content, title string) (string, bool, string, error) {
	if strings.TrimSpace(content) == "" || strings.TrimSpace(title) == "" {
		return content, false, "", nil
	}
	tokenizer := xhtml.NewTokenizer(strings.NewReader(content))
	wrappers := make([]string, 0)
	offset := 0
	for {
		tokenType := tokenizer.Next()
		raw := tokenizer.Raw()
		start := offset
		offset += len(raw)
		switch tokenType {
		case xhtml.ErrorToken:
			if tokenizer.Err() == io.EOF {
				return content, false, "", nil
			}
			return "", false, "", fmt.Errorf("scan rendered Markdown heading: %w", tokenizer.Err())
		case xhtml.TextToken:
			if strings.TrimSpace(string(tokenizer.Text())) == "" {
				continue
			}
			return content, false, "", nil
		case xhtml.CommentToken:
			continue
		case xhtml.EndTagToken:
			tagName, _ := tokenizer.TagName()
			if len(wrappers) > 0 && strings.EqualFold(string(tagName), wrappers[len(wrappers)-1]) {
				wrappers = wrappers[:len(wrappers)-1]
				continue
			}
			return content, false, "", nil
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			tagName := strings.ToLower(token.Data)
			if strictHTMLTagInvisible(tagName, token.Attr) {
				if tokenType != xhtml.SelfClosingTagToken && !strictHTMLVoidTag(tagName) {
					consumed, err := strictSkipInvisibleHTML(tokenizer, tagName)
					offset += consumed
					if err != nil && err != io.EOF {
						return "", false, "", fmt.Errorf("scan invisible rendered HTML: %w", err)
					}
				}
				continue
			}
			if tagName != "h1" {
				if tokenType == xhtml.SelfClosingTagToken || strictHTMLVoidTag(tagName) {
					return content, false, "", nil
				}
				wrappers = append(wrappers, tagName)
				continue
			}
			if tokenType == xhtml.SelfClosingTagToken {
				return content, false, "", nil
			}
			end := offset
			depth := 1
			for depth > 0 {
				innerType := tokenizer.Next()
				innerRaw := tokenizer.Raw()
				end += len(innerRaw)
				if innerType == xhtml.ErrorToken {
					if tokenizer.Err() == io.EOF {
						return content, false, "", nil
					}
					return "", false, "", fmt.Errorf("scan rendered Markdown heading: %w", tokenizer.Err())
				}
				switch innerType {
				case xhtml.StartTagToken:
					innerName, _ := tokenizer.TagName()
					if string(innerName) == "h1" {
						depth++
					}
				case xhtml.EndTagToken:
					innerName, _ := tokenizer.TagName()
					if string(innerName) == "h1" {
						depth--
					}
				}
			}
			headingID, matches, err := strictRenderedHeadingIdentity(content[start:end], title)
			if err != nil {
				return "", false, "", err
			}
			if !matches {
				return content, false, "", nil
			}
			preservedComments := strictRenderedHTMLComments(content[start:end])
			if len(preservedComments) == 0 {
				return content[:start] + content[end:], true, headingID, nil
			}
			return content[:start] + string(preservedComments) + content[end:], true, headingID, nil
		default:
			return content, false, "", nil
		}
	}
}

func strictRenderedHeadingIdentity(fragment, title string) (string, bool, error) {
	context := &xhtml.Node{Type: xhtml.ElementNode, DataAtom: atom.Div, Data: "div"}
	nodes, err := xhtml.ParseFragment(strings.NewReader(fragment), context)
	if err != nil {
		return "", false, fmt.Errorf("parse rendered Markdown heading: %w", err)
	}
	for _, node := range nodes {
		if node.Type != xhtml.ElementNode || node.Data != "h1" {
			continue
		}
		if normalizeStrictHeadingText(strictHTMLText(node)) != normalizeStrictHeadingText(title) {
			return "", false, nil
		}
		for _, attr := range node.Attr {
			if attr.Key == "id" {
				return string(attr.Val), true, nil
			}
		}
		return "", true, nil
	}
	return "", false, nil
}

func normalizeStrictHeadingText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func strictHTMLText(node *xhtml.Node) string {
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	if strictHTMLNodeInvisible(node) {
		return ""
	}
	var text strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		boundary := child.Type == xhtml.ElementNode && strictHTMLBoundaryTag(child.Data)
		if boundary {
			text.WriteByte(' ')
		}
		text.WriteString(strictHTMLText(child))
		if boundary {
			text.WriteByte(' ')
		}
	}
	return text.String()
}

func strictHTMLNodeInvisible(node *xhtml.Node) bool {
	return node != nil && strictHTMLTagInvisible(node.Data, node.Attr)
}

func strictHTMLTagInvisible(tag string, attrs []xhtml.Attribute) bool {
	if tag == "script" || tag == "style" || tag == "template" {
		return true
	}
	for _, attr := range attrs {
		switch attr.Key {
		case "hidden":
			return true
		case "style":
			if markdown.StyleHidesHTMLText(string(attr.Val)) {
				return true
			}
		}
	}
	return false
}

func strictHTMLVoidTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func strictSkipInvisibleHTML(tokenizer *xhtml.Tokenizer, tag string) (int, error) {
	depth := 1
	consumed := 0
	for depth > 0 {
		tokenType := tokenizer.Next()
		consumed += len(tokenizer.Raw())
		if tokenType == xhtml.ErrorToken {
			return consumed, tokenizer.Err()
		}
		switch tokenType {
		case xhtml.StartTagToken:
			token := tokenizer.Token()
			if strings.ToLower(token.Data) == tag && !strictHTMLVoidTag(tag) {
				depth++
			}
		case xhtml.EndTagToken:
			tagName, _ := tokenizer.TagName()
			if strings.EqualFold(string(tagName), tag) {
				depth--
			}
		}
	}
	return consumed, nil
}

func strictHTMLBoundaryTag(tag string) bool {
	switch tag {
	case "address", "article", "aside", "blockquote", "br", "caption", "dd", "div", "dl", "dt", "figcaption", "figure", "footer", "form", "header", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "li", "main", "nav", "ol", "p", "pre", "section", "table", "tbody", "td", "tfoot", "th", "thead", "tr", "ul":
		return true
	default:
		return false
	}
}

func strictPublicVersions(plan *model.SitePlan) []*model.Version {
	if plan == nil {
		return nil
	}
	result := make([]*model.Version, 0, len(plan.Versions))
	for _, version := range plan.Versions {
		if version != nil && version.Root != nil && version.Root.EffectivePublish && version.Root.Route != "" {
			result = append(result, version)
		}
	}
	return result
}

func findStrictSection(plan *model.SitePlan, relPath, versionID string) *model.Section {
	for _, section := range plan.Sections {
		if section != nil && section.RelPath == relPath && section.VersionID == versionID {
			return section
		}
	}
	return nil
}

func strictNavigationIsCurrent(raw, currentRoute, canonical string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !parsed.IsAbs() && parsed.Host == "" {
		return strictNavigationMatch(raw) == strictNavigationMatch(currentRoute)
	}

	current, err := url.Parse(canonical)
	if err != nil || !current.IsAbs() || !strings.EqualFold(parsed.Scheme, current.Scheme) || !strings.EqualFold(parsed.Host, current.Host) {
		return false
	}
	return strictNavigationMatch(navigationRelativeURI(parsed)) == strictNavigationMatch(navigationRelativeURI(current))
}

func strictNavigationMatch(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(raw, "/") {
		return raw
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "/" || escapedPath == "" {
		return "/"
	}

	parts := strings.Split(strings.Trim(escapedPath, "/"), "/")
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil {
			return raw
		}
		parts[index] = slug.EncodeSegment(norm.NFKC.String(decoded))
	}
	match := "/" + strings.Join(parts, "/")
	if strings.HasSuffix(escapedPath, "/") || path.Ext(match) == "" {
		match += "/"
	}
	if parsed.RawQuery != "" {
		match += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		match += "#" + parsed.Fragment
	}
	return match
}

func navigationRelativeURI(value *url.URL) string {
	if value == nil {
		return "/"
	}
	pathValue := value.EscapedPath()
	if pathValue == "" {
		pathValue = "/"
	}
	if value.RawQuery != "" {
		pathValue += "?" + value.RawQuery
	}
	if value.Fragment != "" {
		pathValue += "#" + value.Fragment
	}
	return pathValue
}

func strictSitePath(plan *model.SitePlan, route string) string {
	if plan == nil || route == "" {
		return route
	}
	parsed, err := url.Parse(plan.Config.BaseURL)
	if err != nil {
		return route
	}
	prefix := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if prefix == "" {
		return route
	}
	return prefix + "/" + strings.TrimPrefix(route, "/")
}

func strictAbsoluteURL(plan *model.SitePlan, route string) string {
	return strings.TrimSuffix(plan.Config.BaseURL, "/") + route
}

func strictSourceURL(templateURL, sourcePath string) string {
	return strings.Replace(templateURL, ":path", slug.EncodeSourcePath(sourcePath), 1)
}

func esc(value string) string { return template.HTMLEscapeString(value) }

// StrictRouteOutputPath returns the URL-escaped logical output path for route.
// The publisher decodes it once when selecting the corresponding disk path.
func StrictRouteOutputPath(route string) string {
	trimmed := strings.Trim(route, "/")
	if trimmed == "" {
		return "index.html"
	}
	return path.Join(trimmed, "index.html")
}
