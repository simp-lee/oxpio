package render

import (
	"reflect"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
	internaltheme "github.com/simp-lee/oxpio/internal/theme"
)

func TestStrictNavigationDoesNotActivateQueryVariant(t *testing.T) {
	if strictNavigationIsCurrent("/article/?x=1", "/article/", "https://example.test/article/") {
		t.Fatal("query navigation variant was marked as current page")
	}
	if !strictNavigationIsCurrent("/article/", "/article/", "https://example.test/article/") {
		t.Fatal("exact navigation route was not marked as current page")
	}
}

func TestStrictArticleThemeReceivesRecommendationEvidence(t *testing.T) {
	files, err := internaltheme.BaseFiles()
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/base.html"] = []byte(`{{define "base"}}{{range .Related}}{{.Path}}|{{.Score}}|{{range .Reasons}}{{.}},{{end}}|{{.Signals.Content}}|{{.Signals.Link}}|{{.Signals.Tag}}{{end}}{{end}}`)
	plan := &model.SitePlan{Config: model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Theme: "base"}, ThemeFiles: files}
	article := &model.Note{RelPath: "alpha.md", Route: "/alpha/", Frontmatter: model.Frontmatter{Title: "Alpha", Type: "doc"}, RawContent: []byte("Body\n")}
	index := &model.VaultIndex{Notes: map[string]*model.Note{
		"beta.md": {RelPath: "beta.md", Route: "/beta/", Frontmatter: model.Frontmatter{Title: "Beta", Type: "doc"}},
	}}
	recommendations := []model.RelatedRecommendation{{
		RelPath: "beta.md", Score: 1.15, Reasons: []string{"content", "tag"},
		Signals: model.RecommendationSignals{Content: 1, Tag: 0.15},
	}}
	data, err := RenderStrictArticleWithRecommendations(plan, article, nil, nil, 0, 0, nil, nil, recommendations, index, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "beta.md|1.15|content,tag,|1|0|0.15") {
		t.Fatalf("theme did not receive recommendation evidence: %s", data)
	}
}

func TestStrictArticleThemeMarksContentForFocusedPreview(t *testing.T) {
	files, err := internaltheme.BaseFiles()
	if err != nil {
		t.Fatal(err)
	}
	files["layouts/base.html"] = []byte(`{{define "base"}}<!doctype html><html><body><article>{{.Content}}</article></body></html>{{end}}`)
	plan := &model.SitePlan{Config: model.SiteConfig{Title: "Site", BaseURL: "https://example.test/", Theme: "base"}, ThemeFiles: files}
	article := &model.Note{RelPath: "article.md", Route: "/article/", Frontmatter: model.Frontmatter{Title: "Article", Type: "doc"}, RawContent: []byte("Body\n")}
	data, err := RenderStrictArticleWithRecommendations(plan, article, nil, nil, 0, 0, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, want := range []string{"<!--oxpio-content-start-7c4f7e1b6d2a-->", "<!--oxpio-content-end-7c4f7e1b6d2a-->", "Body"} {
		if !strings.Contains(output, want) {
			t.Fatalf("themed article output missing %q: %s", want, output)
		}
	}
}

func TestThemedRecommendationCardsPreserveAgentEvidence(t *testing.T) {
	plan := &model.SitePlan{Config: model.SiteConfig{BaseURL: "https://example.test/"}}
	index := &model.VaultIndex{Notes: map[string]*model.Note{
		"beta.md": {RelPath: "beta.md", Route: "/beta/", Frontmatter: model.Frontmatter{Title: "Beta", Type: "doc"}},
	}}
	recommendations := []model.RelatedRecommendation{{
		RelPath: "beta.md", Score: 1.15, Reasons: []string{"content", "tag"},
		Signals: model.RecommendationSignals{Content: 1, Tag: 0.15},
	}}
	cards := themedRecommendationCards(plan, index, recommendations)
	if len(cards) != 1 {
		t.Fatalf("recommendation cards = %#v, want one card", cards)
	}
	card := cards[0]
	if card.Path != "beta.md" || card.Href != "/beta/" || card.Title != "Beta" || card.Score != 1.15 {
		t.Fatalf("card identity = %#v", card)
	}
	if !reflect.DeepEqual(card.Reasons, recommendations[0].Reasons) || card.Signals.Content != 1 || card.Signals.Tag != 0.15 {
		t.Fatalf("card evidence = %#v", card)
	}
}

func TestStrictLeadingTitlePlacementAvoidsRetainedIDCollisions(t *testing.T) {
	note := &model.Note{Headings: []model.Heading{{Level: 1, Text: "Title", ID: "title"}}}
	if shellID, tocID := strictLeadingTitlePlacement(`<p>Body</p>`, note, "title"); shellID != "title" || tocID != "title" {
		t.Fatalf("normal placement = (%q, %q); want (title, title)", shellID, tocID)
	}
	note.RawContent = []byte(`<h1 id="title">Title</h1>`)
	if shellID, tocID := strictLeadingTitlePlacement(`<h1 id="title">Title</h1>`, note, "title"); shellID != "" || tocID != "" {
		t.Fatalf("raw collision placement = (%q, %q); want empty IDs", shellID, tocID)
	}
	note.RawContent = []byte("# Title\n# Title\n")
	note.Headings = append(note.Headings, model.Heading{Level: 1, Text: "Title", ID: "title"})
	if shellID, tocID := strictLeadingTitlePlacement(`<h1 id="title">Title</h1>`, note, "title"); shellID != "" || tocID != "title" {
		t.Fatalf("duplicate Markdown placement = (%q, %q); want no shell ID and TOC omission", shellID, tocID)
	}
}

func TestStrictDropLeadingTitleHeadingInsideWrapperPreservesWrapperBytes(t *testing.T) {
	content := `<div data-wrapper='keep'><h1 id=title>Title</h1><p>Body</p></div>`
	got, removed, headingID, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	want := `<div data-wrapper='keep'><p>Body</p></div>`
	if !removed || headingID != "title" || got != want {
		t.Fatalf("result = %q, removed = %t, headingID = %q; want wrapped body", got, removed, headingID)
	}
}

func TestStrictDropLeadingTitleHeadingPreservesComments(t *testing.T) {
	prefix := "\n<!-- keep this comment -->\n"
	suffix := "<p class='authored' data-example=OXPIO>Body &amp; content</p><!-- tail -->"
	content := prefix + "<h1 id=title>Title<!-- keep inside heading --><script><!-- keep script comment --></script><style><!-- keep style comment --></style></h1>" + suffix
	got, removed, headingID, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || headingID != "title" {
		t.Fatalf("removed = %t, headingID = %q; want true, title", removed, headingID)
	}
	if got != prefix+"<!-- keep inside heading --><!-- keep script comment --><!-- keep style comment -->"+suffix {
		t.Fatalf("result = %q, want byte-exact comments and body without duplicate heading", got)
	}
}

func TestStrictDropLeadingTitleHeadingIgnoresInvisibleText(t *testing.T) {
	content := `<h1>Title <span hidden>extra</span><span style="display:none!important">hidden</span><script>ignored</script><style>ignored</style><template>ignored</template></h1><p>Body</p>`
	got, removed, _, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || got != "<p>Body</p>" {
		t.Fatalf("result = %q, removed = %t; want body only", got, removed)
	}
}

func TestStrictDropLeadingTitleHeadingKeepsLaterSameTitleHeading(t *testing.T) {
	content := `<h1>Title</h1><h1 id="title-2">Title</h1><p>Body</p>`
	got, removed, headingID, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || headingID != "" || got != `<h1 id="title-2">Title</h1><p>Body</p>` {
		t.Fatalf("result = %q, removed = %t, headingID = %q; want later heading preserved", got, removed, headingID)
	}
}

func TestStrictDropLeadingTitleHeadingSkipsInvisibleLeadingElements(t *testing.T) {
	content := `<script>ignored</script><span hidden>before</span><span style="visibility: hidden">also before</span><h1>Title</h1><p>Body</p>`
	got, removed, _, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || got != content[:strings.Index(content, "<h1")]+`<p>Body</p>` {
		t.Fatalf("result = %q, removed = %t; want invisible elements and body", got, removed)
	}
}

func TestStrictDropLeadingTitleHeadingDoesNotMisreadCSSValues(t *testing.T) {
	content := `<h1>Title <span style="background-image: url(display:none.png)">Visible</span></h1><p>Body</p>`
	got, removed, _, err := strictDropLeadingTitleHeading(content, "Title Visible")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || got != "<p>Body</p>" {
		t.Fatalf("result = %q, removed = %t; want body only", got, removed)
	}
}

func TestStrictDropLeadingTitleHeadingUsesVisibleHTMLBoundaries(t *testing.T) {
	content := `<h1>Title<hr>Next</h1><p>Body</p>`
	got, removed, _, err := strictDropLeadingTitleHeading(content, "Title Next")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || got != "<p>Body</p>" {
		t.Fatalf("result = %q, removed = %t; want body only", got, removed)
	}
}

func TestStrictDropLeadingTitleHeadingRequiresExactTitle(t *testing.T) {
	content := "<h1>Other title</h1><p>Body</p>"
	got, removed, headingID, err := strictDropLeadingTitleHeading(content, "Title")
	if err != nil {
		t.Fatal(err)
	}
	if removed || headingID != "" || got != content {
		t.Fatalf("result = %q, removed = %t; want unchanged content", got, removed)
	}
}
