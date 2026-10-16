package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
