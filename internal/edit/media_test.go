package edit

import (
	"path/filepath"
	"testing"
)

func TestValidateMediaPathRejectsProtectedEntries(t *testing.T) {
	for _, pathValue := range []string{
		"../image.png",
		".hidden/image.png",
		".git/image.png",
		"node_modules/image.png",
		"uploads\\image.png",
		"uploads/image.txt",
	} {
		if err := validateMediaPath(pathValue); err == nil {
			t.Fatalf("validateMediaPath(%q) succeeded", pathValue)
		}
	}
}

func TestValidateManagedBoundaryRejectsOutputDescendant(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(vault, "public")
	if err := validateManagedBoundary(vault, output, "public/image.png"); err == nil {
		t.Fatal("output descendant was accepted")
	}
}
