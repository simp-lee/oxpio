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

func TestContextCapabilitiesIncludeEveryAgentMutation(t *testing.T) {
	capabilities := map[string]bool{}
	for _, capability := range contextCapabilities() {
		capabilities[capability] = true
	}
	for _, name := range []string{"upload", "import", "mkdir", "write", "edit"} {
		if !capabilities[name] {
			t.Fatalf("context capabilities omit %q", name)
		}
		if _, ok := FindCommand(name); !ok {
			t.Fatalf("command registry omits %q", name)
		}
	}
	if capabilities["rename-dir"] {
		t.Fatal("context capabilities expose unavailable rename-dir")
	}
	if _, ok := FindCommand("rename-dir"); ok {
		t.Fatal("command registry exposes unavailable rename-dir")
	}
}

func TestMutationCommandsDeclareOutputBoundary(t *testing.T) {
	for _, name := range []string{"write", "edit", "move", "delete", "upload", "mkdir", "config write", "config edit", "theme validate", "theme init", "theme write", "theme edit"} {
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

func TestAgentHelpDocumentsSafeInputAndInitializationExamples(t *testing.T) {
	help := HelpText("write", false)
	for _, want := range []string{"--content-file FILE|-", "use - for stdin", "Vault-relative POSIX"} {
		if !strings.Contains(help, want) {
			t.Fatalf("write help = %q, want %q", help, want)
		}
	}
	themeHelp := HelpText("theme write", false)
	for _, want := range []string{".oxpio/themes/minimal/layouts/example.html", "--content-file -"} {
		if !strings.Contains(themeHelp, want) {
			t.Fatalf("theme write help = %q, want %q", themeHelp, want)
		}
	}
	agentHelp := HelpText("", true)
	for _, want := range []string{"Input files:", "Import order:", "write navigation after all target sections exist"} {
		if !strings.Contains(agentHelp, want) {
			t.Fatalf("agent help = %q, want %q", agentHelp, want)
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
	workflow := strings.Join(document.Workflow, "\n")
	for _, want := range []string{"write or edit", "publish --dry-run", "attachments.directory"} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("help workflow = %q, want %q", workflow, want)
		}
	}
	for _, command := range document.Commands {
		if command.Name == "build" {
			t.Fatal("help exposes build")
		}
	}
}
