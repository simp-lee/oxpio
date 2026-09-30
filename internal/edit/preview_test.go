package edit

import (
	"strings"
	"testing"
)

func TestRewritePreviewHTMLRewritesQuotedRootURLs(t *testing.T) {
	body := []byte(`<a href = "/docs/">Docs</a><img SRC='/images/logo.svg'><script src="//cdn.example/app.js"></script><img srcset="/images/small.png 1x, /images/large.png 2x">`)
	got := string(rewritePreviewHTML(body, "/_obsite/preview/token"))
	for _, want := range []string{
		`href = "/_obsite/preview/token/docs/"`,
		`SRC='/_obsite/preview/token/images/logo.svg'`,
		`src="//cdn.example/app.js"`,
		`srcset="/_obsite/preview/token/images/small.png 1x,/_obsite/preview/token/images/large.png 2x"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten HTML missing %q: %s", want, got)
		}
	}
}
