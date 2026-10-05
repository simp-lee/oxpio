package vault

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestScanCollectsMarkdownAndResourceCandidates(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":"assets/uploads"}`)
	writeVaultFile(t, vaultPath, ".obsidian/workspace.json", `{}`)
	writeVaultFile(t, vaultPath, "oxpio.yaml", "edit:\n  passwordHash: secret\n")
	writeVaultFile(t, vaultPath, ".git/config", "[remote \"origin\"]\nurl = https://user:secret@example.test/repo.git\n")
	writeVaultFile(t, vaultPath, ".oxpio-output-backup-stale/index.html", "stale output")
	writeVaultFile(t, vaultPath, ".public-oxpio-stage-stale/index.html", "stale stage")
	writeVaultFile(t, vaultPath, ".oxpio-displaced-stale", "stale source")
	writeVaultFile(t, vaultPath, "notes/alpha.md", "# Alpha")
	writeVaultFile(t, vaultPath, "notes/Guide.MD", "# Guide")
	writeVaultFile(t, vaultPath, "notes/diagram.png", "png")
	writeVaultFile(t, vaultPath, "assets/uploads/photo.jpg", "jpg")
	writeVaultFile(t, vaultPath, "assets/uploads/attachment.pdf", "pdf")
	writeVaultFile(t, vaultPath, "scripts/build.js", "console.log('ok')")
	writeVaultFile(t, vaultPath, ".hidden/private.md", "# Hidden")
	writeVaultFile(t, vaultPath, "notes/.draft.md", "# Draft")
	writeVaultFile(t, vaultPath, "node_modules/pkg/readme.md", "# Ignore")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if got.VaultPath != vaultPath {
		t.Fatalf("VaultPath = %q, want %q", got.VaultPath, vaultPath)
	}
	if got.AttachmentFolderPath != "assets/uploads" {
		t.Fatalf("AttachmentFolderPath = %q, want %q", got.AttachmentFolderPath, "assets/uploads")
	}

	wantMarkdown := []string{
		".hidden/private.md",
		"notes/.draft.md",
		"notes/Guide.MD",
		"notes/alpha.md",
	}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}

	wantResources := []string{
		"assets/uploads/attachment.pdf",
		"assets/uploads/photo.jpg",
		"notes/diagram.png",
		"scripts/build.js",
	}
	if !reflect.DeepEqual(got.ResourceFiles, wantResources) {
		t.Fatalf("ResourceFiles = %#v, want %#v", got.ResourceFiles, wantResources)
	}

	if got.LookupResourcePath("assets/uploads/photo.jpg").Path == "" {
		t.Fatal("HasResource(photo.jpg) = false, want true")
	}
	if got.LookupResourcePath(".obsidian/workspace.json").Path != "" {
		t.Fatal("HasResource(.obsidian/workspace.json) = true, want false")
	}
	if got.LookupResourcePath("node_modules/pkg/readme.md").Path != "" {
		t.Fatal("HasResource(node_modules/pkg/readme.md) = true, want false")
	}
	for _, reserved := range []string{"oxpio.yaml", ".git/config", ".oxpio-output-backup-stale/index.html", ".public-oxpio-stage-stale/index.html", ".oxpio-displaced-stale"} {
		if got.LookupResourcePath(reserved).Path != "" {
			t.Fatalf("LookupResourcePath(%q) = %q, want reserved path excluded", reserved, got.LookupResourcePath(reserved).Path)
		}
	}
	if got.LookupResourcePath(".hidden/private.md").Path != "" {
		t.Fatal("HasResource(.hidden/private.md) = true, want false")
	}
}

func TestScanExcludesResolvedOutputAndInternalInputDirectories(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	writeVaultFile(t, vaultPath, "notes/kept.md", "# Kept")
	writeVaultFile(t, vaultPath, "assets/kept.png", "kept")
	writeVaultFile(t, vaultPath, "public/generated.md", "# Generated")
	writeVaultFile(t, vaultPath, "public/assets/generated.png", "generated")
	writeVaultFile(t, vaultPath, ".oxpio/theme/theme.css", "body{}")
	writeVaultFile(t, vaultPath, ".obsidian/workspace.json", "{}")
	writeVaultFile(t, vaultPath, "node_modules/pkg/index.md", "# Dependency")
	writeVaultFile(t, vaultPath, ".hidden/private.md", "# Hidden")

	got, err := ScanWithOptions(vaultPath, ScanOptions{OutputPath: outputPath})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if !reflect.DeepEqual(got.MarkdownFiles, []string{".hidden/private.md", "notes/kept.md"}) {
		t.Fatalf("MarkdownFiles = %#v, want hidden and visible source files", got.MarkdownFiles)
	}
	if !reflect.DeepEqual(got.ResourceFiles, []string{"assets/kept.png"}) {
		t.Fatalf("ResourceFiles = %#v, want only assets/kept.png", got.ResourceFiles)
	}
}

func TestScanNormalizesOverlayPathsAndExcludesOutputBoundary(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	got, err := ScanWithOptions(vaultPath, ScanOptions{
		OutputPath: outputPath,
		OverlayMarkdown: map[string][]byte{
			`notes\draft.md`:     []byte("# Draft"),
			"public/injected.md": []byte("# Injected"),
			"publicity.md":       []byte("# Publicity"),
			"oxpio.yaml":         []byte("# Config"),
		},
		OverlayDeleted: map[string]bool{
			`notes\deleted.md`:  true,
			"public/deleted.md": true,
		},
	})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if !reflect.DeepEqual(got.MarkdownFiles, []string{"notes/draft.md", "publicity.md"}) {
		t.Fatalf("MarkdownFiles = %#v, want normalized visible overlays", got.MarkdownFiles)
	}
	if string(got.OverlayMarkdown["notes/draft.md"]) != "# Draft" {
		t.Fatalf("OverlayMarkdown[notes/draft.md] = %q, want normalized overlay bytes", got.OverlayMarkdown["notes/draft.md"])
	}
	for _, excluded := range []string{"public/injected.md", "oxpio.yaml"} {
		if _, ok := got.OverlayMarkdown[excluded]; ok {
			t.Fatalf("OverlayMarkdown contains excluded path %q", excluded)
		}
	}
	if _, ok := got.OverlayDeleted["notes/deleted.md"]; !ok {
		t.Fatal("OverlayDeleted is missing normalized deleted path")
	}
	if _, ok := got.OverlayDeleted["public/deleted.md"]; ok {
		t.Fatal("OverlayDeleted contains output path")
	}
}

func TestScanExcludesOverlayThroughOutputSymlinkAlias(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	outputPath := filepath.Join(vaultPath, "public")
	if err := os.Mkdir(outputPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outputPath, filepath.Join(vaultPath, "alias")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	got, err := ScanWithOptions(vaultPath, ScanOptions{
		OutputPath: outputPath,
		OverlayMarkdown: map[string][]byte{
			"alias/injected.md": []byte("# Injected"),
		},
	})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if len(got.MarkdownFiles) != 0 {
		t.Fatalf("MarkdownFiles = %#v, want output symlink alias excluded", got.MarkdownFiles)
	}
	if _, ok := got.OverlayMarkdown["alias/injected.md"]; ok {
		t.Fatal("OverlayMarkdown retained a path resolving inside the formal output")
	}
}

func TestScanExcludesOverlayThroughReservedSymlinkAlias(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "oxpio.yaml", "title: Protected\n")
	if err := os.Mkdir(filepath.Join(vaultPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(vaultPath, "oxpio.yaml"), filepath.Join(vaultPath, "credential.md")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(vaultPath, ".git"), filepath.Join(vaultPath, "git-alias")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	got, err := ScanWithOptions(vaultPath, ScanOptions{OverlayMarkdown: map[string][]byte{
		"credential.md":    []byte("# Credential"),
		"git-alias/config": []byte("# Git config"),
	}})
	if err != nil {
		t.Fatalf("ScanWithOptions() error = %v", err)
	}
	if len(got.OverlayMarkdown) != 0 {
		t.Fatalf("OverlayMarkdown = %#v, want reserved symlink aliases excluded", got.OverlayMarkdown)
	}
}

func TestScanRejectsOverlayThroughExternalSymlink(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	outsidePath := t.TempDir()
	if err := os.Symlink(outsidePath, filepath.Join(vaultPath, "escape")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	_, err := ScanWithOptions(vaultPath, ScanOptions{
		OverlayMarkdown: map[string][]byte{
			"escape/injected.md": []byte("# Injected"),
		},
	})
	if err == nil {
		t.Fatal("ScanWithOptions() accepted an overlay resolving outside the vault")
	}
}

func TestScanRejectsUnsafeOverlayPaths(t *testing.T) {
	t.Parallel()

	for _, relPath := range []string{
		"/absolute.md",
		`C:\absolute.md`,
		`\\server\share\injected.md`,
		"../outside.md",
		"notes/../outside.md",
	} {
		t.Run(relPath, func(t *testing.T) {
			vaultPath := t.TempDir()
			_, err := ScanWithOptions(vaultPath, ScanOptions{OverlayMarkdown: map[string][]byte{relPath: []byte("# Unsafe")}})
			if err == nil {
				t.Fatalf("ScanWithOptions(%q) error = nil, want unsafe overlay rejection", relPath)
			}
		})
	}
}

func TestScanLookupResourcePathRefusesCanonicalUnicodeCollisions(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	writeVaultFile(t, vaultPath, "images/Cafe\u0301 Chart.png", "png")
	writeVaultFile(t, vaultPath, "images/Café Chart.png", "png")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if exact := got.LookupResourcePath("images/Café Chart.png").Path; exact != "images/Café Chart.png" {
		t.Fatalf("ResolveResourcePath(exact) = %q, want %q", exact, "images/Café Chart.png")
	}

	lookup := got.LookupResourcePath("images/CAFÉ Chart.png")
	if lookup.Path != "" {
		t.Fatalf("LookupResourcePath().Path = %q, want empty for ambiguous canonical fallback", lookup.Path)
	}
	want := []string{"images/Cafe\u0301 Chart.png", "images/Café Chart.png"}
	if !reflect.DeepEqual(lookup.Ambiguous, want) {
		t.Fatalf("LookupResourcePath().Ambiguous = %#v, want %#v", lookup.Ambiguous, want)
	}
	if got := got.LookupResourcePath("images/CAFÉ Chart.png").Path; got != "" {
		t.Fatalf("ResolveResourcePath(ambiguous) = %q, want empty", got)
	}
}

func TestScanNormalizesAttachmentFolderPath(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":".\\assets\\images\\..\\uploads"}`)
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if got.AttachmentFolderPath != "assets/uploads" {
		t.Fatalf("AttachmentFolderPath = %q, want %q", got.AttachmentFolderPath, "assets/uploads")
	}
	if len(got.MarkdownFiles) != 1 || got.MarkdownFiles[0] != "notes/post.md" {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, []string{"notes/post.md"})
	}
}

func TestScanRejectsAbsoluteAttachmentFolderPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{name: "windows backslash absolute path", raw: `C:\attachments`},
		{name: "windows slash absolute path", raw: "C:/attachments"},
		{name: "unc backslash absolute path", raw: `\\server\share`},
		{name: "unc slash absolute path", raw: "//server/share"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vaultPath := t.TempDir()
			writeVaultFile(t, vaultPath, ".obsidian/app.json", fmt.Sprintf(`{"attachmentFolderPath":%q}`, tt.raw))
			writeVaultFile(t, vaultPath, "notes/post.md", "# Post")

			_, err := Scan(vaultPath)
			if err == nil {
				t.Fatalf("Scan() error = nil, want rejection for %q", tt.raw)
			}
			if !strings.Contains(err.Error(), "attachmentFolderPath must stay inside the vault") {
				t.Fatalf("Scan() error = %v, want vault boundary rejection", err)
			}
		})
	}
}

func TestScanSkipsTopLevelObsidianFileWhenNotDirectory(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, ".obsidian", "hidden file")
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if got.AttachmentFolderPath != "" {
		t.Fatalf("AttachmentFolderPath = %q, want empty", got.AttachmentFolderPath)
	}

	wantMarkdown := []string{"notes/post.md"}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}

	if len(got.ResourceFiles) != 0 {
		t.Fatalf("ResourceFiles = %#v, want empty", got.ResourceFiles)
	}

	if !scanContainsMarkdown(got, "notes/post.md") {
		t.Fatal("HasMarkdown(notes/post.md) = false, want true")
	}
	if got.LookupResourcePath(".obsidian").Path != "" {
		t.Fatal("HasResource(.obsidian) = true, want false")
	}
}

func TestScanPreservesLeadingSlashAttachmentFolderPath(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":"/assets/uploads"}`)
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if got.AttachmentFolderPath != "assets/uploads" {
		t.Fatalf("AttachmentFolderPath = %q, want %q", got.AttachmentFolderPath, "assets/uploads")
	}
}

func TestScanKeepsConfiguredAttachmentFolderInsideSkippedSubtreeExcluded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		attachmentFolderPath string
		attachmentResources  []string
		skippedMarkdown      []string
		skippedResources     []string
	}{
		{
			name:                 "hidden directory",
			attachmentFolderPath: ".hidden/attachments",
			attachmentResources: []string{
				".hidden/attachments/nested/diagram.svg",
				".hidden/attachments/photo.png",
			},
			skippedMarkdown: []string{
				".hidden/private.md",
			},
			skippedResources: []string{
				".hidden/other/skip.txt",
			},
		},
		{
			name:                 "node_modules subtree",
			attachmentFolderPath: "node_modules/pkg/assets",
			attachmentResources: []string{
				"node_modules/pkg/assets/photo.png",
			},
			skippedMarkdown: []string{
				"node_modules/pkg/readme.md",
			},
			skippedResources: []string{
				"node_modules/other/skip.txt",
			},
		},
		{
			name:                 ".obsidian subtree",
			attachmentFolderPath: ".obsidian/assets",
			attachmentResources: []string{
				".obsidian/assets/photo.png",
			},
			skippedResources: []string{
				".obsidian/plugins/extra.js",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vaultPath := t.TempDir()
			writeVaultFile(t, vaultPath, ".obsidian/app.json", fmt.Sprintf(`{"attachmentFolderPath":%q}`, tt.attachmentFolderPath))
			writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
			writeVaultFile(t, vaultPath, "assets/public/logo.png", "logo")

			for _, relPath := range tt.attachmentResources {
				writeVaultFile(t, vaultPath, relPath, "asset")
			}
			for _, relPath := range tt.skippedMarkdown {
				writeVaultFile(t, vaultPath, relPath, "# Skip")
			}
			for _, relPath := range tt.skippedResources {
				writeVaultFile(t, vaultPath, relPath, "skip")
			}

			got, err := Scan(vaultPath)
			if err != nil {
				t.Fatalf("Scan() error = %v", err)
			}

			if got.AttachmentFolderPath != tt.attachmentFolderPath {
				t.Fatalf("AttachmentFolderPath = %q, want %q", got.AttachmentFolderPath, tt.attachmentFolderPath)
			}

			wantMarkdown := []string{"notes/post.md"}
			if tt.name == "hidden directory" {
				wantMarkdown = []string{".hidden/private.md", "notes/post.md"}
			}
			if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
				t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
			}

			wantResources := []string{"assets/public/logo.png"}
			if tt.name == "hidden directory" {
				wantResources = append(wantResources, tt.attachmentResources...)
				wantResources = append(wantResources, tt.skippedResources...)
				sort.Strings(wantResources)
			}
			if !reflect.DeepEqual(got.ResourceFiles, wantResources) {
				t.Fatalf("ResourceFiles = %#v, want %#v", got.ResourceFiles, wantResources)
			}

			for _, relPath := range tt.attachmentResources {
				if tt.name != "hidden directory" && got.LookupResourcePath(relPath).Path != "" {
					t.Fatalf("HasResource(%q) = true, want false", relPath)
				}
				if tt.name == "hidden directory" && got.LookupResourcePath(relPath).Path == "" {
					t.Fatalf("HasResource(%q) = false, want true", relPath)
				}
			}
			for _, relPath := range tt.skippedMarkdown {
				if tt.name == "hidden directory" {
					if !scanContainsMarkdown(got, relPath) {
						t.Fatalf("HasMarkdown(%q) = false, want true", relPath)
					}
					continue
				}
				if scanContainsMarkdown(got, relPath) {
					t.Fatalf("HasMarkdown(%q) = true, want false", relPath)
				}
			}
			for _, relPath := range tt.skippedResources {
				if tt.name == "hidden directory" {
					if got.LookupResourcePath(relPath).Path == "" {
						t.Fatalf("HasResource(%q) = false, want true", relPath)
					}
					continue
				}
				if got.LookupResourcePath(relPath).Path != "" {
					t.Fatalf("HasResource(%q) = true, want false", relPath)
				}
			}
			if !scanContainsMarkdown(got, "notes/post.md") {
				t.Fatal("HasMarkdown(notes/post.md) = false, want true")
			}
			if got.LookupResourcePath("assets/public/logo.png").Path == "" {
				t.Fatal("HasResource(assets/public/logo.png) = false, want true")
			}
		})
	}
}

func TestScanTreatsConfiguredAttachmentFolderPathAsMetadataInsideSkippedSubtree(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":".hidden/attachments"}`)
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	writeVaultFile(t, vaultPath, ".hidden/attachments/nested/diagram.svg", "svg")
	writeVaultFile(t, vaultPath, ".hidden/attachments/photo.png", "png")
	writeVaultFile(t, vaultPath, ".hidden/attachments/.hidden/secret.png", "secret")
	writeVaultFile(t, vaultPath, ".hidden/attachments/.obsidian/workspace.json", `{}`)
	writeVaultFile(t, vaultPath, ".hidden/attachments/node_modules/pkg/file.bin", "bin")

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	if got.AttachmentFolderPath != ".hidden/attachments" {
		t.Fatalf("AttachmentFolderPath = %q, want %q", got.AttachmentFolderPath, ".hidden/attachments")
	}

	wantMarkdown := []string{"notes/post.md"}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}

	wantResources := []string{".hidden/attachments/.hidden/secret.png", ".hidden/attachments/nested/diagram.svg", ".hidden/attachments/photo.png"}
	if !reflect.DeepEqual(got.ResourceFiles, wantResources) {
		t.Fatalf("ResourceFiles = %#v, want hidden attachment resources", got.ResourceFiles)
	}

	if !scanContainsMarkdown(got, "notes/post.md") {
		t.Fatal("HasMarkdown(notes/post.md) = false, want true")
	}
	if got.LookupResourcePath(".hidden/attachments/photo.png").Path == "" {
		t.Fatal("HasResource(.hidden/attachments/photo.png) = false, want true")
	}
	if got.LookupResourcePath(".hidden/attachments/.hidden/secret.png").Path == "" {
		t.Fatal("HasResource(.hidden/attachments/.hidden/secret.png) = false, want true")
	}
	if got.LookupResourcePath(".hidden/attachments/.obsidian/workspace.json").Path != "" {
		t.Fatal("HasResource(.hidden/attachments/.obsidian/workspace.json) = true, want false")
	}
	if got.LookupResourcePath(".hidden/attachments/node_modules/pkg/file.bin").Path != "" {
		t.Fatal("HasResource(.hidden/attachments/node_modules/pkg/file.bin) = true, want false")
	}
}

func TestScanSkipsSymlinkEntries(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	outsidePath := t.TempDir()

	writeVaultFile(t, vaultPath, ".obsidian/app.json", `{"attachmentFolderPath":"assets/uploads"}`)
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	writeVaultFile(t, vaultPath, "assets/uploads/photo.png", "png")
	writeVaultFile(t, outsidePath, "outside.md", "# Outside")
	writeVaultFile(t, outsidePath, "outside.png", "png")

	writeVaultSymlink(t, filepath.Join(outsidePath, "outside.md"), filepath.Join(vaultPath, "notes", "link.md"))
	writeVaultSymlink(t, filepath.Join(outsidePath, "outside.png"), filepath.Join(vaultPath, "assets", "link.png"))

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	wantMarkdown := []string{"notes/post.md"}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}

	wantResources := []string{"assets/uploads/photo.png"}
	if !reflect.DeepEqual(got.ResourceFiles, wantResources) {
		t.Fatalf("ResourceFiles = %#v, want %#v", got.ResourceFiles, wantResources)
	}

	if scanContainsMarkdown(got, "notes/link.md") {
		t.Fatal("HasMarkdown(notes/link.md) = true, want false")
	}
	if got.LookupResourcePath("assets/link.png").Path != "" {
		t.Fatal("HasResource(assets/link.png) = true, want false")
	}
}

func TestScanSkipsNonRegularMarkdownAndResourceCandidates(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	writeVaultNamedPipe(t, filepath.Join(vaultPath, "notes", "blocked.md"))
	writeVaultNamedPipe(t, filepath.Join(vaultPath, "assets", "blocked.png"))

	got, err := Scan(vaultPath)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	wantMarkdown := []string{"notes/post.md"}
	if !reflect.DeepEqual(got.MarkdownFiles, wantMarkdown) {
		t.Fatalf("MarkdownFiles = %#v, want %#v", got.MarkdownFiles, wantMarkdown)
	}
	if len(got.ResourceFiles) != 0 {
		t.Fatalf("ResourceFiles = %#v, want empty", got.ResourceFiles)
	}
	if scanContainsMarkdown(got, "notes/blocked.md") {
		t.Fatal("HasMarkdown(notes/blocked.md) = true, want false")
	}
	if got.LookupResourcePath("assets/blocked.png").Path != "" {
		t.Fatal("HasResource(assets/blocked.png) = true, want false")
	}
}

func scanContainsMarkdown(result ScanResult, relPath string) bool {
	_, ok := result.markdownSet[normalizeLookupPath(relPath)]
	return ok
}

func TestScanRejectsNonRegularObsidianAppJSON(t *testing.T) {
	t.Parallel()

	vaultPath := t.TempDir()
	writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
	if err := os.MkdirAll(filepath.Join(vaultPath, ".obsidian"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.obsidian) error = %v", err)
	}
	writeVaultNamedPipe(t, filepath.Join(vaultPath, ".obsidian", "app.json"))

	_, err := Scan(vaultPath)
	if err == nil {
		t.Fatal("Scan() error = nil, want non-regular app.json rejection")
	}
	if !strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("Scan() error = %v, want regular-file rejection", err)
	}
}

func TestScanRejectsSymlinkedObsidianConfigPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setupVault func(t *testing.T, vaultPath string)
	}{
		{
			name: "symlinked obsidian directory",
			setupVault: func(t *testing.T, vaultPath string) {
				outsidePath := t.TempDir()
				externalConfigDir := filepath.Join(outsidePath, "external-obsidian")
				writeVaultFile(t, externalConfigDir, "app.json", `{"attachmentFolderPath":"outside-assets"}`)
				writeVaultSymlink(t, externalConfigDir, filepath.Join(vaultPath, ".obsidian"))
			},
		},
		{
			name: "symlinked app json",
			setupVault: func(t *testing.T, vaultPath string) {
				outsidePath := t.TempDir()
				writeVaultFile(t, outsidePath, "app.json", `{"attachmentFolderPath":"outside-assets"}`)
				if err := os.MkdirAll(filepath.Join(vaultPath, ".obsidian"), 0o755); err != nil {
					t.Fatalf("MkdirAll(.obsidian) error = %v", err)
				}
				writeVaultSymlink(t, filepath.Join(outsidePath, "app.json"), filepath.Join(vaultPath, ".obsidian", "app.json"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vaultPath := t.TempDir()
			writeVaultFile(t, vaultPath, "notes/post.md", "# Post")
			tt.setupVault(t, vaultPath)

			_, err := Scan(vaultPath)
			if err == nil {
				t.Fatal("Scan() error = nil, want symlink rejection")
			}
			if !strings.Contains(err.Error(), "must not be a symbolic link") {
				t.Fatalf("Scan() error = %v, want symlink rejection", err)
			}
		})
	}
}

func writeVaultFile(t *testing.T, vaultPath string, relPath string, content string) {
	t.Helper()

	absPath := filepath.Join(vaultPath, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(absPath), err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", absPath, err)
	}
}

func writeVaultSymlink(t *testing.T, targetPath string, linkPath string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(linkPath), err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("Symlink(%q, %q) unsupported: %v", targetPath, linkPath, err)
	}
}

func writeVaultNamedPipe(t *testing.T, pipePath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("mkfifo-based regression is not supported on Windows")
	}
	if err := os.MkdirAll(filepath.Dir(pipePath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(pipePath), err)
	}

	mkfifoPath, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	output, err := exec.Command(mkfifoPath, pipePath).CombinedOutput()
	if err != nil {
		t.Skipf("mkfifo(%q) unsupported: %v (%s)", pipePath, err, strings.TrimSpace(string(output)))
	}
}
