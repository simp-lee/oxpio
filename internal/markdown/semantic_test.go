package markdown

import (
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/yuin/goldmark/text"
)

func TestRelatedSemanticTextBoundsMalformedInvisibleHTMLToBlock(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		open string
	}{
		{name: "hidden", open: "<span hidden>"},
		{name: "script", open: "<script>"},
		{name: "style", open: "<style>"},
		{name: "template", open: "<template>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := []byte("Visible before " + test.open + "hidden remainder\n\nVisible next block\n")
			root := NewParser(diag.NewCollector()).Parser().Parse(text.NewReader(source))
			_, body := RelatedSemanticText(root, source)
			if body != "Visible before Visible next block" {
				t.Fatalf("RelatedSemanticText() body = %q, want visible text after malformed hidden block", body)
			}
		})
	}
}
