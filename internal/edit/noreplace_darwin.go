//go:build darwin

package edit

import "golang.org/x/sys/unix"

func ensureNoReplaceRename() error { return nil }

func renamePathReplace(source, destination string) error {
	return unix.Rename(source, destination)
}

func renamePathNoReplace(source, destination string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_EXCL)
}
