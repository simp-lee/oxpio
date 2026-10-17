package vault

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScanCollectsSourcesWithExplicitAttachmentDirectory(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":"must-not-be-read"}`)
	writeVaultFile(t, vaultPath, ".obsidian/workspace.json", `{}`)
	writeVaultFile(t, vaultPath, ".oxpio/unknown.json", `{"secret":"must-not-be-read"}`)
	writeVaultFile(t, vaultPath, "notes/alpha.md", "# Alpha")
	writeVaultFile(t, vaultPath, "notes/Guide.MD", "# Guide")
	writeVaultFile(t, vaultPath, "assets/uploads/photo.jpg", "jpg")
	writeVaultFile(t, vaultPath, "scripts/build.js", "console.log('ok')")
	writeVaultFile(t, vaultPath, ".hidden/private.md", "# Hidden")
	writeVaultFile(t, vaultPath, "node_modules/pkg/readme.md", "# Ignore")

	got, err := ScanWithOptions(vaultPath, ScanOptions{AttachmentDirectory: "assets/uploads"})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if got.AttachmentDirectory != "assets/uploads" || got.AttachmentFolderPath != "assets/uploads" {
		t.Fatalf("attachment directory = %#v", got)
	}
	wantMarkdown := []string{".hidden/private.md", "notes/Guide.MD", "notes/alpha.md"}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}
	if got.LookupResourcePath(".obsidian/workspace.json").Path != "" || got.LookupResourcePath(".oxpio/unknown.json").Path != "" {
		t.Fatal("management files were included as resources")
	}
	if got.LookupResourcePath("assets/uploads/photo.jpg").Path == "" {
		t.Fatal("explicit attachment resource was not scanned")
	}
}

func TestScanRejectsUnpublishedAndUnreferencedPortablePathViolations(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "private/CON.md", "# hidden")
	writeVaultFile(t, vaultPath, "unused/Cafe\u0301.png", "one")
	writeVaultFile(t, vaultPath, "unused/Café.png", "two")
	_, err := Scan(vaultPath)
	if err == nil {
		t.Fatal("Scan() error = nil, want portable path/collision error")
	}
	if !strings.Contains(err.Error(), "non-portable path segment") && !strings.Contains(err.Error(), "collide on portable filesystems") {
		t.Fatalf("Scan() error = %v, want portable path/collision error", err)
	}
}

func TestScanExcludesResolvedOutputAndReservedDirectories(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	writeVaultFile(t, vaultPath, "notes/kept.md", "# Kept")
	writeVaultFile(t, vaultPath, "public/generated.md", "# Generated")
	writeVaultFile(t, vaultPath, ".obsidian/workspace.json", "{}")
	writeVaultFile(t, vaultPath, ".oxpio/cache/state", "cache")
	writeVaultFile(t, vaultPath, "node_modules/pkg/index.md", "# Dependency")

	got, err := ScanWithOptions(vaultPath, ScanOptions{OutputPath: outputPath})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if !reflect.DeepEqual(got.MarkdownFiles, []string{"notes/kept.md"}) {
		t.Fatalf("MarkdownFiles = %#v", got.MarkdownFiles)
	}
}

func TestScanNormalizesOverlayPathsAndExcludesOutput(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	got, err := ScanWithOptions(vaultPath, ScanOptions{
		OutputPath: outputPath,
		OverlayMarkdown: map[string][]byte{
			` notes/draft.md`:    []byte("# Leading Draft"),
			`notes\\draft.md`:    []byte("# Draft"),
			"public/injected.md": []byte("# Injected"),
			"publicity.md":       []byte("# Publicity"),
			"oxpio.yaml":         []byte("# Config"),
		},
		OverlayDeleted: map[string]bool{"notes/deleted.md": true, "public/deleted.md": true},
	})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if !reflect.DeepEqual(got.MarkdownFiles, []string{" notes/draft.md", "notes/draft.md", "publicity.md"}) {
		t.Fatalf("MarkdownFiles = %#v", got.MarkdownFiles)
	}
	if string(got.OverlayMarkdown[" notes/draft.md"]) != "# Leading Draft" || string(got.OverlayMarkdown["notes/draft.md"]) != "# Draft" || got.OverlayDeleted["notes/deleted.md"] != true {
		t.Fatal("overlay paths were not normalized")
	}
	if _, ok := got.OverlayMarkdown["public/injected.md"]; ok {
		t.Fatal("output overlay was retained")
	}
}

func TestScanRejectsOverlayTraversalAndIgnoresObsidianChanges(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	before, err := Scan(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":"different"}`)
	writeVaultFile(t, vaultPath, ".obsidian/changed", "changed")
	after, err := Scan(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.MarkdownFiles, after.MarkdownFiles) || !reflect.DeepEqual(before.ResourceFiles, after.ResourceFiles) {
		t.Fatal(".obsidian changed the scan result")
	}
	if _, err := ScanWithOptions(vaultPath, ScanOptions{OverlayMarkdown: map[string][]byte{"../escape.md": []byte("# Unsafe")}}); err == nil {
		t.Fatal("traversal overlay was accepted")
	}
}

func TestScanRejectsNonReservedSymlinkInputs(t *testing.T) {
	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "target.md", "# target")
	if err := os.Symlink(filepath.Join(vaultPath, "target.md"), filepath.Join(vaultPath, "alias.md")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := Scan(vaultPath); err == nil || !strings.Contains(err.Error(), "must not contain symbolic links") {
		t.Fatalf("Scan() error = %v, want symlink boundary error", err)
	}
}

func TestScanExcludesOutputThroughSymlinkAlias(t *testing.T) {
	t.Parallel()
	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	if err := os.Mkdir(outputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outputPath, filepath.Join(vaultPath, "alias")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	got, err := ScanWithOptions(vaultPath, ScanOptions{OutputPath: outputPath, OverlayMarkdown: map[string][]byte{"alias/injected.md": []byte("# Injected")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MarkdownFiles) != 0 || len(got.OverlayMarkdown) != 0 {
		t.Fatalf("output symlink alias was not excluded: %#v", got)
	}
}

func writeVaultFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
