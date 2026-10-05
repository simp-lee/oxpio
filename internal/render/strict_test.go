package render

import (
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

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
