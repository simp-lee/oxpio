//go:build !linux

package workspace

import "errors"

var ErrAtomicityUnavailable = errors.New("OUTPUT_ATOMICITY_UNAVAILABLE")

func AtomicReplaceDirectory(string, string) error { return ErrAtomicityUnavailable }
func ProbeAtomicReplace(string) error             { return ErrAtomicityUnavailable }
