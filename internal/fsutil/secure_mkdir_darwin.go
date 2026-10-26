//go:build darwin && !ios

package fsutil

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// VerifyDirectoryHandleWithinRoot rejects a directory descriptor that has
// been renamed outside root after it was opened. Darwin exposes the physical
// descriptor path through fcntl(F_GETPATH), rather than procfs.
func VerifyDirectoryHandleWithinRoot(root string, directory *os.File) error {
	if directory == nil {
		return os.ErrInvalid
	}

	fdInfo, err := directory.Stat()
	if err != nil {
		return ErrSecureDirectoryMutationUnavailable
	}
	actual, err := darwinDirectoryHandlePath(directory)
	if err != nil {
		return ErrSecureDirectoryMutationUnavailable
	}
	actualInfo, err := os.Stat(actual)
	if err != nil || !os.SameFile(fdInfo, actualInfo) || !PathWithinRoot(root, actual) {
		return ErrPathOutsideRoot
	}
	return nil
}

func darwinDirectoryHandlePath(directory *os.File) (string, error) {
	// F_GETPATH writes a NUL-terminated path into a MAXPATHLEN-sized buffer.
	// 4096 is larger than MAXPATHLEN on supported Darwin systems and avoids
	// depending on a platform-specific generated constant.
	path := make([]byte, 4096)
	if _, err := unix.FcntlInt(directory.Fd(), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&path[0])))); err != nil {
		return "", err
	}
	end := bytes.IndexByte(path, 0)
	if end < 0 {
		return "", unix.EINVAL
	}
	return string(path[:end]), nil
}
