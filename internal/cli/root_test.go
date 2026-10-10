package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
)

func TestExecuteShowsRootHelp(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), nil)
	if err != nil {
		t.Fatalf("executeForTest() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}

	for _, want := range []string{"publish", "serve", "init", "version", "find"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q\n%s", want, stdout)
		}
	}
}

func TestExecuteVersionFormsMatch(t *testing.T) {
	setVersionTestState(t)
	var outputs []string
	for _, args := range [][]string{{"version"}, {"--version"}} {
		stdout, _, err := executeForTest(t, testCommandDependencies(), args)
		if err != nil {
			t.Fatalf("executeForTest(%v) error = %v", args, err)
		}
		outputs = append(outputs, stdout)
	}
	if outputs[0] != outputs[1] || outputs[0] != "oxpio version=dev commit=unknown date=unknown type=dev\n" {
		t.Fatalf("version outputs = %#v", outputs)
	}
	for _, args := range [][]string{{"version", "extra"}, {"--version", "extra"}} {
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil {
			t.Fatalf("executeForTest(%v) error = nil", args)
		}
	}
}

func TestExecuteRejectsUnknownCommand(t *testing.T) {
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"foo"})
	if err == nil {
		t.Fatal("executeForTest() error = nil, want unknown command error")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty stdout", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty stderr", stderr)
	}
	if !strings.Contains(err.Error(), `unknown command "foo" for "oxpio"`) {
		t.Fatalf("error = %q, want unknown command message", err.Error())
	}
}

func TestJSONFailureContainsSanitizedDiagnosticsOnly(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, defaultConfigFilename), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil {
		t.Fatal("JSON validate succeeded")
	}
	if stderr != "" || strings.Contains(stdout, vault) || !strings.Contains(stdout, `"diagnostics"`) || !strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("JSON failure = stdout %q, stderr %q", stdout, stderr)
	}
}

func TestJSONDiagnosticsRedactHiddenTargets(t *testing.T) {
	vault := t.TempDir()
	writeValidateFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeValidateFile(t, vault, "public.md", "---\ntitle: Public\npublish: true\ntype: doc\n---\n[[private/article]]\n")
	writeValidateFile(t, vault, "private/_index.md", "---\ntitle: Private\npublish: false\n---\n")
	writeValidateFile(t, vault, "private/article.md", "---\ntitle: Private article\npublish: true\ntype: doc\n---\nSecret\n")
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", vault, "--format", "json"})
	if err == nil || stderr != "" || strings.Contains(stdout, "private/article.md") || strings.Contains(stdout, "Private article") {
		t.Fatalf("hidden JSON failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestJSONDiagnosticsRedactResolvedSymlinkVaultPath(t *testing.T) {
	realVault := t.TempDir()
	linkRoot := t.TempDir()
	linkedVault := filepath.Join(linkRoot, "vault")
	if err := os.Symlink(realVault, linkedVault); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realVault, defaultConfigFilename), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\nunknown: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{"validate", "--vault", linkedVault, "--format", "json"})
	if err == nil || stderr != "" || strings.Contains(stdout, realVault) || strings.Contains(stdout, linkedVault) {
		t.Fatalf("symlink JSON failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
	}
}

func TestExecuteRejectsUnknownRootFlagAndExtraArgs(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"extra", "arg"}} {
		_, _, err := executeForTest(t, testCommandDependencies(), args)
		if err == nil {
			t.Fatalf("executeForTest(%v) error = nil", args)
		}
	}
}

func executeForTest(t *testing.T, deps commandDependencies, args []string) (stdout string, stderr string, err error) {
	t.Helper()

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	err = executeWithDeps(args, deps, &stdoutBuf, &stderrBuf)

	return stdoutBuf.String(), stderrBuf.String(), err
}

func setTestBuildDependency(deps *commandDependencies, build func(string, string, internalbuild.Options) (*internalbuild.BuildResult, error)) {
	deps.buildSiteWithOptions = build
	deps.publishSite = func(vaultPath string, outputPath string, options internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
		result, err := deps.buildSiteWithOptions(vaultPath, outputPath, internalbuild.Options{Strict: true, DiagnosticsWriter: options.DiagnosticsWriter, TrackOutputTransactionPath: options.TrackOutputTransactionPath})
		return &internalapplication.PublishResult{OutputPath: outputPath, Published: err == nil, Build: result}, err
	}
}

func testCommandDependencies() commandDependencies {
	deps := commandDependencies{
		buildSiteWithOptions: func(vaultPath string, outputPath string, options internalbuild.Options) (*internalbuild.BuildResult, error) {
			return nil, fmt.Errorf("unexpected buildSiteWithOptions call")
		},
		newPreviewServer: func(outputPath string, port int) (previewServer, error) {
			return nil, fmt.Errorf("unexpected newPreviewServer call")
		},
		newFileWatcher: func() (fileWatcher, error) {
			return nil, fmt.Errorf("unexpected newFileWatcher call")
		},
	}
	return deps
}
