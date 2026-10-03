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
	placeholderHash, err := hashMovedPath(placeholder, isDir)
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
	movedInfo, statErr := os.Lstat(placeholder)
	if statErr != nil {
		restoreErr := restore()
		cleanupErr := removeIdentityPath(placeholder, placeholderInfo, placeholderHash, isDir)
		return true, errors.Join(statErr, restoreErr, cleanupErr)
	}
	movedHash, hashErr := hashMovedPath(placeholder, isDir)
	if hashErr != nil {
		restoreErr := restore()
		cleanupErr := removeIdentityPath(placeholder, placeholderInfo, placeholderHash, isDir)
		return true, errors.Join(hashErr, restoreErr, cleanupErr)
	}
	if movedHash != expectedHash || !os.SameFile(movedInfo, targetInfo) {
		restoreErr := restore()
		cleanupErr := removeIdentityPath(placeholder, placeholderInfo, placeholderHash, isDir)
		return true, errors.Join(&ConflictError{Path: relPath, Expected: expectedHash, Actual: movedHash}, restoreErr, cleanupErr)
	}

	// The exchanged target is only the disposable placeholder if its identity
	// is unchanged. An external replacement at filename is left untouched.
	currentTarget, statErr := os.Lstat(filename)
	removeOriginal := removeIdentityPath(placeholder, targetInfo, expectedHash, isDir)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return true, errors.Join(removeOriginal)
		}
		return true, errors.Join(statErr, removeOriginal)
	}
	if os.SameFile(currentTarget, placeholderInfo) {
		removePublic := removeIdentityPath(filename, placeholderInfo, placeholderHash, isDir)
		return true, errors.Join(removePublic, removeOriginal)
	}
	return true, removeOriginal
}

func removeIdentityPath(filename string, expectedInfo os.FileInfo, expectedHash string, isDir bool) error {
	current, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(current, expectedInfo) {
		return fmt.Errorf("rollback path changed")
	}
	actualHash, err := hashMovedPath(filename, isDir)
	if err != nil {
		return err
	}
	if actualHash != expectedHash {
		return fmt.Errorf("rollback path contents changed")
	}
	tombstone, err := createRollbackPlaceholder(filepath.Dir(filename), isDir)
	if err != nil {
		return err
	}
	if err := os.Rename(filename, tombstone); err != nil {
		_ = os.RemoveAll(tombstone)
		return err
	}
	moved, err := os.Lstat(tombstone)
	if err != nil {
		return err
	}
	movedHash, err := hashMovedPath(tombstone, isDir)
	if err != nil {
		return err
	}
	if !os.SameFile(moved, expectedInfo) || movedHash != expectedHash {
		if _, statErr := os.Lstat(filename); errors.Is(statErr, os.ErrNotExist) {
			if restoreErr := os.Rename(tombstone, filename); restoreErr != nil {
				return errors.Join(fmt.Errorf("rollback identity changed"), restoreErr)
			}
		}
		return fmt.Errorf("rollback identity changed")
	}
	return os.RemoveAll(tombstone)
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
