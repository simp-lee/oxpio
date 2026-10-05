//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireConfigLock(vaultRoot string) (func() error, error) {
	lockPath := filepath.Join(vaultRoot, configLockFilename)
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock %q: %w", lockPath, err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open config lock %q: create file handle", lockPath)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect config lock %q: %w", lockPath, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("config lock %q must be a regular file", lockPath)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set config lock permissions %q: %w", lockPath, err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock config %q: %w", lockPath, err)
	}

	return func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}
