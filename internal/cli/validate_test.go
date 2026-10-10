package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDoesNotCreateFormalOutput(t *testing.T) {
	vault := t.TempDir()
	if _, _, err := executeForTest(t, testCommandDependencies(), []string{"init", "--vault", vault}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeForTest(t, defaultCommandDependencies(), []string{"validate", "--vault", vault}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vault, "public")); !os.IsNotExist(err) {
		t.Fatalf("validate created output: %v", err)
	}
}

func TestValidateReportsInputFailureWithoutWritingOutput(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := executeForTest(t, defaultCommandDependencies(), []string{"validate", "--vault", vault})
	if err == nil || !strings.Contains(stderr, "error") {
		t.Fatalf("validate = %v, stderr %q", err, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(vault, "public")); !os.IsNotExist(statErr) {
		t.Fatalf("failed validate created output: %v", statErr)
	}
}
