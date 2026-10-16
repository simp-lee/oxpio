package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internaltheme "github.com/simp-lee/oxpio/internal/theme"
)

type themeContextTestFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Hash string `json:"sha256"`
	Size int    `json:"size"`
}

func TestThemeContextInventoriesEveryLocalTheme(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, defaultConfigFilename, "title: Site\nbaseURL: https://example.test/\ntheme: alpha\nnavigation: []\n")

	alpha, err := internaltheme.InitFiles("alpha")
	if err != nil {
		t.Fatal(err)
	}
	writeThemeContextFiles(t, vault, "alpha", alpha)

	beta := map[string][]byte{
		"theme.yaml":            []byte("apiVersion: 1\nname: beta\n"),
		"partials/example.html": []byte("{{define \"example\"}}beta{{end}}\n"),
		"assets/font.woff":      []byte("wOFFbeta"),
	}
	writeThemeContextFiles(t, vault, "beta", beta)
	customCSS := []byte("body { color: red; }\n")
	writeValidateFile(t, vault, "custom.css", string(customCSS))

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"theme", "context", "--vault", vault, "--format", "json"})
	if err != nil || stderr != "" {
		t.Fatalf("theme context = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode theme context envelope: %v (%q)", err, stdout)
	}
	var data struct {
		SelectedTheme string `json:"selectedTheme"`
		Theme         struct {
			Files []themeContextTestFile `json:"files"`
		} `json:"theme"`
		AvailableThemes []struct {
			ID    string                 `json:"id"`
			Files []themeContextTestFile `json:"files"`
		} `json:"availableThemes"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode theme context data: %v", err)
	}
	if data.SelectedTheme != "alpha" {
		t.Fatalf("selectedTheme = %q, want alpha", data.SelectedTheme)
	}
	assertThemeContextFiles(t, data.Theme.Files, expectedThemeContextFiles("alpha", alpha, customCSS))

	available := make(map[string][]themeContextTestFile)
	for _, item := range data.AvailableThemes {
		if item.ID != internaltheme.BaseID {
			available[item.ID] = item.Files
		}
	}
	if len(available) != 2 {
		t.Fatalf("local available themes = %#v, want alpha and beta", available)
	}
	assertThemeContextFiles(t, available["alpha"], expectedThemeContextFiles("alpha", alpha, customCSS))
	assertThemeContextFiles(t, available["beta"], map[string]themeContextTestFile{
		".oxpio/themes/beta/theme.yaml":            {Kind: "text", Hash: themeContextHash(beta["theme.yaml"]), Size: len(beta["theme.yaml"])},
		".oxpio/themes/beta/partials/example.html": {Kind: "text", Hash: themeContextHash(beta["partials/example.html"]), Size: len(beta["partials/example.html"])},
		".oxpio/themes/beta/assets/font.woff":      {Kind: "binary", Hash: themeContextHash(beta["assets/font.woff"]), Size: len(beta["assets/font.woff"])},
		"custom.css":                               {Kind: "text", Hash: themeContextHash(customCSS), Size: len(customCSS)},
	})
	if strings.Contains(stdout, vault) || strings.Contains(stdout, "wOFFbeta") {
		t.Fatalf("theme context leaked host path or binary content: %q", stdout)
	}
}

func TestThemeContextOmitsEmbeddedBaseFiles(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, defaultConfigFilename, "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	customCSS := []byte("body { color: red; }\n")
	writeValidateFile(t, vault, "custom.css", string(customCSS))

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"theme", "context", "--vault", vault, "--format", "json"})
	if err != nil || stderr != "" {
		t.Fatalf("theme context = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode theme context envelope: %v", err)
	}
	var data struct {
		Theme struct {
			Files []themeContextTestFile `json:"files"`
		} `json:"theme"`
		AvailableThemes []map[string]json.RawMessage `json:"availableThemes"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode theme context data: %v", err)
	}
	assertThemeContextFiles(t, data.Theme.Files, map[string]themeContextTestFile{
		"custom.css": {Kind: "text", Hash: themeContextHash(customCSS), Size: len(customCSS)},
	})
	for _, theme := range data.AvailableThemes {
		if string(theme["id"]) == `"base"` {
			if _, ok := theme["files"]; ok {
				t.Fatalf("embedded base context unexpectedly exposed files: %#v", theme)
			}
			return
		}
	}
	t.Fatal("embedded base theme was not listed")
}

func writeThemeContextFiles(t *testing.T, vault, id string, files map[string][]byte) {
	t.Helper()
	for relPath, data := range files {
		filename := filepath.Join(vault, ".oxpio", "themes", id, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func themeContextHash(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func expectedThemeContextFiles(id string, files map[string][]byte, customCSS []byte) map[string]themeContextTestFile {
	result := make(map[string]themeContextTestFile, len(files)+1)
	for relPath, data := range files {
		kind, err := internaltheme.FileKind(relPath)
		if err != nil {
			panic(err)
		}
		path := filepath.ToSlash(filepath.Join(".oxpio", "themes", id, relPath))
		result[path] = themeContextTestFile{Kind: kind, Hash: themeContextHash(data), Size: len(data)}
	}
	result["custom.css"] = themeContextTestFile{Kind: "text", Hash: themeContextHash(customCSS), Size: len(customCSS)}
	return result
}

func assertThemeContextFiles(t *testing.T, files []themeContextTestFile, want map[string]themeContextTestFile) {
	t.Helper()
	if len(files) != len(want) {
		t.Fatalf("theme context files = %#v, want %d files", files, len(want))
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		expected, ok := want[file.Path]
		if !ok || seen[file.Path] {
			t.Fatalf("unexpected or duplicate theme context file %#v", file)
		}
		seen[file.Path] = true
		if file.Kind != expected.Kind || file.Hash != expected.Hash || file.Size != expected.Size {
			t.Fatalf("theme context file %#v, want %#v", file, expected)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("theme context files omitted entries: got=%#v want=%#v", seen, want)
	}
}
