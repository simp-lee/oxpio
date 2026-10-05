package build

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublisherKeepsPublishedOutputWhenBackupCleanupPartiallyFails(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "site")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, managedOutputMarkerFilename), []byte(managedOutputMarkerContents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "index.html"), []byte("old output"), 0o644); err != nil {
		t.Fatal(err)
	}
	publisher, err := prepareStagedOutputPublisher(root, output)
	if err != nil {
		t.Fatal(err)
	}
	staging := publisher.OutputPath()
	if err := writeManagedOutputMarker(staging); err != nil {
		t.Fatal(err)
	}
	want := []byte("new output")
	if err := writeOutputFile(staging, "index.html", want); err != nil {
		t.Fatal(err)
	}
	cleanupFailure := errors.New("simulated partial backup cleanup failure")
	originalRemove := stagedOutputRemoveAll
	stagedOutputRemoveAll = func(name string) error {
		if strings.Contains(name, "-backup-") {
			if err := os.Remove(filepath.Join(name, managedOutputMarkerFilename)); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(name, "index.html")); err != nil {
				return err
			}
			return cleanupFailure
		}
		return originalRemove(name)
	}
	defer func() { stagedOutputRemoveAll = originalRemove }()
	if err := publisher.Finalize(true); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	if !errors.Is(publisher.cleanupErr, cleanupFailure) {
		t.Fatalf("cleanup error = %v, want %v", publisher.cleanupErr, cleanupFailure)
	}
	after, err := os.ReadFile(filepath.Join(output, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, want) {
		t.Fatalf("output = %q, want published output %q", after, want)
	}
	marker, err := os.ReadFile(filepath.Join(output, managedOutputMarkerFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker) != managedOutputMarkerContents {
		t.Fatalf("output marker = %q, want %q", marker, managedOutputMarkerContents)
	}
}

func TestStrictBuildReportsPostCommitCleanupWithoutReturningFailure(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "site")
	writeStrictFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nOld body\n")
	if _, err := BuildWithOptions(vault, output, Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	writeStrictFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\nNew body\n")

	cleanupFailure := errors.New("simulated committed backup cleanup failure")
	originalRemove := stagedOutputRemoveAll
	stagedOutputRemoveAll = func(name string) error {
		if strings.Contains(name, "-backup-") {
			return cleanupFailure
		}
		return originalRemove(name)
	}
	defer func() { stagedOutputRemoveAll = originalRemove }()

	var diagnostics bytes.Buffer
	result, err := BuildWithOptions(vault, output, Options{Strict: true, DiagnosticsWriter: &diagnostics})
	if err != nil {
		t.Fatalf("BuildWithOptions() error = %v, want committed build success", err)
	}
	if result == nil || result.WarningCount != 0 || len(result.Diagnostics) != 0 || !errors.Is(result.OutputCleanupError, cleanupFailure) {
		t.Fatalf("result = %#v, want separate post-commit cleanup status without a quality warning", result)
	}
	if !strings.Contains(diagnostics.String(), "cleanup output_cleanup") || !strings.Contains(diagnostics.String(), cleanupFailure.Error()) {
		t.Fatalf("diagnostics = %q, want separate cleanup status", diagnostics.String())
	}
	if html := string(readBuildOutputFile(t, output, "index.html")); !strings.Contains(html, "New body") || strings.Contains(html, "Old body") {
		t.Fatalf("published HTML = %q, want newly committed output", html)
	}
}

func TestPublisherPublishesExpectedOutputDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not portable on Windows")
	}

	for _, tt := range []struct {
		name       string
		prepareOld bool
		wantMode   os.FileMode
	}{
		{name: "new output", wantMode: 0o755},
		{name: "replace managed output", prepareOld: true, wantMode: 0o751},
		{name: "replace read-only managed output", prepareOld: true, wantMode: 0o555},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "site")
			t.Cleanup(func() { _ = os.Chmod(output, 0o755) })
			if tt.prepareOld {
				if err := writeManagedOutputMarker(output); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(output, tt.wantMode); err != nil {
					t.Fatal(err)
				}
			}

			publisher, err := prepareStagedOutputPublisher(root, output)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeManagedOutputMarker(publisher.OutputPath()); err != nil {
				t.Fatal(err)
			}
			if err := publisher.Finalize(true); err != nil {
				t.Fatal(err)
			}
			if publisher.cleanupErr != nil {
				t.Fatalf("backup cleanup error = %v", publisher.cleanupErr)
			}

			info, err := os.Stat(output)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tt.wantMode {
				t.Fatalf("published output mode = %04o, want %04o", got, tt.wantMode)
			}
		})
	}
}

func TestPublisherCleansStagingWhenReadOnlyOutputBackupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not portable on Windows")
	}

	root := t.TempDir()
	output := filepath.Join(root, "site")
	if err := writeManagedOutputMarker(output); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(output, 0o755) })

	publisher, err := prepareStagedOutputPublisher(root, output)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagedOutputMarker(publisher.OutputPath()); err != nil {
		t.Fatal(err)
	}
	originalRename := stagedOutputRename
	stagedOutputRename = func(oldPath, newPath string) error {
		if oldPath == output {
			return errors.New("injected backup failure")
		}
		return originalRename(oldPath, newPath)
	}
	t.Cleanup(func() { stagedOutputRename = originalRename })

	if err := publisher.Finalize(true); err == nil || !strings.Contains(err.Error(), "injected backup failure") {
		t.Fatalf("Finalize() error = %v, want injected backup failure", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "site" {
		t.Fatalf("transaction left temporary output: %v", entries)
	}
}

func TestPublisherPreservesAllOutputAndCleansStagingOnPublicationFailures(t *testing.T) {
	for _, failure := range []string{"staging write", "backup rename", "publication rename"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "site")
			if err := writeManagedOutputMarker(output); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"index.html", ".oxpio-cache/manifest.json", "assets/social/old/card.png"} {
				if err := writeOutputFile(output, name, []byte("previous "+name)); err != nil {
					t.Fatal(err)
				}
			}
			before := strictOutputBytes(t, output)
			publisher, err := prepareStagedOutputPublisher(root, output)
			if err != nil {
				t.Fatal(err)
			}
			staging := publisher.OutputPath()
			if err := writeManagedOutputMarker(staging); err != nil {
				t.Fatal(err)
			}
			if err := writeOutputFile(staging, "assets/social/new/card.png", []byte("new card")); err != nil {
				t.Fatal(err)
			}
			if failure == "staging write" {
				// A directory at a file destination deterministically fails on all
				// supported platforms, including privileged test processes.
				if err := os.Mkdir(filepath.Join(staging, "index.html"), 0o755); err != nil {
					t.Fatal(err)
				}
				registry := newStrictOutputRegistry("", nil)
				if err := registry.write(staging, "index.html", "index", []byte("new page")); err == nil {
					t.Fatal("staging write unexpectedly succeeded")
				}
				if err := publisher.Finalize(false); err != nil {
					t.Fatal(err)
				}
			} else {
				originalRename := stagedOutputRename
				stagedOutputRename = func(oldPath, newPath string) error {
					if failure == "backup rename" && oldPath == output || failure == "publication rename" && oldPath == staging {
						return errors.New("injected " + failure + " failure")
					}
					return originalRename(oldPath, newPath)
				}
				t.Cleanup(func() { stagedOutputRename = originalRename })
				if err := publisher.Finalize(true); err == nil || !strings.Contains(err.Error(), "injected "+failure) {
					t.Fatalf("Finalize() error = %v, want injected failure", err)
				}
			}
			after := strictOutputBytes(t, output)
			if len(after) != len(before) {
				t.Fatalf("output file count changed: %d -> %d", len(before), len(after))
			}
			for name, data := range before {
				if !bytes.Equal(data, after[name]) {
					t.Fatalf("output %q changed after %s failure", name, failure)
				}
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != "site" {
				t.Fatalf("transaction left temporary output: %v", entries)
			}
		})
	}
}

func TestStrictOutputRegistryReusesUnchangedOutputAfterOwnerChange(t *testing.T) {
	previousRoot := t.TempDir()
	const route = "assets/banner.png"
	content := []byte("shared version asset")
	if err := writeOutputFile(previousRoot, route, content); err != nil {
		t.Fatal(err)
	}
	previousInfo, err := os.Stat(filepath.Join(previousRoot, filepath.FromSlash(route)))
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	manifest := &strictCacheManifest{Outputs: []strictCacheOutput{{
		Owner: "asset:docs/v1/banner.png", Route: route, OutputHash: hash,
	}}}

	staging := t.TempDir()
	registry := newStrictOutputRegistry(previousRoot, manifest)
	const currentOwner = "asset:docs/v2/banner.png"
	if err := registry.write(staging, route, currentOwner, content); err != nil {
		t.Fatal(err)
	}
	currentInfo, err := os.Stat(filepath.Join(staging, filepath.FromSlash(route)))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(previousInfo, currentInfo) {
		t.Fatal("unchanged output with a new owner was physically rewritten")
	}
	if len(registry.records) != 1 || registry.records[0].Owner != currentOwner || registry.records[0].Route != route || registry.records[0].OutputHash != hash {
		t.Fatalf("output records = %#v, want current owner and unchanged output hash", registry.records)
	}
	if err := registry.write(staging, route, "asset:docs/v3/banner.png", content); err == nil {
		t.Fatal("duplicate output write in the current build succeeded")
	}
}

func TestStrictOutputRegistryDoesNotReuseCachedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links are not portable on Windows")
	}

	previousRoot := t.TempDir()
	externalRoot := t.TempDir()
	const route = "assets/banner.png"
	content := []byte("cached asset")
	externalPath := filepath.Join(externalRoot, "banner.png")
	if err := os.WriteFile(externalPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	previousPath := filepath.Join(previousRoot, filepath.FromSlash(route))
	if err := os.MkdirAll(filepath.Dir(previousPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalPath, previousPath); err != nil {
		t.Fatal(err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	manifest := &strictCacheManifest{Outputs: []strictCacheOutput{{
		Route: route, OutputHash: hash,
	}}}
	staging := t.TempDir()
	registry := newStrictOutputRegistry(previousRoot, manifest)
	if err := registry.write(staging, route, "asset:current/banner.png", content); err != nil {
		t.Fatal(err)
	}

	info, err := os.Lstat(filepath.Join(staging, filepath.FromSlash(route)))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("cached output mode = %v, want a regular file", info.Mode())
	}
	if got, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(route))); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("cached output = %q, %v; want regular file contents %q", got, err, content)
	}
}

func TestStrictOutputRegistryRejectsDuplicateOwners(t *testing.T) {
	root := t.TempDir()
	registry := newStrictOutputRegistry("", nil)
	if err := registry.write(root, "index.html", "index", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := registry.write(root, "index.html", "article:home", []byte("two")); err == nil {
		t.Fatal("duplicate output write error = nil")
	}
	if err := registry.claim("assets/card.png", "asset:left"); err != nil {
		t.Fatal(err)
	}
	if err := registry.claim("assets/card.png", "asset:right"); err != nil {
		t.Fatalf("identical asset destination claim error = %v", err)
	}
	if err := registry.claim("assets/card.png", "runtime"); err == nil {
		t.Fatal("cross-owner output claim error = nil")
	}
}
