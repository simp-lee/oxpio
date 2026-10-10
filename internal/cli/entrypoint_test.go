package cli

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRealEntrypointInitValidateAndDiagnostics(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binaryName := "oxpio"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/oxpio")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build error = %v\n%s", err, output)
	}

	vault := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binaryPath, args...)
		cmd.Dir = vault
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	if output, err := run("init"); err != nil || !strings.Contains(output, "Replace baseURL") {
		t.Fatalf("init = %v\n%s", err, output)
	}
	if output, err := run("validate"); err != nil {
		t.Fatalf("validate = %v\n%s", err, output)
	}
	if output, err := run("context", "--format", "json"); err != nil || !strings.Contains(output, `"vault"`) {
		t.Fatalf("context = %v\n%s", err, output)
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		output, err := run(args...)
		if err != nil || !strings.Contains(output, "oxpio version=dev ") || !strings.Contains(output, " type=dev\n") {
			t.Fatalf("%v = %v, %q", args, err, output)
		}
	}
	if output, err := run("build"); err == nil || !strings.Contains(output, "unknown command") {
		t.Fatalf("build = %v\n%s", err, output)
	}
	invalid := exec.Command(binaryPath, "--unknown", "--format", "json")
	invalid.Dir = vault
	if err := invalid.Run(); err == nil || invalid.ProcessState.ExitCode() != 2 {
		t.Fatalf("invalid JSON command exit = %v, code=%d", err, invalid.ProcessState.ExitCode())
	}
}
