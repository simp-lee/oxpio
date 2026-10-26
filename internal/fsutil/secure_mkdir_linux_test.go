//go:build linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirRelativeWithinRootCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}

	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()

	if err := MkdirRelativeWithinRoot(root, parent, "child", 0o755); err != nil {
		t.Fatalf("MkdirRelativeWithinRoot() error = %v", err)
	}
	if info, err := os.Stat(filepath.Join(parentPath, "child")); err != nil || !info.IsDir() {
		t.Fatalf("created child = %v, info=%v", err, info)
	}
}

func TestVerifyDirectoryHandleWithinRootRejectsDeletedDirectory(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}

	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := os.Remove(parentPath); err != nil {
		t.Fatal(err)
	}

	err = VerifyDirectoryHandleWithinRoot(root, parent)
	if errors.Is(err, ErrSecureDirectoryMutationUnavailable) {
		t.Skipf("directory descriptor location is unavailable: %v", err)
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("VerifyDirectoryHandleWithinRoot() error = %v, want ErrPathOutsideRoot", err)
	}
}

func TestVerifyDirectoryHandleWithinRootRejectsDeletedDirectoryNameCollision(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}

	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := os.Remove(parentPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "parent (deleted)"), 0o755); err != nil {
		t.Fatal(err)
	}

	err = VerifyDirectoryHandleWithinRoot(root, parent)
	if errors.Is(err, ErrSecureDirectoryMutationUnavailable) {
		t.Skipf("directory descriptor location is unavailable: %v", err)
	}
	if !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("VerifyDirectoryHandleWithinRoot() error = %v, want ErrPathOutsideRoot", err)
	}
}
