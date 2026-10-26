package fsutil

import (
	"errors"
	"os"
)

// ErrSecureDirectoryMutationUnavailable indicates that the platform does not
// provide a primitive that atomically combines directory-handle-relative mkdir
// with root containment.
var ErrSecureDirectoryMutationUnavailable = errors.New("secure relative directory creation is unavailable")

// ErrDirectoryCreated indicates that a relative mkdir created its entry before
// a later containment check failed. Callers must roll it back through the
// CreatedDirectory returned by CreateDirectoryRelativeWithinRoot.
var ErrDirectoryCreated = errors.New("directory was created before secure mkdir failed")

// ErrDirectoryIdentityChanged indicates that the entry at the requested name
// no longer refers to the object created by this operation.
var ErrDirectoryIdentityChanged = errors.New("created directory identity changed")

// CreatedDirectory owns the handle and identity proof for a directory created
// by CreateDirectoryRelativeWithinRoot. Callers must Close it after a
// successful mutation or Rollback it after any later failure.
type CreatedDirectory struct {
	file     *os.File
	verify   func() error
	rollback func() error
}

func newCreatedDirectory(file *os.File, verify func() error, rollback func() error) *CreatedDirectory {
	return &CreatedDirectory{file: file, verify: verify, rollback: rollback}
}

// File returns the handle for the created directory while it is owned by the
// caller.
func (directory *CreatedDirectory) File() *os.File {
	if directory == nil {
		return nil
	}
	return directory.file
}

// Verify checks that the name still refers to the object held by this result.
func (directory *CreatedDirectory) Verify() error {
	if directory == nil || directory.file == nil || directory.verify == nil {
		return os.ErrInvalid
	}
	return directory.verify()
}

// Rollback removes only the object whose identity was captured at creation.
func (directory *CreatedDirectory) Rollback() error {
	if directory == nil || directory.rollback == nil {
		return os.ErrInvalid
	}
	return directory.rollback()
}

// Close performs one final identity check before releasing the creation
// handle. A close or identity error is reported as a post-create error so the
// caller can still attempt the identity-checked rollback.
func (directory *CreatedDirectory) Close() error {
	if directory == nil || directory.file == nil {
		return os.ErrInvalid
	}
	if err := directory.Verify(); err != nil {
		return errors.Join(ErrDirectoryCreated, err)
	}
	if err := directory.file.Close(); err != nil {
		return errors.Join(ErrDirectoryCreated, err)
	}
	directory.file = nil
	return nil
}

// CheckDirectoryMutationSupport verifies that a parent can be opened and
// physically proven to remain under root without creating anything.
func CheckDirectoryMutationSupport(root, parentPath string) error {
	parent, err := OpenDirectoryNoFollow(parentPath)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return VerifyDirectoryHandleWithinRoot(root, parent)
}

// CreateDirectoryRelativeWithinRoot is implemented by each supported secure
// filesystem backend. Its result is non-nil when creation succeeded, even if
// a later check failed, so the caller can roll back by identity.

// MkdirRelativeWithinRoot preserves the original error-only API for callers
// that do not need to inspect the created handle.
func MkdirRelativeWithinRoot(root string, parent *os.File, name string, mode os.FileMode) error {
	directory, err := CreateDirectoryRelativeWithinRoot(root, parent, name, mode)
	if directory == nil {
		return err
	}
	if err != nil {
		return errors.Join(err, directory.Rollback())
	}
	if err := directory.Close(); err != nil {
		return errors.Join(err, directory.Rollback())
	}
	return nil
}
