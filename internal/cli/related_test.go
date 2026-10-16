package cli

import (
	"encoding/json"
	"errors"
	"testing"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
)

func TestRelatedSectionSourceUsesStructuredParameterFailure(t *testing.T) {
	vault := t.TempDir()
	writeCLIConfig(t, vault)
	writeValidateFile(t, vault, "_index.md", "---\ntitle: Home\npublish: true\n---\n")

	stdout, stderr, err := executeForTest(t, testCommandDependencies(), []string{
		"related", "_index.md", "--vault", vault, "--format", "json",
	})
	if err == nil {
		t.Fatal("related unexpectedly succeeded for a section source")
	}
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("related error = %#v, want exit code 2", err)
	}
	var envelope internalagent.Envelope
	if unmarshalErr := json.Unmarshal([]byte(stdout), &envelope); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	if envelope.OK || envelope.Command != "related" || len(envelope.Diagnostics) != 1 || envelope.Diagnostics[0].Code != "INVALID_ARGUMENT" || envelope.Diagnostics[0].Category != "parameter" {
		t.Fatalf("envelope = %#v", envelope)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
}
