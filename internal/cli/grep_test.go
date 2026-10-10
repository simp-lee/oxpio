package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
)

func TestGrepJSONSearchUsesNativeCommandPath(t *testing.T) {
	vault := t.TempDir()
	files := map[string]string{
		"oxpio.yaml": "title: Site\nbaseURL: https://example.test/\nnavigation: []\n",
		"_index.md":  "---\ntitle: Home\npublish: true\n---\n",
		"note.md":    "---\ntitle: Note\npublish: true\ntype: doc\n---\nprefix Needle suffix\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(vault, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := executeWithDeps([]string{"grep", "needle", "--vault", vault, "--format", "json"}, defaultCommandDependencies(), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var envelope internalagent.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Command != "grep" || len(envelope.Diagnostics) != 0 || len(envelope.Data) == 0 || stderr.Len() != 0 {
		t.Fatalf("envelope=%#v stderr=%q", envelope, stderr.String())
	}
	var textOut, textErr bytes.Buffer
	if err := executeWithDeps([]string{"grep", "needle", "--vault", vault, "--format", "text"}, defaultCommandDependencies(), &textOut, &textErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOut.String(), "prefix Needle suffix") || textErr.Len() != 0 {
		t.Fatalf("text=%q stderr=%q", textOut.String(), textErr.String())
	}
}

func TestGrepInvalidFrontmatterBytesUseStructuredSourceFailure(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "oxpio.yaml"), []byte("title: Site\nbaseURL: https://example.test/\nnavigation: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "_index.md"), []byte("---\ntitle: Home\npublish: true\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "oxpio.yaml.md"), []byte("---\ntitle: \xff\npublish: true\ntype: doc\n---\nneedle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := executeWithDeps([]string{"grep", "needle", "--vault", vault, "--format", "json"}, defaultCommandDependencies(), &stdout, &stderr)
	var exit interface{ ExitCode() int }
	if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("error = %#v", err)
	}
	var envelope internalagent.Envelope
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &envelope); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if envelope.OK || len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Code != "INVALID_SOURCE" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestGrepMalformedNumericFlagUsesStructuredParameterFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := executeWithDeps([]string{"grep", "needle", "--max-results", "nope", "--format", "json"}, defaultCommandDependencies(), &stdout, &stderr)
	if err == nil {
		t.Fatal("grep unexpectedly succeeded")
	}
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("error = %#v", err)
	}
	var envelope internalagent.Envelope
	if unmarshalErr := json.Unmarshal(stdout.Bytes(), &envelope); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if envelope.OK || len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Code != "INVALID_ARGUMENT" || envelope.Diagnostics[0].Category != "parameter" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
