//go:build windows

package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquireConfigLock(vaultRoot string) (func() error, error) {
	lockPath := filepath.Join(vaultRoot, configLockFilename)
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open config lock %q: %w", lockPath, err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open config lock %q: %w", lockPath, err)
	}
	var handleInfo windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &handleInfo); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect config lock %q: %w", lockPath, err)
	}
	if handleInfo.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("config lock %q must not be a symbolic link or reparse point", lockPath)
	}
	file := os.NewFile(uintptr(handle), lockPath)
	if file == nil {
		_ = windows.CloseHandle(handle)
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
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock config %q: %w", lockPath, err)
	}

	return func() error {
		unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}
