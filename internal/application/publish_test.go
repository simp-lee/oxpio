package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestPublishRepeatedCustomOutputHasNoChanges(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "published-site")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nBody\n")
	first, err := Publish(vault, output, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || !first.Published || len(first.Changes) == 0 {
		t.Fatalf("first custom publish = %#v", first)
	}
	second, err := Publish(vault, output, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || !second.Published || len(second.Changes) != 0 {
		t.Fatalf("repeated custom publish = %#v, want no output changes", second)
	}
	if descriptor := OutputDescriptor(vault, output); descriptor["kind"] != "external" || descriptor["path"] != "<external-output>" {
		t.Fatalf("custom output descriptor = %#v", descriptor)
	}
}

func TestPublishCacheFailureAfterOutputCommitIsOnlyPostCommitWarning(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	writePublishTestFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writePublishTestFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nBody\n")
	if err := os.MkdirAll(filepath.Join(vault, ".oxpio"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the cache parent unusable so the failure happens only after the
	// publisher has exchanged the formal output.
	if err := os.WriteFile(filepath.Join(vault, ".oxpio", "cache"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Publish(vault, output, PublishOptions{})
	if err != nil {
		t.Fatalf("publish error = %v, want committed output with warning", err)
	}
	if result == nil || !result.Published || result.Build == nil || result.Build.OutputCleanupError == nil {
		t.Fatalf("publish result = %#v, want published post-commit warning", result)
	}
	if !strings.Contains(result.Build.OutputCleanupError.Error(), "strict cache manifest") {
		t.Fatalf("post-commit warning = %v, want cache manifest failure", result.Build.OutputCleanupError)
	}
	if _, err := os.Stat(filepath.Join(output, "index.html")); err != nil {
		t.Fatalf("committed output missing: %v", err)
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
