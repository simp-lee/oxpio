// Package application contains shared, interface-independent OXPIO application contracts.
package application

import (
	"encoding/json"
	"sort"
	"strings"
)

// FlagSpec documents a command flag in the discoverable command registry.
type FlagSpec struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Value    string `json:"value,omitempty"`
	Summary  string `json:"summary"`
}

// CommandSpec is the stable, public description of one command.
type CommandSpec struct {
	Name         string     `json:"name"`
	Summary      string     `json:"summary"`
	Arguments    string     `json:"arguments,omitempty"`
	Flags        []FlagSpec `json:"flags,omitempty"`
	SideEffects  string     `json:"sideEffects"`
	ReadScope    string     `json:"readScope"`
	WriteScope   string     `json:"writeScope"`
	OutputFields []string   `json:"outputFields,omitempty"`
}

var commandSpecs = []CommandSpec{
	{Name: "help", Summary: "Show command help and Agent workflow", Flags: []FlagSpec{{Name: "agent", Summary: "Show the concise Agent workflow"}}, SideEffects: "none", ReadScope: "none", WriteScope: "none", OutputFields: []string{"commands", "workflow"}},
	{Name: "context", Summary: "Show static or Vault-specific Agent capabilities", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "oxpio.yaml and Markdown sources", WriteScope: "none", OutputFields: []string{"apiVersion", "theme", "capabilities"}},
	{Name: "init", Summary: "Create a minimal OXPIO Vault", SideEffects: "creates missing Vault files", ReadScope: "Vault boundary", WriteScope: "oxpio.yaml and _index.md", OutputFields: []string{"created"}},
	{Name: "validate", Summary: "Analyze a Vault without publishing", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}}, SideEffects: "read-only", ReadScope: "Vault and selected output boundary", WriteScope: "none", OutputFields: []string{"errors", "warnings"}},
	{Name: "publish", Summary: "Strictly generate and atomically publish the site", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "dry-run", Summary: "Generate without replacing output"}}, SideEffects: "publishes output unless dry-run", ReadScope: "Vault and selected theme", WriteScope: "output", OutputFields: []string{"output", "preview", "published", "artifacts"}},
	{Name: "serve", Summary: "Serve an existing output, optionally watching publish", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "watch", Summary: "Publish on input changes"}}, SideEffects: "serves local output", ReadScope: "output", WriteScope: "output only with watch", OutputFields: []string{"address"}},
	{Name: "editor", Summary: "Run the explicitly enabled local editor", Flags: []FlagSpec{{Name: "setup", Summary: "Configure the editor account"}, {Name: "reset-password", Summary: "Reset the editor password"}, {Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "listen", Value: "ADDRESS", Summary: "Listener bind address"}}, SideEffects: "serves and may publish", ReadScope: "Vault and output", WriteScope: "source and output", OutputFields: []string{"address", "configured"}},
	{Name: "version", Summary: "Print the OXPIO version", SideEffects: "none", ReadScope: "none", WriteScope: "none", OutputFields: []string{"version"}},
	{Name: "config read", Summary: "Read a redacted configuration summary", SideEffects: "read-only", ReadScope: "oxpio.yaml", WriteScope: "none", OutputFields: []string{"configured", "hash", "config"}},
	{Name: "config edit", Summary: "Patch configuration bytes with CAS", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true}}, SideEffects: "source mutation", ReadScope: "oxpio.yaml", WriteScope: "oxpio.yaml", OutputFields: []string{"changes", "next"}},
	{Name: "config write", Summary: "Replace configuration bytes with CAS", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true}}, SideEffects: "source mutation", ReadScope: "oxpio.yaml", WriteScope: "oxpio.yaml", OutputFields: []string{"changes", "next"}},
	{Name: "read", Summary: "Read an original Markdown source", Arguments: "PATH", Flags: []FlagSpec{{Name: "offset", Value: "LINE", Summary: "0-based starting line"}, {Name: "limit", Value: "LINES", Summary: "Maximum number of lines"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault-relative Markdown", WriteScope: "none", OutputFields: []string{"path", "content", "hash", "offset", "limit", "totalLines", "eof"}},
	{Name: "write", Summary: "Replace one Markdown source with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "publish", Summary: "Explicitly publish source"}, {Name: "unpublish", Summary: "Explicitly unpublish source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes", "publish"}},
	{Name: "edit", Summary: "Apply exact source text patches with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "publish", Summary: "Explicitly publish source"}, {Name: "unpublish", Summary: "Explicitly unpublish source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes", "publish"}},
	{Name: "move", Summary: "Move one Markdown article with CAS", Arguments: "SOURCE DEST", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256", Required: true}, {Name: "dest-if-hash", Value: "absent", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes"}},
	{Name: "delete", Summary: "Delete one Markdown article with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes"}},
	{Name: "mkdir", Summary: "Create one Vault directory", Arguments: "PATH", SideEffects: "directory mutation", ReadScope: "Vault-relative directory", WriteScope: "Vault directory", OutputFields: []string{"changes"}},
	{Name: "rename-dir", Summary: "Rename one empty Vault directory", Arguments: "SOURCE DEST", SideEffects: "directory mutation", ReadScope: "Vault-relative directory", WriteScope: "Vault directory", OutputFields: []string{"changes"}},
	{Name: "recover", Summary: "Inspect or recover a publish transaction", Flags: []FlagSpec{{Name: "dry-run", Summary: "Only report recovery"}}, SideEffects: "transaction recovery", ReadScope: ".oxpio/transactions", WriteScope: "transaction state", OutputFields: []string{"recovered"}},
	{Name: "upload", Summary: "Create one Markdown or attachment file", Arguments: "DIR", Flags: []FlagSpec{{Name: "file", Value: "FILE|-", Required: true}, {Name: "name", Value: "NAME"}, {Name: "kind", Value: "article|section-index|attachment", Required: true}, {Name: "if-hash", Value: "absent", Required: true}, {Name: "include-unpublished", Summary: "Allow unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative directory", WriteScope: "new source or attachment", OutputFields: []string{"changes"}},
	{Name: "find", Summary: "Find normalized Markdown sources", Flags: []FlagSpec{{Name: "path", Value: "PATH", Summary: "Vault-relative prefix"}, {Name: "kind", Value: "section|article|all", Summary: "Source kind"}, {Name: "section", Value: "PATH", Summary: "Section prefix"}, {Name: "publish", Value: "true|false", Summary: "Publication state"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "ls", Summary: "List Vault files and directories", Arguments: "PATH", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault boundary", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "tags", Summary: "List normalized published tags", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "grep", Summary: "Search Markdown source with literal or RE2 matching", Arguments: "QUERY", Flags: []FlagSpec{{Name: "regex", Summary: "Use RE2 matching"}, {Name: "case-sensitive", Summary: "Disable Unicode folding"}, {Name: "scope", Value: "source|body|title|path|metadata|all"}, {Name: "context", Value: "N"}, {Name: "before", Value: "N"}, {Name: "after", Value: "N"}, {Name: "max-results", Value: "N"}, {Name: "max-line-length", Value: "BYTES"}, {Name: "max-scan-bytes", Value: "BYTES"}, {Name: "path", Value: "PATH"}, {Name: "file", Value: "NAME"}, {Name: "tag", Value: "TAG"}, {Name: "section", Value: "PATH"}, {Name: "type", Value: "doc|post|page"}, {Name: "status", Value: "stable|experimental|deprecated"}, {Name: "publish", Value: "true|false"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"query", "matches", "contexts", "truncated"}},
	{Name: "related", Summary: "Return production related-article recommendations", Arguments: "PATH", SideEffects: "read-only", ReadScope: "Published article source", WriteScope: "none", OutputFields: []string{"source", "partition", "candidates"}},
	{Name: "theme context", Summary: "Describe the theme View Model contract", SideEffects: "read-only", ReadScope: "selected theme", WriteScope: "none", OutputFields: []string{"apiVersion", "theme", "pages"}},
	{Name: "theme validate", Summary: "Validate a local or embedded theme", Flags: []FlagSpec{{Name: "theme", Value: "ID", Summary: "Temporarily select a theme"}}, SideEffects: "read-only", ReadScope: "selected theme", WriteScope: "none", OutputFields: []string{"errors", "warnings"}},
	{Name: "theme init", Summary: "Create an editable local theme scaffold", Arguments: "[NAME]", Flags: []FlagSpec{{Name: "dry-run", Summary: "Do not write"}}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: ".oxpio/themes/<id>", OutputFields: []string{"changes", "theme"}},
	{Name: "theme read", Summary: "Read a local theme file", Arguments: "PATH", SideEffects: "read-only", ReadScope: "theme boundary", WriteScope: "none", OutputFields: []string{"path", "content", "hash", "size"}},
	{Name: "theme write", Summary: "Replace a local theme file with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: "theme file", OutputFields: []string{"changes"}},
	{Name: "theme edit", Summary: "Patch a local theme text file with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: "theme file", OutputFields: []string{"changes"}},
	{Name: "theme preview", Summary: "Run an isolated local theme preview", Flags: []FlagSpec{{Name: "theme", Value: "ID", Summary: "Temporarily select a theme"}, {Name: "output", Value: "PATH", Summary: "Validate formal boundary only"}}, SideEffects: "serves isolated preview", ReadScope: "Vault and selected theme", WriteScope: "none", OutputFields: []string{"address", "preview"}},
}

// CommandSpecs returns the registry in stable display order.
func CommandSpecs() []CommandSpec {
	result := make([]CommandSpec, len(commandSpecs))
	for i, spec := range commandSpecs {
		result[i] = enrichCommandSpec(spec)
	}
	return result
}

func enrichCommandSpec(spec CommandSpec) CommandSpec {
	spec.Flags = append([]FlagSpec(nil), spec.Flags...)
	spec.OutputFields = append([]string(nil), spec.OutputFields...)
	addFlag := func(name, value, summary string) {
		for _, flag := range spec.Flags {
			if flag.Name == name {
				return
			}
		}
		spec.Flags = append(spec.Flags, FlagSpec{Name: name, Value: value, Summary: summary})
	}
	if spec.Name != "version" {
		addFlag("vault", "PATH", "Path to the Vault")
	}
	addFlag("format", "text|json", "Output format")
	if spec.Name == "context" || spec.Name == "find" || spec.Name == "ls" || spec.Name == "read" || spec.Name == "tags" || spec.Name == "grep" {
		addFlag("output", "PATH", "Formal output boundary")
	}
	if spec.Name == "editor" || spec.Name == "serve" {
		addFlag("port", "NUM", "Listener port")
	}
	if spec.Name == "editor" {
		addFlag("listen", "ADDRESS", "Listener bind address")
	}
	if spec.Name == "theme preview" {
		addFlag("port", "NUM", "Listener port")
	}
	return spec
}

// FindCommand returns a registry entry by its canonical space-separated name.
func FindCommand(name string) (CommandSpec, bool) {
	name = strings.Join(strings.Fields(name), " ")
	for _, raw := range commandSpecs {
		if raw.Name == name {
			return enrichCommandSpec(raw), true
		}
	}
	return CommandSpec{}, false
}

// HelpJSON returns the stable machine-readable registry document.
func HelpJSON(agentOnly bool) ([]byte, error) {
	specs := CommandSpecs()
	if agentOnly {
		filtered := make([]CommandSpec, 0, len(specs))
		for _, spec := range specs {
			if spec.Name == "help" || spec.Name == "version" || spec.Name == "serve" || spec.Name == "editor" {
				continue
			}
			filtered = append(filtered, spec)
		}
		specs = filtered
	}
	return json.Marshal(struct {
		Schema   string        `json:"schema"`
		Commands []CommandSpec `json:"commands"`
		Workflow []string      `json:"workflow"`
	}{Schema: "oxpio.agent/help/v1", Commands: specs, Workflow: []string{
		"find or ls, then read the original source",
		"write or edit with an explicit CAS hash",
		"validate, optionally publish --dry-run, then explicitly publish",
	}})
}

// HelpText returns a deterministic human-readable registry overview.
func HelpText(command string, agentOnly bool) string {
	if command != "" {
		if spec, ok := FindCommand(command); ok {
			return spec.Name + " - " + spec.Summary + "\nArguments: " + valueOrNone(spec.Arguments) + "\nSide effects: " + spec.SideEffects + "\nReads: " + spec.ReadScope + "\nWrites: " + spec.WriteScope + "\n"
		}
		return "unknown command " + command + "\n"
	}
	var b strings.Builder
	b.WriteString("OXPIO commands:\n")
	for _, spec := range commandSpecs {
		if agentOnly && (spec.Name == "help" || spec.Name == "version" || spec.Name == "serve" || spec.Name == "editor") {
			continue
		}
		b.WriteString("  ")
		b.WriteString(spec.Name)
		b.WriteString(" - ")
		b.WriteString(spec.Summary)
		b.WriteByte('\n')
	}
	if agentOnly {
		b.WriteString("Workflow: find/ls/read -> write/edit -> validate -> publish --dry-run -> publish\n")
	}
	return b.String()
}

func valueOrNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

// SortedNames is useful to adapters that need command names without metadata.
func SortedNames() []string {
	result := make([]string, 0, len(commandSpecs))
	for _, spec := range commandSpecs {
		result = append(result, spec.Name)
	}
	sort.Strings(result)
	return result
}
