//go:build windows

package edit

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func atomicReplace(source, destination string) error {
	sourceName, err := windows.UTF16PtrFromString(filepath.Clean(source))
	if err != nil {
		return err
	}
	destinationName, err := windows.UTF16PtrFromString(filepath.Clean(destination))
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(sourceName, destinationName, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}
