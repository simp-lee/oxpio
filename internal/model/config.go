package model

// SiteConfig is the stable site-level configuration contract shared across packages.
// URL-like fields intentionally remain plain strings so html/template keeps contextual escaping.
type SiteConfig struct {
	Title              string
	BaseURL            string
	Author             string
	Description        string
	Language           string
	DefaultImg         string
	DefaultImgExternal bool
	DefaultImgURL      string
	// Theme is the selected theme ID. The empty YAML value is normalized to
	// the embedded base theme; local themes are addressed only by this ID.
	Theme                  string
	ThemeDir               string
	ThemeCSS               string
	ThemeSlots             string
	CustomCSS              string
	AttachmentDirectory    string
	UnknownManagementPaths []string
	Navigation             []NavigationItem
	Source                 SourceConfig
	Versions               *VersionsConfig
	Pagination             PaginationConfig
	Sidebar                SidebarConfig
	Popover                PopoverConfig
	Related                RelatedConfig
	RSS                    RSSConfig
	Timeline               TimelineConfig
	Editor                 *EditorConfig

	RuntimeJSURL string
	// FieldLines preserves YAML source locations for semantic diagnostics.
	FieldLines map[string]int `json:"-"`
}

// NavigationItem is one server-rendered global navigation entry. Exactly one
// of URL and Section is populated after configuration normalization.
type NavigationItem struct {
	Name    string
	URL     string
	Section string
}

// SourceConfig contains optional absolute source-link templates.
type SourceConfig struct {
	EditURL string
	ViewURL string
}

// EditorConfig is the optional single-account editor credential configuration.
// PasswordHash contains only a PHC Argon2id password hash.
type EditorConfig struct {
	Username     string
	PasswordHash string
}

// SourceCatalog is the immutable source mapping handed from analysis to the
// editor. RelPath is always vault-relative and Route is empty for unpublished
// sources.
type SourceCatalog struct {
	BasePath string
	Entries  []SourceCatalogEntry
}

// SourceCatalogEntry identifies one exact article or section source.
type SourceCatalogEntry struct {
	RelPath          string
	Route            string
	Title            string
	Kind             string
	Type             string
	Publish          bool
	EffectivePublish bool
	SectionPath      string
	VersionID        string
}

// VersionsConfig describes an explicitly configured set of document versions.
type VersionsConfig struct {
	Root    string
	Default string
	Entries []VersionEntry
}

// VersionEntry identifies one version source directory.
type VersionEntry struct {
	ID     string
	Label  string
	Source string
}

// PaginationConfig controls list-page pagination behavior.
type PaginationConfig struct {
	PageSize int
}

// SidebarConfig controls the optional collapsible sidebar file tree.
type SidebarConfig struct {
	Enabled bool
}

// PopoverConfig controls internal-link preview generation.
type PopoverConfig struct {
	Enabled bool
}

// RelatedConfig controls related-article generation. Enabled is a boolean
// feature switch; Count is the requested number of recommendations and must be
// normalized to the inclusive range 1..20.
type RelatedConfig struct {
	Enabled bool
	Count   int
}

// RSSConfig controls RSS feed emission.
type RSSConfig struct {
	Enabled bool
}

// TimelineConfig controls the recent-notes timeline page.
type TimelineConfig struct {
	Enabled bool
	Path    string
}
