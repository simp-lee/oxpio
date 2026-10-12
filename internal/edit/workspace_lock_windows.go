//go:build windows

package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquireWorkspaceLock(vault string) (func() error, error) {
	lockPath := filepath.Join(vault, workspaceLockFilename)
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock %q: %w", lockPath, err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open workspace lock %q: %w", lockPath, err)
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect workspace lock %q: %w", lockPath, err)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("workspace lock %q must not be a symbolic link or reparse point", lockPath)
	}
	file := os.NewFile(uintptr(handle), lockPath)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open workspace lock %q: create file handle", lockPath)
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		_ = file.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, fmt.Errorf("workspace busy: %s", lockPath)
		}
		return nil, fmt.Errorf("lock workspace %q: %w", lockPath, err)
	}
	return func() error {
		return errors.Join(windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped), file.Close())
	}, nil
}
