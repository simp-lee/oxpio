//go:build windows

package fsutil

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsSecureRelativeMkdirCreatesDirectory(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}

	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatalf("OpenDirectoryNoFollow() error = %v", err)
	}
	defer func() { _ = parent.Close() }()

	if err := MkdirRelativeWithinRoot(root, parent, "created", 0o755); err != nil {
		t.Fatalf("MkdirRelativeWithinRoot() error = %v", err)
	}
	if info, err := os.Stat(filepath.Join(parentPath, "created")); err != nil || !info.IsDir() {
		t.Fatalf("created directory = %v, info=%v", err, info)
	}
}

func TestWindowsSecureDirectoryRejectsJunction(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	junction := filepath.Join(root, "junction")
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create junction: %v (%s)", err, output)
	}

	_, err = OpenDirectoryNoFollow(junction)
	if !errors.Is(err, ErrSymlinkPath) {
		t.Fatalf("OpenDirectoryNoFollow() error = %v, want reparse-point rejection", err)
	}
}
