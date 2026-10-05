package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

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

	for _, want := range []string{"build", "serve", "init", "version"} {
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

func testCommandDependencies() commandDependencies {
	return commandDependencies{
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
}
