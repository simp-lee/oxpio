// Package social generates deterministic page-level social card assets.
package social

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"github.com/rivo/uniseg"
)

const (
	Width                = 1200
	Height               = 630
	GeneratorVersion     = "oxpio-social-card-v2"
	cardPrimaryFontName  = "KaTeX_Main-Regular.ttf"
	cardFallbackFontName = "DroidSansFallbackFull.ttf"
)

// cardFontData and cardFallbackFontData are pinned embedded fonts used for
// deterministic Latin and CJK card text; generation never consults a machine
// font installation.
//
//go:embed assets/KaTeX_Main-Regular.ttf
var cardFontData []byte

//go:embed assets/DroidSansFallbackFull.ttf
var cardFallbackFontData []byte

var (
	parsedCardFont         = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(cardFontData) })
	parsedCardFallbackFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(cardFallbackFontData) })
)

var (
	background = color.RGBA{R: 0x0f, G: 0x17, B: 0x2a, A: 0xff}
	primary    = color.RGBA{R: 0xf8, G: 0xfa, B: 0xfc, A: 0xff}
	secondary  = color.RGBA{R: 0xcb, G: 0xd5, B: 0xe1, A: 0xff}
	accent     = color.RGBA{R: 0x38, G: 0xbd, B: 0xf8, A: 0xff}
)

// Input is the normalized article input to the card generator. The article
// metadata fields are included in the canonical identity even when they are
// not rendered on the card; banner and cover paths are intentionally not
// included (only the cover content hash is).
type Input struct {
	CanonicalURL string
	SiteTitle    string
	Title        string
	// Context is the normalized section title plus the optional version label,
	// joined with " / ".
	Context        string
	Description    string
	Date           string
	Updated        string
	Tags           []string
	Aliases        []string
	Slug           string
	Type           string
	Order          *int
	Author         string
	Reviewed       string
	Status         string
	Audience       string
	ProductVersion string
	Series         string
	Cover          []byte
}

// Result contains the canonical input, PNG bytes, and content-addressed output
// path. Path is always relative to the generated output root.
type Result struct {
	CanonicalJSON []byte
	PNG           []byte
	Path          string
	InputHash     string
	PNGHash       string
}

// canonicalInput is the R8.6 schema. Struct declaration order is the wire
// key order; encoding/json emits it without whitespace. Optional metadata is
// omitted when its normalized value is absent.
type canonicalInput struct {
	CanonicalURL   string   `json:"canonicalURL"`
	SiteTitle      string   `json:"siteTitle"`
	Title          string   `json:"title"`
	Context        string   `json:"context"`
	Description    string   `json:"description,omitempty"`
	Date           string   `json:"date,omitempty"`
	Updated        string   `json:"updated,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	Slug           string   `json:"slug,omitempty"`
	Type           string   `json:"type,omitempty"`
	Order          *int     `json:"order,omitempty"`
	Author         string   `json:"author,omitempty"`
	Reviewed       string   `json:"reviewed,omitempty"`
	Status         string   `json:"status,omitempty"`
	Audience       string   `json:"audience,omitempty"`
	ProductVersion string   `json:"productVersion,omitempty"`
	Series         string   `json:"series,omitempty"`
	CoverHash      string   `json:"coverHash,omitempty"`
	Generator      string   `json:"generator"`
}

// Generate creates a deterministic PNG without system fonts, timestamps, or
// randomness. A non-empty Cover must be a supported, decodable local image.
func Generate(input Input) (Result, error) {
	input, err := normalizeInput(input)
	if err != nil {
		return Result{}, err
	}
	coverHash := ""
	var cover image.Image
	if len(input.Cover) > 0 {
		decoded, format, err := image.Decode(bytes.NewReader(input.Cover))
		if err != nil {
			return Result{}, fmt.Errorf("decode cover: %w", err)
		}
		if format != "png" && format != "jpeg" && format != "webp" {
			return Result{}, fmt.Errorf("cover format %q is not supported", format)
		}
		cover = decoded
		hash := sha256.Sum256(input.Cover)
		coverHash = hex.EncodeToString(hash[:])
	}
	canonical := canonicalInput{
		CanonicalURL: input.CanonicalURL, SiteTitle: input.SiteTitle, Title: input.Title, Context: input.Context,
		Description: input.Description, Date: input.Date, Updated: input.Updated, Tags: input.Tags,
		Aliases: input.Aliases, Slug: input.Slug, Type: input.Type, Order: input.Order,
		Author: input.Author, Reviewed: input.Reviewed, Status: input.Status, Audience: input.Audience,
		ProductVersion: input.ProductVersion, Series: input.Series, CoverHash: coverHash, Generator: GeneratorVersion,
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return Result{}, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, Width, Height))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	if cover != nil {
		drawCover(canvas, cover)
	} else {
		fillRect(canvas, image.Rect(72, 72, 84, 558), accent)
	}
	maxWidth := 1056
	if cover != nil {
		maxWidth = 624
	}
	if err := drawTextColorLines(canvas, 72, 80, input.SiteTitle, 28, maxWidth, 1, primary); err != nil {
		return Result{}, err
	}
	if err := drawText(canvas, 72, 160, input.Title, 64, maxWidth); err != nil {
		return Result{}, err
	}
	if input.Context != "" {
		if err := drawTextColor(canvas, 72, 420, input.Context, 24, maxWidth, secondary); err != nil {
			return Result{}, err
		}
	}
	metadata := make([]string, 0, 3)
	if input.Author != "" {
		metadata = append(metadata, input.Author)
	}
	if input.Date != "" {
		metadata = append(metadata, input.Date)
	}
	if input.Status != "" {
		metadata = append(metadata, input.Status)
	}
	for index, value := range metadata {
		if err := drawTextColor(canvas, 72, 478+index*24, value, 24, maxWidth, secondary); err != nil {
			return Result{}, err
		}
	}
	var encoded bytes.Buffer
	// Cards are independently content-addressed; BestSpeed keeps their bytes
	// deterministic while avoiding default zlib compression cost at scale.
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&encoded, canvas); err != nil {
		return Result{}, err
	}
	pngBytes := encoded.Bytes()
	inputHash := sha256.Sum256(canonicalJSON)
	pngHash := sha256.Sum256(pngBytes)
	urlHash := sha256.Sum256([]byte(input.CanonicalURL))
	return Result{CanonicalJSON: append([]byte(nil), canonicalJSON...), PNG: append([]byte(nil), pngBytes...), Path: fmt.Sprintf("assets/social/%x/%x-%x.png", urlHash, inputHash, pngHash), InputHash: hex.EncodeToString(inputHash[:]), PNGHash: hex.EncodeToString(pngHash[:])}, nil
}

func normalizeInput(input Input) (Input, error) {
	input.CanonicalURL = strings.TrimSpace(input.CanonicalURL)
	input.SiteTitle = strings.TrimSpace(input.SiteTitle)
	input.Title = strings.TrimSpace(input.Title)
	input.Context = strings.TrimSpace(input.Context)
	input.Description = strings.TrimSpace(input.Description)
	var err error
	if input.Updated, err = normalizeCardTime(input.Updated, "updated"); err != nil {
		return Input{}, err
	}
	if input.Date, err = normalizeCardTime(input.Date, "date"); err != nil {
		return Input{}, err
	}
	if input.Reviewed, err = normalizeCardTime(input.Reviewed, "reviewed"); err != nil {
		return Input{}, err
	}
	input.Slug = strings.TrimSpace(input.Slug)
	input.Type = strings.TrimSpace(input.Type)
	input.Author = strings.TrimSpace(input.Author)
	input.Status = strings.TrimSpace(input.Status)
	input.Audience = strings.TrimSpace(input.Audience)
	input.ProductVersion = strings.TrimSpace(input.ProductVersion)
	input.Series = strings.TrimSpace(input.Series)
	input.Tags = normalizeCardList(input.Tags)
	input.Aliases = normalizeCardList(input.Aliases)
	if input.CanonicalURL == "" || input.Title == "" {
		return Input{}, fmt.Errorf("canonical URL and title are required")
	}
	return input, nil
}

func normalizeCardList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeCardTime(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) == len("2006-01-02") {
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return "", fmt.Errorf("%s must be RFC 3339 or YYYY-MM-DD", name)
		}
		return parsed.UTC().Format(time.RFC3339), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("%s must be RFC 3339 or YYYY-MM-DD", name)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func drawCover(dst *image.RGBA, src image.Image) {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw == 0 || sh == 0 {
		return
	}
	scaleX, scaleY := float64(432)/float64(sw), float64(630)/float64(sh)
	scale := scaleX
	if scaleY > scale {
		scale = scaleY
	}
	width, height := int(float64(sw)*scale+0.5), int(float64(sh)*scale+0.5)
	left, top := 768+(432-width)/2, (630-height)/2
	for y := range height {
		for x := range width {
			dx, dy := left+x, top+y
			if dx < 768 || dx >= 1200 || dy < 0 || dy >= 630 {
				continue
			}
			sx := bounds.Min.X + int(float64(x)/scale)
			sy := bounds.Min.Y + int(float64(y)/scale)
			pixel := color.NRGBAModel.Convert(src.At(sx, sy)).(color.NRGBA)
			composite := func(channel, base uint8) uint8 {
				return uint8((uint32(channel)*uint32(pixel.A) + uint32(base)*uint32(255-pixel.A)) / 255)
			}
			r, g, b := composite(pixel.R, background.R), composite(pixel.G, background.G), composite(pixel.B, background.B)
			const overlayAlpha = uint32(46)
			r = uint8((uint32(r)*(255-overlayAlpha) + uint32(background.R)*overlayAlpha) / 255)
			g = uint8((uint32(g)*(255-overlayAlpha) + uint32(background.G)*overlayAlpha) / 255)
			b = uint8((uint32(b)*(255-overlayAlpha) + uint32(background.B)*overlayAlpha) / 255)
			dst.SetRGBA(dx, dy, color.RGBA{R: r, G: g, B: b, A: 0xff})
		}
	}
}

func fillRect(dst *image.RGBA, rect image.Rectangle, value color.Color) {
	draw.Draw(dst, rect, &image.Uniform{C: value}, image.Point{}, draw.Src)
}

func drawText(dst *image.RGBA, x, y int, text string, size, maxWidth int) error {
	return drawTextColorLines(dst, x, y, text, size, maxWidth, 3, primary)
}

func drawTextColor(dst *image.RGBA, x, y int, text string, size, maxWidth int, foreground color.Color) error {
	return drawTextColorLines(dst, x, y, text, size, maxWidth, 1, foreground)
}

func drawTextColorLines(dst *image.RGBA, x, y int, text string, size, maxWidth, maxLines int, foreground color.Color) error {
	if text == "" {
		return nil
	}
	fontValue, err := parsedCardFont()
	if err != nil {
		return fmt.Errorf("parse social-card font %s: %w", cardPrimaryFontName, err)
	}
	fallbackValue, err := parsedCardFallbackFont()
	if err != nil {
		return fmt.Errorf("parse social-card fallback font %s: %w", cardFallbackFontName, err)
	}
	primaryFace, err := opentype.NewFace(fontValue, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return fmt.Errorf("create social-card font face: %w", err)
	}
	fallbackFace, err := opentype.NewFace(fallbackValue, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		_ = primaryFace.Close()
		return fmt.Errorf("create social-card fallback font face: %w", err)
	}
	face := &fallbackFontFace{primary: primaryFace, fallback: fallbackFace}
	defer func() { _ = face.Close() }()
	// x/image/font draws a font's .notdef glyph when the selected face lacks a
	// rune. Replace unsupported grapheme clusters before measuring and drawing,
	// so distinct valid input cannot silently become the same pixels.
	text = replaceMissingGlyphs(text, face)
	lines := wrapMeasured(text, maxWidth, maxLines, face)
	for lineIndex, line := range lines {
		if err := drawLineColor(dst, x, y+lineIndex*76, line, face, foreground); err != nil {
			return err
		}
	}
	return nil
}

func wrapMeasured(text string, maxWidth, maxLines int, face font.Face) []string {
	clusters := make([]string, 0)
	iterator := uniseg.NewGraphemes(text)
	for iterator.Next() {
		clusters = append(clusters, iterator.Str())
	}
	lines := make([]string, 0, maxLines)
	current := ""
	currentWidth := 0
	for len(clusters) > 0 {
		cluster := clusters[0]
		clusters = clusters[1:]
		width := font.MeasureString(face, cluster).Ceil()
		if current != "" && currentWidth+width > maxWidth {
			if len(lines) == maxLines-1 {
				return append(lines, truncateMeasured(current, maxWidth, face))
			}
			lines = append(lines, current)
			current, currentWidth = "", 0
		}
		if current == "" && width > maxWidth {
			// The cluster cannot be split without corrupting the grapheme, so
			// omit it and the remaining text in favor of an ellipsis.
			return append(lines, truncateMeasured(cluster, maxWidth, face))
		}
		current += cluster
		currentWidth += width
	}
	if current != "" && len(lines) < maxLines {
		lines = append(lines, current)
	} else if current != "" && len(lines) == maxLines {
		lines[maxLines-1] = truncateMeasured(lines[maxLines-1], maxWidth, face)
	}
	return lines
}

func truncateMeasured(value string, maxWidth int, face font.Face) string {
	ellipsis := "…"
	if font.MeasureString(face, value+ellipsis).Ceil() <= maxWidth {
		return value + ellipsis
	}
	iterator := uniseg.NewGraphemes(value)
	result := ""
	for iterator.Next() {
		candidate := result + iterator.Str()
		if font.MeasureString(face, candidate+ellipsis).Ceil() > maxWidth {
			break
		}
		result = candidate
	}
	return result + ellipsis
}

func replaceMissingGlyphs(text string, face *fallbackFontFace) string {
	iterator := uniseg.NewGraphemes(text)
	var result strings.Builder
	for iterator.Next() {
		cluster := iterator.Str()
		missing := false
		for _, value := range cluster {
			if face.faceFor(value) == nil {
				missing = true
				break
			}
		}
		if !missing {
			result.WriteString(cluster)
			continue
		}

		// Keep the placeholder deterministic and specific to the original
		// cluster. ASCII is present in the pinned primary font, unlike many
		// Unicode replacement or symbol characters.
		result.WriteByte('[')
		first := true
		for _, value := range cluster {
			if !first {
				result.WriteByte('-')
			}
			first = false
			fmt.Fprintf(&result, "U+%X", value)
		}
		result.WriteByte(']')
	}
	return result.String()
}

type fallbackFontFace struct {
	primary  font.Face
	fallback font.Face
}

func (f *fallbackFontFace) faceFor(value rune) font.Face {
	if f == nil {
		return nil
	}
	if f.primary != nil {
		if _, ok := f.primary.GlyphAdvance(value); ok {
			return f.primary
		}
	}
	if f.fallback != nil {
		if _, ok := f.fallback.GlyphAdvance(value); ok {
			return f.fallback
		}
	}
	return nil
}

func (f *fallbackFontFace) Glyph(dot fixed.Point26_6, value rune) (image.Rectangle, image.Image, image.Point, fixed.Int26_6, bool) {
	face := f.faceFor(value)
	if face == nil {
		return image.Rectangle{}, nil, image.Point{}, 0, false
	}
	return face.Glyph(dot, value)
}

func (f *fallbackFontFace) GlyphBounds(value rune) (fixed.Rectangle26_6, fixed.Int26_6, bool) {
	face := f.faceFor(value)
	if face == nil {
		return fixed.Rectangle26_6{}, 0, false
	}
	return face.GlyphBounds(value)
}

func (f *fallbackFontFace) GlyphAdvance(value rune) (fixed.Int26_6, bool) {
	face := f.faceFor(value)
	if face == nil {
		return 0, false
	}
	return face.GlyphAdvance(value)
}

func (f *fallbackFontFace) Kern(left, right rune) fixed.Int26_6 {
	leftFace, rightFace := f.faceFor(left), f.faceFor(right)
	if leftFace == nil || leftFace != rightFace {
		return 0
	}
	return leftFace.Kern(left, right)
}

func (f *fallbackFontFace) Metrics() font.Metrics {
	if f == nil || f.primary == nil {
		return font.Metrics{}
	}
	return f.primary.Metrics()
}

func (f *fallbackFontFace) Close() error {
	if f == nil {
		return nil
	}
	var first error
	if f.primary != nil {
		first = f.primary.Close()
	}
	if f.fallback != nil {
		if err := f.fallback.Close(); first == nil {
			first = err
		}
	}
	return first
}

func drawLineColor(dst *image.RGBA, x, y int, text string, face font.Face, foreground color.Color) error {
	if face == nil {
		return fmt.Errorf("social-card font face is required")
	}
	drawer := &font.Drawer{Dst: dst, Src: image.NewUniform(foreground), Face: face, Dot: fixed.P(x, y+face.Metrics().Ascent.Ceil())}
	drawer.DrawString(text)
	return nil
}
