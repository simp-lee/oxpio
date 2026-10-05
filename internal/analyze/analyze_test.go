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

func TestAnalyzeWithOutputExcludesFixedInputsWithinBoundary(t *testing.T) {
	tests := []struct {
		name       string
		outputPath string
		badInput   string
		content    string
	}{
		{name: "theme", outputPath: ".oxpio", badInput: ".oxpio/theme/unsupported.txt", content: "stale output"},
		{name: "nested theme asset", outputPath: ".oxpio/theme/assets/generated", badInput: ".oxpio/theme/assets/generated/bad.css", content: "body { background: url(missing.png); }"},
		{name: "Obsidian metadata", outputPath: ".obsidian", badInput: ".obsidian/app.json", content: "not JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vault := t.TempDir()
			writeAnalyzeFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
			writeAnalyzeFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
			writeAnalyzeFile(t, vault, test.badInput, test.content)

			result, err := AnalyzeWithOutput(vault, filepath.Join(vault, test.outputPath))
			if err != nil || len(result.Diagnostics) != 0 {
				t.Fatalf("AnalyzeWithOutput() error = %v; diagnostics = %#v", err, result.Diagnostics)
			}
		})
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
