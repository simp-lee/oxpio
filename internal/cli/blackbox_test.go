package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
)

// TestRealCommandMatrixUsesStableJSONEnvelope exercises the command registry
// through the real executable. Destructive and long-running commands are
// probed through command-level help; the protocol probes below execute only
// read-only or ephemeral-dry-run commands.
func TestRealCommandMatrixUsesStableJSONEnvelope(t *testing.T) {
	binary := buildBlackBoxBinary(t)
	for index, spec := range internalapplication.CommandSpecs() {
		args := []string{"help"}
		if spec.Name != "help" {
			args = append(args, strings.Fields(spec.Name)...)
		}
		if index%2 == 0 {
			args = append(args, "--format", "json")
		} else {
			args = append([]string{"--format", "json"}, args...)
		}
		stdout, stderr, exitCode := runBlackBox(t, binary, args, nil)
		if exitCode != 0 || stderr != "" {
			t.Fatalf("help probe %q: exit=%d stderr=%q stdout=%q", spec.Name, exitCode, stderr, stdout)
		}
		assertSingleEnvelope(t, stdout, "help")
	}

	vault := t.TempDir()
	writeBlackBoxFile(t, vault, "oxpio.yaml", "title: Site\nbaseURL: https://example.test/\nnavigation: []\n")
	writeBlackBoxFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")
	writeBlackBoxFile(t, vault, "article.md", "---\ntitle: Article\npublish: true\ntype: doc\n---\nBody\n")
	probes := [][]string{
		{"--version", "--format", "json"},
		{"context", "--version", "--format", "json"},
		{"version", "--format", "json"},
		{"--format", "json", "context", "--vault", vault},
		{"validate", "--vault", vault, "--format", "json"},
		{"read", "article.md", "--vault", vault, "--format", "json"},
		{"find", "--vault", vault, "--format", "json"},
		{"ls", ".", "--vault", vault, "--format", "json"},
		{"tags", "--vault", vault, "--format", "json"},
		{"grep", "Body", "--vault", vault, "--format", "json"},
		{"related", "article.md", "--vault", vault, "--format", "json"},
		{"publish", "--vault", vault, "--dry-run", "--format", "json"},
	}
	for _, args := range probes {
		stdout, stderr, exitCode := runBlackBox(t, binary, args, []byte{})
		if stdout == "" || stderr != "" {
			t.Fatalf("probe %v: exit=%d stderr=%q stdout=%q", args, exitCode, stderr, stdout)
		}
		assertSingleEnvelope(t, stdout, blackBoxExpectedCommand(args))
	}

	invalidProbes := []struct {
		args []string
		code int
	}{
		{args: []string{"read", "--format", "json"}, code: 2},
		{args: []string{"read", "../outside", "--vault", vault, "--format", "json"}, code: 2},
		{args: []string{"read", "article.md", "--unknown", "--vault", vault, "--format", "json"}, code: 2},
		{args: []string{"config", "--format", "json"}, code: 2},
		{args: []string{"theme", "--format", "json"}, code: 2},
	}
	for _, probe := range invalidProbes {
		stdout, stderr, exitCode := runBlackBox(t, binary, probe.args, nil)
		if exitCode != probe.code || stderr != "" {
			t.Fatalf("invalid probe %v: exit=%d want=%d stderr=%q stdout=%q", probe.args, exitCode, probe.code, stderr, stdout)
		}
		assertSingleEnvelope(t, stdout, blackBoxExpectedCommand(probe.args))
	}

	initVault := t.TempDir()
	stdout, stderr, exitCode := runBlackBox(t, binary, []string{"init", "--vault", initVault, "--format", "json"}, nil)
	if exitCode != 0 || stderr != "" || strings.Contains(stdout, initVault) {
		t.Fatalf("init probe: exit=%d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	var initEnvelope struct {
		Changes []internalagent.Change `json:"changes"`
	}
	if err := json.Unmarshal([]byte(stdout), &initEnvelope); err != nil || len(initEnvelope.Changes) != 2 {
		t.Fatalf("init envelope: err=%v envelope=%#v", err, initEnvelope)
	}
}

func buildBlackBoxBinary(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	name := "oxpio"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-o", binary, "./cmd/oxpio")
	command.Dir = repoRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}

func runBlackBox(t *testing.T, binary string, args []string, stdin []byte) (string, string, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		return stdout.String(), stderr.String(), exitError.ExitCode()
	}
	t.Fatalf("run %v: %v", args, err)
	return "", "", -1
}

func blackBoxExpectedCommand(args []string) string {
	for _, arg := range args {
		if arg == "--version" || arg == "--version=true" {
			return "version"
		}
	}
	words := make([]string, 0, 2)
	valueFlags := map[string]bool{"--format": true, "--vault": true, "--output": true, "--port": true, "--listen": true}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			if valueFlags[arg] && index+1 < len(args) && !strings.Contains(arg, "=") {
				index++
			}
			continue
		}
		words = append(words, arg)
		if len(words) == 2 {
			break
		}
	}
	if len(words) >= 2 {
		candidate := words[0] + " " + words[1]
		if candidate == "config read" || candidate == "config edit" || candidate == "config write" || candidate == "theme context" || candidate == "theme validate" || candidate == "theme init" || candidate == "theme read" || candidate == "theme write" || candidate == "theme edit" || candidate == "theme preview" {
			return candidate
		}
	}
	if len(words) != 0 {
		if words[0] == "config" || words[0] == "theme" {
			return ""
		}
		return words[0]
	}
	for _, arg := range args {
		if arg == "--version" || arg == "--version=true" {
			return "version"
		}
	}
	return ""
}

func writeBlackBoxFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSingleEnvelope(t *testing.T, output, wantCommand string) {
	t.Helper()
	if strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, "\n") {
		t.Fatalf("output is not one JSON line: %q", output)
	}
	var envelope internalagent.Envelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%q)", err, output)
	}
	if envelope.Schema != internalagent.Schema || envelope.Command != wantCommand || envelope.Data == nil || envelope.Diagnostics == nil || envelope.Changes == nil {
		t.Fatalf("envelope = %#v", envelope)
	}
}
