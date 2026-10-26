//go:build windows

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsDirectoryAccess = windows.FILE_GENERIC_READ

// OpenDirectoryNoFollow opens every component of name without traversing a
// reparse point. Relative NtCreateFile calls keep the operation attached to
// the previously opened directory handle, even if its path is replaced.
func OpenDirectoryNoFollow(name string) (*os.File, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	absolute = filepath.Clean(absolute)

	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return nil, os.ErrInvalid
	}
	rootPath := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(absolute, volume)
	components := strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' })

	current, err := openWindowsDirectoryPathNoFollow(rootPath)
	if err != nil {
		return nil, err
	}
	for _, component := range components {
		next, openErr := openWindowsDirectoryRelative(current, component, windowsDirectoryAccess)
		closeErr := windows.CloseHandle(current)
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(closeErr, windows.CloseHandle(next))
		}
		current = next
	}

	file := os.NewFile(uintptr(current), absolute)
	if file == nil {
		return nil, errors.Join(os.ErrInvalid, windows.CloseHandle(current))
	}
	return file, nil
}

// OpenDirectoryRelativeNoFollow opens a child directory relative to parent
// without traversing a reparse point in the child name.
func OpenDirectoryRelativeNoFollow(parent *os.File, name string) (*os.File, error) {
	if err := validateDirectoryEntryName(name); err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, os.ErrInvalid
	}
	handle, err := openWindowsDirectoryRelative(windows.Handle(parent.Fd()), name, windowsDirectoryAccess)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name))
	if file == nil {
		return nil, errors.Join(os.ErrInvalid, windows.CloseHandle(handle))
	}
	return file, nil
}

// VerifyDirectoryHandleWithinRoot verifies the physical location of directory
// using the handle itself. This catches a parent that was renamed outside the
// vault after it was opened.
func VerifyDirectoryHandleWithinRoot(root string, directory *os.File) error {
	if directory == nil {
		return os.ErrInvalid
	}
	if err := rejectWindowsReparsePoint(windows.Handle(directory.Fd())); err != nil {
		return err
	}
	actual, err := windowsFinalPath(windows.Handle(directory.Fd()))
	if err != nil {
		return err
	}
	expected, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if !windowsPathWithinRoot(expected, actual) {
		return ErrPathOutsideRoot
	}
	return nil
}

// MkdirRelative creates a single directory entry relative to parent. NtCreateFile
// receives the parent handle as RootDirectory, so replacing the parent path
// cannot redirect the operation.
func MkdirRelative(parent *os.File, name string, mode os.FileMode) error {
	if err := validateDirectoryEntryName(name); err != nil {
		return err
	}
	if parent == nil {
		return os.ErrInvalid
	}
	if err := rejectWindowsReparsePoint(windows.Handle(parent.Fd())); err != nil {
		return err
	}

	handle, err := createWindowsDirectoryRelative(windows.Handle(parent.Fd()), name)
	if err != nil {
		return err
	}
	reparseErr := rejectWindowsReparsePoint(handle)
	closeErr := windows.CloseHandle(handle)
	if reparseErr != nil || closeErr != nil {
		return errors.Join(ErrDirectoryCreated, reparseErr, closeErr)
	}
	return nil
}

// RemoveDirectoryRelative removes an empty directory entry relative to parent
// without following a reparse point or relying on the parent's path.
func RemoveDirectoryRelative(parent *os.File, name string) error {
	if err := validateDirectoryEntryName(name); err != nil {
		return err
	}
	if parent == nil {
		return os.ErrInvalid
	}
	if err := rejectWindowsReparsePoint(windows.Handle(parent.Fd())); err != nil {
		return err
	}

	handle, err := removeWindowsDirectoryRelative(windows.Handle(parent.Fd()), name)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

// CreateDirectoryRelativeWithinRoot creates a child relative to parent after
// verifying that both the parent and the created child remain within root. It
// retains the creation handle for validation and rollback.
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

	handle, err := createWindowsDirectoryRelative(windows.Handle(parent.Fd()), name)
	if err != nil {
		return nil, err
	}
	child := os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name))
	if child == nil {
		closeErr := windows.CloseHandle(handle)
		return nil, errors.Join(ErrDirectoryCreated, os.ErrInvalid, closeErr)
	}
	created := newWindowsCreatedDirectory(root, parent, name, child, handle)
	if err := rejectWindowsReparsePoint(handle); err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	if err := VerifyDirectoryHandleWithinRoot(root, child); err != nil {
		return created, errors.Join(ErrDirectoryCreated, err)
	}
	return created, nil
}

func CheckDirectoryRollbackSupport() error { return nil }

func newWindowsCreatedDirectory(root string, parent *os.File, name string, child *os.File, handle windows.Handle) *CreatedDirectory {
	verify := func() error {
		if err := rejectWindowsReparsePoint(handle); err != nil {
			return err
		}
		if err := VerifyDirectoryHandleWithinRoot(root, parent); err != nil {
			return err
		}
		if err := VerifyDirectoryHandleWithinRoot(root, child); err != nil {
			return err
		}
		current, err := OpenDirectoryRelativeNoFollow(parent, name)
		if err != nil {
			return err
		}
		verifyErr := func() error {
			if err := rejectWindowsReparsePoint(windows.Handle(current.Fd())); err != nil {
				return err
			}
			createdInfo, err := windowsDirectoryFileIdentity(handle)
			if err != nil {
				return err
			}
			currentInfo, err := windowsDirectoryFileIdentity(windows.Handle(current.Fd()))
			if err != nil {
				return err
			}
			if createdInfo != currentInfo {
				return ErrDirectoryIdentityChanged
			}
			return nil
		}()
		return errors.Join(verifyErr, current.Close())
	}
	rollback := func() error {
		deleteInfo := fileDispositionInformation{DeleteFile: 1}
		deleteErr := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&deleteInfo)), uint32(unsafe.Sizeof(deleteInfo)))
		closeErr := child.Close()
		return errors.Join(deleteErr, closeErr)
	}
	return newCreatedDirectory(child, verify, rollback)
}

type windowsDirectoryIdentity struct {
	volume uint32
	high   uint32
	low    uint32
}

func windowsDirectoryFileIdentity(handle windows.Handle) (windowsDirectoryIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return windowsDirectoryIdentity{}, err
	}
	return windowsDirectoryIdentity{volume: info.VolumeSerialNumber, high: info.FileIndexHigh, low: info.FileIndexLow}, nil
}

type fileDispositionInformation struct {
	DeleteFile byte
}

func openWindowsDirectoryPathNoFollow(path string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(
		name,
		windowsDirectoryAccess,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := rejectWindowsReparsePoint(handle); err != nil {
		return windows.InvalidHandle, errors.Join(err, windows.CloseHandle(handle))
	}
	return handle, nil
}

func openWindowsDirectoryRelative(parent windows.Handle, name string, access uint32) (windows.Handle, error) {
	handle, err := openWindowsRelative(parent, name, access, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := rejectWindowsReparsePoint(handle); err != nil {
		return windows.InvalidHandle, errors.Join(err, windows.CloseHandle(handle))
	}
	return handle, nil
}

func createWindowsDirectoryRelative(parent windows.Handle, name string) (windows.Handle, error) {
	return openWindowsRelative(parent, name, windowsDirectoryAccess|windows.DELETE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT)
}

func removeWindowsDirectoryRelative(parent windows.Handle, name string) (windows.Handle, error) {
	return openWindowsRelative(parent, name, windows.DELETE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_DELETE_ON_CLOSE)
}

func openWindowsRelative(parent windows.Handle, name string, access uint32, disposition uint32, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	objectAttributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	var ioStatus windows.IO_STATUS_BLOCK
	var allocationSize int64
	var handle windows.Handle
	statusErr := windows.NtCreateFile(
		&handle,
		access,
		objectAttributes,
		&ioStatus,
		&allocationSize,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition,
		options,
		0,
		0,
	)
	if statusErr != nil {
		return windows.InvalidHandle, translateWindowsNTError(statusErr)
	}
	return handle, nil
}

func rejectWindowsReparsePoint(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return ErrSymlinkPath
	}
	return nil
}

func translateWindowsNTError(err error) error {
	status, ok := err.(windows.NTStatus)
	if !ok {
		return err
	}
	switch status {
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return os.ErrExist
	case windows.STATUS_REPARSE,
		windows.STATUS_REPARSE_OBJECT,
		windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED,
		windows.STATUS_REPARSE_POINT_NOT_RESOLVED,
		windows.STATUS_DIRECTORY_IS_A_REPARSE_POINT,
		windows.STATUS_REPARSE_POINT_ENCOUNTERED:
		return ErrSymlinkPath
	}
	winErr := status.Errno()
	if winErr == windows.ERROR_FILE_NOT_FOUND || winErr == windows.ERROR_PATH_NOT_FOUND {
		return os.ErrNotExist
	}
	return winErr
}

func windowsFinalPath(handle windows.Handle) (string, error) {
	for size := uint32(256); size <= 1<<16; size *= 2 {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], size, 0)
		if err != nil {
			if err == windows.ERROR_INSUFFICIENT_BUFFER {
				continue
			}
			return "", err
		}
		if length < size {
			return normalizeWindowsPath(windows.UTF16ToString(buffer[:length])), nil
		}
	}
	return "", windows.ERROR_INSUFFICIENT_BUFFER
}

func normalizeWindowsPath(path string) string {
	path = filepath.Clean(path)
	lower := strings.ToLower(path)
	switch {
	case strings.HasPrefix(lower, `\\?\unc\`):
		path = `\\` + path[len(`\\?\UNC\`):]
	case strings.HasPrefix(lower, `\\?\`):
		path = path[len(`\\?\`):]
	case strings.HasPrefix(lower, `\??\`):
		path = path[len(`\??\`):]
	}
	return filepath.Clean(path)
}

func windowsPathWithinRoot(root, candidate string) bool {
	root = normalizeWindowsPath(root)
	candidate = normalizeWindowsPath(candidate)
	if strings.EqualFold(root, candidate) {
		return true
	}
	rootLower := strings.ToLower(root)
	candidateLower := strings.ToLower(candidate)
	if strings.HasSuffix(root, string(filepath.Separator)) {
		return strings.HasPrefix(candidateLower, rootLower)
	}
	return strings.HasPrefix(candidateLower, rootLower+string(filepath.Separator))
}

func validateDirectoryEntryName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.IsAbs(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\\:`) {
		return os.ErrInvalid
	}
	return nil
}
