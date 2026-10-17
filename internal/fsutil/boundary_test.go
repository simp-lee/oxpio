package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPortableSitePathComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want bool
	}{
		{path: "notes/archive/index.html", want: true},
		{path: ".oxpio-cache/manifest.json", want: true},
		{path: "notes/CON/index.html"},
		{path: "notes/com1.txt/index.html"},
		{path: "notes/COM¹/index.html"},
		{path: "notes/lpt².txt/index.html"},
		{path: "notes/CONIN$/index.html"},
		{path: "notes/conout$.txt/index.html"},
		{path: "notes/CON .txt/index.html"},
		{path: "notes/trailing./index.html"},
		{path: "notes/archive*/index.html"},
		{path: "notes/topic#draft/index.html", want: true},
		{path: "notes/topic%2Fd/index.html", want: true},
		{path: "notes/\xff.md", want: false},
	}
	for _, tt := range tests {
		if got := IsPortableSitePath(tt.path); got != tt.want {
			t.Errorf("IsPortableSitePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestInspectContainedPathPreservesLeadingSpaceIdentity(t *testing.T) {
	vault := t.TempDir()
	section := filepath.Join(vault, " guide")
	if err := os.Mkdir(section, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(section, "_index.md")
	if err := os.WriteFile(file, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, _, err := InspectContainedRegularFile(vault, filepath.Join(" guide", "_index.md"))
	if err != nil || resolved != file {
		t.Fatalf("InspectContainedRegularFile() = %q, %v; want %q", resolved, err, file)
	}
}

func TestResolveVaultOutputRejectsCaseVariantManagementDirectory(t *testing.T) {
	vault := t.TempDir()
	if err := os.Mkdir(filepath.Join(vault, ".OXPIO"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveVaultOutput(vault, filepath.Join(vault, ".OXPIO", "site")); err == nil {
		t.Fatal("ResolveVaultOutput() accepted output below case-variant management directory")
	}
}

func TestResolveVaultOutputBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(vault, "output-file")
	if err := os.WriteFile(outputFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		output     string
		wantInside bool
		wantErr    string
	}{
		{name: "vault descendant", output: filepath.Join(vault, "public"), wantInside: true},
		{name: "reserved management directory", output: filepath.Join(vault, ".oxpio", "out"), wantErr: "reserved Vault directory"},
		{name: "reserved metadata directory", output: filepath.Join(vault, ".obsidian", "out"), wantErr: "reserved Vault directory"},
		{name: "external output", output: filepath.Join(root, "published", "site")},
		{name: "vault root", output: vault, wantErr: "must not equal the vault root"},
		{name: "vault ancestor", output: root, wantErr: "must not contain the vault"},
		{name: "existing regular file", output: outputFile, wantErr: "must be a directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveVaultOutput(vault, tt.output)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveVaultOutput() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveVaultOutput() error = %v", err)
			}
			if got.VaultPath != vault {
				t.Fatalf("VaultPath = %q, want %q", got.VaultPath, vault)
			}
			if gotInside := PathWithinRoot(got.VaultPath, got.OutputPath); gotInside != tt.wantInside {
				t.Fatalf("resolved output inside vault = %v, want %v", gotInside, tt.wantInside)
			}
		})
	}
}

func TestResolveVaultOutputRejectsParentSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	external := filepath.Join(root, "external")
	alias := filepath.Join(root, "alias")
	for _, dir := range []string{vault, external} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(external, alias); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveVaultOutput(vault, filepath.Join(alias, "nested", "site")); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ResolveVaultOutput() error = %v, want parent symlink rejection", err)
	}
}

func TestResolveVaultOutputRejectsVaultSymlinkAlias(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	alias := filepath.Join(root, "vault-alias")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vault, alias); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveVaultOutput(vault, filepath.Join(alias, "public")); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ResolveVaultOutput() error = %v, want symlink alias rejection", err)
	}
}

func TestResolveVaultOutputRejectsFinalSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	target := filepath.Join(root, "target")
	output := filepath.Join(root, "site")
	for _, dir := range []string{vault, target} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, output); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveVaultOutput(vault, output)
	if err == nil || !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("ResolveVaultOutput() error = %v, want final symlink rejection", err)
	}
}

func TestContainedRegularFileRejectsExternalAndSymlinkInputs(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on some Windows hosts")
	}

	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	external := filepath.Join(root, "sentinel.txt")
	if err := os.MkdirAll(filepath.Join(vault, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "notes", "valid.md"), []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(vault, "notes", "escaped.md")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := InspectContainedRegularFile(vault, external); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("external InspectContainedRegularFile() error = %v, want ErrPathOutsideRoot", err)
	}
	if _, _, err := InspectContainedRegularFile(vault, filepath.Join("notes", "escaped.md")); !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("symlink InspectContainedRegularFile() error = %v, want ErrSymlinkPath", err)
	}
	resolved, data, _, err := ReadContainedRegularFile(vault, filepath.Join("notes", "valid.md"))
	if err != nil {
		t.Fatalf("ReadContainedRegularFile() error = %v", err)
	}
	if resolved != filepath.Join(vault, "notes", "valid.md") || string(data) != "valid" {
		t.Fatalf("ReadContainedRegularFile() = %q, %q", resolved, data)
	}
}
