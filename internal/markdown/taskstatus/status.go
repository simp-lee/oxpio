package taskstatus

import (
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type Status struct {
	gast.BaseInline
	Code byte
}

var KindStatus = gast.NewNodeKind("TaskStatus")

func NewStatus(code byte) *Status {
	return &Status{Code: code}
}

func (s *Status) Kind() gast.NodeKind { return KindStatus }

func (s *Status) Dump(source []byte, level int) {
	gast.DumpHelper(s, source, level, map[string]string{"Code": string([]byte{s.Code})}, nil)
}

type extender struct{}

func NewExtension() goldmark.Extender { return extender{} }

func (extender) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(statusParser{}, -100),
	))
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(statusRenderer{Config: gmhtml.NewConfig()}, 600),
	))
}

type statusParser struct{}

func (statusParser) Trigger() []byte { return []byte{'['} }

func (statusParser) Parse(parent gast.Node, reader text.Reader, _ parser.Context) gast.Node {
	if parent.Parent() == nil || parent.Parent().FirstChild() != parent {
		return nil
	}
	if parent.HasChildren() {
		return nil
	}
	if _, ok := parent.Parent().(*gast.ListItem); !ok {
		return nil
	}
	line, _ := reader.PeekLine()
	if len(line) < 4 || line[0] != '[' || line[2] != ']' || line[3] != ' ' {
		return nil
	}
	if !supported(line[1]) {
		return nil
	}
	reader.Advance(4)
	return NewStatus(line[1])
}

func (statusParser) CloseBlock(gast.Node, parser.Context) {}

func supported(code byte) bool {
	switch code {
	case ' ', '/', 'x', 'X', '-', '>', '<', '?', '!', '*', '"', 'l', 'b', 'i', 'S', 'I', 'p', 'c', 'f', 'k', 'w', 'u', 'd', 'D', 'P', 'M':
		return true
	default:
		return false
	}
}

type statusRenderer struct {
	gmhtml.Config
}

func (r statusRenderer) RegisterFuncs(register renderer.NodeRendererFuncRegisterer) {
	register.Register(KindStatus, r.render)
}

func (r statusRenderer) render(writer util.BufWriter, _ []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
	if !entering {
		return gast.WalkContinue, nil
	}
	status := node.(*Status)
	_, label := presentation(status.Code)
	_, _ = writer.WriteString(`<input class="oxpio-task-input" data-task="`)
	writeAttributeByte(writer, status.Code)
	_, _ = writer.WriteString(`" aria-label="`)
	_, _ = writer.WriteString(label)
	_, _ = writer.WriteString(`" disabled="" type="checkbox"`)
	if status.Code != ' ' {
		_, _ = writer.WriteString(` checked=""`)
	}
	_, _ = writer.WriteString(`>`)
	return gast.WalkContinue, nil
}

func writeAttributeByte(writer util.BufWriter, code byte) {
	switch code {
	case '&':
		_, _ = writer.WriteString(`&amp;`)
	case '<':
		_, _ = writer.WriteString(`&lt;`)
	case '>':
		_, _ = writer.WriteString(`&gt;`)
	case '"':
		_, _ = writer.WriteString(`&quot;`)
	default:
		_ = writer.WriteByte(code)
	}
}

func presentation(code byte) (className, label string) {
	switch code {
	case ' ':
		return "status-todo", "to-do"
	case '/':
		return "status-incomplete", "incomplete"
	case 'x', 'X':
		return "status-done", "done"
	case '-':
		return "status-canceled", "canceled"
	case '>':
		return "status-forwarded", "forwarded"
	case '<':
		return "status-scheduling", "scheduling"
	case '?':
		return "status-question", "question"
	case '!':
		return "status-important", "important"
	case '*':
		return "status-star", "star"
	case '"':
		return "status-quote", "quote"
	case 'l':
		return "status-location", "location"
	case 'b':
		return "status-bookmark", "bookmark"
	case 'i':
		return "status-information", "information"
	case 'S':
		return "status-savings", "savings"
	case 'I':
		return "status-idea", "idea"
	case 'p':
		return "status-pros", "pros"
	case 'c':
		return "status-cons", "cons"
	case 'f':
		return "status-fire", "fire"
	case 'k':
		return "status-key", "key"
	case 'w':
		return "status-win", "win"
	case 'u':
		return "status-up", "up"
	case 'd':
		return "status-down", "down"
	case 'D':
		return "status-draft-pr", "draft pull request"
	case 'P':
		return "status-open-pr", "open pull request"
	case 'M':
		return "status-merged-pr", "merged pull request"
	default:
		return "status-unknown", "task status"
	}
}
