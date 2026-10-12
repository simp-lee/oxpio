//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package workspace

import "errors"

func acquireFileLock(string, bool) (func() error, error) {
	return nil, errors.New("cross-process workspace locking is unavailable on this platform")
}

func replaceFile(string, string) error {
	return errors.New("atomic file replacement is unavailable on this platform")
}
