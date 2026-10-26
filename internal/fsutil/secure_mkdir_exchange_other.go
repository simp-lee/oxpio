//go:build aix || dragonfly || freebsd || illumos || ios || netbsd || openbsd || solaris

package fsutil

import (
	"errors"
	"os"
)

func exchangeDirectoryEntries(*os.File, string, string) error {
	return errors.Join(ErrSecureDirectoryMutationUnavailable, errors.New("atomic directory exchange is unavailable"))
}

func moveDirectoryNoReplace(*os.File, string, string) error {
	return errors.Join(ErrSecureDirectoryMutationUnavailable, errors.New("atomic no-replace directory move is unavailable"))
}

func CheckDirectoryRollbackSupport() error {
	return errors.Join(ErrSecureDirectoryMutationUnavailable, errors.New("atomic directory rollback is unavailable"))
}
