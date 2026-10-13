//go:build linux

package fsutil

import "golang.org/x/sys/unix"

// RenameNoReplace performs an atomic same-filesystem rename that refuses an
// existing destination.
func RenameNoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
