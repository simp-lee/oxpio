package asset

import (
	"fmt"
	"strings"
	"testing"
)

func TestRewriteCSSURLs(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{`a { background: URL( logo.png ); }`, `a { background: url("logo.hash.png"); }`},
		{`a { background: url("logo.png" /* keep */); color: red; }`, `a { background: url("logo.hash.png"); color: red; }`},
		{`a { background: url("logo.png" /* ) keep */); color: red; }`, `a { background: url("logo.hash.png"); color: red; }`},
		{`a { background: u\72l("logo.png" /* ) keep */); color: red; }`, `a { background: url("logo.hash.png"); color: red; }`},
		{`a { mask: url("#local" /* ) keep */); color: red; }`, `a { mask: url("#local" /* ) keep */); color: red; }`},
		{`@import /* comment */ 'logo.png' screen;`, `@import /* comment */ "logo.hash.png" screen;`},
		{`@import url("logo.png");`, `@import url("logo.hash.png");`},
		{`a { background: url(lo\67 o.png); }`, `a { background: url("logo.hash.png"); }`},
		{`a { background: u\72l("logo.png"); }`, `a { background: url("logo.hash.png"); }`},
		{`a { background: \75rl(logo.png); }`, `a { background: url("logo.hash.png"); }`},
		{`a { background: \75 rl(logo.png); }`, `a { background: url("logo.hash.png"); }`},
		{`a { background: \75rl(logo.png?x=/*); color: red; }`, `a { background: url("logo.hash.png?x=/*"); color: red; }`},
		{`a { background: u\72l('logo.png?x=)'); color: red; }`, `a { background: url("logo.hash.png?x=)"); color: red; }`},
		{"a { background: url(\"lo\\\r\ngo.png\"); }", `a { background: url("logo.hash.png"); }`},
		{`a { background: image-set('logo.png' type('image/png') 1x, url(logo.png) 2x); }`, `a { background: image-set("logo.hash.png" type('image/png') 1x, url("logo.hash.png") 2x); }`},
		{`/* url(missing.png) */ a::after { content: 'url(missing.png)'; }`, `/* url(missing.png) */ a::after { content: 'url(missing.png)'; }`},
		{`a { mask: url(#local); background: url('data:image/png;base64,AA=='); }`, `a { mask: url(#local); background: url('data:image/png;base64,AA=='); }`},
		{`a { background: url("" /* keep */); color: red; }`, `a { background: url("" /* keep */); color: red; }`},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := RewriteCSSURLs([]byte(test.input), func(raw string) (string, error) {
				if raw == "logo.png" || strings.HasPrefix(raw, "logo.png?") {
					return "logo.hash.png" + strings.TrimPrefix(raw, "logo.png"), nil
				}
				if raw == "" || raw == "#local" || strings.HasPrefix(raw, "data:") {
					return raw, nil
				}
				return "", fmt.Errorf("unexpected resource: %q", raw)
			})
			if err != nil || string(got) != test.want {
				t.Fatalf("RewriteCSSURLs = %s, %v; want %s", got, err, test.want)
			}
		})
	}
}

func TestRewriteCSSURLsRejectsMalformedURL(t *testing.T) {
	for _, input := range []string{
		`a { background: url("missing.png); }`,
		`a { background: url(missing.png; }`,
		`a { background: url(missing.png bad); }`,
	} {
		t.Run(input, func(t *testing.T) {
			resolved := false
			_, err := RewriteCSSURLs([]byte(input), func(raw string) (string, error) {
				resolved = true
				return raw, nil
			})
			if err == nil || !strings.Contains(err.Error(), "malformed CSS url() reference") {
				t.Fatalf("RewriteCSSURLs() error = %v, want malformed URL error", err)
			}
			if resolved {
				t.Fatal("malformed URL was passed to the resolver")
			}
		})
	}
}

func TestRewriteCSSURLsDecodesQuotedReferenceOnce(t *testing.T) {
	const input = `a { fill: url("\\23 gradient"); }`
	var reference string
	got, err := RewriteCSSURLs([]byte(input), func(raw string) (string, error) {
		reference = raw
		return raw, nil
	})
	if err != nil {
		t.Fatalf("RewriteCSSURLs() error = %v", err)
	}
	if string(got) != input {
		t.Fatalf("RewriteCSSURLs() = %q, want unchanged %q", got, input)
	}
	if reference != `\23 gradient` {
		t.Fatalf("resolved reference = %q, want one level of CSS unescaping", reference)
	}
}
