package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/render/viewmodel"
	internaltheme "github.com/simp-lee/oxpio/internal/theme"
)

type themedPageExtras struct {
	index                          *model.VaultIndex
	tag                            *model.Tag
	entries, backlinks, related    []*model.Note
	relatedRecommendations         []model.RelatedRecommendation
	previous, next                 *model.Note
	position, total                int
	titleID, metadataHTML, tocHTML string
}

func renderThemedDocument(plan *model.SitePlan, currentRoute, title, description string, breadcrumbs []model.Breadcrumb, versionID string, versionRoutes map[string]string, socialImage string, metadata *model.Note, sourcePath string, sidebarNodes []model.SidebarNode, body string, extras themedPageExtras) ([]byte, error) {
	if plan == nil {
		return nil, fmt.Errorf("theme page requires a site plan")
	}
	if len(plan.ThemeFiles) == 0 {
		return nil, fmt.Errorf("selected theme %q has no validated file snapshot", plan.Config.Theme)
	}
	selected := internaltheme.Source{ID: plan.Config.Theme, Name: plan.Config.Theme, APIVersion: internaltheme.APIVersion, Files: plan.ThemeFiles, Dirs: map[string]bool{"layouts": true, "partials": true, "assets": true}}
	renderer, err := internaltheme.Compile(selected, plan.ThemeAssetURLs, plan.Config.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("compile selected theme: %w", err)
	}
	canonical := strictAbsoluteURL(plan, currentRoute)
	kind := strictPageKind(currentRoute, metadata, sourcePath)
	isNotFound := kind == "404"
	head, err := themedHead(plan, canonical, title, description, socialImage, metadata, body, isNotFound)
	if err != nil {
		return nil, err
	}
	pageSEO := themedSEO(plan, title, description, canonical, socialImage, isNotFound)
	page := viewmodel.Page{
		Schema: viewmodel.SchemaVersion, Kind: kind, Site: viewmodel.Site{Title: plan.Config.Title, BaseURL: plan.Config.BaseURL, Author: plan.Config.Author, Description: plan.Config.Description, Language: plan.Config.Language},
		Title: title, Description: description, URL: currentRoute, Href: strictSitePath(plan, currentRoute), Canonical: canonical,
		Content: template.HTML(body), HeadHTML: template.HTML(head),
		Navigation: themedNavigation(plan, currentRoute, canonical), Breadcrumbs: themedBreadcrumbs(plan, breadcrumbs), Sidebar: themedSidebar(plan, sidebarNodes, currentRoute),
		Versions: themedVersions(plan, versionID, versionRoutes), VersionID: versionID, HasMath: strings.Contains(body, "data-oxpio-math-source"), HasMermaid: strings.Contains(body, `class="mermaid"`), Popover: plan.Config.Popover.Enabled, BasePath: strictBasePath(plan),
		SEO: pageSEO,
	}
	page.Entries = themedCards(plan, extras.entries)
	page.Backlinks = themedCards(plan, extras.backlinks)
	page.Related = themedCards(plan, extras.related)
	if len(extras.relatedRecommendations) > 0 {
		page.Related = themedRecommendationCards(plan, extras.index, extras.relatedRecommendations)
	}
	if extras.tag != nil {
		page.Tags = append([]string(nil), extras.tag.Name)
	}
	if len(page.Entries) > 0 || extras.entries != nil {
		page.Pagination = themedPagination(plan, page.Entries, currentRoute, 1, 1)
	}
	if extras.position > 0 && page.Article != nil {
		page.Article.Position, page.Article.Total = extras.position, extras.total
	}
	if extras.previous != nil && page.Article != nil {
		previous := themedCard(plan, extras.previous)
		page.Article.Previous = &previous
	}
	if extras.next != nil && page.Article != nil {
		next := themedCard(plan, extras.next)
		page.Article.Next = &next
	}
	if extras.tag != nil {
		page.Kind = "tag"
	}
	if plan.Timeline != nil && (currentRoute == plan.Config.Timeline.Path || strings.HasPrefix(currentRoute, "/"+strings.Trim(plan.Config.Timeline.Path, "/")+"/")) {
		page.Kind = "timeline"
	}
	if page.Kind == "timeline" {
		page.Pagination = themedTimelinePagination(plan, page.Entries, currentRoute)
	}
	if sourcePath != "" && (plan.Config.Source.EditURL != "" || plan.Config.Source.ViewURL != "") {
		page.Source = &viewmodel.Source{Path: sourcePath}
		if plan.Config.Source.EditURL != "" {
			page.Source.EditURL = strictSourceURL(plan.Config.Source.EditURL, sourcePath)
		}
		if plan.Config.Source.ViewURL != "" {
			page.Source.ViewURL = strictSourceURL(plan.Config.Source.ViewURL, sourcePath)
		}
	}
	if metadata != nil {
		page.Article = themedArticle(plan, metadata, socialImage)
		page.Article.TitleID = extras.titleID
		page.Article.MetadataHTML = template.HTML(extras.metadataHTML)
		page.Article.TOCHTML = template.HTML(extras.tocHTML)
		page.Article.Deprecated = metadata.Frontmatter.Status == "deprecated"
		page.Tags = append([]string(nil), metadata.Tags...)
	}
	if metadata != nil {
		if section := findStrictSection(plan, metadata.SectionPath, metadata.VersionID); section != nil {
			page.Section = themedSection(plan, section)
			page.Article.Position, page.Article.Total = themedArticlePosition(section, metadata)
			docs := section.Documents
			for index, note := range docs {
				if note == nil || note.RelPath != metadata.RelPath {
					continue
				}
				if index > 0 {
					previous := themedCard(plan, docs[index-1])
					page.Article.Previous = &previous
				}
				if index+1 < len(docs) {
					next := themedCard(plan, docs[index+1])
					page.Article.Next = &next
				}
				break
			}
		}
	} else if sourcePath != "" {
		if section := findSectionBySource(plan, sourcePath); section != nil {
			page.Section = themedSection(plan, section)
			page.Section.TitleID = extras.titleID
			if extras.entries == nil {
				page.Entries = append(page.Entries, page.Section.Articles...)
			}
		}
	}
	if currentRoute == "/404.html" {
		page.Kind = "404"
		page.NotFound = true
		page.Canonical = ""
		page.SEO = viewmodel.SEO{}
		page.Source = nil
	}
	if strings.HasPrefix(currentRoute, "/tags/") {
		page.Kind = "tag"
	}
	if plan.Timeline != nil {
		if currentRoute == plan.Config.Timeline.Path || strings.HasPrefix(currentRoute, "/"+strings.Trim(plan.Config.Timeline.Path, "/")+"/") {
			page.Kind = "timeline"
		}
	}
	if pagination, ok := themedPaginationForRoute(plan, page.Entries, currentRoute); ok {
		page.Pagination = pagination
	}
	page.SEO.JSONLD = themedJSONLD(plan, page, metadata)
	page.HeadHTML = template.HTML(themedHeadWithJSONLD(string(page.HeadHTML), page.SEO.JSONLD, metadata != nil))
	return renderer.Render(page.Kind, page)
}

func themedSEO(plan *model.SitePlan, title, description, canonical, socialImage string, notFound bool) viewmodel.SEO {
	seo := viewmodel.SEO{Title: title, Description: description, Canonical: canonical}
	if notFound {
		return seo
	}
	seo.Image = socialImageURL(plan, socialImage)
	if seo.Image == "" {
		seo.Image = themedDefaultImageURL(plan)
	}
	if seo.Image != "" {
		seo.TwitterCard = "summary_large_image"
	} else {
		seo.TwitterCard = "summary"
	}
	return seo
}

func themedDefaultImageURL(plan *model.SitePlan) string {
	if plan == nil || plan.Config.DefaultImgURL == "" {
		return ""
	}
	if plan.Config.DefaultImgExternal {
		return plan.Config.DefaultImgURL
	}
	return strictAbsoluteURL(plan, "/"+strings.TrimPrefix(plan.Config.DefaultImgURL, "/"))
}

func themedResourceURL(plan *model.SitePlan, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if strings.Contains(value, "://") || strings.HasPrefix(value, "//") {
		return value
	}
	return strictSitePath(plan, "/"+strings.TrimPrefix(value, "/"))
}

func themedHead(plan *model.SitePlan, canonical, title, description, socialImage string, metadata *model.Note, body string, notFound bool) (string, error) {
	runtimePath, err := SharedRuntimeOutputPath()
	if err != nil {
		return "", fmt.Errorf("resolve shared runtime: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<title>%s | %s</title>`, esc(title), esc(plan.Config.Title))
	if !notFound {
		if description != "" {
			fmt.Fprintf(&b, `<meta name="description" content="%s">`, esc(description))
		}
		fmt.Fprintf(&b, `<meta property="og:url" content="%s"><meta property="og:title" content="%s"><meta name="twitter:title" content="%s">`, esc(canonical), esc(title), esc(title))
		if description != "" {
			fmt.Fprintf(&b, `<meta property="og:description" content="%s"><meta name="twitter:description" content="%s">`, esc(description), esc(description))
		}
		if image := socialImageURL(plan, socialImage); image != "" {
			fmt.Fprintf(&b, `<meta property="og:image" content="%s"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:image" content="%s">`, esc(image), esc(image))
		} else if image := themedDefaultImageURL(plan); image != "" {
			fmt.Fprintf(&b, `<meta property="og:image" content="%s"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:image" content="%s">`, esc(image), esc(image))
		} else {
			b.WriteString(`<meta name="twitter:card" content="summary">`)
		}
	}
	if metadata != nil && !notFound {
		b.WriteString(`<meta property="og:type" content="article">`)
		values := []struct{ name, value string }{{"author", metadata.Frontmatter.Author}, {"audience", metadata.Frontmatter.Audience}, {"product-version", metadata.Frontmatter.ProductVersion}, {"series", metadata.Frontmatter.Series}, {"status", metadata.Frontmatter.Status}}
		for _, item := range values {
			if item.value != "" {
				fmt.Fprintf(&b, `<meta name="%s" content="%s">`, item.name, esc(item.value))
			}
		}
		if !metadata.Frontmatter.Date.IsZero() {
			fmt.Fprintf(&b, `<meta property="article:published_time" content="%s">`, esc(metadata.Frontmatter.Date.UTC().Format(time.RFC3339)))
		}
		if !metadata.Frontmatter.Updated.IsZero() {
			fmt.Fprintf(&b, `<meta property="article:modified_time" content="%s">`, esc(metadata.Frontmatter.Updated.UTC().Format(time.RFC3339)))
		}
	}
	if notFound {
		fmt.Fprintf(&b, `<script src="%s"></script><link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/"+runtimePath)), esc(strictSitePath(plan, "/style.css")))
	} else {
		fmt.Fprintf(&b, `<script src="%s"></script><link rel="stylesheet" href="%s"><link rel="canonical" href="%s">`, esc(strictSitePath(plan, "/"+runtimePath)), esc(strictSitePath(plan, "/style.css")), esc(canonical))
	}
	if plan.Config.ThemeCSS != "" {
		fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/"+plan.Config.ThemeCSS)))
	}
	if plan.Config.CustomCSS != "" {
		fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/assets/custom.css")))
	}
	if strings.Contains(body, "data-oxpio-math-source") {
		fmt.Fprintf(&b, `<link rel="stylesheet" href="%s">`, esc(strictSitePath(plan, "/"+katexCSSOutputPath)))
	}
	if strings.Contains(body, `class="mermaid"`) {
		fmt.Fprintf(&b, `<meta name="oxpio:mermaid" content="local">`)
	}
	return b.String(), nil
}

func themedHeadWithJSONLD(head, jsonLD string, article bool) string {
	if !article || strings.TrimSpace(jsonLD) == "" {
		return head
	}
	return head + `<script type="application/ld+json">` + jsonLD + `</script>`
}

func themedJSONLD(plan *model.SitePlan, page viewmodel.Page, article *model.Note) string {
	if article == nil {
		return ""
	}
	value := map[string]any{"@context": "https://schema.org", "@type": "Article", "headline": article.Frontmatter.Title, "url": page.Canonical}
	if page.Description != "" {
		value["description"] = page.Description
	}
	if image := socialImageURL(plan, article.SocialImage); image != "" {
		value["image"] = image
	}
	if article.Frontmatter.Author != "" {
		value["author"] = map[string]string{"@type": "Person", "name": article.Frontmatter.Author}
	}
	if article.Frontmatter.Audience != "" {
		value["audience"] = map[string]string{"@type": "Audience", "audienceType": article.Frontmatter.Audience}
	}
	if article.Frontmatter.ProductVersion != "" {
		value["version"] = article.Frontmatter.ProductVersion
	}
	if article.Frontmatter.Series != "" {
		value["isPartOf"] = article.Frontmatter.Series
	}
	if !article.Frontmatter.Date.IsZero() {
		value["datePublished"] = article.Frontmatter.Date.UTC().Format(time.RFC3339)
	}
	if !article.Frontmatter.Updated.IsZero() {
		value["dateModified"] = article.Frontmatter.Updated.UTC().Format(time.RFC3339)
	}
	if !article.Frontmatter.Reviewed.IsZero() {
		value["lastReviewed"] = article.Frontmatter.Reviewed.UTC().Format(time.RFC3339)
	}
	if article.Frontmatter.Status != "" {
		value["creativeWorkStatus"] = article.Frontmatter.Status
	}
	if len(article.Tags) > 0 {
		value["keywords"] = article.Tags
	}
	data, _ := json.Marshal(value)
	return string(data)
}

func socialImageURL(plan *model.SitePlan, image string) string {
	if strings.TrimSpace(image) == "" {
		return ""
	}
	return strictAbsoluteURL(plan, "/"+strings.TrimPrefix(image, "/"))
}

func themedNavigation(plan *model.SitePlan, route, canonical string) []viewmodel.NavigationItem {
	result := make([]viewmodel.NavigationItem, 0, len(plan.Config.Navigation))
	for _, item := range plan.Config.Navigation {
		href := item.URL
		current := ""
		if item.Section != "" {
			for _, section := range plan.Sections {
				if section != nil && section.RelPath == item.Section {
					href = section.Route
					if route == section.Route {
						current = "page"
					} else if strings.HasPrefix(route, strings.TrimSuffix(section.Route, "/")+"/") {
						current = "location"
					}
					break
				}
			}
		} else if strictNavigationIsCurrent(item.URL, route, canonical) {
			current = "page"
		}
		hrefValue := href
		if strings.HasPrefix(hrefValue, "/") {
			hrefValue = strictSitePath(plan, hrefValue)
		}
		result = append(result, viewmodel.NavigationItem{Name: item.Name, URL: href, Href: hrefValue, Active: current != "", Current: current})
	}
	return result
}

func themedBreadcrumbs(plan *model.SitePlan, crumbs []model.Breadcrumb) []viewmodel.Breadcrumb {
	result := make([]viewmodel.Breadcrumb, 0, len(crumbs))
	for _, crumb := range crumbs {
		result = append(result, viewmodel.Breadcrumb{Name: crumb.Name, URL: crumb.URL, Href: strictSitePath(plan, crumb.URL)})
	}
	return result
}

func themedSidebar(plan *model.SitePlan, nodes []model.SidebarNode, currentRoute string) []viewmodel.SidebarNode {
	result := make([]viewmodel.SidebarNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, viewmodel.SidebarNode{Name: node.Name, URL: node.URL, Href: strictSitePath(plan, node.URL), Source: node.Source, IsDir: node.IsDir, Current: node.URL == currentRoute, Children: themedSidebar(plan, node.Children, currentRoute)})
	}
	return result
}

func themedCards(plan *model.SitePlan, notes []*model.Note) []viewmodel.Card {
	if notes == nil {
		return nil
	}
	result := make([]viewmodel.Card, 0, len(notes))
	for _, note := range notes {
		if note != nil {
			result = append(result, themedCard(plan, note))
		}
	}
	return result
}

func themedRecommendationCards(plan *model.SitePlan, index *model.VaultIndex, recommendations []model.RelatedRecommendation) []viewmodel.Card {
	if recommendations == nil {
		return nil
	}
	result := make([]viewmodel.Card, 0, len(recommendations))
	for _, recommendation := range recommendations {
		if index == nil {
			continue
		}
		if note := index.Notes[recommendation.RelPath]; note != nil {
			card := themedCard(plan, note)
			card.Score = recommendation.Score
			card.Reasons = append([]string(nil), recommendation.Reasons...)
			card.Signals = viewmodel.RecommendationSignals{Content: recommendation.Signals.Content, Link: recommendation.Signals.Link, Tag: recommendation.Signals.Tag}
			result = append(result, card)
		}
	}
	return result
}

func themedPagination(plan *model.SitePlan, entries []viewmodel.Card, route string, current, total int) viewmodel.Pagination {
	if total < 1 {
		total = 1
	}
	result := viewmodel.Pagination{Enabled: total > 1, CurrentPage: current, TotalPages: total, Entries: append([]viewmodel.Card(nil), entries...)}
	if current > 1 {
		previous := paginationPageRoute(plan, route, current-1)
		result.PreviousURL, result.PreviousHref = previous, strictSitePath(plan, previous)
	}
	if current < total {
		next := paginationPageRoute(plan, route, current+1)
		result.NextURL, result.NextHref = next, strictSitePath(plan, next)
	}
	_ = route
	return result
}

func themedTimelinePagination(plan *model.SitePlan, entries []viewmodel.Card, route string) viewmodel.Pagination {
	current, total := timelinePageInfo(plan, route)
	return themedPagination(plan, entries, route, current, total)
}

func strictPagePlanForRoute(plan *model.SitePlan, route string) *model.TimelinePagePlan {
	if plan == nil {
		return nil
	}
	if pages := plan.SectionPages[route]; pages != nil {
		return pages
	}
	if pages := plan.TagPages[route]; pages != nil {
		return pages
	}
	if plan.Timeline != nil {
		if _, ok := plan.Timeline.PageForRoute(route); ok {
			return plan.Timeline
		}
	}
	for _, pages := range plan.SectionPages {
		if pages != nil {
			if _, ok := pages.PageForRoute(route); ok {
				return pages
			}
		}
	}
	for _, pages := range plan.TagPages {
		if pages != nil {
			if _, ok := pages.PageForRoute(route); ok {
				return pages
			}
		}
	}
	return nil
}

func paginationPageRoute(plan *model.SitePlan, route string, page int) string {
	planned, ok := strictPagePlanForRoute(plan, route).Page(page)
	if !ok {
		return ""
	}
	return planned.Route
}

func themedPaginationForRoute(plan *model.SitePlan, entries []viewmodel.Card, route string) (viewmodel.Pagination, bool) {
	pages := strictPagePlanForRoute(plan, route)
	if pages == nil {
		return viewmodel.Pagination{}, false
	}
	current, ok := pages.PageForRoute(route)
	if !ok {
		return viewmodel.Pagination{}, false
	}
	return themedPagination(plan, entries, route, current.Number, len(pages.Pages)), true
}

func themedVersions(plan *model.SitePlan, current string, routes map[string]string) []viewmodel.Version {
	result := make([]viewmodel.Version, 0, len(plan.Versions))
	for _, version := range strictPublicVersions(plan) {
		if version == nil || version.Root == nil {
			continue
		}
		route := version.Root.Route
		if routes != nil && routes[version.ID] != "" {
			route = routes[version.ID]
		}
		result = append(result, viewmodel.Version{ID: version.ID, Label: version.Label, URL: route, Href: strictSitePath(plan, route), Current: version.ID == current})
	}
	return result
}

func findSectionBySource(plan *model.SitePlan, source string) *model.Section {
	if plan == nil {
		return nil
	}
	for _, section := range plan.Sections {
		if section != nil && section.SourcePath == source {
			return section
		}
	}
	return nil
}

func themedCard(plan *model.SitePlan, note *model.Note) viewmodel.Card {
	if note == nil {
		return viewmodel.Card{}
	}
	published := ""
	if !note.Frontmatter.Date.IsZero() {
		published = note.Frontmatter.Date.UTC().Format(time.RFC3339)
	}
	return viewmodel.Card{Path: note.RelPath, URL: note.Route, Href: strictSitePath(plan, note.Route), Title: note.Frontmatter.Title, Summary: note.Frontmatter.Description, PublishedAt: published, Type: note.Frontmatter.Type, VersionID: note.VersionID, Tags: append([]string(nil), note.Tags...)}
}

func themedSection(plan *model.SitePlan, section *model.Section) *viewmodel.Section {
	if section == nil {
		return nil
	}
	result := &viewmodel.Section{Path: section.RelPath, URL: section.Route, Href: strictSitePath(plan, section.Route), Title: section.Title, Description: section.Description, BannerURL: themedResourceURL(plan, section.BannerURL), BannerAlt: section.BannerAlt}
	for _, child := range section.Children {
		if child != nil && child.EffectivePublish {
			result.Children = append(result.Children, viewmodel.Card{Path: child.SourcePath, URL: child.Route, Href: strictSitePath(plan, child.Route), Title: child.Title, Type: "section", VersionID: child.VersionID})
		}
	}
	for _, note := range section.Articles {
		if note != nil {
			result.Articles = append(result.Articles, themedCard(plan, note))
		}
	}
	return result
}

func themedArticlePosition(section *model.Section, note *model.Note) (int, int) {
	if section == nil || note == nil || note.Frontmatter.Type != "doc" {
		return 0, 0
	}
	for index, candidate := range section.Documents {
		if candidate != nil && candidate.RelPath == note.RelPath {
			return index + 1, len(section.Documents)
		}
	}
	return 0, len(section.Documents)
}

func themedArticle(plan *model.SitePlan, note *model.Note, socialImage string) *viewmodel.Article {
	if note == nil {
		return nil
	}
	result := &viewmodel.Article{Path: note.RelPath, URL: note.Route, Href: strictSitePath(plan, note.Route), Title: note.Frontmatter.Title, Description: note.Frontmatter.Description, Type: note.Frontmatter.Type, Author: note.Frontmatter.Author, Status: note.Frontmatter.Status, Audience: note.Frontmatter.Audience, ProductVersion: note.Frontmatter.ProductVersion, Series: note.Frontmatter.Series, BannerURL: themedResourceURL(plan, note.BannerURL), BannerAlt: note.Frontmatter.BannerAlt, CoverURL: themedResourceURL(plan, note.CoverURL), SocialImage: themedResourceURL(plan, socialImage), Tags: append([]string(nil), note.Tags...), Position: 0, Total: 0}
	if !note.Frontmatter.Date.IsZero() {
		result.Date = note.Frontmatter.Date.UTC().Format(time.RFC3339)
	}
	if !note.Frontmatter.Updated.IsZero() {
		result.Updated = note.Frontmatter.Updated.UTC().Format(time.RFC3339)
	}
	if !note.Frontmatter.Reviewed.IsZero() {
		result.Reviewed = note.Frontmatter.Reviewed.UTC().Format(time.RFC3339)
	}
	return result
}
