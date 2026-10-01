package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/simp-lee/obsite/internal/diag"
	"github.com/simp-lee/obsite/internal/markdown"
	"github.com/simp-lee/obsite/internal/model"
	"github.com/simp-lee/obsite/internal/slug"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"
)

// RenderStrictSection renders the fixed shell for a normalized section page.
// It consumes only the immutable model and keeps navigation server-rendered.
func RenderStrictSection(plan *model.SitePlan, section *model.Section, index *model.VaultIndex, assets markdown.AssetSink) ([]byte, error) {
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
	var body strings.Builder
	headingID, _ := strictLeadingTitlePlacement(content, sectionNote, leadingTitleID)
	if headingID != "" {
		_, _ = fmt.Fprintf(&body, `<section class="section-landing"><header><h1 id="%s">%s</h1>`, esc(headingID), esc(section.Title))
	} else {
		_, _ = fmt.Fprintf(&body, `<section class="section-landing"><header><h1>%s</h1>`, esc(section.Title))
	}
	if section.Description != "" {
		_, _ = fmt.Fprintf(&body, `<p class="section-description">%s</p>`, esc(section.Description))
	}
	if section.Banner != "" {
		if section.BannerURL == "" {
			return nil, fmt.Errorf("section banner %q has no planned asset destination", section.Banner)
		}
		_, _ = fmt.Fprintf(&body, `<img class="page-banner" src="%s" alt="%s" />`, esc(strictSitePath(plan, "/"+strings.TrimPrefix(section.BannerURL, "/"))), esc(section.BannerAlt))
	}
	body.WriteString(`</header><div class="entry-content section-content" data-page-content>`)
	body.WriteString(content)
	body.WriteString(`</div>`)
	visibleChildren := publishedSectionChildren(section)
	if len(visibleChildren) > 0 {
		body.WriteString(`<h2>Sections</h2><ul class="section-children">`)
		for _, child := range visibleChildren {
			_, _ = fmt.Fprintf(&body, `<li><a href="%s">%s</a></li>`, esc(strictSitePath(plan, child.Route)), esc(child.Title))
		}
		body.WriteString(`</ul>`)
	}
	if versions := strictChildVersions(plan, section); len(versions) > 0 {
		body.WriteString(`<h2>Versions</h2><ul class="section-versions">`)
		for _, version := range versions {
			if version != nil && version.Root != nil {
				_, _ = fmt.Fprintf(&body, `<li><a href="%s">%s</a></li>`, esc(strictSitePath(plan, version.Root.Route)), esc(version.Label))
			}
		}
		body.WriteString(`</ul>`)
	}
	if len(section.Articles) > 0 {
		body.WriteString(`<h2>Articles</h2><ul class="section-articles">`)
		for _, article := range section.Articles {
			strictWriteListingItem(&body, plan, article)
		}
		body.WriteString(`</ul>`)
	}
	body.WriteString(`</section>`)
	return strictDocument(plan, section.Route, section.Title, section.Description, section.Breadcrumbs, section.VersionID, section.VersionRoutes, "", nil, section.SourcePath, StrictSidebarNodes(plan, section.VersionID), body.String())
}

// RenderStrictArticle renders a normalized article page and its single shared
// document reading-flow metadata.
func RenderStrictArticle(plan *model.SitePlan, article *model.Note, previous, next *model.Note, position, total int, backlinks, related []*model.Note, index *model.VaultIndex, assets markdown.AssetSink) ([]byte, error) {
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
	var body strings.Builder
	if headingID != "" {
		_, _ = fmt.Fprintf(&body, `<article class="article-page article-sheet"><header><h1 id="%s">%s</h1>`, esc(headingID), esc(article.Frontmatter.Title))
	} else {
		_, _ = fmt.Fprintf(&body, `<article class="article-page article-sheet"><header><h1>%s</h1>`, esc(article.Frontmatter.Title))
	}
	if article.Frontmatter.Description != "" {
		_, _ = fmt.Fprintf(&body, `<p class="article-description">%s</p>`, esc(article.Frontmatter.Description))
	}
	if article.Frontmatter.Banner != "" {
		if article.BannerURL == "" {
			return nil, fmt.Errorf("article banner %q has no planned asset destination", article.Frontmatter.Banner)
		}
		_, _ = fmt.Fprintf(&body, `<img class="page-banner" src="%s" alt="%s" />`, esc(strictSitePath(plan, "/"+strings.TrimPrefix(article.BannerURL, "/"))), esc(article.Frontmatter.BannerAlt))
	}
	_, _ = fmt.Fprintf(&body, `</header>`)
	writeStrictArticleMetadata(&body, article)
	writeStrictTOC(&body, article, tocHeadingID)
	_, _ = fmt.Fprintf(&body, `<div class="entry-content article-content" data-page-content>%s</div>`, content)
	if len(article.Tags) > 0 {
		body.WriteString(`<ul class="article-tags" aria-label="Tags">`)
		for _, tag := range article.Tags {
			_, _ = fmt.Fprintf(&body, `<li><a href="%s">%s</a></li>`, esc(strictSitePath(plan, "/tags/"+strictEncodePath(tag)+"/")), esc(tag))
		}
		body.WriteString(`</ul>`)
	}
	if article.Frontmatter.Status == "deprecated" {
		body.WriteString(`<aside class="deprecated-notice" role="note">This page is deprecated.</aside>`)
	}
	if len(related) > 0 {
		body.WriteString(`<section class="related-articles"><h2>Related articles</h2><ul>`)
		for _, note := range related {
			strictWriteListingItem(&body, plan, note)
		}
		body.WriteString(`</ul></section>`)
	}
	if len(backlinks) > 0 {
		body.WriteString(`<section class="backlinks"><h2>Backlinks</h2><ul>`)
		for _, note := range backlinks {
			if note != nil {
				_, _ = fmt.Fprintf(&body, `<li><a href="%s">%s</a></li>`, esc(strictSitePath(plan, note.Route)), esc(note.Frontmatter.Title))
			}
		}
		body.WriteString(`</ul></section>`)
	}
	if article.Frontmatter.Type == "doc" {
		body.WriteString(`<nav class="reading-flow" aria-label="Document navigation">`)
		if previous != nil {
			_, _ = fmt.Fprintf(&body, `<a class="previous" rel="prev" href="%s">← Previous</a>`, esc(strictSitePath(plan, previous.Route)))
		} else {
			body.WriteString(`<span class="reading-flow-spacer" aria-hidden="true"></span>`)
		}
		_, _ = fmt.Fprintf(&body, `<span class="position">%d of %d</span>`, position, total)
		if next != nil {
			_, _ = fmt.Fprintf(&body, `<a class="next" rel="next" href="%s">Next →</a>`, esc(strictSitePath(plan, next.Route)))
		} else {
			body.WriteString(`<span class="reading-flow-spacer" aria-hidden="true"></span>`)
		}
		body.WriteString(`</nav>`)
	}
	body.WriteString(`</article>`)
	return strictDocument(plan, article.Route, article.Frontmatter.Title, article.Frontmatter.Description, breadcrumbs, article.VersionID, article.VersionRoutes, article.SocialImage, article, article.RelPath, StrictSidebarNodes(plan, article.VersionID), body.String())
}

// RenderStrictNotFound renders the fixed static 404 page.
func RenderStrictNotFound(plan *model.SitePlan) ([]byte, error) {
	if plan == nil {
		return nil, fmt.Errorf("404 page requires a plan")
	}
	body := `<section class="not-found-page"><h1>Not found</h1><p>The requested page could not be found.</p><a href="` + esc(strictSitePath(plan, "/")) + `">Home</a></section>`
	return strictDocument(plan, "/404.html", "Not found", "", nil, "", nil, "", nil, "", StrictSidebarNodes(plan, ""), body)
}

// RenderStrictTag renders a deterministic tag archive from the normalized index.
func RenderStrictTag(plan *model.SitePlan, tag *model.Tag, notes []*model.Note) ([]byte, error) {
	if plan == nil || tag == nil {
		return nil, fmt.Errorf("tag page requires a plan and tag")
	}
	var body strings.Builder
	_, _ = fmt.Fprintf(&body, `<section class="tag-page"><h1>Tag: %s</h1><ul class="tag-articles">`, esc(tag.Name))
	for _, note := range notes {
		strictWriteListingItem(&body, plan, note)
	}
	body.WriteString(`</ul></section>`)
	route := "/" + slug.EncodePath(tag.Slug) + "/"
	return strictDocument(plan, route, "Tag: "+tag.Name, "", nil, "", nil, "", nil, "", StrictSidebarNodes(plan, ""), body.String())
}

// RenderStrictTimeline renders the optional recent-article archive.
func RenderStrictTimeline(plan *model.SitePlan, route string, notes []*model.Note) ([]byte, error) {
	if plan == nil || strings.Trim(route, "/") == "" {
		return nil, fmt.Errorf("timeline page requires a plan and route")
	}
	var body strings.Builder
	_, _ = fmt.Fprintf(&body, `<section class="timeline-page"><h1>Recent articles</h1><ul>`)
	for _, note := range notes {
		strictWriteListingItem(&body, plan, note)
	}
	body.WriteString(`</ul>`)
	if page, total := timelinePageInfo(plan, route); total > 1 {
		body.WriteString(`<nav class="pagination-nav" aria-label="Timeline pages">`)
		if page > 1 {
			_, _ = fmt.Fprintf(&body, `<a class="pagination-link pagination-link-prev" href="%s">Previous</a>`, esc(strictSitePath(plan, timelinePageRoute(plan, page-1))))
		}
		if page < total {
			_, _ = fmt.Fprintf(&body, `<a class="pagination-link pagination-link-next" href="%s">Next</a>`, esc(strictSitePath(plan, timelinePageRoute(plan, page+1))))
		}
		body.WriteString(`</nav>`)
	}
	body.WriteString(`</section>`)
	return strictDocument(plan, route, "Recent articles", "", nil, "", nil, "", nil, "", StrictSidebarNodes(plan, ""), body.String())
}

func timelinePageInfo(plan *model.SitePlan, route string) (page, total int) {
	if plan == nil || plan.Timeline == nil || len(plan.Timeline.Pages) == 0 {
		return 1, 1
	}
	total = len(plan.Timeline.Pages)
	planned, ok := plan.Timeline.PageForRoute(route)
	if !ok {
		return 1, total
	}
	return planned.Number, total
}

func timelinePageRoute(plan *model.SitePlan, page int) string {
	if plan == nil || plan.Timeline == nil {
		return ""
	}
	planned, ok := plan.Timeline.Page(page)
	if !ok {
		return ""
	}
	return planned.Route
}

func annotateStrictPopovers(content string, article *model.Note, index *model.VaultIndex, plan *model.SitePlan) (string, error) {
	if strings.TrimSpace(content) == "" || article == nil || index == nil {
		return content, nil
	}
	basePath := "/"
	if plan != nil {
		basePath = strictBasePath(plan)
	}
	base, _ := url.Parse("https://obsite.invalid" + strings.TrimSuffix(basePath, "/") + article.Route)
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

func strictDocument(plan *model.SitePlan, currentRoute, title, description string, breadcrumbs []model.Breadcrumb, versionID string, versionRoutes map[string]string, socialImage string, metadata *model.Note, sourcePath string, sidebarNodes []model.SidebarNode, body string) ([]byte, error) {
	canonical := strictAbsoluteURL(plan, currentRoute)
	runtimePath, err := SharedRuntimeOutputPath()
	if err != nil {
		return nil, fmt.Errorf("resolve shared runtime: %w", err)
	}
	slots, err := RenderStrictThemeSlots(plan, currentRoute, title, metadata, sourcePath)
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	rootAttrs := `data-obsite-base-path="` + esc(strictBasePath(plan)) + `" data-obsite-kind="strict"`
	if plan.Config.Sidebar.Enabled && len(sidebarNodes) > 0 {
		rootAttrs += ` data-obsite-sidebar`
	}
	if versionID != "" {
		rootAttrs += ` data-obsite-version="` + esc(versionID) + `"`
	}
	if plan.Config.Popover.Enabled {
		rootAttrs += ` data-obsite-popover`
	}
	if strings.Contains(body, "data-obsite-math-source") {
		rootAttrs += ` data-obsite-math`
	}
	if strings.Contains(body, `class="mermaid"`) {
		rootAttrs += ` data-obsite-mermaid`
	}
	_, _ = fmt.Fprintf(&output, `<!doctype html><html lang="%s" %s><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s | %s</title>`, esc(plan.Config.Language), rootAttrs, esc(title), esc(plan.Config.Title))
	if description != "" {
		_, _ = fmt.Fprintf(&output, `<meta name="description" content="%s">`, esc(description))
	}
	_, _ = fmt.Fprintf(&output, `<meta property="og:url" content="%s"><meta property="og:title" content="%s"><meta name="twitter:title" content="%s">`, esc(canonical), esc(title), esc(title))
	if description != "" {
		_, _ = fmt.Fprintf(&output, `<meta property="og:description" content="%s"><meta name="twitter:description" content="%s">`, esc(description), esc(description))
	}
	socialImageURL := ""
	if socialImage != "" {
		socialImageURL = strictAbsoluteURL(plan, "/"+strings.TrimPrefix(socialImage, "/"))
		_, _ = fmt.Fprintf(&output, `<meta property="og:image" content="%s"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:image" content="%s">`, esc(socialImageURL), esc(socialImageURL))
	} else if plan.Config.DefaultImgURL != "" {
		imageURL := plan.Config.DefaultImgURL
		if !plan.Config.DefaultImgExternal {
			imageURL = strictAbsoluteURL(plan, "/"+strings.TrimPrefix(imageURL, "/"))
		}
		_, _ = fmt.Fprintf(&output, `<meta property="og:image" content="%s">`, esc(imageURL))
	}
	if metadata != nil {
		output.WriteString(`<meta property="og:type" content="article">`)
		if metadata.Frontmatter.Author != "" {
			_, _ = fmt.Fprintf(&output, `<meta name="author" content="%s"><meta property="article:author" content="%s">`, esc(metadata.Frontmatter.Author), esc(metadata.Frontmatter.Author))
		}
		if metadata.Frontmatter.Audience != "" {
			_, _ = fmt.Fprintf(&output, `<meta name="audience" content="%s">`, esc(metadata.Frontmatter.Audience))
		}
		if metadata.Frontmatter.ProductVersion != "" {
			_, _ = fmt.Fprintf(&output, `<meta name="product-version" content="%s">`, esc(metadata.Frontmatter.ProductVersion))
		}
		if metadata.Frontmatter.Series != "" {
			_, _ = fmt.Fprintf(&output, `<meta name="series" content="%s">`, esc(metadata.Frontmatter.Series))
		}
		if metadata.Frontmatter.Status != "" {
			_, _ = fmt.Fprintf(&output, `<meta name="status" content="%s">`, esc(metadata.Frontmatter.Status))
		}
		if !metadata.Frontmatter.Date.IsZero() {
			_, _ = fmt.Fprintf(&output, `<meta property="article:published_time" content="%s">`, esc(metadata.Frontmatter.Date.UTC().Format(time.RFC3339)))
		}
		if !metadata.Frontmatter.Updated.IsZero() {
			_, _ = fmt.Fprintf(&output, `<meta property="article:modified_time" content="%s">`, esc(metadata.Frontmatter.Updated.UTC().Format(time.RFC3339)))
		}
		jsonData := map[string]any{"@context": "https://schema.org", "@type": "Article", "headline": metadata.Frontmatter.Title, "url": canonical}
		if socialImageURL != "" {
			jsonData["image"] = socialImageURL
		}
		if description != "" {
			jsonData["description"] = description
		}
		if metadata.Frontmatter.Author != "" {
			jsonData["author"] = map[string]string{"@type": "Person", "name": metadata.Frontmatter.Author}
		}
		if !metadata.Frontmatter.Date.IsZero() {
			jsonData["datePublished"] = metadata.Frontmatter.Date.UTC().Format(time.RFC3339)
		}
		if !metadata.Frontmatter.Updated.IsZero() {
			jsonData["dateModified"] = metadata.Frontmatter.Updated.UTC().Format(time.RFC3339)
		}
		if !metadata.Frontmatter.Reviewed.IsZero() {
			jsonData["lastReviewed"] = metadata.Frontmatter.Reviewed.UTC().Format(time.RFC3339)
		}
		if metadata.Frontmatter.Status != "" {
			jsonData["creativeWorkStatus"] = metadata.Frontmatter.Status
		}
		if metadata.Frontmatter.Audience != "" {
			jsonData["audience"] = map[string]string{"@type": "Audience", "audienceType": metadata.Frontmatter.Audience}
		}
		if metadata.Frontmatter.ProductVersion != "" {
			jsonData["version"] = metadata.Frontmatter.ProductVersion
		}
		if metadata.Frontmatter.Series != "" {
			jsonData["isPartOf"] = metadata.Frontmatter.Series
		}
		if len(metadata.Tags) > 0 {
			jsonData["keywords"] = metadata.Tags
		}
		jsonLD, _ := json.Marshal(jsonData)
		_, _ = fmt.Fprintf(&output, `<script type="application/ld+json">%s</script>`, jsonLD)
	}
	_, _ = fmt.Fprintf(&output, `<script src="%s"></script><link rel="stylesheet" href="%s"><link rel="canonical" href="%s">`, esc(strictSitePath(plan, "/"+runtimePath)), esc(strictSitePath(plan, "/style.css")), esc(canonical))
	if plan.Config.CustomCSS != "" {
		_, _ = fmt.Fprintf(&output, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/assets/custom.css")))
	}
	if plan.Config.ThemeCSS != "" {
		_, _ = fmt.Fprintf(&output, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/"+plan.Config.ThemeCSS)))
	}
	if strings.Contains(body, "data-obsite-math-source") {
		_, _ = fmt.Fprintf(&output, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/"+katexCSSOutputPath)))
	}
	output.WriteString(slots["obsite-head-end"])
	output.WriteString(`</head><body class="site-body" data-site-body><div class="site-frame"><header class="site-masthead site-header"><div class="masthead-band"><a class="site-mark site-title" href="` + esc(strictSitePath(plan, "/")) + `">` + esc(plan.Config.Title) + `</a></div><div class="masthead-copy"><div class="masthead-actions"><button type="button" class="theme-toggle" data-theme-toggle hidden aria-pressed="false"><span class="theme-toggle-caption">Theme</span><span class="theme-toggle-value" data-theme-toggle-value></span><span data-theme-toggle-state class="sr-only"></span><span data-theme-toggle-source class="sr-only"></span></button></div><nav aria-label="Global navigation">`)
	for _, item := range plan.Config.Navigation {
		href, active := item.URL, false
		ariaValue := ""
		if item.Section != "" {
			for _, section := range plan.Sections {
				if section != nil && section.RelPath == item.Section {
					href = strictSitePath(plan, section.Route)
					if currentRoute == section.Route {
						ariaValue = "page"
					} else if strings.HasPrefix(currentRoute, strings.TrimSuffix(section.Route, "/")+"/") {
						ariaValue = "location"
					}
					active = ariaValue != ""
				}
			}
		} else {
			if strings.HasPrefix(href, "/") {
				href = strictSitePath(plan, href)
			}
			active = strictNavigationIsCurrent(item.URL, currentRoute, canonical)
			if active {
				ariaValue = "page"
			}
		}
		aria := ""
		if active {
			aria = ` aria-current="` + ariaValue + `"`
		}
		_, _ = fmt.Fprintf(&output, `<a href="%s"%s>%s</a>`, esc(href), aria, esc(item.Name))
	}
	output.WriteString(`</nav></div>`)
	output.WriteString(slots["obsite-header-end"])
	output.WriteString(`</header><main class="site-main" data-obsite-main>`)
	if plan.Config.Sidebar.Enabled && len(sidebarNodes) > 0 {
		output.WriteString(`<button type="button" class="sidebar-toggle-mobile sidebar-launch" data-sidebar-toggle hidden aria-expanded="false"><span class="sidebar-launch-icon" aria-hidden="true"></span>Open navigation</button><aside class="sidebar-shell" data-sidebar-shell><div class="sidebar-panel-head"><strong>Navigation</strong><button type="button" class="sidebar-close" data-sidebar-close>Close navigation</button></div><nav class="sidebar" aria-label="Sidebar" data-sidebar-root>`)
		// Render the complete current-version tree so the Sidebar remains usable
		// when the runtime is unavailable or JavaScript is disabled.
		output.WriteString(`<ul class="sidebar-list sidebar-list-root">`)
		strictWriteSidebarFallbackHTML(&output, plan, sidebarNodes, currentRoute)
		output.WriteString(`</ul></nav></aside><button type="button" class="sidebar-overlay" data-sidebar-overlay hidden aria-label="Close navigation"></button>`)
	}
	output.WriteString(`<div class="site-content"><nav class="breadcrumbs" aria-label="Breadcrumb"><ol>`)
	for _, crumb := range breadcrumbs {
		_, _ = fmt.Fprintf(&output, `<li><a href="%s">%s</a></li>`, esc(strictSitePath(plan, crumb.URL)), esc(crumb.Name))
	}
	_, _ = fmt.Fprintf(&output, `</ol></nav>%s`, body)
	if versionID != "" {
		output.WriteString(`<nav class="version-selector" aria-label="Versions">`)
		for _, version := range strictPublicVersions(plan) {
			if version == nil {
				continue
			}
			href := version.Root.Route
			if versionRoutes != nil && versionRoutes[version.ID] != "" {
				href = versionRoutes[version.ID]
			}
			href = strictSitePath(plan, href)
			aria := ""
			if version.ID == versionID {
				aria = ` aria-current="page"`
			}
			_, _ = fmt.Fprintf(&output, `<a href="%s"%s>%s</a>`, esc(href), aria, esc(version.Label))
		}
		output.WriteString(`</nav>`)
	}
	if sourcePath != "" && (plan.Config.Source.EditURL != "" || plan.Config.Source.ViewURL != "") {
		output.WriteString(`<nav class="source-links" aria-label="Source">`)
		if plan.Config.Source.EditURL != "" {
			_, _ = fmt.Fprintf(&output, `<a href="%s">Edit this page</a>`, esc(strictSourceURL(plan.Config.Source.EditURL, sourcePath)))
		}
		if plan.Config.Source.ViewURL != "" {
			_, _ = fmt.Fprintf(&output, `<a href="%s">View source</a>`, esc(strictSourceURL(plan.Config.Source.ViewURL, sourcePath)))
		}
		output.WriteString(`</nav>`)
	}
	if plan.Config.Popover.Enabled {
		output.WriteString(`<div id="obsite-popover-card" class="popover-card" data-popover-card hidden aria-hidden="true"></div>`)
	}
	output.WriteString(slots["obsite-main-end"])
	output.WriteString(`</div></main><footer class="site-footer"><small>Generated by Obsite</small>`)
	output.WriteString(slots["obsite-footer-end"])
	output.WriteString(`</footer></div></body></html>`)
	return []byte(output.String()), nil
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

func strictWriteSidebarFallbackHTML(output *strings.Builder, plan *model.SitePlan, nodes []model.SidebarNode, currentRoute string) {
	if output == nil {
		return
	}
	for _, node := range nodes {
		className := "sidebar-link"
		if node.IsDir {
			className += " sidebar-link-dir"
		}
		_, _ = fmt.Fprintf(output, `<li><a class="%s" href="%s"%s>%s</a>`, className, esc(strictSitePath(plan, node.URL)), strictCurrentARIA(node.URL, currentRoute), esc(node.Name))
		if len(node.Children) > 0 {
			output.WriteString(`<ul class="sidebar-list">`)
			strictWriteSidebarFallbackHTML(output, plan, node.Children, currentRoute)
			output.WriteString(`</ul>`)
		}
		output.WriteString(`</li>`)
	}
}

func strictCurrentARIA(route, current string) string {
	if route == current {
		return ` aria-current="page"`
	}
	return ""
}

func strictSlotKind(route string, metadata *model.Note, sourcePath string) string {
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

func strictSlotRootRel(route string) string {
	cleaned := strings.Trim(route, "/")
	if cleaned == "" {
		return "./"
	}
	segments := strings.Split(cleaned, "/")
	if len(segments) > 0 && segments[len(segments)-1] == "404.html" {
		return "./"
	}
	return strings.Repeat("../", len(segments))
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

func publishedSectionChildren(section *model.Section) []*model.Section {
	if section == nil {
		return nil
	}
	result := make([]*model.Section, 0, len(section.Children))
	for _, child := range section.Children {
		if child != nil && child.EffectivePublish && child.Route != "" {
			result = append(result, child)
		}
	}
	return result
}

func strictWriteListingItem(body *strings.Builder, plan *model.SitePlan, note *model.Note) {
	if body == nil || plan == nil || note == nil {
		return
	}
	_, _ = fmt.Fprintf(body, `<li><a class="listing-card" href="%s"><span class="listing-title">%s</span>`, esc(strictSitePath(plan, note.Route)), esc(note.Frontmatter.Title))
	summary := strings.TrimSpace(note.Frontmatter.Description)
	if summary == "" {
		summary = strings.TrimSpace(note.Summary)
	}
	if summary != "" {
		_, _ = fmt.Fprintf(body, `<span class="listing-summary">%s</span>`, esc(summary))
	}
	if published := note.Frontmatter.Date; !published.IsZero() {
		_, _ = fmt.Fprintf(body, `<time class="listing-date" datetime="%s">%s</time>`, esc(published.UTC().Format(time.RFC3339)), esc(strictMetadataTime(published)))
	}
	body.WriteString(`</a></li>`)
}

func strictChildVersions(plan *model.SitePlan, section *model.Section) []*model.Version {
	if plan == nil || section == nil {
		return nil
	}
	result := make([]*model.Version, 0)
	for _, version := range strictPublicVersions(plan) {
		if path.Dir(version.Root.RelPath) == section.RelPath || strings.HasPrefix(version.Root.RelPath, section.RelPath+"/") {
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
		return currentRoute == strictNavigationMatch(raw)
	}

	current, err := url.Parse(canonical)
	if err != nil || !current.IsAbs() || !strings.EqualFold(parsed.Scheme, current.Scheme) || !strings.EqualFold(parsed.Host, current.Host) {
		return false
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return strictNavigationMatch(parsed.EscapedPath()) == strictNavigationMatch(current.EscapedPath())
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
	return match
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

func strictEncodePath(value string) string {
	return slug.EncodePath(value)
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
