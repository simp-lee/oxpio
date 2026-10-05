package markdown

import (
	"bytes"
	"io"
	"strings"

	internalasset "github.com/simp-lee/oxpio/internal/asset"
	"github.com/simp-lee/oxpio/internal/markdown/pathutil"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
	nethtml "golang.org/x/net/html"
)

type rawHTMLExtender struct {
	sourceNote *model.Note
	outputNote *model.Note
	index      *model.VaultIndex
	assetSink  AssetSink
}

func newRawHTMLExtender(sourceNote, outputNote *model.Note, index *model.VaultIndex, assetSink AssetSink) goldmark.Extender {
	return &rawHTMLExtender{sourceNote: sourceNote, outputNote: outputNote, index: index, assetSink: assetSink}
}

func (e *rawHTMLExtender) Extend(md goldmark.Markdown) {
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(newRawHTMLRenderer(e.sourceNote, e.outputNote, e.index, e.assetSink), 501),
	))
}

type rawHTMLRenderer struct {
	gmhtml.Config
	context    RawHTMLResourceContext
	sourceNote *model.Note
	outputNote *model.Note
	index      *model.VaultIndex
	assetSink  AssetSink
}

func newRawHTMLRenderer(sourceNote, outputNote *model.Note, index *model.VaultIndex, assetSink AssetSink) *rawHTMLRenderer {
	if outputNote == nil {
		outputNote = sourceNote
	}
	return &rawHTMLRenderer{
		Config: gmhtml.NewConfig(), sourceNote: sourceNote, outputNote: outputNote,
		index: index, assetSink: assetSink,
	}
}

func (r *rawHTMLRenderer) SetOption(name renderer.OptionName, value any) {
	r.Config.SetOption(name, value)
}

func (r *rawHTMLRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(gast.KindHTMLBlock, r.renderHTMLBlock)
	reg.Register(gast.KindRawHTML, r.renderRawHTML)
	reg.Register(gast.KindText, r.renderText)
}

func (r *rawHTMLRenderer) renderHTMLBlock(
	w util.BufWriter,
	source []byte,
	node gast.Node,
	entering bool,
) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	n := node.(*gast.HTMLBlock)
	var raw bytes.Buffer
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		_, _ = raw.Write(line.Value(source))
	}
	if n.HasClosure() {
		_, _ = raw.Write(n.ClosureLine.Value(source))
	}
	rewritten, err := r.context.Rewrite(raw.Bytes(), r.rewriteTarget)
	if err != nil {
		return gast.WalkStop, err
	}
	_, _ = w.Write(rewritten)
	return gast.WalkContinue, nil
}

func (r *rawHTMLRenderer) renderRawHTML(
	w util.BufWriter,
	source []byte,
	node gast.Node,
	entering bool,
) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkSkipChildren, nil
	}

	n := node.(*gast.RawHTML)
	raw := n.Segments.Value(source)
	rewritten, err := r.context.Rewrite(raw, r.rewriteTarget)
	if err != nil {
		return gast.WalkStop, err
	}
	_, _ = w.Write(rewritten)
	return gast.WalkSkipChildren, nil
}

func (r *rawHTMLRenderer) renderText(w util.BufWriter, source []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	text := node.(*gast.Text)
	value := text.Segment.Value(source)
	if r.context.InStyle() {
		rewritten, err := internalasset.RewriteCSSURLs(value, r.rewriteTarget)
		if err != nil {
			return gast.WalkStop, err
		}
		_, _ = w.Write(rewritten)
	} else if text.IsRaw() {
		r.Writer.RawWrite(w, value)
	} else {
		r.Writer.Write(w, value)
	}
	if text.HardLineBreak() || text.SoftLineBreak() && r.HardWraps {
		if r.XHTML {
			_, _ = w.WriteString("<br />\n")
		} else {
			_, _ = w.WriteString("<br>\n")
		}
	} else if text.SoftLineBreak() {
		_ = w.WriteByte('\n')
	}
	return gast.WalkContinue, nil
}

func (r *rawHTMLRenderer) rewriteTarget(raw string) (string, error) {
	if r == nil || r.index == nil || !resourcepath.IsLocalTarget(raw) {
		return raw, nil
	}
	target, suffix := splitDestinationSuffix(raw)
	if target == "" {
		return raw, nil
	}
	resource := resourcepath.ResolveIndexedAssetPath(r.sourceNote, r.index, target)
	if resource == "" || !resourcepath.IsResourceAllowedForNote(r.index, r.sourceNote, resource) {
		return raw, nil
	}
	destination := resource
	if r.assetSink != nil {
		if planned := pathutil.NormalizeSitePath(r.assetSink.Register(resource)); planned != "" {
			destination = planned
		}
	}
	return pathutil.RelativeToNoteOutput(r.outputNote, destination) + suffix, nil
}

// Targets returns resource URL spellings while retaining inline HTML context.
func (c *RawHTMLResourceContext) Targets(data []byte) ([]string, error) {
	targets := make([]string, 0)
	_, err := c.Rewrite(data, func(raw string) (string, error) {
		targets = append(targets, raw)
		return raw, nil
	})
	return targets, err
}

// CSSResourceTargets returns URL and import targets from CSS text.
func CSSResourceTargets(data []byte) ([]string, error) {
	targets := make([]string, 0)
	_, err := internalasset.RewriteCSSURLs(data, func(raw string) (string, error) {
		targets = append(targets, raw)
		return raw, nil
	})
	return targets, err
}

// RawHTMLResourceContext retains raw-text/RCDATA context across Markdown AST
// fragments, where apparent tags inside textarea or script are literal text.
type RawHTMLResourceContext struct {
	textTag string
}

// InStyle reports whether subsequent text belongs to an inline stylesheet.
func (c *RawHTMLResourceContext) InStyle() bool {
	return c.textTag == "style"
}

// RewriteRawHTMLResources preserves authored raw HTML bytes except for local
// resource URL values selected by resolve. Raw HTML remains trusted content;
// this function only routes its resource dependencies through the asset plan.
func RewriteRawHTMLResources(data []byte, resolve func(string) (string, error)) ([]byte, error) {
	var context RawHTMLResourceContext
	return context.Rewrite(data, resolve)
}

// Rewrite plans resource values without interpreting raw-text contents as tags.
func (c *RawHTMLResourceContext) Rewrite(data []byte, resolve func(string) (string, error)) ([]byte, error) {
	if len(data) == 0 || resolve == nil {
		return data, nil
	}
	z := nethtml.NewTokenizerFragment(bytes.NewReader(data), c.textTag)
	var output bytes.Buffer
	for {
		kind := z.Next()
		if kind == nethtml.ErrorToken {
			if err := z.Err(); err != nil && err != io.EOF {
				return nil, err
			}
			return output.Bytes(), nil
		}
		raw := bytes.Clone(z.Raw())
		switch kind {
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			token := z.Token()
			rewritten, err := rewriteRawHTMLTag(raw, token, resolve)
			if err != nil {
				return nil, err
			}
			_, _ = output.Write(rewritten)
			switch token.Data {
			case "noscript":
				// Fallback markup must also publish its resources for readers
				// with scripting disabled.
				z.NextIsNotRawText()
			case "iframe", "noembed", "noframes", "plaintext", "script", "style", "textarea", "title", "xmp":
				c.textTag = token.Data
			}
		case nethtml.EndTagToken:
			_, _ = output.Write(raw)
			token := z.Token()
			if token.Data == c.textTag {
				c.textTag = ""
			}
		case nethtml.TextToken:
			if !c.InStyle() {
				_, _ = output.Write(raw)
				continue
			}
			rewritten, err := internalasset.RewriteCSSURLs(raw, resolve)
			if err != nil {
				return nil, err
			}
			_, _ = output.Write(rewritten)
		default:
			_, _ = output.Write(raw)
		}
	}
}

type rawHTMLReplacement struct {
	start int
	end   int
	value string
}

func rewriteRawHTMLTag(raw []byte, token nethtml.Token, resolve func(string) (string, error)) ([]byte, error) {
	tag := strings.ToLower(token.Data)
	linkFetchesResource := rawHTMLLinkFetchesResource(token)
	replacements := make([]rawHTMLReplacement, 0)
	attributeIndex := 0
	for index := skipRawHTMLTagName(raw); index < len(raw); {
		index = skipRawHTMLSpace(raw, index)
		if index >= len(raw) || raw[index] == '>' || raw[index] == '/' && index+1 < len(raw) && raw[index+1] == '>' {
			break
		}
		nameStart := index
		for index < len(raw) && !isRawHTMLSpace(raw[index]) && raw[index] != '=' && raw[index] != '>' && raw[index] != '/' {
			index++
		}
		if nameStart == index {
			index++
			continue
		}
		name := strings.ToLower(string(raw[nameStart:index]))
		decodedValue := ""
		if attributeIndex < len(token.Attr) {
			decodedValue = token.Attr[attributeIndex].Val
		}
		attributeIndex++
		index = skipRawHTMLSpace(raw, index)
		if index >= len(raw) || raw[index] != '=' {
			continue
		}
		index++
		index = skipRawHTMLSpace(raw, index)
		if index >= len(raw) {
			break
		}
		quote := byte(0)
		valueStart := index
		var valueEnd int
		if raw[index] == '\'' || raw[index] == '"' {
			quote = raw[index]
			index++
			valueStart = index
			for index < len(raw) && raw[index] != quote {
				index++
			}
			valueEnd = index
			if index < len(raw) {
				index++
			}
		} else {
			for index < len(raw) && !isRawHTMLSpace(raw[index]) && raw[index] != '>' {
				index++
			}
			valueEnd = index
		}

		value := decodedValue
		rewritten, relevant, err := rewriteRawHTMLAttribute(tag, name, value, linkFetchesResource, resolve)
		if err != nil {
			return nil, err
		}
		if relevant && rewritten != value {
			replacements = append(replacements, rawHTMLReplacement{start: valueStart, end: valueEnd, value: escapeRawHTMLAttribute(rewritten, quote)})
		}
	}
	if len(replacements) == 0 {
		return raw, nil
	}
	var output bytes.Buffer
	position := 0
	for _, replacement := range replacements {
		_, _ = output.Write(raw[position:replacement.start])
		_, _ = output.WriteString(replacement.value)
		position = replacement.end
	}
	_, _ = output.Write(raw[position:])
	return output.Bytes(), nil
}

func rawHTMLLinkFetchesResource(token nethtml.Token) bool {
	if !strings.EqualFold(token.Data, "link") {
		return false
	}
	for _, attribute := range token.Attr {
		if !strings.EqualFold(attribute.Key, "rel") {
			continue
		}
		for relation := range strings.FieldsSeq(strings.ToLower(attribute.Val)) {
			switch relation {
			case "stylesheet", "icon", "preload", "modulepreload", "manifest", "apple-touch-icon", "mask-icon":
				return true
			}
		}
	}
	return false
}

func rewriteRawHTMLAttribute(tag, name, value string, linkFetchesResource bool, resolve func(string) (string, error)) (string, bool, error) {
	switch name {
	case "style":
		rewritten, err := internalasset.RewriteCSSURLs([]byte(value), resolve)
		return string(rewritten), true, err
	case "src", "poster", "background":
		rewritten, err := resolve(value)
		return rewritten, true, err
	case "srcset":
		rewritten, err := rewriteRawHTMLSrcset(value, resolve)
		return rewritten, true, err
	case "imagesrcset":
		if tag == "link" && linkFetchesResource {
			rewritten, err := rewriteRawHTMLSrcset(value, resolve)
			return rewritten, true, err
		}
	case "data":
		if tag == "object" {
			rewritten, err := resolve(value)
			return rewritten, true, err
		}
	case "href", "xlink:href":
		if tag == "link" && linkFetchesResource || tag == "image" || tag == "use" {
			rewritten, err := resolve(value)
			return rewritten, true, err
		}
	}
	return value, false, nil
}

func rewriteRawHTMLSrcset(value string, resolve func(string) (string, error)) (string, error) {
	replacements := make([]rawHTMLReplacement, 0)
	for index := 0; index < len(value); {
		for index < len(value) && (isRawHTMLSpace(value[index]) || value[index] == ',') {
			index++
		}
		start := index
		for index < len(value) && !isRawHTMLSpace(value[index]) {
			index++
		}
		end := index
		for end > start && value[end-1] == ',' {
			end--
		}
		if end > start {
			raw := value[start:end]
			rewritten, err := resolve(raw)
			if err != nil {
				return "", err
			}
			if rewritten != raw {
				replacements = append(replacements, rawHTMLReplacement{start: start, end: end, value: rewritten})
			}
		}
		if end < index {
			continue
		}
		for index < len(value) && value[index] != ',' {
			index++
		}
	}
	if len(replacements) == 0 {
		return value, nil
	}
	var output strings.Builder
	position := 0
	for _, replacement := range replacements {
		output.WriteString(value[position:replacement.start])
		output.WriteString(replacement.value)
		position = replacement.end
	}
	output.WriteString(value[position:])
	return output.String(), nil
}

func skipRawHTMLTagName(raw []byte) int {
	index := 0
	if index < len(raw) && raw[index] == '<' {
		index++
	}
	index = skipRawHTMLSpace(raw, index)
	for index < len(raw) && !isRawHTMLSpace(raw[index]) && raw[index] != '>' && raw[index] != '/' {
		index++
	}
	return index
}

func skipRawHTMLSpace(raw []byte, index int) int {
	for index < len(raw) && isRawHTMLSpace(raw[index]) {
		index++
	}
	return index
}

func isRawHTMLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r' || value == '\f'
}

func escapeRawHTMLAttribute(value string, quote byte) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	if quote == '\'' {
		return strings.ReplaceAll(value, "'", "&#39;")
	}
	if quote == '"' {
		return strings.ReplaceAll(value, `"`, "&#34;")
	}
	return `"` + strings.ReplaceAll(value, `"`, "&#34;") + `"`
}
