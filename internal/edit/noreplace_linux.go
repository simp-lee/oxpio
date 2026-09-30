//go:build linux

package edit

import "golang.org/x/sys/unix"

func renamePathNoReplace(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
