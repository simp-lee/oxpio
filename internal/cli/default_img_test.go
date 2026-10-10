package cli

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIsNotACompatibilityAliasForPublish(t *testing.T) {
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"build"})
	if err == nil || !strings.Contains(err.Error(), `unknown command "build"`) {
		t.Fatalf("build error = %v, want unknown command", err)
	}
}

func TestPublishPreservesHostedDefaultImageURL(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\ndefaultImg: https://cdn.example.test/card.png\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err != nil {
		t.Fatal(err)
	}
	page := string(readCLIOutputFile(t, output, "index.html"))
	if !strings.Contains(page, `og:image" content="https://cdn.example.test/card.png`) {
		t.Fatalf("hosted default image missing: %s", page)
	}
}

func TestPublishPublishesLocalDefaultImage(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/site/\nnavigation: []\ndefaultImg: images/default.png\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeValidateFile(t, vault, "images/default.png", imageData.String())
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"publish", "--vault", vault, "--output", output}); err != nil {
		t.Fatal(err)
	}
	page := string(readCLIOutputFile(t, output, "index.html"))
	if !strings.Contains(page, "og:image") {
		t.Fatalf("index page has no default image metadata: %s", page)
	}
	entries, err := filepath.Glob(filepath.Join(output, "assets", "default.*.png"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("default image outputs = %v, error=%v", entries, err)
	}
	published, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(published, imageData.Bytes()) {
		t.Fatal("published default image bytes changed")
	}
}
