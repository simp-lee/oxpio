//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package server

import (
	"errors"
	"os"
)

func openOutputParent(string) (*os.File, error) {
	return nil, errors.New("secure output parent opening is unavailable on this platform")
}

func openOutputFileRelative(*os.File, string, string) (*os.File, error) {
	return nil, errors.New("secure output file opening is unavailable on this platform")
}
