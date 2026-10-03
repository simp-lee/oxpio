//go:build !linux && !darwin && !windows

package edit

import (
	"errors"
	"os"
)

func ensureNoReplaceRename() error {
	return errors.New("atomic no-replace rename is unavailable on this platform")
}

func renamePathReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func renamePathNoReplace(source, destination string) error {
	return errors.New("atomic no-replace rename is unavailable on this platform")
}
