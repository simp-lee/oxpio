// Package agent contains stable machine-facing Agent protocol DTOs.
package agent

import (
	"encoding/json"
	"io"
)

const Schema = "oxpio.agent/v1"

type Diagnostic struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"`
	Category string  `json:"category"`
	Path     *string `json:"path"`
	Line     *int    `json:"line"`
	Column   *int    `json:"column"`
	Field    *string `json:"field"`
	Target   *string `json:"target"`
	Message  string  `json:"message"`
}

type Change struct {
	Kind       string  `json:"kind"`
	Domain     string  `json:"domain"`
	Path       string  `json:"path"`
	BeforeHash *string `json:"beforeHash"`
	AfterHash  *string `json:"afterHash"`
}

type Envelope struct {
	Schema      string          `json:"schema"`
	OK          bool            `json:"ok"`
	Command     string          `json:"command"`
	Data        json.RawMessage `json:"data"`
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Changes     []Change        `json:"changes"`
}

func Success(command string, data json.RawMessage) Envelope {
	return SuccessWithChanges(command, data, nil)
}

func SuccessWithChanges(command string, data json.RawMessage, changes []Change) Envelope {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	if changes == nil {
		changes = []Change{}
	}
	return Envelope{Schema: Schema, OK: true, Command: command, Data: data, Diagnostics: []Diagnostic{}, Changes: changes}
}

func Failure(command, code, category, message string) Envelope {
	return FailureWithDiagnostics(command, []Diagnostic{{Code: code, Severity: "error", Category: category, Message: message}})
}

func FailureWithDiagnostics(command string, diagnostics []Diagnostic) Envelope {
	return FailureWithDataAndDiagnostics(command, nil, diagnostics)
}

// FailureWithDataAndDiagnostics preserves an analysis summary while keeping a
// failed command's envelope unsuccessful and free of uncommitted changes.
func FailureWithDataAndDiagnostics(command string, data json.RawMessage, diagnostics []Diagnostic) Envelope {
	if len(diagnostics) == 0 {
		diagnostics = []Diagnostic{{Code: "COMMAND_ERROR", Severity: "error", Category: "command", Message: "command failed"}}
	}
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	return Envelope{
		Schema: Schema, OK: false, Command: command, Data: data,
		Diagnostics: diagnostics,
		Changes:     []Change{},
	}
}

func Write(w io.Writer, envelope Envelope) error {
	if envelope.Schema == "" {
		envelope.Schema = Schema
	}
	if envelope.Data == nil {
		envelope.Data = json.RawMessage("null")
	}
	if envelope.Diagnostics == nil {
		envelope.Diagnostics = []Diagnostic{}
	}
	if envelope.Changes == nil {
		envelope.Changes = []Change{}
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}
