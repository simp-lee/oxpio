package edit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	internalserver "github.com/simp-lee/oxpio/internal/server"
)

func TestRewritePreviewHTMLRewritesQuotedRootURLs(t *testing.T) {
	body := []byte(`<a href = "/docs/">Docs</a><img SRC='/images/logo.svg'><script src="//cdn.example/app.js">const src="/images/inner.svg"</script><img srcset="/images/small.png 1x, /images/large.png 2x"><img imagesrcset="/images/hero.png 1x">`)
	got := string(rewritePreviewHTML(body, "/_oxpio/preview/token"))
	for _, want := range []string{
		`href = "/_oxpio/preview/token/docs/"`,
		`SRC='/_oxpio/preview/token/images/logo.svg'`,
		`src="//cdn.example/app.js"`,
		`const src="/images/inner.svg"`,
		`srcset="/_oxpio/preview/token/images/small.png 1x, /_oxpio/preview/token/images/large.png 2x"`,
		`imagesrcset="/_oxpio/preview/token/images/hero.png 1x"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten HTML missing %q: %s", want, got)
		}
	}
}

func TestRewritePreviewHTMLRewritesMinifiedUnquotedRootURLs(t *testing.T) {
	body := []byte(`<link rel=stylesheet href=/assets/oxpio-runtime/katex.min.css><script src=/assets/oxpio-runtime/katex.min.js></script><img src=/images/logo.svg><img srcset=/images/logo.svg,/images/other.svg><img imagesrcset=/images/hero.svg>`)
	got := string(rewritePreviewHTML(body, "/_oxpio/preview/token"))
	for _, want := range []string{
		`href=/_oxpio/preview/token/assets/oxpio-runtime/katex.min.css`,
		`src=/_oxpio/preview/token/assets/oxpio-runtime/katex.min.js`,
		`src=/_oxpio/preview/token/images/logo.svg`,
		`srcset=/_oxpio/preview/token/images/logo.svg,/_oxpio/preview/token/images/other.svg`,
		`imagesrcset=/_oxpio/preview/token/images/hero.svg`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten minified HTML missing %q: %s", want, got)
		}
	}
}

func TestRewritePreviewHTMLRewritesRouteRelativeResources(t *testing.T) {
	body := []byte(`<img src="../assets/image.png"><img srcset="../assets/small.png 1x, ../../assets/large.png 2x"><img imagesrcset=../assets/hero.png,../../assets/other.png>`)
	got := string(rewritePreviewHTMLAt(body, "/_oxpio/preview/token", "/docs/article/"))
	for _, want := range []string{
		`src="/_oxpio/preview/token/docs/assets/image.png"`,
		`srcset="/_oxpio/preview/token/docs/assets/small.png 1x, /_oxpio/preview/token/assets/large.png 2x"`,
		`imagesrcset=/_oxpio/preview/token/docs/assets/hero.png,/_oxpio/preview/token/assets/other.png`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("relative preview URL missing %q: %s", want, got)
		}
	}
}

func TestRewritePreviewHTMLSkipsRCDATAAndClosingTagPrefixes(t *testing.T) {
	body := []byte(`<textarea>literal <img src="/textarea.png"></textarea><title>literal <img src="/title.png"></title><script>const value="</scriptx><img src='/script.png'>";</script>`)
	got := string(rewritePreviewHTMLAtWithBase(body, "/_oxpio/preview/token", "/docs/article/", "https://example.test/docs/"))
	if got != string(body) {
		t.Fatalf("raw text content changed: %s", got)
	}
	formFeed := []byte("<script\f>const x='<img src=\"/form-feed.png\">';</script>")
	if got := rewritePreviewHTMLAtWithBase(formFeed, "/_oxpio/preview/token", "/docs/article/", "https://example.test/docs/"); string(got) != string(formFeed) {
		t.Fatalf("form-feed raw content changed: %s", got)
	}
	unclosed := []byte(`<style>background:url("https://example.test/docs/assets/unclosed.png")`)
	if got := rewritePreviewHTMLAtWithBase(unclosed, "/_oxpio/preview/token", "/docs/article/", "https://example.test/docs/"); string(got) != string(unclosed) {
		t.Fatalf("unclosed style content changed: %s", got)
	}
}

func TestRewritePreviewHTMLLeavesTextAndCommentsUntouched(t *testing.T) {
	body := []byte(`<p>href="assets/example.png" style="background:url('/assets/text.png')" &lt;a href="assets/entity.png"&gt;</p><!-- href="/docs/comment.png" -->`)
	got := string(rewritePreviewHTMLAt(body, "/_oxpio/preview/token", "/docs/article/"))
	if got != string(body) {
		t.Fatalf("HTML text or comment changed: %s", got)
	}
}

func TestRewritePreviewHTMLRewritesConfiguredBaseURL(t *testing.T) {
	body := []byte(`<link rel="stylesheet" href="https://example.test/docs/assets/style.css?v=1#theme"><script src=https://example.test/docs/assets/app.js></script><img src="https://cdn.example/assets/logo.svg"><img srcset="https://example.test/docs/assets/small.png 1x, https://example.test/docs/assets/large.png 2x"><svg><use xlink:href="/docs/assets/symbols.svg#icon"></use></svg><style>@import "https://example.test/docs/assets/theme.css";.hero{background:url("https://example.test/docs/assets/hero.svg")}</style><div style="background:url('https://example.test/docs/assets/inline.svg');background-image:url('assets/relative.svg');content:'url(https://example.test/docs/assets/literal.png)'"></div><div style=background:url(/assets/unquoted.svg)></div><script>const src="https://example.test/docs/assets/script.js";</script><div data-éhref="/docs/not-an-attribute.png"></div><div data-note="literal src='/assets/inside.png'"></div><p>&lt;img src="/docs/example.png"&gt;</p><p>__oxpio_preview_css_0__</p>`)
	got := string(rewritePreviewHTMLAtWithBase(body, "/_oxpio/preview/token", "/docs/article/", "https://example.test/docs/"))
	for _, want := range []string{
		`href="/_oxpio/preview/token/docs/assets/style.css?v=1#theme"`,
		`src=/_oxpio/preview/token/docs/assets/app.js`,
		`src="https://cdn.example/assets/logo.svg"`,
		`srcset="/_oxpio/preview/token/docs/assets/small.png 1x, /_oxpio/preview/token/docs/assets/large.png 2x"`,
		`xlink:href="/_oxpio/preview/token/docs/assets/symbols.svg#icon"`,
		`@import "/_oxpio/preview/token/docs/assets/theme.css";`,
		`url("/_oxpio/preview/token/docs/assets/hero.svg")`,
		`style="background:url('/_oxpio/preview/token/docs/assets/inline.svg');background-image:url('/_oxpio/preview/token/docs/article/assets/relative.svg');content:'url(https://example.test/docs/assets/literal.png)'"`,
		`style=background:url(/_oxpio/preview/token/assets/unquoted.svg)`,
		`const src="https://example.test/docs/assets/script.js";`,
		`data-éhref="/docs/not-an-attribute.png"`,
		`data-note="literal src='/assets/inside.png'"`,
		`&lt;img src="/docs/example.png"&gt;`,
		`__oxpio_preview_css_0__`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("configured preview URL missing %q: %s", want, got)
		}
	}
	css := string(rewritePreviewCSSURLs([]byte(`/* url("https://example.test/docs/assets/comment.png") */ .label::after{content:"url(https://example.test/docs/assets/literal.png)"}.entity{content:&#39;url(https://example.test/docs/assets/entity.png)&#39;}`), "/_oxpio/preview/token", "https://example.test/docs/"))
	for _, want := range []string{
		`url("https://example.test/docs/assets/comment.png")`,
		`content:"url(https://example.test/docs/assets/literal.png)"`,
		`content:&#39;url(https://example.test/docs/assets/entity.png)&#39;`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("CSS non-resource text changed, missing %q: %s", want, css)
		}
	}
	cssRelative := string(rewritePreviewCSSURLsAt([]byte(`@import &#39;https://example.test/docs/assets/theme.css&#39;;.hero{background-image:image-set(/* ) */ "assets/one.png" 1x, type(/* comment */ "image/png") 1x, url(assets/three.png) 1x);mask-image:u\72l("assets/two.png")}.boundary{a:myu\72l("assets/no.png");b:画像url("assets/no2.png");c:url("assets/yes.png")}`), "/_oxpio/preview/token", "https://example.test/docs/", "/docs/article/"))
	for _, want := range []string{
		`@import &#39;/_oxpio/preview/token/docs/assets/theme.css&#39;`,
		`image-set(/* ) */ "/_oxpio/preview/token/docs/article/assets/one.png" 1x,`,
		`u\72l("/_oxpio/preview/token/docs/article/assets/two.png")`,
		`type(/* comment */ "image/png")`,
		`url(/_oxpio/preview/token/docs/article/assets/three.png)`,
		`a:myu\72l("assets/no.png")`,
		`b:画像url("assets/no2.png")`,
		`c:url("/_oxpio/preview/token/docs/article/assets/yes.png")`,
	} {
		if !strings.Contains(cssRelative, want) {
			t.Fatalf("relative CSS function missing %q: %s", want, cssRelative)
		}
	}
	for _, value := range []string{
		"https://example.test/docs/../outside.css",
		"https://example.test/docs/%2e%2e/outside.css",
	} {
		if got := rewritePreviewConfiguredURL(value, "/_oxpio/preview/token", "https://example.test/docs/"); got != value {
			t.Fatalf("dot-segment URL was rewritten: %s", got)
		}
	}
	if got := rewritePreviewConfiguredURL("https://example.test/docs/", "/_oxpio/preview/token", "https://example.test/docs/"); got != "/_oxpio/preview/token/docs/" {
		t.Fatalf("directory URL lost trailing slash: %s", got)
	}
	root := string(rewritePreviewHTMLAtWithBase([]byte(`<img src="/docs/%2e%2e/outside.css">`), "/_oxpio/preview/token", "/docs/", "https://example.test/docs/"))
	if !strings.Contains(root, `src="/docs/%2e%2e/outside.css"`) {
		t.Fatalf("root dot-segment URL was rewritten: %s", root)
	}
	fileBase := previewDocumentBasePath("/docs/article/index.html")
	if fileBase != "/docs/article/" {
		t.Fatalf("file preview base = %q", fileBase)
	}
	fileHTML := string(rewritePreviewHTMLAt([]byte(`<img src="assets/image.png">`), "/_oxpio/preview/token", fileBase))
	if !strings.Contains(fileHTML, `src="/_oxpio/preview/token/docs/article/assets/image.png"`) {
		t.Fatalf("file preview relative URL = %s", fileHTML)
	}
	escapedBase := previewDocumentBasePath("/foo%2520/index.html")
	escapedHTML := string(rewritePreviewHTMLAt([]byte(`<img src="assets/image.png">`), "/_oxpio/preview/token", escapedBase))
	if !strings.Contains(escapedHTML, `src="/_oxpio/preview/token/foo%2520/assets/image.png"`) {
		t.Fatalf("escaped file preview relative URL = %s", escapedHTML)
	}
	backslashHTML := string(rewritePreviewHTML([]byte(`<div title="C:\" src="/img.png"></div>`), "/_oxpio/preview/token"))
	if !strings.Contains(backslashHTML, `src="/_oxpio/preview/token/img.png"`) {
		t.Fatalf("backslash attribute URL = %s", backslashHTML)
	}
}

func TestRewritePreviewHTMLDoesNotConfusePrefixCollisions(t *testing.T) {
	body := []byte(`<img src="/_oxpio/preview/token2/assets/logo.svg"><img srcset="/_oxpio/preview/token2/assets/logo.svg 1x">`)
	got := string(rewritePreviewHTML(body, "/_oxpio/preview/token"))
	for _, want := range []string{
		`src="/_oxpio/preview/token/_oxpio/preview/token2/assets/logo.svg"`,
		`srcset="/_oxpio/preview/token/_oxpio/preview/token2/assets/logo.svg 1x"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prefix-collision URL = %s, want %q", got, want)
		}
	}
}

func TestServePreviewPreservesEscapedPaths(t *testing.T) {
	output := t.TempDir()
	if err := os.MkdirAll(filepath.Join(output, "docs x", "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "docs x", "index.html"), []byte(`<!doctype html><html><head><link rel="stylesheet" href="https://example.test/docs%20x/assets/style.css"><link rel="stylesheet" href="https://cdn.example/style.css"></head><body>escaped preview</body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "docs x", "assets", "style.css"), []byte(`@import "https://example.test/docs%20x/assets/theme.css"; @import "/assets/root.css"; .hero { background: url("https://example.test/docs%20x/assets/bg(a).png"); } .root { background: url("/assets/root.png"); } .external { background: url("https://cdn.example/external.png"); }`), 0o644); err != nil {
		t.Fatal(err)
	}
	static, err := internalserver.New(output, 0)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{
		baseURL:      "https://example.test/docs%20x/",
		sessions:     map[string]session{"valid": {username: "admin", expires: time.Now().Add(time.Hour)}},
		previews:     map[string]*editorPreview{"token": {static: static, root: output, expires: time.Now().Add(time.Hour)}},
		previewRoots: map[string]*internalserver.Server{},
	}
	defer func() {
		if err := server.Close(); err != nil {
			t.Errorf("close preview server: %v", err)
		}
	}()
	redirectRequest := httptest.NewRequest(http.MethodGet, "/_oxpio/preview/token/docs%20x", nil)
	redirectRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	redirectResponse := httptest.NewRecorder()
	server.ServeHTTP(redirectResponse, redirectRequest)
	if redirectResponse.Code < http.StatusMultipleChoices || redirectResponse.Code >= http.StatusBadRequest || redirectResponse.Header().Get("Location") != "/_oxpio/preview/token/docs%20x/" {
		t.Fatalf("escaped preview redirect = %d Location=%q", redirectResponse.Code, redirectResponse.Header().Get("Location"))
	}
	if _, ok := rewritePreviewLocation("https://cdn.example/outside", "/_oxpio/preview/token", "https://example.test/docs%20x/"); ok {
		t.Fatal("external preview redirect was allowed")
	}
	request := httptest.NewRequest(http.MethodGet, "/_oxpio/preview/token/docs%20x/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "escaped preview") {
		t.Fatalf("escaped preview response = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Accept-Ranges") != "" || response.Header().Get("Content-Range") != "" {
		t.Fatalf("rewritten preview retained range headers: Accept-Ranges=%q Content-Range=%q", response.Header().Get("Accept-Ranges"), response.Header().Get("Content-Range"))
	}
	methodRequest := httptest.NewRequest(http.MethodPost, "/_oxpio/preview/token/docs%20x/", nil)
	methodRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	methodResponse := httptest.NewRecorder()
	server.ServeHTTP(methodResponse, methodRequest)
	if methodResponse.Code != http.StatusMethodNotAllowed || methodResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("preview resource method response = %d Allow=%q", methodResponse.Code, methodResponse.Header().Get("Allow"))
	}
	page := response.Body.String()
	headRequest := httptest.NewRequest(http.MethodHead, "/_oxpio/preview/token/docs%20x/", nil)
	headRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	headResponse := httptest.NewRecorder()
	server.ServeHTTP(headResponse, headRequest)
	headLength, err := strconv.Atoi(headResponse.Header().Get("Content-Length"))
	if err != nil || headLength != len(page) || headResponse.Body.Len() != 0 {
		t.Fatalf("escaped preview HEAD length = %q body=%d, want %d and empty body", headResponse.Header().Get("Content-Length"), headResponse.Body.Len(), len(page))
	}
	for _, want := range []string{
		`href="/_oxpio/preview/token/docs%20x/assets/style.css"`,
		`href="https://cdn.example/style.css"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("escaped preview HTML missing %q: %s", want, page)
		}
	}
	cssRequest := httptest.NewRequest(http.MethodGet, "/_oxpio/preview/token/docs%20x/assets/style.css", nil)
	cssRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "valid"})
	cssResponse := httptest.NewRecorder()
	server.ServeHTTP(cssResponse, cssRequest)
	if cssResponse.Code != http.StatusOK {
		t.Fatalf("escaped preview CSS response = %d %s", cssResponse.Code, cssResponse.Body.String())
	}
	if cssResponse.Header().Get("Accept-Ranges") != "" || cssResponse.Header().Get("Content-Range") != "" {
		t.Fatalf("rewritten preview CSS retained range headers: Accept-Ranges=%q Content-Range=%q", cssResponse.Header().Get("Accept-Ranges"), cssResponse.Header().Get("Content-Range"))
	}
	css := cssResponse.Body.String()
	for _, want := range []string{
		`@import "/_oxpio/preview/token/docs%20x/assets/theme.css"`,
		`@import "/_oxpio/preview/token/assets/root.css"`,
		`url("/_oxpio/preview/token/docs%20x/assets/bg(a).png")`,
		`url("/_oxpio/preview/token/assets/root.png")`,
		`url("https://cdn.example/external.png")`,
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("escaped preview CSS missing %q: %s", want, css)
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

func TestServerCloseRemovesTemporaryPreviews(t *testing.T) {
	t.Parallel()

	mainOutput := t.TempDir()
	if err := os.WriteFile(filepath.Join(mainOutput, "index.html"), []byte("main"), 0o644); err != nil {
		t.Fatal(err)
	}
	previewRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(previewRoot, "index.html"), []byte("preview"), 0o644); err != nil {
		t.Fatal(err)
	}
	static, err := internalserver.New(mainOutput, 0)
	if err != nil {
		t.Fatal(err)
	}
	previewStatic, err := internalserver.New(previewRoot, 0)
	if err != nil {
		_ = static.Close()
		t.Fatal(err)
	}
	server := &Server{
		static: static,
		previews: map[string]*editorPreview{
			"preview": {static: previewStatic, root: previewRoot},
		},
	}

	if err := server.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(previewRoot); !os.IsNotExist(err) {
		t.Fatalf("preview root stat error = %v, want not exist", err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestRenderContentPreviewHTMLKeepsArticleContentAndDropsSiteShell(t *testing.T) {
	fullPage := []byte(`<!doctype html><html lang="en" data-oxpio-math data-oxpio-sidebar data-oxpio-popover><head><title>Article</title><link rel="stylesheet" href="/docs/theme.css"><link rel="stylesheet" href="/assets/style.abc123.css"><script src="/docs/runtime.js"></script></head><body class="site-body"><header class="site-header">Navigation</header><main class="site-main"><div class="site-content"><article><h1>Title</h1><div data-page-content><p>Body</p></div></article></div></main><footer>Footer</footer></body></html>`)
	got, err := renderContentPreviewHTML(fullPage)
	if err != nil {
		t.Fatal(err)
	}
	html := string(got)
	for _, want := range []string{"data-oxpio-math", "<h1>Title</h1>", "data-page-content", "data-oxpio-basic-preview", "/assets/style.abc123.css", "Body"} {
		if !strings.Contains(html, want) {
			t.Fatalf("content preview missing %q: %s", want, html)
		}
	}
	for _, unwanted := range []string{"data-oxpio-sidebar", "data-oxpio-popover", "theme.css", "runtime.js"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("content preview retained runtime marker %q: %s", unwanted, html)
		}
	}
	for _, unwanted := range []string{"site-header", "Footer", "Navigation"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("content preview retained %q: %s", unwanted, html)
		}
	}
}

func TestRenderContentPreviewHTMLRejectsMissingContentMarker(t *testing.T) {
	fullPage := []byte(`<!doctype html><html><head></head><body><header class="site-header">Navigation</header><main><article><p>Body</p></article></main></body></html>`)
	if _, err := renderContentPreviewHTML(fullPage); err == nil {
		t.Fatal("renderContentPreviewHTML accepted a page without a content marker")
	}
}

func TestRenderedContentStylePreviewRewritesEncodedAttributeQuotes(t *testing.T) {
	fullPage := []byte(`<!doctype html><html><head></head><body><main><article><div data-page-content><div style="background:url('https://example.test/docs/assets/inline.svg');background-image:image-set('https://example.test/docs/assets/set.svg' 1x);@import 'https://example.test/docs/assets/theme.css'"></div></div></article></main></body></html>`)
	content, err := renderContentPreviewHTML(fullPage)
	if err != nil {
		t.Fatal(err)
	}
	got := string(rewritePreviewHTMLAtWithBase(content, "/_oxpio/preview/token", "/docs/article/", "https://example.test/docs/"))
	if !strings.Contains(got, `style="background:url(&#39;/_oxpio/preview/token/docs/assets/inline.svg&#39;);background-image:image-set(&#39;/_oxpio/preview/token/docs/assets/set.svg&#39; 1x);@import &#39;/_oxpio/preview/token/docs/assets/theme.css&#39;"`) {
		t.Fatalf("rendered style attribute was not rewritten: %s", got)
	}
}

func TestRenderContentPreviewHTMLUsesRendererMarkersWithoutThemeMarker(t *testing.T) {
	fullPage := []byte(`<!doctype html><html><head></head><body><header class="site-header">Navigation</header><main><article><h1>Title</h1><!--oxpio-content-start-7c4f7e1b6d2a--><p>Body</p><script>window.contentScript=1</script><!--oxpio-content-end-7c4f7e1b6d2a--></article></main><footer>Footer</footer></body></html>`)
	got, err := renderContentPreviewHTML(fullPage)
	if err != nil {
		t.Fatal(err)
	}
	html := string(got)
	for _, want := range []string{"<h1>Title</h1>", "<p>Body</p>", "data-page-content"} {
		if !strings.Contains(html, want) {
			t.Fatalf("marker-based content preview missing %q: %s", want, html)
		}
	}
	for _, unwanted := range []string{"site-header", "Footer", "Navigation"} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("marker-based content preview retained %q: %s", unwanted, html)
		}
	}
	if strings.Contains(html, "window.contentScript=1") {
		t.Fatalf("marker-based content preview retained an arbitrary content script: %s", html)
	}
}

func TestRewritePreviewHTMLPreservesThemedShellAndRewritesResources(t *testing.T) {
	fullPage := []byte(`<!doctype html><html><head><link rel="stylesheet" href="/docs/theme.css"></head><body><header class="site-header">Navigation</header><main><div data-page-content><p>Body</p><img src="/docs/assets/image.png"></div></main></body></html>`)
	html := string(rewritePreviewHTML(fullPage, "/_oxpio/preview/token"))
	for _, want := range []string{"site-header", "data-page-content", "/_oxpio/preview/token/docs/theme.css", "/_oxpio/preview/token/docs/assets/image.png"} {
		if !strings.Contains(html, want) {
			t.Fatalf("preview missing %q: %s", want, html)
		}
	}
}
