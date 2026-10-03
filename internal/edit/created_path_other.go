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
	if err := os.Rename(filename, placeholder); err != nil {
		_ = os.RemoveAll(placeholder)
		return true, err
	}

	restoreIfAbsent := func() error {
		if _, statErr := os.Lstat(filename); errors.Is(statErr, os.ErrNotExist) {
			return os.Rename(placeholder, filename)
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

	if current, statErr := os.Lstat(filename); statErr == nil {
		// An external replacement owns the public path; retain it.
		if !os.SameFile(current, targetInfo) {
			return true, removeIdentityOther(placeholder, targetInfo)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return true, statErr
	}
	return true, removeIdentityOther(placeholder, targetInfo)
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

func removeIdentityOther(filename string, expectedInfo os.FileInfo) error {
	current, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if !os.SameFile(current, expectedInfo) {
		return fmt.Errorf("rollback path changed")
	}
	return os.RemoveAll(filename)
}
