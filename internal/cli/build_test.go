package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
)

func TestBuildCommandUsesAnalyzerOwnedBuildInput(t *testing.T) {
	vaultPath := t.TempDir()
	t.Chdir(vaultPath)
	writeCLIConfig(t, vaultPath)
	deps := testCommandDependencies()
	called := false
	deps.buildSiteWithOptions = func(vault, output string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
		called = true
		if vault != vaultPath || output != filepath.Join(vaultPath, "public") || options.Strict || options.DiagnosticsWriter == nil {
			t.Fatalf("build arguments = %q, %q, %#v", vault, output, options)
		}
		return &internalbuild.BuildResult{}, nil
	}
	if _, _, err := executeForTest(t, deps, []string{"build"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("build callback was not called")
	}
}

func TestBuildCommandRejectsForceAsRemovedFlag(t *testing.T) {
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"build", "--force"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --force") {
		t.Fatalf("error = %v, want removed force flag rejection", err)
	}
}

func TestBuildCommandRejectsRemovedConfigFlag(t *testing.T) {
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"build", "--config", "other.yaml"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --config") {
		t.Fatalf("error = %v, want removed config flag rejection", err)
	}
}

func writeCLIConfig(t *testing.T, vault string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte("title: Garden\nbaseURL: https://example.com/\nnavigation: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
