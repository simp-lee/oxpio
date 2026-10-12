//go:build !windows

package edit

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
