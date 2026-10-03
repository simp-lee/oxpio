//go:build !linux

package edit

// Other platforms use the existing checked rollback path. Their native atomic
// exchange APIs differ and are kept out of the shared editor implementation.
func removeCreatedPathAtomic(filename, relPath, expectedHash string, isDir bool) (bool, error) {
	return false, nil
}
