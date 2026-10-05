package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRealEntrypointDefaultInitBuildAndDiagnostics(t *testing.T) {
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
	if output, err := run("build"); err != nil {
		t.Fatalf("build = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(vault, "public", "index.html")); err != nil {
		t.Fatalf("os.Stat(public/index.html) error = %v", err)
	}
	var versionOutputs []string
	for _, args := range [][]string{{"version"}, {"--version"}} {
		output, err := run(args...)
		if err != nil || !strings.Contains(output, "oxpio version=dev ") || !strings.Contains(output, " type=dev\n") {
			t.Fatalf("%v = %v, %q", args, err, output)
		}
		versionOutputs = append(versionOutputs, output)
	}
	if versionOutputs[0] != versionOutputs[1] {
		t.Fatalf("version outputs differ: %#v", versionOutputs)
	}
	if output, err := run("unknown"); err == nil || !strings.Contains(output, "unknown command") {
		t.Fatalf("unknown = %v\n%s", err, output)
	}

	emptyVault := t.TempDir()
	cmd := exec.Command(binaryPath, "serve")
	cmd.Dir = emptyVault
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "does not exist") {
		t.Fatalf("serve missing output = %v\n%s", err, output)
	}
}
