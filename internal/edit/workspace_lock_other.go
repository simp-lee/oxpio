//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package edit

import "errors"

func acquireWorkspaceLock(string) (func() error, error) {
	return nil, errors.New("cross-process workspace locking is unavailable on this platform")
}
