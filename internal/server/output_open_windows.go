//go:build windows

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func openOutputParent(parentPath string) (*os.File, error) {
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, err
	}
	finalPath, err := finalWindowsPath(parent)
	if err != nil {
		return nil, errors.Join(err, parent.Close())
	}
	if !sameWindowsPath(finalPath, parentPath) {
		return nil, errors.Join(os.ErrPermission, parent.Close())
	}
	return parent, nil
}

func openOutputFileRelative(parent *os.File, rootName string, relativePath string) (*os.File, error) {
	if parent == nil {
		return nil, os.ErrNotExist
	}
	if _, err := secureOutputPathComponents(relativePath); err != nil {
		return nil, err
	}

	rootPath := filepath.Join(parent.Name(), rootName)
	root, err := os.Open(rootPath)
	if err != nil {
		return nil, err
	}

	filePath := filepath.Join(rootPath, relativePath)
	file, err := os.Open(filePath)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return nil, closeWindowsOutputFiles(file, root, err)
	}
	if !info.Mode().IsRegular() {
		return nil, closeWindowsOutputFiles(file, root, os.ErrPermission)
	}

	parentFinalPath, err := finalWindowsPath(parent)
	if err != nil {
		return nil, closeWindowsOutputFiles(file, root, err)
	}
	rootFinalPath, err := finalWindowsPath(root)
	if err != nil {
		return nil, closeWindowsOutputFiles(file, root, err)
	}
	fileFinalPath, err := finalWindowsPath(file)
	if err != nil {
		return nil, closeWindowsOutputFiles(file, root, err)
	}
	if !sameWindowsPath(rootFinalPath, filepath.Join(parentFinalPath, rootName)) || !pathWithinWindowsRoot(rootFinalPath, fileFinalPath) {
		return nil, closeWindowsOutputFiles(file, root, os.ErrPermission)
	}
	if err := root.Close(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func closeWindowsOutputFiles(file, root *os.File, err error) error {
	return errors.Join(err, file.Close(), root.Close())
}

func finalWindowsPath(file *os.File) (string, error) {
	for size := uint32(256); ; size *= 2 {
		buffer := make([]uint16, size)
		length, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], size, 0)
		if err != nil {
			return "", err
		}
		if length < size-1 {
			return windows.UTF16ToString(buffer[:length]), nil
		}
	}
}

func sameWindowsPath(left string, right string) bool {
	return strings.EqualFold(normalizeWindowsPath(left), normalizeWindowsPath(right))
}

func normalizeWindowsPath(value string) string {
	cleaned := filepath.Clean(value)
	if strings.HasPrefix(cleaned, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(cleaned, `\\?\UNC\`)
	}
	return strings.TrimPrefix(cleaned, `\\?\`)
}

func pathWithinWindowsRoot(root string, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
