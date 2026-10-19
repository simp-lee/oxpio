package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThemeInputReadIsBounded(t *testing.T) {
	if _, err := readBoundedThemeInput(bytes.NewReader(make([]byte, maxThemeInput+1))); err == nil {
		t.Fatal("readBoundedThemeInput accepted oversized input")
	}
}

func TestThemeInputCannotReadFormalOutput(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	output := filepath.Join(vault, "public")
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	writeValidateFile(t, vault, "public/input.css", "body { color: red; }\n")
	_, _, err := executeForTest(t, testCommandDependencies(), []string{
		"theme", "write", "custom.css", "--vault", vault, "--output", output,
		"--content-file", "public/input.css", "--if-hash", "absent", "--format", "json",
	})
	if err == nil || !strings.Contains(err.Error(), "formal output boundary") {
		t.Fatalf("theme output-boundary input error = %v", err)
	}
}

func TestThemeMutationJSONIncludesNullPublished(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "input.css", "body { color: red; }\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{
		"theme", "write", "custom.css", "--vault", vault,
		"--content-file", "input.css", "--if-hash", "absent", "--format", "json",
	})
	if err != nil || stderr != "" {
		t.Fatalf("theme write = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	assertThemeMutationPublishedNull(t, stdout)

	before, err := os.ReadFile(filepath.Join(vault, "custom.css"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(before)
	writeValidateFile(t, vault, "patch.json", `{"edits":[{"oldText":"red","newText":"blue"}]}`)
	stdout, stderr, err = executeForTest(t, testCommandDependencies(), []string{
		"theme", "edit", "custom.css", "--vault", vault,
		"--edits-file", "patch.json", "--if-hash", fmt.Sprintf("%x", digest),
		"--dry-run", "--format", "json",
	})
	if err != nil || stderr != "" {
		t.Fatalf("theme edit dry-run = stdout %q, stderr %q, err %v", stdout, stderr, err)
	}
	assertThemeMutationPublishedNull(t, stdout)
	after, err := os.ReadFile(filepath.Join(vault, "custom.css"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("theme edit dry-run changed custom.css from %q to %q", before, after)
	}
}

func assertThemeMutationPublishedNull(t *testing.T, output string) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode theme mutation envelope: %v (%q)", err, output)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode theme mutation data: %v (%q)", err, envelope.Data)
	}
	published, ok := data["published"]
	if !ok || string(published) != "null" {
		t.Fatalf("theme mutation data published = %s, present=%v; want null", published, ok)
	}
}
