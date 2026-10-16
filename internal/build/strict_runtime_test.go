package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/simp-lee/oxpio/internal/render"
)

func TestStrictBuildPublishesOnlyRequiredFeatureRuntimeAssets(t *testing.T) {
	sharedRuntime, err := render.SharedRuntimeOutputPath()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		body        string
		wantMath    bool
		wantMermaid bool
	}{
		{name: "no feature", body: "Home\n"},
		{name: "math", body: "Formula: $x^2$.\n", wantMath: true},
		{name: "mermaid", body: "```mermaid\ngraph TD\n  A --> B\n```\n", wantMermaid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vault := t.TempDir()
			output := filepath.Join(t.TempDir(), "site")
			writeStrictFile(t, vault, "oxpio.yaml", "title: Runtime\nbaseURL: https://example.test/\nnavigation: []\n")
			writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n"+test.body)

			if _, err := BuildWithOptions(vault, output, Options{}); err != nil {
				t.Fatal(err)
			}

			for _, relPath := range []string{"style.css", sharedRuntime, render.LogoOutputPath()} {
				assertRuntimeFile(t, output, relPath, true)
			}
			for _, relPath := range []string{
				"assets/oxpio-runtime/katex.min.css",
				"assets/oxpio-runtime/katex.min.js",
				"assets/oxpio-runtime/auto-render.min.js",
				"assets/oxpio-runtime/fonts/KaTeX_Main-Regular.woff2",
			} {
				assertRuntimeFile(t, output, relPath, test.wantMath)
			}
			assertRuntimeFile(t, output, "assets/oxpio-runtime/mermaid.min.js", test.wantMermaid)
		})
	}
}

func assertRuntimeFile(t *testing.T, root, relPath string, want bool) {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(relPath)))
	if want && err != nil {
		t.Fatalf("runtime asset %q is missing: %v", relPath, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("runtime asset %q exists, want it omitted (err=%v)", relPath, err)
	}
}
