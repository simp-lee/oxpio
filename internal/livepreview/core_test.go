package livepreview

import (
	"os"
	"strings"
	"testing"
)

func TestRenderUsesCoreMarkdownForMathAndBlocks(t *testing.T) {
	source := "# Title\n\nA $x^2$ and \\(y^2\\) formulas.\n\n```mermaid\ngraph TD\n```\n"
	result, err := Render(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.HTML, `class="oxpio-math"`) || !strings.Contains(result.HTML, `$x^2$`) || !strings.Contains(result.HTML, `\(y^2\)`) {
		t.Fatalf("math marker missing from shared HTML: %s", result.HTML)
	}
	if !result.HasMath || !result.HasMermaid || len(result.Blocks) != 3 {
		t.Logf("HTML=%s blocks=%+v", result.HTML, result.Blocks)
		t.Fatalf("shared metadata = math:%v mermaid:%v blocks:%d", result.HasMath, result.HasMermaid, len(result.Blocks))
	}
	if result.Blocks[0].From != 0 || source[result.Blocks[0].From:result.Blocks[0].To] != "# Title\n" {
		t.Fatalf("first block range = %d:%d", result.Blocks[0].From, result.Blocks[0].To)
	}
}

func TestRenderDisplayMathRemovesContainerMarkers(t *testing.T) {
	result, err := Render("> $$\n> x^2\n> $$\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.HTML, "x^2") || strings.Contains(result.HTML, "> x^2") {
		t.Fatalf("display math HTML = %s", result.HTML)
	}
}

func TestRenderCommentDoesNotEnableMathRuntime(t *testing.T) {
	result, err := Render("%%hidden comment%%")
	if err != nil {
		t.Fatal(err)
	}
	if result.HasMath {
		t.Fatal("comment-only render reported math")
	}
	if !strings.Contains(result.HTML, `class="oxpio-comment"`) {
		t.Fatalf("comment HTML = %s", result.HTML)
	}
}

func TestRenderGalleryFixture(t *testing.T) {
	source, err := os.ReadFile("../../test/testdata/editor/live-preview.md")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(string(source))
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMath || !result.HasMermaid {
		t.Fatalf("gallery feature flags = math:%v mermaid:%v", result.HasMath, result.HasMermaid)
	}
	html := result.HTML
	for _, fragment := range []string{"<div class=\"callout callout-note\">", `<div class="footnotes"`, `class="oxpio-math"`, `class="language-mermaid"`, `class="oxpio-comment"`, `data-task=" "`, `data-task="/"`, `data-task="x"`, `data-task="-"`, `data-task="&gt;"`, `data-task="&lt;"`, `data-task="?"`, `data-task="M"`} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("gallery HTML missing %q", fragment)
		}
	}
}

func TestRenderFixtureBlocks(t *testing.T) {
	source := "# Live Preview fixture\n\nPlain **strong** *emphasis* ~~strike~~ [link](https://example.test) `code` ![image](image.png).\n\n| Name | Value |\n| --- | --- |\n| Draft | Ready |\n\n> A stable blockquote.\n\n```mermaid\ngraph TD\nA-->B\n```\n"
	result, err := Render(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("blocks=%+v", result.Blocks)
	if len(result.Blocks) != 5 {
		t.Fatalf("block count = %d", len(result.Blocks))
	}
	html := ""
	for _, block := range result.Blocks {
		html += block.HTML
	}
	for _, fragment := range []string{"<strong>", "<em>", "<del>", "<a href=", "<code>", "<img"} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("shared HTML missing %q: %s", fragment, html)
		}
	}
}
