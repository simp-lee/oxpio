//go:build ios

package fsutil

import "os"

// iOS does not expose a stable path inspection primitive for a directory
// descriptor, so fail closed rather than creating a directory without a root
// containment proof.
func VerifyDirectoryHandleWithinRoot(string, *os.File) error {
	return ErrSecureDirectoryMutationUnavailable
}
