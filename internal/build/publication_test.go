package build

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
	manifest := &strictCacheManifest{Outputs: []strictCacheOutput{{Route: route, OutputHash: hash}}}
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
