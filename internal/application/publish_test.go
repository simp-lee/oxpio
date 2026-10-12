package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/simp-lee/oxpio/internal/workspace"
)

func TestPublishCommitsFormalOutputWithCurrentInputFingerprint(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nBody\n")
	result, err := Publish(vault, output, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Published || len(result.Changes) == 0 {
		t.Fatalf("publish result = %#v, want committed output changes", result)
	}
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Check(vault); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDryRunIsEphemeralAndLeavesOutputUntouched(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nBody\n")

	result, err := Publish(vault, output, PublishOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.Published || result.Build == nil || len(result.Changes) == 0 {
		t.Fatalf("dry-run result = %#v, want an uncommitted build with candidate changes", result)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output stat error = %v, want absent", err)
	}
	if _, err := os.Stat(filepath.Join(vault, ".oxpio")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("durable state stat error = %v, want absent", err)
	}
}

func writePublishTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
