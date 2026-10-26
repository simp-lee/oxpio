//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package fsutil

import "os"

func OpenDirectoryNoFollow(string) (*os.File, error) {
	return nil, ErrSecureDirectoryMutationUnavailable
}

func OpenDirectoryRelativeNoFollow(*os.File, string) (*os.File, error) {
	return nil, ErrSecureDirectoryMutationUnavailable
}

func VerifyDirectoryHandleWithinRoot(string, *os.File) error {
	return ErrSecureDirectoryMutationUnavailable
}

func MkdirRelative(*os.File, string, os.FileMode) error {
	return ErrSecureDirectoryMutationUnavailable
}

func RemoveDirectoryRelative(*os.File, string) error {
	return ErrSecureDirectoryMutationUnavailable
}

func CreateDirectoryRelativeWithinRoot(string, *os.File, string, os.FileMode) (*CreatedDirectory, error) {
	return nil, ErrSecureDirectoryMutationUnavailable
}

func CheckDirectoryRollbackSupport() error {
	return ErrSecureDirectoryMutationUnavailable
}
