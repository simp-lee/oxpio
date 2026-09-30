//go:build !linux && !darwin && !windows

package edit

import "os"

func renamePathNoReplace(source, destination string) error {
	return os.Rename(source, destination)
}
