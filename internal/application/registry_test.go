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
	for _, name := range []string{"help", "init", "validate", "publish", "read", "find", "ls", "tags", "related", "editor", "theme preview"} {
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

func TestMutationCommandsDeclareOutputBoundary(t *testing.T) {
	for _, name := range []string{"write", "edit", "move", "delete", "upload", "mkdir", "rename-dir", "config write", "config edit", "theme validate", "theme init", "theme write", "theme edit"} {
		spec, ok := FindCommand(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		found := false
		for _, flag := range spec.Flags {
			if flag.Name == "output" && flag.Default == "<vault>/public" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s flags = %#v, want output boundary with default", name, spec.Flags)
		}
	}
}

func TestGrepRegistryDeclaresBoundsRepeatabilityAndAgentHelp(t *testing.T) {
	spec, ok := FindCommand("grep")
	if !ok {
		t.Fatal("grep is not registered")
	}
	seenDefault := false
	for _, flag := range spec.Flags {
		if flag.Name == "max-results" && flag.Default == "1000" && flag.Value == "1..10000" {
			seenDefault = true
		}
		if flag.Name == "path" && !flag.Repeatable {
			t.Fatal("path is not repeatable")
		}
	}
	if !seenDefault {
		t.Fatalf("grep flags = %#v", spec.Flags)
	}
	if help := HelpText("", true); !strings.Contains(help, "grep QUERY") || !strings.Contains(help, "max-scan-bytes") {
		t.Fatalf("agent help = %s", help)
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
