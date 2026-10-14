package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internaldiag "github.com/simp-lee/oxpio/internal/diag"
)

func TestEditorArticleMutationChecksHiddenSectionReferences(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeMutationS11File(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeMutationS11File(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeMutationS11File(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	writeMutationS11File(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n[Target](../target.md)\n")
	writeMutationS11File(t, vault, "target.md", "---\ntitle: Target\npublish: true\ntype: doc\n---\nTarget\n")

	err := ValidateEditorArticleMove(vault, output, "target.md", "docs/target.md")
	assertMutationS11ReferenceConflict(t, err, "private/_index.md")
	assertMutationS11ReferenceConflict(t, ValidateEditorArticleDelete(vault, output, "target.md"), "private/_index.md")
}

func TestAgentMutationRedactsHiddenReferenceConflicts(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	writeMutationS11File(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeMutationS11File(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	writeMutationS11File(t, vault, "docs/_index.md", "---\ntitle: Docs\npublish: true\n---\nDocs\n")
	writeMutationS11File(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n[Target](../target.md)\n")
	target := "---\ntitle: Target\npublish: true\ntype: doc\n---\nTarget\n"
	writeMutationS11File(t, vault, "target.md", target)
	request := SourceMutationRequest{Operation: MutationMove, Path: "target.md", Destination: "docs/target.md", ExpectedHash: hashBytes([]byte(target)), DestinationExpectedHash: AbsentHash}
	result, err := MutateSource(vault, output, request)
	if !errors.Is(err, ErrHiddenSource) {
		t.Fatalf("public move error = %v, want hidden-source permission error", err)
	}
	if result.Diagnostics != nil {
		t.Fatalf("public move result diagnostics = %#v, want empty result", result.Diagnostics)
	}
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) || len(mutationErr.AnalysisDiagnostics()) != 1 || mutationErr.AnalysisDiagnostics()[0].Location.Path != "" {
		t.Fatalf("public move diagnostics = %#v, want redacted hidden reference", mutationErr.AnalysisDiagnostics())
	}

	request.IncludeUnpublished = true
	_, err = MutateSource(vault, output, request)
	var detailedErr *MutationError
	if !errors.As(err, &detailedErr) || len(detailedErr.AnalysisDiagnostics()) != 1 || detailedErr.AnalysisDiagnostics()[0].Location.Path != "private/_index.md" {
		t.Fatalf("include-unpublished move diagnostics = %#v, want hidden source path", detailedErr.AnalysisDiagnostics())
	}
}

func assertMutationS11ReferenceConflict(t *testing.T, err error, source string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "source reference conflict") {
		t.Fatalf("error = %v, want source reference conflict", err)
	}
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) {
		t.Fatalf("error = %v, want MutationError", err)
	}
	diagnostics := mutationErr.AnalysisDiagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one reference diagnostic", diagnostics)
	}
	item := diagnostics[0]
	if item.Kind != internaldiag.KindReferenceConflict || item.Location.Path != source || item.Location.Line != 5 || item.Target != "target.md" {
		t.Fatalf("diagnostic = %#v, want source=%q line=5 target=%q", item, source, "target.md")
	}
}

func writeMutationS11File(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
