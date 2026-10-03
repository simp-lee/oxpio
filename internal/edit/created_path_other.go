//go:build !linux

package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// removeCreatedPathAtomic uses a same-directory rename so an external
// replacement made after the rename is never removed by rollback. If a race
// occurs before the rename, it restores the displaced path only when the
// destination remains absent; it never overwrites an external replacement.
func removeCreatedPathAtomic(filename, relPath, expectedHash string, isDir bool) (bool, error) {
	if err := ensureNoReplaceRename(); err != nil {
		return false, err
	}
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
	if err := renamePathReplace(filename, placeholder); err != nil {
		_ = os.RemoveAll(placeholder)
		return true, err
	}

	restoreIfAbsent := func() error {
		if _, statErr := os.Lstat(filename); errors.Is(statErr, os.ErrNotExist) {
			return renamePathNoReplace(placeholder, filename)
		} else if statErr != nil {
			return statErr
		}
		return fmt.Errorf("created path changed during rollback")
	}
	movedInfo, statErr := os.Lstat(placeholder)
	if statErr != nil {
		restoreErr := restoreIfAbsent()
		return true, errors.Join(statErr, restoreErr)
	}
	movedHash, hashErr := hashMovedPath(placeholder, isDir)
	if hashErr != nil {
		restoreErr := restoreIfAbsent()
		return true, errors.Join(hashErr, restoreErr)
	}
	if movedHash != expectedHash || !os.SameFile(movedInfo, targetInfo) {
		restoreErr := restoreIfAbsent()
		return true, errors.Join(&ConflictError{Path: relPath, Expected: expectedHash, Actual: movedHash}, restoreErr)
	}

	return true, removeIdentityPath(placeholder, targetInfo, expectedHash, isDir)
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
	if err := renamePathReplace(filename, tombstone); err != nil {
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
			if restoreErr := renamePathNoReplace(tombstone, filename); restoreErr != nil {
				return errors.Join(fmt.Errorf("rollback identity changed"), restoreErr)
			}
		}
		return fmt.Errorf("rollback identity changed")
	}
	return os.RemoveAll(tombstone)
}
