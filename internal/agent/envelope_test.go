package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeWritesOneStableJSONObject(t *testing.T) {
	var output bytes.Buffer
	if err := Write(&output, Success("read", json.RawMessage(`{"content":"正文"}`))); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 || !strings.HasSuffix(output.String(), "\n") {
		t.Fatalf("output = %q", output.String())
	}
	var decoded Envelope
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != Schema || !decoded.OK || decoded.Command != "read" || len(decoded.Changes) != 0 || len(decoded.Diagnostics) != 0 {
		t.Fatalf("decoded envelope = %#v", decoded)
	}
}

func TestFailureEnvelopeDoesNotExposePathsByDefault(t *testing.T) {
	envelope := Failure("read", "PERMISSION_DENIED", "permission", "source is not available")
	if envelope.OK || string(envelope.Data) != "null" || envelope.Diagnostics[0].Path != nil || envelope.Changes == nil {
		t.Fatalf("failure envelope = %#v", envelope)
	}
}
