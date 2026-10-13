//go:build !linux && !darwin && !windows

package fsutil

import "errors"

// RenameNoReplace fails closed on platforms without a native atomic
// no-replace primitive. A preflight check followed by os.Rename is not safe.
func RenameNoReplace(source, destination string) error {
	return errors.New("atomic no-replace rename is unavailable on this platform")
}
