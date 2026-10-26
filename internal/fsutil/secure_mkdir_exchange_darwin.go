//go:build darwin && !ios

package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

const darwinRenameSwap = 0x2

func exchangeDirectoryEntries(parent *os.File, left, right string) error {
	return unix.RenameatxNp(int(parent.Fd()), left, int(parent.Fd()), right, darwinRenameSwap)
}

func moveDirectoryNoReplace(parent *os.File, source, destination string) error {
	return unix.RenameatxNp(int(parent.Fd()), source, int(parent.Fd()), destination, unix.RENAME_EXCL)
}

func CheckDirectoryRollbackSupport() error { return nil }
