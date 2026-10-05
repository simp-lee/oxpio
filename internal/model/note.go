package model

import (
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/simp-lee/oxpio/internal/slug"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Note describes a Markdown note discovered in a vault.
type Note struct {
	RelPath      string
	Frontmatter  Frontmatter
	LastModified time.Time
	FieldLines   map[string]int `json:"-"`

	// The planning fields are assigned once by the strict site planner and are
	// intentionally kept on the canonical note handed to later render phases.
	Route           string
	SectionPath     string
	VersionID       string
	VersionRoutes   map[string]string
	BasePath        string
	SocialImage     string
	BannerURL       string
	CoverURL        string
	Slug            string
	Aliases         []string
	Tags            []string
	Headings        []Heading
	HeadingSections map[string]SectionRange

	RawContent    []byte
	BodyStartLine int
	HTMLContent   string
	Summary       string

	OutLinks  []LinkRef
	Embeds    []EmbedRef
	ImageRefs []ImageRef

	HasMath    bool
	HasMermaid bool
}

// PublishedAt returns the best available article timestamp for this note.
func (n *Note) PublishedAt() time.Time {
	if n == nil {
		return time.Time{}
	}
	if !n.Frontmatter.Date.IsZero() {
		return n.Frontmatter.Date
	}

	return n.LastModified
}

// LessRecentNote orders notes by published time descending, then slug ascending.
func LessRecentNote(left *Note, right *Note) bool {
	leftDate := time.Time{}
	rightDate := time.Time{}
	if left != nil {
		leftDate = left.PublishedAt()
	}
	if right != nil {
		rightDate = right.PublishedAt()
	}

	switch {
	case leftDate.IsZero() && !rightDate.IsZero():
		return false
	case !leftDate.IsZero() && rightDate.IsZero():
		return true
	case !leftDate.Equal(rightDate):
		return leftDate.After(rightDate)
	}

	leftSlug := noteSortKey(left)
	rightSlug := noteSortKey(right)
	if leftSlug != rightSlug {
		return leftSlug < rightSlug
	}

	leftPath := ""
	rightPath := ""
	if left != nil {
		leftPath = left.RelPath
	}
	if right != nil {
		rightPath = right.RelPath
	}
	return leftPath < rightPath
}

func noteSortKey(note *Note) string {
	if note == nil {
		return ""
	}
	if note.Slug != "" {
		return note.Slug
	}
	return note.RelPath
}

// LessCollectionNote applies the canonical article collection order shared by
// section, sidebar, reading-flow, and tag lists.
func LessCollectionNote(left *Note, right *Note) bool {
	if left == nil || right == nil {
		return left != nil
	}
	leftType, rightType := collectionTypeRank(left.Frontmatter.Type), collectionTypeRank(right.Frontmatter.Type)
	if leftType != rightType {
		return leftType < rightType
	}
	if left.Frontmatter.Type == "doc" && right.Frontmatter.Type == "doc" {
		leftOrder, rightOrder := left.Frontmatter.Order, right.Frontmatter.Order
		if (leftOrder != nil) != (rightOrder != nil) {
			return leftOrder != nil
		}
		if leftOrder != nil && *leftOrder != *rightOrder {
			return *leftOrder < *rightOrder
		}
		leftPrefix, leftHasPrefix, _ := slug.NumericPrefix(collectionFileStem(left.RelPath))
		rightPrefix, rightHasPrefix, _ := slug.NumericPrefix(collectionFileStem(right.RelPath))
		if leftHasPrefix != rightHasPrefix {
			return leftHasPrefix
		}
		if leftHasPrefix && collectionNumericPrefixValue(leftPrefix) != collectionNumericPrefixValue(rightPrefix) {
			return collectionNumericPrefixValue(leftPrefix) < collectionNumericPrefixValue(rightPrefix)
		}
	} else if left.Frontmatter.Type == "post" && right.Frontmatter.Type == "post" && !left.Frontmatter.Date.Equal(right.Frontmatter.Date) {
		return left.Frontmatter.Date.After(right.Frontmatter.Date)
	}
	leftTitle, rightTitle := collectionFold(left.Frontmatter.Title), collectionFold(right.Frontmatter.Title)
	if leftTitle != rightTitle {
		return leftTitle < rightTitle
	}
	leftPath, rightPath := collectionFold(left.RelPath), collectionFold(right.RelPath)
	return leftPath < rightPath
}

// CollectionNotesTie reports whether two notes have identical keys under the
// canonical collection order. A complete tie is invalid because the contract
// does not define an additional implementation-dependent tie-breaker.
func CollectionNotesTie(left *Note, right *Note) bool {
	if left == nil || right == nil {
		return false
	}
	return !LessCollectionNote(left, right) && !LessCollectionNote(right, left)
}

func collectionTypeRank(typeName string) int {
	switch typeName {
	case "doc":
		return 0
	case "post":
		return 1
	case "page":
		return 2
	default:
		return 3
	}
}

func collectionFileStem(relPath string) string {
	filename := path.Base(relPath)
	return strings.TrimSuffix(filename, path.Ext(filename))
}

func collectionNumericPrefixValue(prefix string) int64 {
	prefix = strings.TrimRight(prefix, "-_ .")
	value, _ := strconv.ParseInt(prefix, 10, 32)
	return value
}

func collectionFold(value string) string {
	return cases.Fold().String(norm.NFKC.String(value))
}

// SectionRange identifies a source slice within Note.RawContent.
type SectionRange struct {
	StartOffset int
	EndOffset   int
}

// Frontmatter holds the normalized strict article metadata.
type Frontmatter struct {
	Title          string
	Description    string
	Date           time.Time
	Updated        time.Time
	Tags           []string
	Aliases        []string
	Publish        *bool
	Slug           string
	Type           string
	Order          *int
	Author         string
	Reviewed       time.Time
	Status         string
	Audience       string
	ProductVersion string
	Series         string
	Cover          string
	Banner         string
	BannerAlt      string
}

// SectionFrontmatter is the deliberately smaller schema used by _index.md.
type SectionFrontmatter struct {
	Title       string
	Description string
	Publish     *bool
	Order       *int
	Banner      string
	BannerAlt   string
}

// SectionSource is a parsed _index.md source, kept separate from articles.
type SectionSource struct {
	RelPath       string
	SectionPath   string
	Frontmatter   SectionFrontmatter
	FieldLines    map[string]int `json:"-"`
	RawContent    []byte
	BodyStartLine int
	LastModified  time.Time
}

// Heading captures a heading extracted from Markdown.
type Heading struct {
	Level int
	Text  string
	ID    string
}

// LinkRef records an outbound wikilink across extraction and resolution phases.
type LinkRef struct {
	// RawTarget is captured from the source wikilink during AST extraction.
	RawTarget string
	// ResolvedRelPath is filled on render-time link copies once RawTarget
	// matches an article or section source.
	ResolvedRelPath string
	Display         string
	Fragment        string
	Standard        bool
	Line            int
	Offset          int
}

// EmbedRef records an embed reference discovered during parsing.
type EmbedRef struct {
	Target   string
	Fragment string
	IsImage  bool
	Width    int
	Line     int
	Offset   int
}

// ImageRef records a standard Markdown image reference discovered during parsing.
type ImageRef struct {
	RawTarget string
	Line      int
	Offset    int
}

// Tag represents a tag and the notes currently associated with it.
type Tag struct {
	Name  string
	Slug  string
	Notes []string
}

// Asset represents a published non-Markdown source resource.
type Asset struct {
	SrcPath  string
	DstPath  string
	RefCount int
}

// PlannedAsset holds the exact emitted bytes and destination of an explicitly
// discovered input. Data may differ from the source after URL rewriting.
type PlannedAsset struct {
	Asset
	Data []byte
}
