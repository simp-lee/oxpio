// Package viewmodel defines the immutable public data contract passed to Go
// html/template themes. It intentionally contains no Vault or internal plan
// pointers.
package viewmodel

import "html/template"

const SchemaVersion = "oxpio.theme.vm/v1"

type Site struct {
	Title       string
	BaseURL     string
	Author      string
	Description string
	Language    string
}

type NavigationItem struct {
	Name    string
	URL     string
	Href    string
	Active  bool
	Current string
}

type (
	Breadcrumb struct{ Name, URL, Href string }
	Version    struct {
		ID, Label, URL, Href string
		Current              bool
	}
)

type (
	Source      struct{ Path, EditURL, ViewURL string }
	SidebarNode struct {
		Name, URL, Href, Source string
		IsDir                   bool
		Current                 bool
		Children                []SidebarNode
	}
)

type RecommendationSignals struct {
	Content float64
	Link    float64
	Tag     float64
}

type Card struct {
	Path, URL, Href, Title, Summary, PublishedAt, Type, VersionID string
	Score                                                         float64
	Reasons                                                       []string
	Signals                                                       RecommendationSignals
	Tags                                                          []string
}
type Pagination struct {
	Enabled                                      bool
	CurrentPage, TotalPages                      int
	PreviousURL, PreviousHref, NextURL, NextHref string
	Entries                                      []Card
}
type Article struct {
	Path, URL, Href, Title, Description, Type, Author, Status, Audience, ProductVersion, Series, BannerURL, BannerAlt, CoverURL, SocialImage string
	TitleID, Date, Updated, Reviewed                                                                                                         string
	MetadataHTML, TOCHTML                                                                                                                    template.HTML
	Deprecated                                                                                                                               bool
	Tags                                                                                                                                     []string
	Position, Total                                                                                                                          int
	Previous, Next                                                                                                                           *Card
}
type Section struct {
	Path, URL, Href, Title, Description, BannerURL, BannerAlt, TitleID string
	Children, Articles                                                 []Card
}
type (
	SEO  struct{ Title, Description, Canonical, Image, TwitterCard, JSONLD string }
	Page struct {
		Schema      string
		Kind        string
		Site        Site
		Title       string
		Description string
		URL         string
		Href        string
		Canonical   string
		BasePath    string
		Content     template.HTML
		HeadHTML    template.HTML
		Navigation  []NavigationItem
		Breadcrumbs []Breadcrumb
		Sidebar     []SidebarNode
		Versions    []Version
		Source      *Source
		Section     *Section
		Article     *Article
		Entries     []Card
		Tags        []string
		Backlinks   []Card
		Related     []Card
		Pagination  Pagination
		SEO         SEO
		VersionID   string
		HasMath     bool
		HasMermaid  bool
		NotFound    bool
		Popover     bool
	}
)
