package application

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandRegistryIsCompleteAndHasNoLegacyWebAliases(t *testing.T) {
	for _, name := range []string{"help", "context", "read", "find", "ls", "tags", "publish", "editor", "theme validate", "theme preview"} {
		if _, ok := FindCommand(name); !ok {
			t.Fatalf("FindCommand(%q) = false", name)
		}
	}
	if _, ok := FindCommand("build"); ok {
		t.Fatal("public build command is still registered")
	}
	if spec, ok := FindCommand("edit"); !ok || !strings.Contains(spec.Summary, "patch") {
		t.Fatalf("Agent edit registry entry = %#v, ok=%v", spec, ok)
	}
}

func TestCommandRegistryDocumentsGlobalAndBoundaryFlags(t *testing.T) {
	for _, name := range []string{"init", "validate", "publish", "read", "find", "ls", "tags", "editor", "theme preview"} {
		spec, ok := FindCommand(name)
		if !ok {
			t.Fatalf("FindCommand(%q) = false", name)
		}
		has := func(flagName string) bool {
			for _, flag := range spec.Flags {
				if flag.Name == flagName {
					return true
				}
			}
			return false
		}
		if !has("format") || (name != "version" && !has("vault")) {
			t.Fatalf("%s flags = %#v", name, spec.Flags)
		}
	}
}

func TestHelpJSONIsStableAndDocumentsAgentWorkflow(t *testing.T) {
	first, err := HelpJSON(true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HelpJSON(true)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("HelpJSON is not deterministic")
	}
	var document struct {
		Schema   string `json:"schema"`
		Commands []struct {
			Name string `json:"name"`
		} `json:"commands"`
		Workflow []string `json:"workflow"`
	}
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "oxpio.agent/help/v1" || len(document.Commands) == 0 || len(document.Workflow) != 3 {
		t.Fatalf("help document = %#v", document)
	}
	for _, command := range document.Commands {
		if command.Name == "build" {
			t.Fatal("help exposes build")
		}
	}
}
