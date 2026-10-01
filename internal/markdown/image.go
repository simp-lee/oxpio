package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"strings"

	"github.com/gohugoio/hugo-goldmark-extensions/passthrough"
	"github.com/simp-lee/obsite/internal/diag"
	"github.com/simp-lee/obsite/internal/markdown/callout"
	"github.com/simp-lee/obsite/internal/markdown/pathutil"
	"github.com/simp-lee/obsite/internal/model"
	"github.com/simp-lee/obsite/internal/resourcepath"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	gast "github.com/yuin/goldmark/ast"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type imageExtender struct {
	sourceNote *model.Note
	outputNote *model.Note
	index      *model.VaultIndex
	assetSink  AssetSink
	imageCount *int
}

func newImageExtender(sourceNote *model.Note, outputNote *model.Note, index *model.VaultIndex, assetSink AssetSink, imageCount *int) goldmark.Extender {
	if outputNote == nil {
		outputNote = sourceNote
	}

	return &imageExtender{
		sourceNote: sourceNote,
		outputNote: outputNote,
		index:      index,
		assetSink:  assetSink,
		imageCount: imageCount,
	}
}

func (e *imageExtender) Extend(md goldmark.Markdown) {
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(newImageHTMLRenderer(e.sourceNote, e.outputNote, e.index, e.assetSink, e.imageCount), 500),
	))
}

type imageHTMLRenderer struct {
	gmhtml.Config
	sourceNote *model.Note
	outputNote *model.Note
	index      *model.VaultIndex
	assetSink  AssetSink
	imageCount *int
}

func newImageHTMLRenderer(sourceNote *model.Note, outputNote *model.Note, index *model.VaultIndex, assetSink AssetSink, imageCount *int) *imageHTMLRenderer {
	if outputNote == nil {
		outputNote = sourceNote
	}

	return &imageHTMLRenderer{
		Config:     gmhtml.NewConfig(),
		sourceNote: sourceNote,
		outputNote: outputNote,
		index:      index,
		assetSink:  assetSink,
		imageCount: imageCount,
	}
}

func (r *imageHTMLRenderer) SetOption(name renderer.OptionName, value any) {
	r.Config.SetOption(name, value)
}

func (r *imageHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(gast.KindImage, r.renderImage)
}

func (r *imageHTMLRenderer) renderImage(
	w util.BufWriter,
	source []byte,
	node gast.Node,
	entering bool,
) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}

	n := node.(*gast.Image)
	imageIndex := r.nextImageIndex()
	rewritten := r.rewriteDestination(NormalizeDestination(string(n.Destination)))

	_, _ = w.WriteString("<img src=\"")
	escapedDestination := util.URLEscape([]byte(rewritten), false)
	if r.Unsafe || !gmhtml.IsDangerousURL(escapedDestination) {
		_, _ = w.Write(util.EscapeHTML(escapedDestination))
	}
	_, _ = w.WriteString(`" alt="`)
	r.writeAltText(w, source, n)
	_ = w.WriteByte('"')

	if len(n.Title) > 0 {
		_, _ = w.WriteString(` title="`)
		r.Writer.Write(w, n.Title)
		_ = w.WriteByte('"')
	}

	if imageIndex > 1 {
		if _, ok := n.Attribute([]byte("loading")); !ok {
			_, _ = w.WriteString(` loading="lazy"`)
		}
	}

	if n.Attributes() != nil {
		gmhtml.RenderAttributes(w, n, gmhtml.ImageAttributeFilter)
	}

	if r.XHTML {
		_, _ = w.WriteString(" />")
	} else {
		_, _ = w.WriteString(">")
	}

	return gast.WalkSkipChildren, nil
}

func (r *imageHTMLRenderer) nextImageIndex() int {
	if r.imageCount == nil {
		return 1
	}

	(*r.imageCount)++
	return *r.imageCount
}

func (r *imageHTMLRenderer) writeAltText(w util.BufWriter, source []byte, node gast.Node) {
	_, _ = w.WriteString(html.EscapeString(plainAltText(source, node)))
}

func plainAltText(source []byte, node gast.Node) string {
	if node == nil {
		return ""
	}

	var builder strings.Builder
	appendPlainAltText(&builder, source, node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func appendPlainAltText(builder *strings.Builder, source []byte, node gast.Node) {
	if builder == nil || node == nil {
		return
	}

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch current := child.(type) {
		case *gast.Text:
			value := current.Value(source)
			hasLineBreak := len(value) > 0 && (value[len(value)-1] == '\r' || value[len(value)-1] == '\n')
			value = bytes.TrimRight(value, "\r\n")
			if current.IsRaw() {
				_, _ = builder.Write(value)
			} else {
				_, _ = builder.WriteString(resolveMarkdownText(value))
			}
			if hasLineBreak || current.SoftLineBreak() || current.HardLineBreak() {
				_ = builder.WriteByte(' ')
			}
		case *gast.String:
			if current.IsRaw() || current.IsCode() {
				_, _ = builder.Write(current.Value)
			} else {
				_, _ = builder.WriteString(resolveMarkdownText(current.Value))
			}
		default:
			appendPlainAltText(builder, source, child)
		}
	}
}

func resolveMarkdownText(value []byte) string {
	var escaped bytes.Buffer
	writer := bufio.NewWriter(&escaped)
	gmhtml.DefaultWriter.Write(writer, value)
	_ = writer.Flush()
	return html.UnescapeString(escaped.String())
}

func (r *imageHTMLRenderer) rewriteDestination(rawDestination string) string {
	trimmed := strings.TrimSpace(rawDestination)
	if trimmed == "" {
		return trimmed
	}

	baseDestination, suffix := splitDestinationSuffix(trimmed)
	vaultRelPath := r.resolveIndexedAssetPath(baseDestination)
	if vaultRelPath == "" {
		return trimmed
	}

	siteRelPath := vaultRelPath
	if r.assetSink != nil {
		if registered := pathutil.NormalizeSitePath(r.assetSink.Register(vaultRelPath)); registered != "" {
			siteRelPath = registered
		}
	}

	return pathutil.RelativeToNoteOutput(r.outputNote, siteRelPath) + suffix
}

func (r *imageHTMLRenderer) resolveIndexedAssetPath(rawDestination string) string {
	if r == nil {
		return ""
	}

	resource := resourcepath.ResolveIndexedAssetPath(r.sourceNote, r.index, rawDestination)
	if !resourcepath.IsResourceAllowedForNote(r.index, r.sourceNote, resource) {
		return ""
	}
	return resource
}

type codeBlockExtender struct {
	note *model.Note
	diag *diag.Collector
}

type mathTrackingExtender struct {
	note *model.Note
}

func newMathTrackingExtender(note *model.Note) goldmark.Extender {
	return &mathTrackingExtender{note: note}
}

func (e *mathTrackingExtender) Extend(md goldmark.Markdown) {
	mathRenderer := newMathTrackingHTMLRenderer(e.note)
	md.Parser().AddOptions(
		parser.WithParagraphTransformers(
			// Footnote blocks are consolidated while block parsing closes, so save
			// each source paragraph's original container chain before that happens.
			util.Prioritized(&displayMathParagraphContainerTransformer{renderer: mathRenderer}, 499),
		),
		parser.WithASTTransformers(
			util.Prioritized(&displayMathContainerTransformer{renderer: mathRenderer}, 998),
		),
	)
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(mathRenderer, 50),
	))
}

type displayMathSourceRange struct {
	start      int
	stop       int
	containers []displayMathSourceContainer
}

type mathTrackingHTMLRenderer struct {
	note                  *model.Note
	paragraphSourceRanges []displayMathSourceRange
	sourceContainers      map[*passthrough.PassthroughBlock][]displayMathSourceContainer
}

func newMathTrackingHTMLRenderer(note *model.Note) *mathTrackingHTMLRenderer {
	return &mathTrackingHTMLRenderer{
		note:             note,
		sourceContainers: make(map[*passthrough.PassthroughBlock][]displayMathSourceContainer),
	}
}

type displayMathParagraphContainerTransformer struct {
	renderer *mathTrackingHTMLRenderer
}

var displayMathContainerCaptureKey = parser.NewContextKey()

func (t *displayMathParagraphContainerTransformer) Transform(node *gast.Paragraph, _ text.Reader, context parser.Context) {
	if t == nil || t.renderer == nil {
		return
	}
	if context.Get(displayMathContainerCaptureKey) == nil {
		t.renderer.paragraphSourceRanges = t.renderer.paragraphSourceRanges[:0]
		t.renderer.sourceContainers = make(map[*passthrough.PassthroughBlock][]displayMathSourceContainer)
		context.Set(displayMathContainerCaptureKey, true)
	}
	if node == nil || node.Parent() == nil || node.Lines().Len() == 0 {
		return
	}
	first := node.Lines().At(0)
	last := node.Lines().At(node.Lines().Len() - 1)
	t.renderer.paragraphSourceRanges = append(t.renderer.paragraphSourceRanges, displayMathSourceRange{
		start:      first.Start,
		stop:       last.Stop,
		containers: displayMathSourceContainers(node),
	})
}

func (r *mathTrackingHTMLRenderer) capturedSourceContainers(position int) ([]displayMathSourceContainer, bool) {
	if r == nil {
		return nil, false
	}
	for _, sourceRange := range r.paragraphSourceRanges {
		if position >= sourceRange.start && position < sourceRange.stop {
			return sourceRange.containers, true
		}
	}
	return nil, false
}

type displayMathContainerTransformer struct {
	renderer *mathTrackingHTMLRenderer
}

func (t *displayMathContainerTransformer) Transform(document *gast.Document, _ text.Reader, _ parser.Context) {
	if t == nil || t.renderer == nil {
		return
	}
	_ = gast.Walk(document, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		block, ok := node.(*passthrough.PassthroughBlock)
		if entering && ok {
			containers, captured := t.renderer.capturedSourceContainers(block.Pos())
			if !captured {
				containers = displayMathSourceContainers(block)
			}
			t.renderer.sourceContainers[block] = containers
			return gast.WalkSkipChildren, nil
		}
		return gast.WalkContinue, nil
	})
}

func (r *mathTrackingHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(passthrough.KindPassthroughInline, r.renderInlineMath)
	reg.Register(passthrough.KindPassthroughBlock, r.renderDisplayMath)
}

func (r *mathTrackingHTMLRenderer) renderInlineMath(w util.BufWriter, source []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	if r.note != nil {
		r.note.HasMath = true
	}
	mathNode := node.(*passthrough.PassthroughInline)
	_, _ = w.WriteString(`<span data-obsite-math-source="inline">`)
	_, _ = w.WriteString(html.EscapeString(string(mathNode.Segment.Value(source))))
	_, _ = w.WriteString(`</span>`)
	return gast.WalkContinue, nil
}

func (r *mathTrackingHTMLRenderer) renderDisplayMath(w util.BufWriter, source []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	if r.note != nil {
		r.note.HasMath = true
	}
	_, _ = w.WriteString(`<div data-obsite-math-source="display">`)
	block := node.(*passthrough.PassthroughBlock)
	containers := r.sourceContainers[block]
	if containers == nil {
		containers = displayMathSourceContainers(block)
	}
	for i := 0; i < node.Lines().Len(); i++ {
		segment := node.Lines().At(i)
		line := stripDisplayMathContainerMarkers(string(segment.Value(source)), containers)
		_, _ = w.WriteString(html.EscapeString(line))
	}
	_, _ = w.WriteString("\n</div>")
	return gast.WalkSkipChildren, nil
}

type displayMathSourceContainer struct {
	quote  bool
	indent int
}

func displayMathSourceContainers(node gast.Node) []displayMathSourceContainer {
	var reversed []displayMathSourceContainer
	for ancestor := node.Parent(); ancestor != nil; ancestor = ancestor.Parent() {
		switch current := ancestor.(type) {
		case *gast.ListItem:
			reversed = append(reversed, displayMathSourceContainer{indent: current.Offset})
		case *extensionast.Footnote:
			reversed = append(reversed, displayMathSourceContainer{indent: 4})
		default:
			if ancestor.Kind() == gast.KindBlockquote || ancestor.Kind() == callout.KindCallout {
				reversed = append(reversed, displayMathSourceContainer{quote: true})
			}
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func stripDisplayMathContainerMarkers(source string, containers []displayMathSourceContainer) string {
	if len(containers) == 0 {
		return source
	}

	lines := strings.SplitAfter(source, "\n")
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		lineOffset := 0
		for _, container := range containers {
			var ok bool
			if container.quote {
				line, lineOffset, ok = stripQuoteContainerMarker(line, lineOffset)
			} else {
				line, lineOffset, ok = stripListContainerIndent(line, lineOffset, container.indent)
			}
			if !ok {
				break
			}
		}
		lines[i] = preserveLeadingTabWidths(line, lineOffset)
	}
	return strings.Join(lines, "")
}

func preserveLeadingTabWidths(line string, lineOffset int) string {
	leadingEnd := 0
	for leadingEnd < len(line) && (line[leadingEnd] == ' ' || line[leadingEnd] == '\t') {
		leadingEnd++
	}
	if !strings.Contains(line[:leadingEnd], "\t") {
		return line
	}

	var normalized strings.Builder
	for index := 0; index < leadingEnd; index++ {
		if line[index] == ' ' {
			normalized.WriteByte(' ')
			lineOffset++
			continue
		}
		width := util.TabWidth(lineOffset)
		normalized.WriteString(strings.Repeat(" ", width))
		lineOffset += width
	}
	normalized.WriteString(line[leadingEnd:])
	return normalized.String()
}

func stripQuoteContainerMarker(line string, lineOffset int) (string, int, bool) {
	indent, marker := util.IndentWidth([]byte(line), lineOffset)
	if marker == len(line) || line[marker] != '>' || indent > 3 {
		return line, lineOffset, false
	}

	lineOffset += indent + 1
	line = line[marker+1:]
	if strings.HasPrefix(line, " ") {
		return line[1:], lineOffset + 1, true
	}
	if strings.HasPrefix(line, "\t") {
		padding := util.TabWidth(lineOffset) - 1
		return strings.Repeat(" ", padding) + line[1:], lineOffset + 1, true
	}
	return line, lineOffset, true
}

func stripListContainerIndent(line string, lineOffset int, want int) (string, int, bool) {
	if want <= 0 {
		return line, lineOffset, true
	}
	position, padding := util.IndentPosition([]byte(line), lineOffset, want)
	if position < 0 {
		return line, lineOffset, false
	}
	return strings.Repeat(" ", padding) + line[position:], lineOffset + want, true
}

func newCodeBlockExtender(note *model.Note, diagCollector *diag.Collector) goldmark.Extender {
	return &codeBlockExtender{note: note, diag: diagCollector}
}

func (e *codeBlockExtender) Extend(md goldmark.Markdown) {
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(newCodeBlockHTMLRenderer(e.note, e.diag), 150),
	))
}

type codeBlockHTMLRenderer struct {
	gmhtml.Config
	note         *model.Note
	diag         *diag.Collector
	fallback     renderer.SetOptioner
	fallbackFunc renderer.NodeRendererFunc
}

func newCodeBlockHTMLRenderer(note *model.Note, diagCollector *diag.Collector) *codeBlockHTMLRenderer {
	fallbackRenderer := highlighting.NewHTMLRenderer(highlighting.WithStyle("github-dark"))
	fallbackRegisterer := newNodeRendererFuncRegisterer()
	fallbackRenderer.RegisterFuncs(fallbackRegisterer)
	setOptioner, _ := fallbackRenderer.(renderer.SetOptioner)

	return &codeBlockHTMLRenderer{
		Config:       gmhtml.NewConfig(),
		note:         note,
		diag:         diagCollector,
		fallback:     setOptioner,
		fallbackFunc: fallbackRegisterer.funcFor(gast.KindFencedCodeBlock),
	}
}

func (r *codeBlockHTMLRenderer) SetOption(name renderer.OptionName, value any) {
	r.Config.SetOption(name, value)
	if r.fallback != nil {
		r.fallback.SetOption(name, value)
	}
}

func (r *codeBlockHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(gast.KindFencedCodeBlock, r.renderFencedCodeBlock)
}

func (r *codeBlockHTMLRenderer) renderFencedCodeBlock(
	w util.BufWriter,
	source []byte,
	node gast.Node,
	entering bool,
) (gast.WalkStatus, error) {
	n := node.(*gast.FencedCodeBlock)
	if isMermaidFence(n.Language(source)) {
		if !entering {
			return gast.WalkContinue, nil
		}

		if r.note != nil {
			r.note.HasMermaid = true
		}

		_, _ = w.WriteString(`<pre class="mermaid">`)
		r.writeCodeLines(w, source, n)
		_, _ = w.WriteString("</pre>\n")
		return gast.WalkSkipChildren, nil
	}

	if language, ok := unsupportedFenceLanguage(n.Language(source)); ok {
		if !entering {
			return gast.WalkContinue, nil
		}

		r.recordUnsupportedFence(source, n, language)
		_, _ = w.WriteString(`<pre class="unsupported-syntax unsupported-`)
		_, _ = w.WriteString(language)
		_, _ = w.WriteString(`">`)
		r.writeCodeLines(w, source, n)
		_, _ = w.WriteString("</pre>\n")
		return gast.WalkSkipChildren, nil
	}

	if r.fallbackFunc == nil {
		return gast.WalkContinue, nil
	}
	return r.fallbackFunc(w, source, node, entering)
}

func (r *codeBlockHTMLRenderer) writeCodeLines(w util.BufWriter, source []byte, node gast.Node) {
	for i := 0; i < node.Lines().Len(); i++ {
		line := node.Lines().At(i)
		_, _ = w.Write(util.EscapeHTML(line.Value(source)))
	}
}

func (r *codeBlockHTMLRenderer) recordUnsupportedFence(source []byte, node *gast.FencedCodeBlock, language string) {
	if r == nil || r.diag == nil {
		return
	}

	location := diag.Location{}
	if r.note != nil {
		location.Path = r.note.RelPath
	}
	location.Line = fencedCodeBlockLine(r.note, source, node)
	r.diag.Add(diag.Diagnostic{Severity: diag.SeverityWarning, Kind: diag.KindUnsupportedSyntax, Location: location, Field: "code-fence", Target: language, Message: fmt.Sprintf("%s fenced code block is not supported; rendering as plain preformatted text", language)})
}

func unsupportedFenceLanguage(language []byte) (string, bool) {
	switch normalized := strings.ToLower(strings.TrimSpace(string(language))); normalized {
	case "dataview", "dataviewjs":
		return normalized, true
	default:
		return "", false
	}
}

func fencedCodeBlockLine(note *model.Note, source []byte, node *gast.FencedCodeBlock) int {
	if node == nil || node.Lines().Len() == 0 {
		return 0
	}

	start := node.Lines().At(0).Start
	if start < 0 {
		start = 0
	}
	if start > len(source) {
		start = len(source)
	}

	line := 1 + bytes.Count(source[:start], []byte("\n"))
	if note != nil && note.BodyStartLine > 1 {
		line += note.BodyStartLine - 1
	}
	return line
}

func isMermaidFence(language []byte) bool {
	return strings.EqualFold(strings.TrimSpace(string(language)), "mermaid")
}

func splitDestinationSuffix(value string) (string, string) {
	index := strings.IndexAny(value, "?#")
	if index < 0 {
		return value, ""
	}

	return value[:index], value[index:]
}

type nodeRendererFuncRegisterer struct {
	funcs map[gast.NodeKind]renderer.NodeRendererFunc
}

func newNodeRendererFuncRegisterer() *nodeRendererFuncRegisterer {
	return &nodeRendererFuncRegisterer{funcs: make(map[gast.NodeKind]renderer.NodeRendererFunc)}
}

func (r *nodeRendererFuncRegisterer) Register(kind gast.NodeKind, fn renderer.NodeRendererFunc) {
	r.funcs[kind] = fn
}

func (r *nodeRendererFuncRegisterer) funcFor(kind gast.NodeKind) renderer.NodeRendererFunc {
	return r.funcs[kind]
}
