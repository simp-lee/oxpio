//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// OpenDirectoryNoFollow opens every component of name without following
// symbolic links. The returned descriptor remains attached to the directory
// even if its path is renamed or replaced after this call.
func OpenDirectoryNoFollow(name string) (*os.File, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	absolute = filepath.Clean(absolute)

	currentFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	components := strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		closeErr := unix.Close(currentFD)
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(closeErr, unix.Close(nextFD))
		}
		currentFD = nextFD
	}

	file := os.NewFile(uintptr(currentFD), absolute)
	if file == nil {
		return nil, errors.Join(os.ErrInvalid, unix.Close(currentFD))
	}
	return file, nil
}

// OpenDirectoryRelativeNoFollow opens a child directory relative to parent
// without following a symbolic link in the child name.
func OpenDirectoryRelativeNoFollow(parent *os.File, name string) (*os.File, error) {
	if err := validateDirectoryEntryName(name); err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, os.ErrInvalid
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name))
	if file == nil {
		return nil, errors.Join(os.ErrInvalid, unix.Close(fd))
	}
	return file, nil
}

// MkdirRelative creates a single directory entry relative to parent. The
// parent descriptor, rather than its path, determines where the entry is
// created, so replacing the parent path cannot redirect the operation.
func MkdirRelative(parent *os.File, name string, mode os.FileMode) error {
	if err := validateDirectoryEntryName(name); err != nil {
		return err
	}
	if parent == nil {
		return os.ErrInvalid
	}
	return unix.Mkdirat(int(parent.Fd()), name, uint32(mode.Perm()))
}

// RemoveDirectoryRelative removes an empty directory entry relative to parent
// without following a symbolic link or relying on the parent's path.
func RemoveDirectoryRelative(parent *os.File, name string) error {
	if err := validateDirectoryEntryName(name); err != nil {
		return err
	}
	if parent == nil {
		return os.ErrInvalid
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

// CreateDirectoryRelativeWithinRoot creates a child relative to parent after
// verifying that both the parent and the created child remain within root. It
// retains the child handle and the entry identity for validation and rollback.
func CreateDirectoryRelativeWithinRoot(root string, parent *os.File, name string, mode os.FileMode) (*CreatedDirectory, error) {
	if err := validateDirectoryEntryName(name); err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, os.ErrInvalid
	}
	if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
		return nil, err
	}
	if err := MkdirRelative(parent, name, mode); err != nil {
		return nil, err
	}

	identity, identityErr := unixDirectoryEntryIdentity(parent, name)
	child, openErr := OpenDirectoryRelativeNoFollow(parent, name)
	created := newUnixCreatedDirectory(root, parent, name, identity, identityErr == nil, child)
	if identityErr != nil {
		return created, errors.Join(ErrDirectoryCreated, identityErr)
	}
	if openErr != nil {
		return created, errors.Join(ErrDirectoryCreated, openErr)
	}
	childIdentity, err := unixDirectoryHandleIdentity(child)
	if err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	if childIdentity != identity {
		return created, errors.Join(ErrDirectoryCreated, ErrDirectoryIdentityChanged)
	}
	if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	if err := VerifyDirectoryHandleWithinRoot(root, child); err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	return created, nil
}

type unixDirectoryIdentity struct {
	device uint64
	inode  uint64
}

func unixDirectoryEntryIdentity(parent *os.File, name string) (unixDirectoryIdentity, error) {
	var info unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unixDirectoryIdentity{}, err
	}
	return unixDirectoryIdentity{device: uint64(info.Dev), inode: uint64(info.Ino)}, nil
}

func unixDirectoryHandleIdentity(directory *os.File) (unixDirectoryIdentity, error) {
	var info unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &info); err != nil {
		return unixDirectoryIdentity{}, err
	}
	return unixDirectoryIdentity{device: uint64(info.Dev), inode: uint64(info.Ino)}, nil
}

func newUnixCreatedDirectory(root string, parent *os.File, name string, identity unixDirectoryIdentity, identityKnown bool, child *os.File) *CreatedDirectory {
	verify := func() error {
		if !identityKnown || child == nil {
			return ErrDirectoryIdentityChanged
		}
		if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
			return err
		}
		if err := VerifyDirectoryHandleWithinRoot(root, child); err != nil {
			return err
		}
		current, err := unixDirectoryEntryIdentity(parent, name)
		if err != nil {
			return err
		}
		handleIdentity, err := unixDirectoryHandleIdentity(child)
		if err != nil {
			return err
		}
		if current != identity || handleIdentity != identity {
			return ErrDirectoryIdentityChanged
		}
		return nil
	}
	rollback := func() error {
		var rollbackErr error
		if !identityKnown {
			rollbackErr = ErrDirectoryIdentityChanged
		} else {
			current, err := unixDirectoryEntryIdentity(parent, name)
			missing := errors.Is(err, unix.ENOENT)
			if missing {
				// The created object is already gone; there is no competing
				// object to remove.
			} else if err != nil {
				rollbackErr = err
			} else if current != identity {
				rollbackErr = ErrDirectoryIdentityChanged
			} else if child != nil {
				handleIdentity, handleErr := unixDirectoryHandleIdentity(child)
				if handleErr != nil {
					rollbackErr = handleErr
				} else if handleIdentity != identity {
					rollbackErr = ErrDirectoryIdentityChanged
				}
			}
			if rollbackErr == nil && !missing {
				rollbackErr = removeDirectoryRelativeIfIdentity(parent, name, identity)
			}
		}
		if child != nil {
			rollbackErr = errors.Join(rollbackErr, child.Close())
		}
		return rollbackErr
	}
	return newCreatedDirectory(child, verify, rollback)
}

func validateDirectoryEntryName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name || strings.ContainsRune(name, filepath.Separator) {
		return os.ErrInvalid
	}
	return nil
}
