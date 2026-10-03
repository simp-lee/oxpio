//go:build !linux && !darwin && !windows

package edit

import "errors"

func ensureNoReplaceRename() error {
	return errors.New("atomic no-replace rename is unavailable on this platform")
}

func renamePathNoReplace(source, destination string) error {
	return errors.New("atomic no-replace rename is unavailable on this platform")
}
