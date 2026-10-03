//go:build linux

package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// removeCreatedPathAtomic uses RENAME_EXCHANGE so rollback never removes a
// pathname that was replaced after the transaction created it. The temporary
// placeholder is always the only path removed after the exchange.
func removeCreatedPathAtomic(filename, relPath, expectedHash string, isDir bool) (bool, error) {
	targetInfo, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	if targetInfo.IsDir() != isDir {
		return true, fmt.Errorf("created path type changed before rollback")
	}

	placeholder, err := createRollbackPlaceholder(filepath.Dir(filename), isDir)
	if err != nil {
		return true, err
	}
	placeholderInfo, err := os.Lstat(placeholder)
	if err != nil {
		_ = os.RemoveAll(placeholder)
		return true, err
	}

	exchange := func() error {
		return unix.Renameat2(unix.AT_FDCWD, filename, unix.AT_FDCWD, placeholder, unix.RENAME_EXCHANGE)
	}
	if err := exchange(); err != nil {
		_ = os.RemoveAll(placeholder)
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
			return true, fmt.Errorf("atomic created-path rollback is unavailable: %w", err)
		}
		return true, err
	}

	restore := func() error {
		current, statErr := os.Lstat(filename)
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(current, placeholderInfo) {
			return fmt.Errorf("created path changed during rollback")
		}
		return exchange()
	}
	removePlaceholder := func() error {
		current, statErr := os.Lstat(placeholder)
		if statErr != nil {
			return statErr
		}
		if !os.SameFile(current, placeholderInfo) {
			return fmt.Errorf("rollback placeholder changed")
		}
		return os.RemoveAll(placeholder)
	}

	movedInfo, statErr := os.Lstat(placeholder)
	if statErr != nil {
		restoreErr := restore()
		return true, errors.Join(statErr, restoreErr)
	}
	movedHash, hashErr := hashMovedPath(placeholder, isDir)
	if hashErr != nil {
		restoreErr := restore()
		return true, errors.Join(hashErr, restoreErr)
	}
	if movedHash != expectedHash || !os.SameFile(movedInfo, targetInfo) {
		restoreErr := restore()
		cleanupErr := removePlaceholder()
		return true, errors.Join(&ConflictError{Path: relPath, Expected: expectedHash, Actual: movedHash}, restoreErr, cleanupErr)
	}

	// The exchanged target is only the disposable placeholder if its identity
	// is unchanged. An external replacement at filename is left untouched.
	currentTarget, statErr := os.Lstat(filename)
	if statErr != nil {
		return true, statErr
	}
	if os.SameFile(currentTarget, placeholderInfo) {
		if err := os.RemoveAll(filename); err != nil {
			return true, err
		}
	}
	return true, removePlaceholder()
}

func createRollbackPlaceholder(parent string, isDir bool) (string, error) {
	if isDir {
		return os.MkdirTemp(parent, ".obsite-rollback-dir-*")
	}
	file, err := os.CreateTemp(parent, ".obsite-rollback-file-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
