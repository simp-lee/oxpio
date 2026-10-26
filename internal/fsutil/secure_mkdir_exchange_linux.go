//go:build linux && !android

package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

func exchangeDirectoryEntries(parent *os.File, left, right string) error {
	return unix.Renameat2(int(parent.Fd()), left, int(parent.Fd()), right, unix.RENAME_EXCHANGE)
}

func moveDirectoryNoReplace(parent *os.File, source, destination string) error {
	return unix.Renameat2(int(parent.Fd()), source, int(parent.Fd()), destination, unix.RENAME_NOREPLACE)
}

func CheckDirectoryRollbackSupport() error { return nil }
