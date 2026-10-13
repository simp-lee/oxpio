package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildPreviewRendersUnpublishedOverlayWithoutChangingPublishState(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	write := func(name, content string) {
		t.Helper()
		absolute := filepath.Join(vault, name)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	write("_index.md", "---\ntitle: Home\npublish: true\n---\nHome\n")
	write("drafts/_index.md", "---\ntitle: Drafts\npublish: false\n---\nDrafts\n")

	result, err := BuildWithOptions(vault, output, Options{
		Strict:            true,
		Ephemeral:         true,
		SourceOutputPath:  output,
		PreviewSourcePath: "drafts/01-draft.md",
		SourceOverlay: map[string][]byte{
			"drafts/01-draft.md": []byte("---\ntitle: Draft\npublish: false\ntype: doc\n---\nDraft\n"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, entry := range result.Catalog.Entries {
		if entry.RelPath != "drafts/01-draft.md" {
			continue
		}
		found = true
		if entry.Route == "" || entry.EffectivePublish {
			t.Fatalf("preview catalog entry = %#v; want route and unpublished state", entry)
		}
	}
	if !found {
		t.Fatal("preview catalog omitted overlaid draft")
	}
	if _, err := os.Stat(filepath.Join(output, "drafts", "draft", "index.html")); err != nil {
		t.Fatalf("preview page was not rendered: %v", err)
	}
}
