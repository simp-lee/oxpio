package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
)

func TestStrictBuildUsesEmbeddedThemeWhenNoLocalThemeIsSelected(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	result, err := internalanalyze.AnalyzeWithOutput(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	if internalanalyze.Failure(result.Diagnostics) != nil {
		t.Fatalf("embedded theme diagnostics = %#v", result.Diagnostics)
	}
}

func TestUnknownManagementDirectoryIsIgnoredButBlocksValidation(t *testing.T) {
	vault := t.TempDir()
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeStrictFile(t, vault, ".oxpio/legacy/old.txt", "legacy")
	result, err := internalanalyze.AnalyzeWithOutput(vault, filepath.Join(vault, "public"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "unknown OXPIO management path") {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(vault, ".oxpio", "legacy", "old.txt")); err != nil {
		t.Fatal(err)
	}
	if internalanalyze.Failure(result.Diagnostics) == nil {
		t.Fatal("warning did not block validation")
	}
}
