package edit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	internalserver "github.com/simp-lee/oxpio/internal/server"
)

func TestRewritePreviewHTMLRewritesQuotedRootURLs(t *testing.T) {
	body := []byte(`<a href = "/docs/">Docs</a><img SRC='/images/logo.svg'><script src="//cdn.example/app.js"></script><img srcset="/images/small.png 1x, /images/large.png 2x">`)
	got := string(rewritePreviewHTML(body, "/_oxpio/preview/token"))
	for _, want := range []string{
		`href = "/_oxpio/preview/token/docs/"`,
		`SRC='/_oxpio/preview/token/images/logo.svg'`,
		`src="//cdn.example/app.js"`,
		`srcset="/_oxpio/preview/token/images/small.png 1x,/_oxpio/preview/token/images/large.png 2x"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten HTML missing %q: %s", want, got)
		}
	}
}

func TestServePreviewRejectsAnonymousAndExpiredTokens(t *testing.T) {
	output := t.TempDir()
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("<!doctype html><html><body>public</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	static, err := internalserver.New(output, 0)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		sessions: map[string]session{
			"valid": {username: "admin", expires: time.Now().Add(time.Hour)},
		},
		previews: map[string]*editorPreview{
			"expired": {static: static, contentHTML: []byte("private draft"), expires: time.Now().Add(-time.Hour)},
		},
	}

	anonymous := httptest.NewRecorder()
	server.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/_oxpio/preview/expired/content", nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous preview status = %d, want %d", anonymous.Code, http.StatusUnauthorized)
	}

	expired := httptest.NewRecorder()
	expiredRequest := httptest.NewRequest(http.MethodGet, "/_oxpio/preview/expired/content", nil)
	expiredRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	server.ServeHTTP(expired, expiredRequest)
	if expired.Code != http.StatusNotFound {
		t.Fatalf("expired preview status = %d, want %d", expired.Code, http.StatusNotFound)
	}
	if expired.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expired preview cache-control = %q, want no-store", expired.Header().Get("Cache-Control"))
	}
}

func TestRenderContentPreviewUsesRenderedEntryContentOnly(t *testing.T) {
	fullPage := []byte(`<!doctype html><html lang="zh"><head>
		<script src="/docs/assets/oxpio/runtime.abc.js"></script>
		<link rel="stylesheet" href="/docs/assets/oxpio-runtime/katex.min.css">
		<link rel="stylesheet" href="/docs/assets/custom.css"><link rel="stylesheet" href="/docs/theme.css">
	</head><body><header class="site-header">Navigation</header><main><div class="site-content"><article class="article-sheet">
		<h1>Article</h1><div class="entry-content article-content" data-page-content><p>Body</p><img src="/docs/assets/image.png"><img src="../../assets/relative.png"><pre><code>code</code></pre><table><tr><td>table</td></tr></table><span data-oxpio-math-source>$$x$$</span></div>
		<section class="related-articles">Related</section></article></div></main><aside class="sidebar-shell">Sidebar</aside><footer class="site-footer">Footer</footer></body></html>`)
	got, err := renderContentPreview(fullPage, "token", "Article", "/docs/guide/article/")
	if err != nil {
		t.Fatal(err)
	}
	html := string(got)
	for _, want := range []string{
		`data-oxpio-content-preview`,
		`data-oxpio-math`,
		`Article`,
		`Body`,
		`/_oxpio/preview/token/content.css`,
		`/_oxpio/logo.svg`,
		`<base href="/_oxpio/preview/token/docs/guide/article/">`,
		`/_oxpio/preview/token/docs/assets/image.png`,
		`../../assets/relative.png`,
		`/_oxpio/preview/token/docs/assets/oxpio/runtime.abc.js`,
		`/_oxpio/preview/token/docs/assets/oxpio-runtime/katex.min.css`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("content preview missing %q: %s", want, html)
		}
	}
	for _, unwanted := range []string{"site-header", "sidebar-shell", "site-footer", "related-articles", "custom.css", "theme.css"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("content preview contains %q: %s", unwanted, html)
		}
	}
}
