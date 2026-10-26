//go:build aix || android || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package fsutil

import (
	"os"
	"path/filepath"
	"strconv"
)

// VerifyDirectoryHandleWithinRoot rejects a directory descriptor that has
// been renamed outside root after it was opened. Platforms without a way to
// inspect the descriptor's physical location fail closed.
func VerifyDirectoryHandleWithinRoot(root string, directory *os.File) error {
	if directory == nil {
		return os.ErrInvalid
	}
	link := filepath.Join(string(filepath.Separator), "proc", "self", "fd", strconv.FormatUint(uint64(directory.Fd()), 10))
	// procfs appends " (deleted)" to an unlinked target, which can also be
	// part of a legitimate directory name. Compare the resolved target's
	// device/inode with the descriptor before applying the root boundary.
	fdInfo, err := directory.Stat()
	if err != nil {
		return ErrSecureDirectoryMutationUnavailable
	}
	actual, err := os.Readlink(link)
	if err != nil {
		return ErrSecureDirectoryMutationUnavailable
	}
	actualInfo, err := os.Stat(actual)
	if err != nil || !os.SameFile(fdInfo, actualInfo) || !PathWithinRoot(root, actual) {
		return ErrPathOutsideRoot
	}
	return nil
}
