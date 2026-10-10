package cli

import (
	"strings"
	"testing"
)

func TestEditorCommandKeepsWebEditorSeparateFromAgentEdit(t *testing.T) {
	cmd := newRootCommand(testCommandDependencies())
	if command, _, err := cmd.Find([]string{"editor"}); err != nil || command == nil {
		t.Fatalf("editor command lookup = %v, %#v", err, command)
	}
	if _, _, err := executeForTest(t, testCommandDependencies(), []string{"edit", "--setup"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("Agent edit accepted editor setup flag: %v", err)
	}
}

func TestEditorRejectsInvalidPortBeforeStartingServer(t *testing.T) {
	_, _, err := executeForTest(t, testCommandDependencies(), []string{"editor", "--port", "0"})
	if err == nil || !strings.Contains(err.Error(), "port must be between") {
		t.Fatalf("editor port error = %v", err)
	}
}
