package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestEditCommandBuildsBeforeListeningAndEnablesReload(t *testing.T) {
	vault := t.TempDir()
	output := filepath.Join(t.TempDir(), "public")
	deps := testCommandDependencies()
	server := &fakePreviewServer{}
	var builtVault, builtOutput string
	deps.buildSiteWithOptions = func(gotVault, gotOutput string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
		builtVault, builtOutput = gotVault, gotOutput
		return &internalbuild.BuildResult{}, nil
	}
	deps.newEditServer = func(gotVault, gotOutput string, port int, _ *model.SourceCatalog) (previewServer, error) {
		if gotVault != vault || gotOutput != output || port != 9090 {
			t.Fatalf("edit server args = %q, %q, %d", gotVault, gotOutput, port)
		}
		return server, nil
	}
	_, _, err := executeForTest(t, deps, []string{"edit", "--vault", vault, "--output", output, "--port", "9090"})
	if err != nil {
		t.Fatal(err)
	}
	if builtVault != vault || builtOutput != output {
		t.Fatalf("build args = %q, %q", builtVault, builtOutput)
	}
	if server.enableCalls != 1 || server.listenCalls != 1 {
		t.Fatalf("server calls = enable %d, listen %d", server.enableCalls, server.listenCalls)
	}
}

func TestEditSetupRejectsServiceFlagsAndNonInteractiveInputWithoutBuild(t *testing.T) {
	for _, args := range [][]string{{"edit", "--setup", "--output", "site"}, {"edit", "--setup", "--port", "9090"}} {
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil || !strings.Contains(err.Error(), "accepts only --vault") {
			t.Fatalf("args %v error = %v", args, err)
		}
	}
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"edit", "--setup", "--vault", vault})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-interactive setup error = %v", err)
	}
}

func TestEditCommandRequiresConfiguredAccountBeforeListening(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	if err := os.WriteFile(filepath.Join(vault, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\nHome\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "public")
	_, _, err := executeForTest(t, defaultCommandDependencies(), []string{"edit", "--vault", vault, "--output", output, "--port", "18080"})
	if err == nil || !strings.Contains(err.Error(), "edit.username and edit.passwordHash must be configured") {
		t.Fatalf("missing account error = %v", err)
	}
	if _, statErr := os.Stat(output); statErr != nil {
		t.Fatalf("initial build output stat = %v", statErr)
	}
}

func TestEditCommandRejectsInvalidExplicitPort(t *testing.T) {
	for _, value := range []string{"0", "65536", "-1"} {
		_, _, err := executeForTest(t, testCommandDependencies(), []string{"edit", "--port", value})
		if err == nil || !strings.Contains(err.Error(), "port must be between 1 and 65535") {
			t.Fatalf("port %s error = %v", value, err)
		}
	}
}
