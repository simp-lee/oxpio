// Package livepreview is the renderer shared by the native builder and the
// browser Write surface. It deliberately contains no vault, theme, or HTTP
// state so the same Markdown grammar and HTML contract can run in Go and WASM.
package livepreview

import (
	"bytes"
	"strings"

	"github.com/gohugoio/hugo-goldmark-extensions/passthrough"
	internalmarkdown "github.com/simp-lee/oxpio/internal/markdown"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type Block struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Kind string `json:"kind"`
	HTML string `json:"html"`
}

type Document struct {
	Source     string  `json:"source"`
	HTML       string  `json:"html"`
	Blocks     []Block `json:"blocks"`
	HasMath    bool    `json:"hasMath"`
	HasMermaid bool    `json:"hasMermaid"`
}

// Render parses and renders one complete Markdown document with the same
// core parser used by the native Markdown pipeline. Block ranges are source
// byte offsets, matching CodeMirror's offsets for UTF-8 documents.
func Render(source string) (Document, error) {
	sourceBytes := []byte(source)
	md := internalmarkdown.NewCoreMarkdown()
	document := md.Parser().Parse(text.NewReader(sourceBytes))

	var full bytes.Buffer
	if err := md.Renderer().Render(&full, sourceBytes, document); err != nil {
		return Document{}, err
	}
	result := Document{Source: source, HTML: full.String()}
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		from, to, ok := nodeRange(node, sourceBytes)
		if !ok || from >= to {
			continue
		}
		var block bytes.Buffer
		if err := md.Renderer().Render(&block, sourceBytes, node); err != nil {
			return Document{}, err
		}
		if node.Kind().String() == "FootnoteList" {
			if len(result.Blocks) > 0 {
				result.Blocks[len(result.Blocks)-1].HTML += block.String()
			}
			continue
		}
		result.Blocks = append(result.Blocks, Block{From: from, To: to, Kind: node.Kind().String(), HTML: block.String()})
		if nodeContainsMath(node) {
			result.HasMath = true
		}
		if nodeContainsMermaid(node, sourceBytes) {
			result.HasMermaid = true
		}
	}
	return result, nil
}

func nodeRange(node gast.Node, source []byte) (int, int, bool) {
	sourceLength := len(source)
	lines := node.Lines()
	if lines != nil && lines.Len() > 0 {
		from := lines.At(0).Start
		to := lines.At(lines.Len() - 1).Stop
		if node.Pos() >= 0 && node.Pos() < from {
			from = node.Pos()
		}
		for from > 0 && source[from-1] != '\n' {
			from--
		}
		if to < sourceLength && source[to] == '\n' {
			to++
		}
		if from >= 0 && to > from && from < sourceLength {
			if to > sourceLength {
				to = sourceLength
			}
			return from, to, true
		}
	}
	from, to := sourceLength, 0
	_ = gast.Walk(node, func(child gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering || child.Pos() < 0 {
			return gast.WalkContinue, nil
		}
		value := nodeText(child, source)
		start := child.Pos()
		if start < from {
			from = start
		}
		if end := start + len(value); end > to {
			to = end
		}
		return gast.WalkContinue, nil
	})
	if from < sourceLength && to > from {
		return from, to, true
	}
	return 0, 0, false
}

func nodeText(node gast.Node, source []byte) []byte {
	switch current := node.(type) {
	case *gast.Text:
		return current.Value(source)
	case *gast.String:
		return current.Value
	case *gast.RawHTML:
		return current.Segments.Value(source)
	case *gast.AutoLink:
		return current.Label(source)
	case *passthrough.PassthroughInline:
		return current.Segment.Value(source)
	default:
		if node.Type() == gast.TypeBlock {
			return node.Lines().Value(source)
		}
		return nil
	}
}

func nodeContainsMath(node gast.Node) bool {
	found := false
	_ = gast.Walk(node, func(child gast.Node, entering bool) (gast.WalkStatus, error) {
		if entering {
			switch current := child.(type) {
			case *passthrough.PassthroughInline:
				if current.Delimiters != nil && current.Delimiters.Open == "%%" {
					return gast.WalkContinue, nil
				}
				found = true
				return gast.WalkStop, nil
			case *passthrough.PassthroughBlock:
				found = true
				return gast.WalkStop, nil
			}
		}
		return gast.WalkContinue, nil
	})
	return found
}

func nodeContainsMermaid(node gast.Node, source []byte) bool {
	found := false
	_ = gast.Walk(node, func(child gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		fenced, ok := child.(*gast.FencedCodeBlock)
		if !ok {
			return gast.WalkContinue, nil
		}
		if strings.EqualFold(strings.TrimSpace(string(fenced.Language(source))), "mermaid") {
			found = true
			return gast.WalkStop, nil
		}
		return gast.WalkSkipChildren, nil
	})
	return found
}
