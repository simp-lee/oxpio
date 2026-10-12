//go:build linux

package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrAtomicityUnavailable = errors.New("OUTPUT_ATOMICITY_UNAVAILABLE")

// AtomicReplaceDirectory exchanges two directories without an empty-output
// window. The caller must place both paths on the same filesystem.
func AtomicReplaceDirectory(staging, output string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, filepath.Clean(staging), unix.AT_FDCWD, filepath.Clean(output), unix.RENAME_EXCHANGE); err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			return fmt.Errorf("%w: %v", ErrAtomicityUnavailable, err)
		}
		return err
	}
	return nil
}

// ProbeAtomicReplace performs the same exchange used by publication against
// disposable directories in the requested parent.
func ProbeAtomicReplace(parent string) error {
	left, err := os.MkdirTemp(parent, ".oxpio-probe-left-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAtomicityUnavailable, err)
	}
	right, err := os.MkdirTemp(parent, ".oxpio-probe-right-*")
	if err != nil {
		_ = os.RemoveAll(left)
		return fmt.Errorf("%w: %v", ErrAtomicityUnavailable, err)
	}
	defer func() { _ = os.RemoveAll(left) }()
	defer func() { _ = os.RemoveAll(right) }()
	return AtomicReplaceDirectory(left, right)
}
