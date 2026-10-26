//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package fsutil

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

var rollbackDirectorySequence atomic.Uint64

// removeDirectoryRelativeIfIdentity moves the entry away with atomic rename
// operations before removing it, so the public name is never unlinked while
// it can still refer to an object other than the one created by this operation.
func removeDirectoryRelativeIfIdentity(parent *os.File, name string, expected unixDirectoryIdentity) error {
	current, err := unixDirectoryEntryIdentity(parent, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if current != expected {
		return ErrDirectoryIdentityChanged
	}

	placeholder, placeholderIdentity, err := createRollbackDirectory(parent)
	if err != nil {
		return err
	}
	exchange := func() error {
		return exchangeDirectoryEntries(parent, name, placeholder)
	}
	if err := exchange(); err != nil {
		// The placeholder identity cannot be established after an exchange
		// failure, so leave it untouched rather than removing a replacement.
		return err
	}

	moved, err := unixDirectoryEntryIdentity(parent, placeholder)
	if err != nil || moved != expected {
		restoreErr := exchange()
		return errors.Join(ErrDirectoryIdentityChanged, err, restoreErr)
	}
	if err := RemoveDirectoryRelative(parent, placeholder); err != nil {
		restoreErr := exchange()
		return errors.Join(err, restoreErr)
	}

	if err := moveDirectoryNoReplace(parent, name, placeholder); err != nil {
		return err
	}
	moved, err = unixDirectoryEntryIdentity(parent, placeholder)
	if err != nil {
		return err
	}
	if moved != placeholderIdentity {
		if _, statErr := unixDirectoryEntryIdentity(parent, name); errors.Is(statErr, unix.ENOENT) {
			_ = moveDirectoryNoReplace(parent, placeholder, name)
		}
		return ErrDirectoryIdentityChanged
	}
	return RemoveDirectoryRelative(parent, placeholder)
}

func createRollbackDirectory(parent *os.File) (string, unixDirectoryIdentity, error) {
	for attempt := 0; attempt < 100; attempt++ {
		name := ".oxpio-rollback-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(rollbackDirectorySequence.Add(1), 10)
		err := MkdirRelative(parent, name, 0o700)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", unixDirectoryIdentity{}, err
		}
		identity, identityErr := unixDirectoryEntryIdentity(parent, name)
		if identityErr != nil {
			return name, unixDirectoryIdentity{}, errors.Join(ErrDirectoryCreated, identityErr)
		}
		return name, identity, nil
	}
	return "", unixDirectoryIdentity{}, fmt.Errorf("could not allocate a private rollback directory")
}
