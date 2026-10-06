package server

import (
	"os"
	"path/filepath"
	"strings"
)

func secureOutputPathComponents(relativePath string) ([]string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) || filepath.VolumeName(relativePath) != "" {
		return nil, os.ErrPermission
	}

	cleanPath := filepath.Clean(relativePath)
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return nil, os.ErrPermission
	}

	components := strings.Split(cleanPath, string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, os.ErrPermission
		}
	}
	return components, nil
}
