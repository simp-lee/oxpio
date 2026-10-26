//go:build darwin && !ios

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyDirectoryHandleWithinRootUsesDarwinFDPath(t *testing.T) {
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

	if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
		t.Fatalf("VerifyDirectoryHandleWithinRoot() error = %v", err)
	}
}

func TestVerifyDirectoryHandleWithinRootRejectsDarwinDescriptorMovedOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	parentPath := filepath.Join(root, "parent")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}

	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if err := os.Rename(parentPath, filepath.Join(outside, "parent")); err != nil {
		t.Fatal(err)
	}

	if err := VerifyDirectoryHandleWithinRoot(root, parent); !errors.Is(err, ErrPathOutsideRoot) {
		t.Fatalf("VerifyDirectoryHandleWithinRoot() error = %v, want ErrPathOutsideRoot", err)
	}
}
