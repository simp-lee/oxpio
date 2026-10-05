// Package publishpath defines vault paths that must never be published as site assets.
package publishpath

import (
	"path"
	"strings"
)

const (
	configFilename     = "oxpio.yaml"
	configLockFilename = ".oxpio-config.lock"
)

// IsReservedPath reports whether relPath names a vault control path rather than
// publishable site content. Control directories and OXPIO transaction names
// are reserved at any depth; oxpio.yaml is reserved only at the vault root.
func IsReservedPath(relPath string) bool {
	normalized := strings.TrimSpace(strings.ReplaceAll(relPath, `\`, "/"))
	normalized = strings.TrimPrefix(normalized, "/")
	if normalized == "" {
		return false
	}

	normalized = path.Clean(normalized)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return false
	}
	if strings.EqualFold(normalized, configFilename) || strings.EqualFold(normalized, configLockFilename) {
		return true
	}
	base := strings.ToLower(path.Base(normalized))
	if strings.HasPrefix(base, ".oxpio-") || (strings.HasPrefix(base, ".") && strings.Contains(base, "-oxpio-")) {
		return true
	}

	for _, segment := range strings.Split(normalized, "/") {
		switch strings.ToLower(segment) {
		case "node_modules", ".git", ".obsidian", ".oxpio":
			return true
		}
	}
	return false
}
