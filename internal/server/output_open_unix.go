//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openOutputParent(parentPath string) (*os.File, error) {
	absolutePath, err := filepath.Abs(parentPath)
	if err != nil || filepath.VolumeName(absolutePath) != "" {
		return nil, os.ErrPermission
	}

	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	currentFD := rootFD
	components := strings.Split(strings.TrimPrefix(filepath.Clean(absolutePath), string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		closeErr := unix.Close(currentFD)
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(closeErr, unix.Close(nextFD))
		}
		currentFD = nextFD
	}

	parent := os.NewFile(uintptr(currentFD), filepath.Clean(absolutePath))
	if parent == nil {
		return nil, errors.Join(os.ErrInvalid, unix.Close(currentFD))
	}
	return parent, nil
}

func openOutputFileRelative(parent *os.File, rootName string, relativePath string) (*os.File, error) {
	if parent == nil {
		return nil, os.ErrNotExist
	}
	components, err := secureOutputPathComponents(relativePath)
	if err != nil {
		return nil, err
	}

	rootFD, err := unix.Openat(int(parent.Fd()), rootName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	currentFD := rootFD
	for _, component := range components[:len(components)-1] {
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		closeErr := unix.Close(currentFD)
		if openErr != nil {
			return nil, errors.Join(openErr, closeErr)
		}
		if closeErr != nil {
			return nil, errors.Join(closeErr, unix.Close(nextFD))
		}
		currentFD = nextFD
	}

	fileFD, err := unix.Openat(currentFD, components[len(components)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	closeErr := unix.Close(currentFD)
	if err != nil {
		return nil, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return nil, errors.Join(closeErr, unix.Close(fileFD))
	}

	file := os.NewFile(uintptr(fileFD), filepath.Join(rootName, relativePath))
	if file == nil {
		return nil, errors.Join(os.ErrInvalid, unix.Close(fileFD))
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(os.ErrPermission, file.Close())
	}
	return file, nil
}
