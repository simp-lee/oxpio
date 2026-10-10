package application

import (
	"path/filepath"
	"testing"
)

func TestOpenSnapshotRejectsOutputThatOverlapsASectionSource(t *testing.T) {
	vault := t.TempDir()
	writeApplicationFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeApplicationFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeApplicationFile(t, vault, "guide/_index.md", "---\ntitle: Guide\npublish: true\n---\n")
	if _, err := OpenSnapshot(vault, filepath.Join(vault, "guide")); err == nil {
		t.Fatal("OpenSnapshot accepted output overlapping a section source")
	}
}
