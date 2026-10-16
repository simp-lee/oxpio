package model

import "time"

// Section is a normalized section node. SourcePath always names its
// _index.md; an _index.md is never represented as a Note.
type Section struct {
	RelPath          string
	SourcePath       string
	Route            string
	Title            string
	Description      string
	Publish          bool
	EffectivePublish bool
	Order            *int
	Banner           string
	BannerURL        string
	BannerAlt        string
	RawContent       []byte
	BodyStartLine    int
	LastModified     time.Time
	VersionID        string
	VersionRoutes    map[string]string
	HiddenBy         string
	Parent           *Section
	Children         []*Section
	Articles         []*Note
	Documents        []*Note
	Posts            []*Note
	Pages            []*Note
	Breadcrumbs      []Breadcrumb
	Headings         []Heading
	HeadingSections  map[string]SectionRange
	OutLinks         []LinkRef
	Embeds           []EmbedRef
	ImageRefs        []ImageRef
	HasMath          bool
	HasMermaid       bool
}

// Version is one independently planned version tree.
type Version struct {
	ID       string
	Label    string
	Source   string
	Root     *Section
	Sections []*Section
}

// RouteOutputIndex tracks physical output paths claimed by generated routes.
// It lets the planner detect exact and ancestor/descendant conflicts without
// rescanning every previously claimed route.
type RouteOutputIndex struct {
	Exact        map[string]string
	Prefixes     map[string]string
	FoldedExact  map[string]string
	FoldedPrefix map[string]string
}

// ContentPartition is the independent public collection for unversioned
// content or one configured version entry. VersionID is empty for the
// unversioned partition.
type ContentPartition struct {
	VersionID string
	Articles  []*Note
	Documents []*Note
	Posts     []*Note
	Pages     []*Note
	Tags      map[string]*Tag
	TagRoutes map[string]string
	TagPages  map[string]*TimelinePagePlan
	Timeline  *TimelinePagePlan
}

// SitePlan is the immutable normalized handoff from vault analysis to later
// validation, asset planning, and rendering. Slices are sorted by the planner;
// consumers must treat the pointed-to values as read-only.
type SitePlan struct {
	VaultPath string
	Config    SiteConfig
	Root      *Section
	Sections  []*Section
	Articles  []*Note
	Documents []*Note
	Posts     []*Note
	Pages     []*Note
	Versions  []*Version
	// Partitions is keyed by VersionID; the empty key is unversioned.
	Partitions   map[string]*ContentPartition
	Timeline     *TimelinePagePlan
	SectionPages map[string]*TimelinePagePlan
	TagPages     map[string]*TimelinePagePlan
	RouteIndex   *RouteOutputIndex
	Routes       map[string]string
	// PublicPageRoutes contains generated HTML page routes that do not have a
	// note or section target in the vault index.
	PublicPageRoutes map[string]struct{}
	ReservedRoutes   map[string]struct{}
	// ThemeFiles is the immutable selected theme snapshot consumed by the
	// single html/template renderer. Keys are theme-relative paths.
	ThemeFiles         map[string][]byte
	ThemeAssetManifest string
	ThemeAssets        map[string]*PlannedAsset
	ThemeAssetURLs     map[string]string
	CustomCSSData      []byte
	// VaultCSSAssets contains transformed vault CSS and its recursive assets,
	// including stylesheets referenced from raw HTML.
	VaultCSSAssets map[string]*PlannedAsset
	// CSSInputFiles retains partial dependency discovery for watch mode.
	CSSInputFiles map[string]struct{}
}

// PlannedSource keeps the strict parse result for either an article or a
// section source. It is useful to diagnostics before publication filtering.
type PlannedSource struct {
	RelPath string
	Article *Note
	Section *SectionSource
	Publish bool
}
