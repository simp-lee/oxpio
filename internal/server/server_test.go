package server

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewValidatesOutputPathAndPort(t *testing.T) {
	t.Parallel()

	validOutputPath := t.TempDir()
	filePath := filepath.Join(t.TempDir(), "site.txt")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
	}

	tests := []struct {
		name       string
		outputPath string
		port       int
		wantErr    string
	}{
		{
			name:       "missing output path",
			outputPath: filepath.Join(t.TempDir(), "missing"),
			port:       DefaultPort,
			wantErr:    "does not exist",
		},
		{
			name:       "output path is not a directory",
			outputPath: filePath,
			port:       DefaultPort,
			wantErr:    "is not a directory",
		},
		{
			name:       "invalid negative port",
			outputPath: validOutputPath,
			port:       -1,
			wantErr:    "port must be between 1 and 65535",
		},
		{
			name:       "invalid high port",
			outputPath: validOutputPath,
			port:       65536,
			wantErr:    "port must be between 1 and 65535",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(tt.outputPath, tt.port)
			if err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestServerDetectsBasePathThroughOutputSymlink(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	writeServerTestFile(t, target, "index.html", `<html data-oxpio-base-path="/docs/"><body>home</body></html>`)
	outputPath := filepath.Join(parent, "public")
	if err := os.Symlink(target, outputPath); err != nil {
		t.Skipf("os.Symlink(%q, %q) unsupported: %v", target, outputPath, err)
	}

	srv, err := New(outputPath, 0)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := srv.BasePath(); got != "/docs/" {
		t.Fatalf("New().BasePath() = %q, want %q", got, "/docs/")
	}
}

func TestNewDefaultsZeroPortToDefaultPort(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()

	srv, err := New(outputPath, 0)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if srv.port != DefaultPort {
		t.Fatalf("New().port = %d, want %d", srv.port, DefaultPort)
	}
	if got := srv.Addr(); got != fmt.Sprintf(":%d", DefaultPort) {
		t.Fatalf("New().Addr() = %q, want %q", got, fmt.Sprintf(":%d", DefaultPort))
	}
}

func TestServerKeepsReservedEditControlUnavailable(t *testing.T) {
	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "home")
	writeServerTestFile(t, outputPath, "_oxpio/login/index.html", "must not be served")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/_oxpio/login/", nil)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("reserved edit route status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestServerServesCleanURLsAndCustom404(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")
	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>alpha page</body></html>")
	writeServerTestFile(t, outputPath, "404.html", "<html><body>custom missing page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	t.Run("slash form serves index file", func(t *testing.T) {
		t.Parallel()

		resp, err := ts.Client().Get(ts.URL + "/alpha/")
		if err != nil {
			t.Fatalf("GET /alpha/ error = %v", err)
		}
		defer closeServerResponseBody(t, resp)

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /alpha/ status = %d, want %d", resp.StatusCode, http.StatusOK)
		}

		body := readServerResponseBody(t, resp)
		if !strings.Contains(body, "alpha page") {
			t.Fatalf("GET /alpha/ body = %q, want alpha page content", body)
		}
	})

	t.Run("non-slash form redirects to clean URL", func(t *testing.T) {
		t.Parallel()

		client := &http.Client{
			Transport: ts.Client().Transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		resp, err := client.Get(ts.URL + "/alpha")
		if err != nil {
			t.Fatalf("GET /alpha error = %v", err)
		}
		defer closeServerResponseBody(t, resp)

		if resp.StatusCode != http.StatusMovedPermanently {
			t.Fatalf("GET /alpha status = %d, want %d", resp.StatusCode, http.StatusMovedPermanently)
		}
		if location := resp.Header.Get("Location"); location != "/alpha/" {
			t.Fatalf("GET /alpha Location = %q, want %q", location, "/alpha/")
		}
	})

	t.Run("missing path serves custom 404 page", func(t *testing.T) {
		t.Parallel()

		resp, err := ts.Client().Get(ts.URL + "/missing/path")
		if err != nil {
			t.Fatalf("GET /missing/path error = %v", err)
		}
		defer closeServerResponseBody(t, resp)

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /missing/path status = %d, want %d", resp.StatusCode, http.StatusNotFound)
		}

		body := readServerResponseBody(t, resp)
		if !strings.Contains(body, "custom missing page") {
			t.Fatalf("GET /missing/path body = %q, want custom 404 content", body)
		}
	})
}

func TestServerRedirectsNonCanonicalPathsToCleanURLs(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home page</body></html>")
	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>alpha page</body></html>")
	writeServerTestFile(t, outputPath, "style.css", "body { color: black; }")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := &http.Client{
		Transport: ts.Client().Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	tests := []struct {
		name         string
		requestPath  string
		wantLocation string
	}{
		{
			name:         "duplicate separators keep query",
			requestPath:  "/alpha//?view=1",
			wantLocation: "/alpha/?view=1",
		},
		{
			name:         "dot segment redirects to directory canonical path",
			requestPath:  "/./alpha/",
			wantLocation: "/alpha/",
		},
		{
			name:         "file trailing slash redirects to file path",
			requestPath:  "/style.css/",
			wantLocation: "/style.css",
		},
		{
			name:         "encoded unreserved segment redirects to canonical path",
			requestPath:  "/%61lpha/",
			wantLocation: "/alpha/",
		},
		{
			name:         "encoded dot segment redirects to canonical path",
			requestPath:  "/%2E/alpha/",
			wantLocation: "/alpha/",
		},
		{
			name:         "encoded root dot redirects to canonical root",
			requestPath:  "/%2E/",
			wantLocation: "/",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp, err := client.Get(ts.URL + tt.requestPath)
			if err != nil {
				t.Fatalf("GET %s error = %v", tt.requestPath, err)
			}
			defer closeServerResponseBody(t, resp)

			if resp.StatusCode != http.StatusMovedPermanently {
				t.Fatalf("GET %s status = %d, want %d", tt.requestPath, resp.StatusCode, http.StatusMovedPermanently)
			}
			if location := resp.Header.Get("Location"); location != tt.wantLocation {
				t.Fatalf("GET %s Location = %q, want %q", tt.requestPath, location, tt.wantLocation)
			}
		})
	}
}

func TestIsUnsafePreviewRequestPathRejectsWindowsDriveAndUNCPrefixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		requestPath string
		want        bool
	}{
		{name: "site root", requestPath: "/", want: false},
		{name: "normal clean url", requestPath: "/alpha/", want: false},
		{name: "redundant separators inside path", requestPath: "/alpha//", want: false},
		{name: "single segment double slash is not unc", requestPath: "//alpha/", want: false},
		{name: "windows drive prefix", requestPath: "/C:/alpha/", want: true},
		{name: "lowercase windows drive prefix", requestPath: "/c:/alpha/", want: true},
		{name: "windows drive with backslashes", requestPath: `\C:\alpha\`, want: true},
		{name: "unc slash prefix", requestPath: "//server/share/page/", want: true},
		{name: "unc backslash prefix", requestPath: `\\server\share\page\`, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isUnsafePreviewRequestPath(tt.requestPath); got != tt.want {
				t.Fatalf("isUnsafePreviewRequestPath(%q) = %v, want %v", tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestServerRejectsUNCStylePreviewRequestsBeforeCanonicalization(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "server/share/page/index.html", "<html><body>trap page</body></html>")
	writeServerTestFile(t, outputPath, "404.html", "<html><body>custom missing page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	client := &http.Client{
		Transport: ts.Client().Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(ts.URL + "//server/share/page/")
	if err != nil {
		t.Fatalf("GET //server/share/page/ error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET //server/share/page/ status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if location := resp.Header.Get("Location"); location != "" {
		t.Fatalf("GET //server/share/page/ Location = %q, want empty redirect target", location)
	}
	if body := readServerResponseBody(t, resp); !strings.Contains(body, "custom missing page") {
		t.Fatalf("GET //server/share/page/ body = %q, want custom 404 content", body)
	}
}

func TestServerRejectsSymlinkedPathsEscapingOutputRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		requestPaths []string
		wantLeakText string
		setup        func(t *testing.T, outputPath string, outsideRoot string)
	}{
		{
			name:         "file symlink",
			requestPaths: []string{"/escape.txt"},
			wantLeakText: "outside secret file",
			setup: func(t *testing.T, outputPath string, outsideRoot string) {
				t.Helper()

				targetPath := filepath.Join(outsideRoot, "secret.txt")
				if err := os.WriteFile(targetPath, []byte("outside secret file"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", targetPath, err)
				}

				linkPath := filepath.Join(outputPath, "escape.txt")
				if err := os.Symlink(targetPath, linkPath); err != nil {
					t.Skipf("os.Symlink(%q, %q) unsupported: %v", targetPath, linkPath, err)
				}
			},
		},
		{
			name:         "directory symlink",
			requestPaths: []string{"/escape-dir/"},
			wantLeakText: "outside directory index",
			setup: func(t *testing.T, outputPath string, outsideRoot string) {
				t.Helper()

				targetDir := filepath.Join(outsideRoot, "outside-dir")
				if err := os.MkdirAll(targetDir, 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v", targetDir, err)
				}
				writeServerTestFile(t, targetDir, "index.html", "<html><body>outside directory index</body></html>")

				linkPath := filepath.Join(outputPath, "escape-dir")
				if err := os.Symlink(targetDir, linkPath); err != nil {
					t.Skipf("os.Symlink(%q, %q) unsupported: %v", targetDir, linkPath, err)
				}
			},
		},
		{
			name:         "index file symlink",
			requestPaths: []string{"/escape-index/", "/escape-index/index.html"},
			wantLeakText: "outside index file",
			setup: func(t *testing.T, outputPath string, outsideRoot string) {
				t.Helper()

				targetPath := filepath.Join(outsideRoot, "index.html")
				if err := os.WriteFile(targetPath, []byte("outside index file"), 0o644); err != nil {
					t.Fatalf("os.WriteFile(%q) error = %v", targetPath, err)
				}
				indexDir := filepath.Join(outputPath, "escape-index")
				if err := os.MkdirAll(indexDir, 0o755); err != nil {
					t.Fatalf("os.MkdirAll(%q) error = %v", indexDir, err)
				}
				linkPath := filepath.Join(indexDir, "index.html")
				if err := os.Symlink(targetPath, linkPath); err != nil {
					t.Skipf("os.Symlink(%q, %q) unsupported: %v", targetPath, linkPath, err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			outputPath := t.TempDir()
			outsideRoot := t.TempDir()
			tt.setup(t, outputPath, outsideRoot)

			srv, err := New(outputPath, DefaultPort)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			ts := httptest.NewServer(srv)
			t.Cleanup(ts.Close)

			for _, requestPath := range tt.requestPaths {
				resp, err := ts.Client().Get(ts.URL + requestPath)
				if err != nil {
					t.Fatalf("GET %s error = %v", requestPath, err)
				}

				if resp.StatusCode != http.StatusNotFound {
					_ = resp.Body.Close()
					t.Fatalf("GET %s status = %d, want %d", requestPath, resp.StatusCode, http.StatusNotFound)
				}

				body := readServerResponseBody(t, resp)
				closeServerResponseBody(t, resp)
				if strings.Contains(body, tt.wantLeakText) {
					t.Fatalf("GET %s body = %q, do not want leaked target content", requestPath, body)
				}
			}
		})
	}
}

func TestServerSecureOutputOpenDoesNotFollowReplacementSymlink(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	outsideRoot := t.TempDir()
	safePath := filepath.Join(outputPath, "page.html")
	outsidePath := filepath.Join(outsideRoot, "secret.html")
	if err := os.WriteFile(safePath, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsidePath, []byte("outside secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := srv.resolveOutputFilePath("/page.html")
	if err != nil {
		t.Fatalf("resolveOutputFilePath() error = %v", err)
	}

	if err := os.Remove(safePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, safePath); err != nil {
		t.Skipf("os.Symlink(%q, %q) unsupported: %v", outsidePath, safePath, err)
	}

	file, err := srv.openOutputFile(resolvedPath)
	if err == nil {
		_ = file.Close()
		t.Fatal("openOutputFile() followed a replacement symlink")
	}
}

func TestServerCloseReleasesOutputParent(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "page.html", "page")
	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatal(err)
	}
	filePath, err := srv.resolveOutputFilePath("/page.html")
	if err != nil {
		t.Fatalf("resolveOutputFilePath() error = %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if file, err := srv.openOutputFile(filePath); err == nil {
		_ = file.Close()
		t.Fatal("openOutputFile() succeeded after Close()")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestServerNestedMissingPathUsesSiteRootForFallbackLinks(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")
	writeServerTestFile(t, outputPath, "style.css", "body { color: black; }")
	writeServerTestFile(t, outputPath, "404.html", `<!doctype html>
<html>
<head>
	<base href="/blog/">
  <link id="preview-style" rel="stylesheet" href="./style.css">
</head>
<body>
  <a id="home-link" href="./">Return home</a>
</body>
</html>`)

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/missing/path")
	if err != nil {
		t.Fatalf("GET /missing/path error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /missing/path status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	body := readServerResponseBody(t, resp)
	if got := strings.Count(strings.ToLower(body), "<base"); got != 1 {
		t.Fatalf("GET /missing/path base tag count = %d, want %d", got, 1)
	}
	baseHref := mustMatchServerTestAttribute(t, body, `<base[^>]*href="([^"]+)"`)
	styleHref := mustMatchServerTestAttribute(t, body, `<link[^>]*id="preview-style"[^>]*href="([^"]+)"`)
	homeHref := mustMatchServerTestAttribute(t, body, `<a[^>]*id="home-link"[^>]*href="([^"]+)"`)

	if baseHref != "/" {
		t.Fatalf("GET /missing/path base href = %q, want %q", baseHref, "/")
	}

	styleResp, err := ts.Client().Get(ts.URL + resolveServerPreviewHref(t, "/missing/path", baseHref, styleHref))
	if err != nil {
		t.Fatalf("GET resolved stylesheet error = %v", err)
	}
	defer closeServerResponseBody(t, styleResp)
	if styleResp.StatusCode != http.StatusOK {
		t.Fatalf("GET resolved stylesheet status = %d, want %d", styleResp.StatusCode, http.StatusOK)
	}
	if styleBody := readServerResponseBody(t, styleResp); styleBody != "body { color: black; }" {
		t.Fatalf("resolved stylesheet body = %q, want %q", styleBody, "body { color: black; }")
	}

	homeResp, err := ts.Client().Get(ts.URL + resolveServerPreviewHref(t, "/missing/path", baseHref, homeHref))
	if err != nil {
		t.Fatalf("GET resolved home link error = %v", err)
	}
	defer closeServerResponseBody(t, homeResp)
	if homeResp.StatusCode != http.StatusOK {
		t.Fatalf("GET resolved home link status = %d, want %d", homeResp.StatusCode, http.StatusOK)
	}
	if homeBody := readServerResponseBody(t, homeResp); !strings.Contains(homeBody, "home") {
		t.Fatalf("resolved home link body = %q, want home page content", homeBody)
	}
}

func TestServerNestedMissingPathResolvesPopoverPayloadFromInjectedBase(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")
	writeServerTestFile(t, outputPath, "404.html", `<!doctype html>
<html>
<head>
  <title>Missing</title>
</head>
<body>
  <aside data-popover-card data-popover-root="./_popover/"></aside>
  <a id="preview-link" href="./alpha/" data-popover-path="alpha">Alpha</a>
</body>
</html>`)

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/missing/path", nil)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ServeHTTP(/missing/path) status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	body := readServerResponseBody(t, resp)
	baseHref := mustMatchServerTestAttribute(t, body, `<base[^>]*href="([^"]+)"`)
	popoverRoot := mustMatchServerTestAttribute(t, body, `<aside[^>]*data-popover-root="([^"]+)"`)
	notePath := mustMatchServerTestAttribute(t, body, `<a[^>]*data-popover-path="([^"]+)"`)

	if got := resolveServerPreviewHref(t, "/missing/path", baseHref, popoverRoot+notePath+"/index.json"); got != "/_popover/alpha/index.json" {
		t.Fatalf("popover payload path = %q, want %q", got, "/_popover/alpha/index.json")
	}
}

func TestInjectPreviewBaseHrefReplacesOnlyActualBaseElement(t *testing.T) {
	t.Parallel()

	body := []byte(`<!doctype html>
<html>
<head>
  <!-- <base href="/comment/"> -->
  <script>const probe = "<base href=\"/script/\">";</script>
  <template><base href="/template/"></template>
  <base href="/blog/">
</head>
<body>missing</body>
</html>`)

	got := string(injectPreviewBaseHref(body))
	if !strings.Contains(got, `<!-- <base href="/comment/"> -->`) {
		t.Fatalf("injectPreviewBaseHref() comment content changed unexpectedly\n%s", got)
	}
	if !strings.Contains(got, `<script>const probe = "<base href=\"/script/\">";</script>`) {
		t.Fatalf("injectPreviewBaseHref() script content changed unexpectedly\n%s", got)
	}
	if !strings.Contains(got, `<template><base href="/template/"></template>`) {
		t.Fatalf("injectPreviewBaseHref() template content changed unexpectedly\n%s", got)
	}
	if !strings.Contains(got, `<base href="/">`) {
		t.Fatalf("injectPreviewBaseHref() missing preview root base tag\n%s", got)
	}
	if strings.Contains(got, `<base href="/blog/">`) {
		t.Fatalf("injectPreviewBaseHref() kept original base tag instead of rewriting it\n%s", got)
	}
}

func TestInjectPreviewBaseHrefEscapesDecodedBasePath(t *testing.T) {
	t.Parallel()

	body := []byte(`<html><head></head><body>missing</body></html>`)
	got := string(injectPreviewBaseHrefAt(body, "/a&copy;/"))
	if !strings.Contains(got, `<base href="/a&amp;copy;/">`) {
		t.Fatalf("injectPreviewBaseHrefAt() = %q, want escaped literal base path", got)
	}
}

func TestInjectPreviewBaseHrefRewritesImplicitHeadBaseElement(t *testing.T) {
	t.Parallel()

	body := []byte(`<!doctype html><html lang=en><meta charset=utf-8><title>Missing</title><base href=/blog/><link rel=stylesheet href=./style.css><body><a href=./>Retry</a></body></html>`)

	got := string(injectPreviewBaseHref(body))
	if count := strings.Count(strings.ToLower(got), "<base"); count != 1 {
		t.Fatalf("injectPreviewBaseHref() base tag count = %d, want %d\n%s", count, 1, got)
	}
	if !strings.Contains(got, `<base href="/">`) {
		t.Fatalf("injectPreviewBaseHref() missing preview root base tag for implicit head HTML\n%s", got)
	}
	if strings.Contains(got, `<base href=/blog/>`) || strings.Contains(got, `<base href="/blog/">`) {
		t.Fatalf("injectPreviewBaseHref() kept original implicit-head base tag instead of rewriting it\n%s", got)
	}
}

func TestServerReturnsDefault404WhenFallbackPageIsMissing(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, req)

	resp := recorder.Result()
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ServeHTTP(/missing) status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	body := readServerResponseBody(t, resp)
	if body != "404 page not found\n" {
		t.Fatalf("ServeHTTP(/missing) body = %q, want default http.NotFound response", body)
	}
}

func TestServerWatchModePreservesLiveReloadNamedFolderPage(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "_livereload/index.html", "<html><body>live reload folder page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	redirectClient := &http.Client{
		Transport: ts.Client().Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	redirectResp, err := redirectClient.Get(ts.URL + "/_livereload")
	if err != nil {
		t.Fatalf("GET /_livereload error = %v", err)
	}
	defer closeServerResponseBody(t, redirectResp)

	if redirectResp.StatusCode != http.StatusMovedPermanently {
		t.Fatalf("GET /_livereload status = %d, want %d", redirectResp.StatusCode, http.StatusMovedPermanently)
	}
	if location := redirectResp.Header.Get("Location"); location != "/_livereload/" {
		t.Fatalf("GET /_livereload Location = %q, want %q", location, "/_livereload/")
	}

	pageResp, err := ts.Client().Get(ts.URL + "/_livereload/")
	if err != nil {
		t.Fatalf("GET /_livereload/ error = %v", err)
	}
	defer closeServerResponseBody(t, pageResp)

	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /_livereload/ status = %d, want %d", pageResp.StatusCode, http.StatusOK)
	}

	pageBody := readServerResponseBody(t, pageResp)
	if !strings.Contains(pageBody, "live reload folder page") {
		t.Fatalf("GET /_livereload/ body = %q, want folder page content", pageBody)
	}
	if !strings.Contains(pageBody, "data-oxpio-livereload") {
		t.Fatalf("GET /_livereload/ body = %q, want live reload script injection", pageBody)
	}

	streamReq, err := http.NewRequest(http.MethodGet, ts.URL+liveReloadEndpoint+"?"+liveReloadQueryParam+"=1", nil)
	if err != nil {
		t.Fatalf("http.NewRequest(live reload stream) error = %v", err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")

	streamResp, err := ts.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("GET live reload stream error = %v", err)
	}
	defer closeServerResponseBody(t, streamResp)

	if contentType := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("live reload stream Content-Type = %q, want text/event-stream", contentType)
	}
}

func TestServerWatchModeInjectsLiveReloadInto404Fallback(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")
	writeServerTestFile(t, outputPath, "404.html", `<!doctype html>
<html>
<head>
	<base href="/blog/">
  <title>Missing</title>
</head>
<body>
  <a id="retry-link" href="./">Retry</a>
  <p>custom missing page</p>
</body>
</html>`)

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/missing/path")
	if err != nil {
		t.Fatalf("GET /missing/path error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /missing/path status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	body := readServerResponseBody(t, resp)
	if !strings.Contains(body, "custom missing page") {
		t.Fatalf("GET /missing/path body = %q, want original 404 content", body)
	}
	if got := strings.Count(body, `<script data-oxpio-livereload>`); got != 1 {
		t.Fatalf("GET /missing/path live reload script count = %d, want %d", got, 1)
	}
	if got := strings.Count(strings.ToLower(body), "<base"); got != 1 {
		t.Fatalf("GET /missing/path base tag count = %d, want %d", got, 1)
	}

	baseHref := mustMatchServerTestAttribute(t, body, `<base[^>]*href="([^"]+)"`)
	homeHref := mustMatchServerTestAttribute(t, body, `<a[^>]*id="retry-link"[^>]*href="([^"]+)"`)

	if baseHref != "/" {
		t.Fatalf("GET /missing/path base href = %q, want %q", baseHref, "/")
	}

	homeResp, err := ts.Client().Get(ts.URL + resolveServerPreviewHref(t, "/missing/path", baseHref, homeHref))
	if err != nil {
		t.Fatalf("GET resolved retry link error = %v", err)
	}
	defer closeServerResponseBody(t, homeResp)
	if homeResp.StatusCode != http.StatusOK {
		t.Fatalf("GET resolved retry link status = %d, want %d", homeResp.StatusCode, http.StatusOK)
	}
	if homeBody := readServerResponseBody(t, homeResp); !strings.Contains(homeBody, "home") {
		t.Fatalf("resolved retry link body = %q, want home page content", homeBody)
	}
}

func TestServerStaticModeDoesNotInjectLiveReloadOrExposeEndpoint(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>alpha page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	htmlResp, err := ts.Client().Get(ts.URL + "/alpha/")
	if err != nil {
		t.Fatalf("GET /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, htmlResp)

	htmlBody := readServerResponseBody(t, htmlResp)
	if strings.Contains(htmlBody, "data-oxpio-livereload") {
		t.Fatalf("GET /alpha/ body = %q, do not want livereload injection in static mode", htmlBody)
	}

	liveReloadResp, err := ts.Client().Get(ts.URL + "/_livereload")
	if err != nil {
		t.Fatalf("GET /_livereload error = %v", err)
	}
	defer closeServerResponseBody(t, liveReloadResp)

	if liveReloadResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /_livereload status = %d, want %d in static mode", liveReloadResp.StatusCode, http.StatusNotFound)
	}
	if contentType := liveReloadResp.Header.Get("Content-Type"); strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("GET /_livereload Content-Type = %q, do not want SSE endpoint in static mode", contentType)
	}
}

func TestServerInjectsLiveReloadScriptIntoHTMLResponsesOnly(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")
	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>alpha page</body></html>")
	writeServerTestFile(t, outputPath, "style.css", "body { color: black; }")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	htmlResp, err := ts.Client().Get(ts.URL + "/alpha/")
	if err != nil {
		t.Fatalf("GET /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, htmlResp)

	htmlBody := readServerResponseBody(t, htmlResp)
	if !strings.Contains(htmlBody, "data-oxpio-livereload") {
		t.Fatalf("GET /alpha/ body = %q, want livereload script injection", htmlBody)
	}
	if acceptRanges := htmlResp.Header.Get("Accept-Ranges"); acceptRanges != "" {
		t.Fatalf("GET /alpha/ Accept-Ranges = %q, want empty after HTML rewrite", acceptRanges)
	}
	if contentRange := htmlResp.Header.Get("Content-Range"); contentRange != "" {
		t.Fatalf("GET /alpha/ Content-Range = %q, want empty after HTML rewrite", contentRange)
	}
	if cacheControl := htmlResp.Header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("GET /alpha/ Cache-Control = %q, want %q", cacheControl, "no-store")
	}
	onDiskHTML, err := os.ReadFile(filepath.Join(outputPath, "alpha", "index.html"))
	if err != nil {
		t.Fatalf("os.ReadFile(index.html) error = %v", err)
	}
	if strings.Contains(string(onDiskHTML), "data-oxpio-livereload") {
		t.Fatalf("served file on disk = %q, do not want injected livereload script persisted", string(onDiskHTML))
	}

	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>updated alpha page</body></html>")
	conditionalReq, err := http.NewRequest(http.MethodGet, ts.URL+"/alpha/", nil)
	if err != nil {
		t.Fatal(err)
	}
	conditionalReq.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	conditionalResp, err := ts.Client().Do(conditionalReq)
	if err != nil {
		t.Fatalf("conditional GET /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, conditionalResp)
	conditionalBody := readServerResponseBody(t, conditionalResp)
	if conditionalResp.StatusCode != http.StatusOK || !strings.Contains(conditionalBody, "updated alpha page") {
		t.Fatalf("conditional GET /alpha/ = %d %q, want fresh updated body", conditionalResp.StatusCode, conditionalBody)
	}

	cssResp, err := ts.Client().Get(ts.URL + "/style.css")
	if err != nil {
		t.Fatalf("GET /style.css error = %v", err)
	}
	defer closeServerResponseBody(t, cssResp)

	cssBody := readServerResponseBody(t, cssResp)
	if strings.Contains(cssBody, "data-oxpio-livereload") {
		t.Fatalf("GET /style.css body = %q, do not want livereload script in non-HTML response", cssBody)
	}
}

func TestServerWatchModeKeepsInjectedHTMLHeadAndGetHeadersAligned(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "alpha/index.html", "<html><body>alpha page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	getResp, err := ts.Client().Get(ts.URL + "/alpha/")
	if err != nil {
		t.Fatalf("GET /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, getResp)

	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /alpha/ status = %d, want %d", getResp.StatusCode, http.StatusOK)
	}
	getBody := readServerResponseBody(t, getResp)
	if !strings.Contains(getBody, "alpha page") {
		t.Fatalf("GET /alpha/ body = %q, want original page content", getBody)
	}
	if !strings.Contains(getBody, "data-oxpio-livereload") {
		t.Fatalf("GET /alpha/ body = %q, want live reload injection", getBody)
	}
	if got := getResp.Header.Get("Accept-Ranges"); got != "" {
		t.Fatalf("GET /alpha/ Accept-Ranges = %q, want empty after injection", got)
	}
	if got := getResp.Header.Get("Content-Range"); got != "" {
		t.Fatalf("GET /alpha/ Content-Range = %q, want empty after injection", got)
	}

	headReq, err := http.NewRequest(http.MethodHead, ts.URL+"/alpha/", nil)
	if err != nil {
		t.Fatalf("http.NewRequest(HEAD /alpha/) error = %v", err)
	}
	headResp, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatalf("HEAD /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, headResp)

	assertServerHeadMatchesInjectedGet(t, getResp, getBody, headResp)
}

func TestServerWatchModeKeepsCustom404HeadAndGetHeadersAligned(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "404.html", "<html><body>custom missing page</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	getResp, err := ts.Client().Get(ts.URL + "/missing/path")
	if err != nil {
		t.Fatalf("GET /missing/path error = %v", err)
	}
	defer closeServerResponseBody(t, getResp)

	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /missing/path status = %d, want %d", getResp.StatusCode, http.StatusNotFound)
	}
	getBody := readServerResponseBody(t, getResp)
	if !strings.Contains(getBody, "custom missing page") {
		t.Fatalf("GET /missing/path body = %q, want custom 404 content", getBody)
	}
	if !strings.Contains(getBody, "data-oxpio-livereload") {
		t.Fatalf("GET /missing/path body = %q, want live reload injection", getBody)
	}

	headReq, err := http.NewRequest(http.MethodHead, ts.URL+"/missing/path", nil)
	if err != nil {
		t.Fatalf("http.NewRequest(HEAD /missing/path) error = %v", err)
	}
	headResp, err := ts.Client().Do(headReq)
	if err != nil {
		t.Fatalf("HEAD /missing/path error = %v", err)
	}
	defer closeServerResponseBody(t, headResp)

	assertServerHeadMatchesInjectedGet(t, getResp, getBody, headResp)
}

func TestServerInjectsLiveReloadScriptWhenPageMentionsMarkerText(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "alpha/index.html", `<html><body><p>Example marker: data-oxpio-livereload</p></body></html>`)

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/alpha/")
	if err != nil {
		t.Fatalf("GET /alpha/ error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	body := readServerResponseBody(t, resp)
	if !strings.Contains(body, `<p>Example marker: data-oxpio-livereload</p>`) {
		t.Fatalf("GET /alpha/ body = %q, want original marker text preserved", body)
	}
	if !strings.Contains(body, `<script data-oxpio-livereload>`) {
		t.Fatalf("GET /alpha/ body = %q, want injected live-reload script", body)
	}
	if got := strings.Count(body, "data-oxpio-livereload"); got != 2 {
		t.Fatalf("GET /alpha/ marker occurrence count = %d, want %d after preserving text and injecting script", got, 2)
	}
}

func TestServerNonHTMLGetBypassesInjectedResponseBuffering(t *testing.T) {
	t.Parallel()

	srv := &Server{
		fileServer: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, buffered := w.(*bufferedResponseWriter); buffered {
				t.Fatal("serveOutput() wrapped non-HTML GET in bufferedResponseWriter")
			}

			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Accept-Ranges", "bytes")
			_, err := w.Write([]byte("png"))
			if err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}),
	}
	srv.EnableLiveReload()

	req := httptest.NewRequest(http.MethodGet, "/image.png", nil)
	recorder := httptest.NewRecorder()

	srv.serveOutput(recorder, req, "/image.png")

	resp := recorder.Result()
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serveOutput(/image.png) status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if contentType := resp.Header.Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("serveOutput(/image.png) Content-Type = %q, want %q", contentType, "image/png")
	}
	if acceptRanges := resp.Header.Get("Accept-Ranges"); acceptRanges != "bytes" {
		t.Fatalf("serveOutput(/image.png) Accept-Ranges = %q, want %q", acceptRanges, "bytes")
	}
	if body := readServerResponseBody(t, resp); body != "png" {
		t.Fatalf("serveOutput(/image.png) body = %q, want %q", body, "png")
	}
}

func TestServerRangeRequestsBypassHTMLInjection(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	html := "<html><body>alpha page</body></html>"
	writeServerTestFile(t, outputPath, "alpha/index.html", html)

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/alpha/index.html", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Range", "bytes=0-15")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("Do(range request) error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range GET /alpha/index.html status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}
	if contentRange := resp.Header.Get("Content-Range"); !strings.HasPrefix(contentRange, "bytes 0-15/") {
		t.Fatalf("range GET /alpha/index.html Content-Range = %q, want bytes 0-15/*", contentRange)
	}
	if acceptRanges := resp.Header.Get("Accept-Ranges"); acceptRanges == "" {
		t.Fatalf("range GET /alpha/index.html Accept-Ranges = %q, want non-empty passthrough header", acceptRanges)
	}

	body := readServerResponseBody(t, resp)
	if strings.Contains(body, "data-oxpio-livereload") {
		t.Fatalf("range GET /alpha/index.html body = %q, do not want livereload script injection", body)
	}
	if body != html[:16] {
		t.Fatalf("range GET /alpha/index.html body = %q, want %q", body, html[:16])
	}
}

func TestServerLiveReloadEndpointStreamsReloadEvent(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", "<html><body>home</body></html>")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	srv.EnableLiveReload()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+liveReloadEndpoint+"?"+liveReloadQueryParam+"=1", nil)
	if err != nil {
		t.Fatalf("http.NewRequest(live reload stream) error = %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET live reload stream error = %v", err)
	}
	defer closeServerResponseBody(t, resp)

	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", contentType)
	}

	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString(connected) error = %v", err)
	}
	if strings.TrimSpace(line) != ": connected" {
		t.Fatalf("first SSE line = %q, want %q", strings.TrimSpace(line), ": connected")
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("ReadString(blank separator) error = %v", err)
	}

	srv.NotifyReload()

	reloadLine := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				reloadLine <- "error: " + err.Error()
				return
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, ":") {
				continue
			}
			reloadLine <- trimmed
			return
		}
	}()

	select {
	case line := <-reloadLine:
		if line != "data: reload" {
			t.Fatalf("reload event line = %q, want %q", line, "data: reload")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reload SSE event")
	}
}

func TestServerServesGeneratedBasePath(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", `<html data-oxpio-base-path="/docs/"><body>home</body></html>`)
	writeServerTestFile(t, outputPath, "style.css", "body{}")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	for _, target := range []string{"/docs/", "/docs/style.css"} {
		resp, err := ts.Client().Get(ts.URL + target)
		if err != nil {
			t.Fatalf("GET %s error = %v", target, err)
		}
		if resp.StatusCode != http.StatusOK {
			closeServerResponseBody(t, resp)
			t.Fatalf("GET %s status = %d, want 200", target, resp.StatusCode)
		}
		closeServerResponseBody(t, resp)
	}
}

func TestServerServesHTMLEntityDecodedBasePath(t *testing.T) {
	t.Parallel()

	outputPath := t.TempDir()
	writeServerTestFile(t, outputPath, "index.html", `<html data-oxpio-base-path="/a&amp;b/"><body>home</body></html>`)
	writeServerTestFile(t, outputPath, "style.css", "body{}")

	srv, err := New(outputPath, DefaultPort)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := srv.currentBasePath(); got != "/a&b/" {
		t.Fatalf("currentBasePath() = %q, want %q", got, "/a&b/")
	}

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	for _, target := range []string{"/a&b/", "/a&b/style.css"} {
		resp, err := ts.Client().Get(ts.URL + target)
		if err != nil {
			t.Fatalf("GET %s error = %v", target, err)
		}
		if resp.StatusCode != http.StatusOK {
			closeServerResponseBody(t, resp)
			t.Fatalf("GET %s status = %d, want 200", target, resp.StatusCode)
		}
		closeServerResponseBody(t, resp)
	}
}

func writeServerTestFile(t *testing.T, root string, relPath string, contents string) {
	t.Helper()

	filePath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("os.MkdirAll(%q) error = %v", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, []byte(contents), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", filePath, err)
	}
}

func closeServerResponseBody(t *testing.T, resp *http.Response) {
	t.Helper()

	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		t.Errorf("resp.Body.Close() error = %v", err)
	}
}

func readServerResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll(response body) error = %v", err)
	}

	return string(body)
}

func mustMatchServerTestAttribute(t *testing.T, body string, pattern string) string {
	t.Helper()

	matches := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(matches) != 2 {
		t.Fatalf("response body did not match %q", pattern)
	}

	return matches[1]
}

func resolveServerPreviewHref(t *testing.T, requestPath string, baseHref string, rawHref string) string {
	t.Helper()

	documentURL := &url.URL{Scheme: "http", Host: "preview.test", Path: requestPath}
	baseURL := documentURL
	if strings.TrimSpace(baseHref) != "" {
		parsedBase, err := url.Parse(baseHref)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", baseHref, err)
		}
		baseURL = documentURL.ResolveReference(parsedBase)
	}

	parsedHref, err := url.Parse(rawHref)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", rawHref, err)
	}

	return baseURL.ResolveReference(parsedHref).EscapedPath()
}

func assertServerHeadMatchesInjectedGet(t *testing.T, getResp *http.Response, getBody string, headResp *http.Response) {
	t.Helper()

	if getResp == nil {
		t.Fatal("getResp = nil, want GET response")
	}
	if headResp == nil {
		t.Fatal("headResp = nil, want HEAD response")
	}
	if headResp.StatusCode != getResp.StatusCode {
		t.Fatalf("HEAD status = %d, want GET status %d", headResp.StatusCode, getResp.StatusCode)
	}
	if got := getResp.Header.Get("Content-Length"); got != fmt.Sprint(len(getBody)) {
		t.Fatalf("GET Content-Length = %q, want %q", got, fmt.Sprint(len(getBody)))
	}
	if body := readServerResponseBody(t, headResp); body != "" {
		t.Fatalf("HEAD body = %q, want empty body", body)
	}

	for _, headerName := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "Content-Range"} {
		if got, want := headResp.Header.Get(headerName), getResp.Header.Get(headerName); got != want {
			t.Fatalf("HEAD %s = %q, want GET %s %q", headerName, got, headerName, want)
		}
	}
}
