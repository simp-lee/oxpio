package build

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictBuildPublishesURLPathsForStandardFileServing(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Assets\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\nbanner: images/Café Banner.png\nbannerAlt: Banner\n---\nHome\n")
	writeStrictFile(t, vault, "Start Here.md", "---\ntitle: Start Here\npublish: true\ntype: page\n---\nPage served from a decoded directory.\n")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "test", "testdata", "e2e", "feature-vault", "images", "cover.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "images", "Café Banner.png"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "site")
	result, err := BuildWithOptions(vault, output, Options{})
	if err != nil {
		t.Fatal(err)
	}
	planned := result.Assets["images/Café Banner.png"]
	if planned == nil || !strings.Contains(planned.DstPath, "caf%C3%A9%20banner.") {
		t.Fatalf("asset URL path = %#v, want escaped space and Unicode", planned)
	}
	decodedAssetPath, err := url.PathUnescape(planned.DstPath)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(decodedAssetPath))); err != nil || !bytes.Equal(data, fixture) {
		t.Fatalf("decoded asset file = %q, err=%v", decodedAssetPath, err)
	}
	if _, err := os.Stat(filepath.Join(output, "Start Here", "index.html")); err != nil {
		t.Fatalf("decoded page directory missing: %v", err)
	}
	page := readBuildOutputFile(t, output, "index.html")
	if !strings.Contains(string(page), "/"+planned.DstPath) {
		t.Fatalf("page did not retain encoded asset URL:\n%s", page)
	}

	server := httptest.NewServer(http.FileServer(http.Dir(output)))
	t.Cleanup(server.Close)
	for _, tt := range []struct {
		urlPath string
		want    []byte
	}{
		{urlPath: "/Start%20Here/", want: []byte("Page served from a decoded directory.")},
		{urlPath: "/" + planned.DstPath, want: fixture},
	} {
		response, err := server.Client().Get(server.URL + tt.urlPath)
		if err != nil {
			t.Fatalf("GET %s: %v", tt.urlPath, err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read GET %s: %v, close: %v", tt.urlPath, readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200; body=%q", tt.urlPath, response.StatusCode, body)
		}
		if !bytes.Contains(body, tt.want) {
			t.Fatalf("GET %s body did not contain expected content", tt.urlPath)
		}
	}
}
