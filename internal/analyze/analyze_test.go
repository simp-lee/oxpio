package analyze

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeUsesStrictPlanWithoutWritingVault(t *testing.T) {
	vault := t.TempDir()
	writeAnalyzeFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeAnalyzeFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeAnalyzeFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\n")
	before, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Analyze(vault)
	if err != nil {
		t.Fatalf("Analyze() error = %v; diagnostics=%v", err, result.Diagnostics)
	}
	if result.Plan == nil || result.Plan.Plan == nil || len(result.Plan.Plan.Articles) != 1 {
		t.Fatalf("result = %#v", result)
	}
	after, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("vault entry count changed: %d -> %d", len(before), len(after))
	}
}

func TestAnalyzeDoesNotInferOutputBoundaryFromDirectoryNameOrMarker(t *testing.T) {
	vault := t.TempDir()
	writeAnalyzeFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeAnalyzeFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeAnalyzeFile(t, vault, "public/.oxpio-output", "managed by oxpio\n")
	writeAnalyzeFile(t, vault, "public/old.html", "generated output")
	writeAnalyzeFile(t, vault, "public/invalid.md", "not strict frontmatter")
	result, err := Analyze(vault)
	if err == nil || len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0].Message, "frontmatter is required") {
		t.Fatalf("Analyze() error = %v diagnostics = %#v, want named directory content diagnosed", err, result.Diagnostics)
	}
}

func TestAnalyzeReportsUnknownOXPIOManagementPathsWithoutReadingThem(t *testing.T) {
	vault := t.TempDir()
	writeAnalyzeFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeAnalyzeFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeAnalyzeFile(t, vault, ".oxpio/old-state.json", "not an input")

	result, err := Analyze(vault)
	if err != nil {
		t.Fatalf("Analyze() should preserve the plan with a warning: %v", err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Severity != "warning" || !strings.Contains(result.Diagnostics[0].Message, "unknown OXPIO management path") {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestAnalyzeReturnsStableSchemaDiagnostic(t *testing.T) {
	vault := t.TempDir()
	writeAnalyzeFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\ndefaultPublish: true\n")
	result, err := Analyze(vault)
	if err == nil || len(result.Diagnostics) != 1 {
		t.Fatalf("Analyze() error=%v diagnostics=%v", err, result.Diagnostics)
	}
	item := result.Diagnostics[0]
	if item.Severity != "error" || item.Kind != "schema" || !strings.Contains(item.Message, "defaultPublish") {
		t.Fatalf("diagnostic = %#v", item)
	}
}

func writeAnalyzeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
