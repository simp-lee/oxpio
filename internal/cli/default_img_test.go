package cli

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalserver "github.com/simp-lee/oxpio/internal/server"
)

func TestCLIBuildPublishesDefaultImageAndServesLocalAsset(t *testing.T) {
	imageData := defaultImagePNG(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	defer remote.Close()

	tests := []struct {
		name       string
		defaultImg string
		wantLocal  bool
	}{
		{name: "hosted", defaultImg: remote.URL + "/card.png"},
		{name: "local", defaultImg: "images/default.png", wantLocal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vault := t.TempDir()
			output := filepath.Join(t.TempDir(), "public")
			writeValidateFile(t, vault, "oxpio.yaml", fmt.Sprintf("title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: %q\n", tt.defaultImg))
			writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
			if tt.wantLocal {
				writeValidateFile(t, vault, "images/default.png", string(imageData))
			}

			_, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"build", "--vault", vault, "--output", output})
			if err != nil {
				t.Fatalf("build error = %v; stderr=%q", err, stderr)
			}
			page := string(readCLIOutputFile(t, output, "index.html"))
			if !strings.Contains(page, `og:image`) {
				t.Fatalf("index page has no default image metadata: %s", page)
			}

			if tt.wantLocal {
				entries, globErr := filepath.Glob(filepath.Join(output, "assets", "default.*.png"))
				if globErr != nil || len(entries) != 1 {
					t.Fatalf("published default image = %v, error = %v; want one asset", entries, globErr)
				}
				assetURL := extractDefaultImageURL(t, page)
				assetPath, err := url.Parse(assetURL)
				if err != nil {
					t.Fatal(err)
				}
				if assetPath.Path != "/site/assets/"+filepath.Base(entries[0]) {
					t.Fatalf("default image URL path = %q, want %q", assetPath.Path, "/site/assets/"+filepath.Base(entries[0]))
				}

				preview, err := internalserver.New(output, 0)
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(preview)
				defer server.Close()
				response, err := http.Get(server.URL + assetPath.RequestURI())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusOK || !bytes.Equal(body, imageData) {
					t.Fatalf("GET %s = status %d, %q; want 200 and published image", assetPath.RequestURI(), response.StatusCode, body)
				}
			} else {
				if !strings.Contains(page, `og:image" content="`+tt.defaultImg) {
					t.Fatalf("hosted default image URL missing from page: %s", page)
				}
				response, err := http.Get(tt.defaultImg)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = response.Body.Close() }()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("GET hosted default image = status %d, want 200", response.StatusCode)
				}
			}
		})
	}
}

func TestCLIFailsDefaultImageBuildAndPreservesPublishedOutput(t *testing.T) {
	imageData := defaultImagePNG(t)
	tests := []struct {
		name   string
		mutate func(t *testing.T, vault string)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, vault string) {
				writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/missing.png\n")
			},
		},
		{
			name: "directory",
			mutate: func(t *testing.T, vault string) {
				if err := os.Remove(filepath.Join(vault, "images", "default.png")); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(vault, "images", "default.png"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/default.png\n")
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, vault string) {
				if err := os.Remove(filepath.Join(vault, "images", "default.png")); err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "default.png")
				if err := os.WriteFile(outside, imageData, 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(vault, "images"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(vault, "images", "default.png")); err != nil {
					t.Fatal(err)
				}
				writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/default.png\n")
			},
		},
		{
			name: "undecodable",
			mutate: func(t *testing.T, vault string) {
				writeValidateFile(t, vault, "images/default.png", "not an image")
				writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/default.png\n")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vault := t.TempDir()
			outputRoot := t.TempDir()
			output := filepath.Join(outputRoot, "public")
			writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/default.png\n")
			writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nPublished baseline\n")
			writeValidateFile(t, vault, "images/default.png", string(imageData))
			if _, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"build", "--vault", vault, "--output", output}); err != nil {
				t.Fatalf("baseline build error = %v; stderr=%q", err, stderr)
			}
			before := snapshotCLIOutput(t, output)

			tt.mutate(t, vault)
			_, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"build", "--vault", vault, "--output", output})
			if err == nil || (!strings.Contains(err.Error(), "defaultImg") && !strings.Contains(stderr, "defaultImg")) {
				t.Fatalf("build error = %v; stderr=%q, want defaultImg failure", err, stderr)
			}
			assertCLIOutputUnchanged(t, output, before, "defaultImg failure")
			assertNoCLITransactionResidue(t, outputRoot, output)
		})
	}
}

func defaultImagePNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func extractDefaultImageURL(t *testing.T, page string) string {
	t.Helper()
	marker := `og:image" content=`
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("page has no Open Graph image: %s", page)
	}
	value := page[start+len(marker):]
	if len(value) == 0 {
		t.Fatalf("Open Graph image metadata has no value: %s", page)
	}
	if value[0] == '"' {
		end := strings.IndexByte(value[1:], '"')
		if end < 0 {
			t.Fatalf("Open Graph image metadata is unterminated: %s", value)
		}
		return value[1 : end+1]
	}
	return strings.Fields(value)[0]
}
