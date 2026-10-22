// Package application contains shared, interface-independent OXPIO application contracts.
package application

import (
	"encoding/json"
	"sort"
	"strings"
)

// FlagSpec documents a command flag in the discoverable command registry.
type FlagSpec struct {
	Name       string `json:"name"`
	Required   bool   `json:"required"`
	Repeatable bool   `json:"repeatable,omitempty"`
	Value      string `json:"value,omitempty"`
	Default    string `json:"default,omitempty"`
	Summary    string `json:"summary"`
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
	Examples     []string   `json:"examples,omitempty"`
}

var commandSpecs = []CommandSpec{
	{Name: "help", Summary: "Show command help and Agent workflow", Arguments: "[COMMAND]", Flags: []FlagSpec{{Name: "agent", Summary: "Show the concise Agent workflow"}, {Name: "include-unpublished", Summary: "Include unpublished capability details"}, {Name: "output", Value: "PATH", Summary: "Formal output boundary"}}, SideEffects: "none", ReadScope: "none", WriteScope: "none", OutputFields: []string{"commands", "workflow"}},
	{Name: "context", Summary: "Show static or Vault-specific Agent capabilities", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "oxpio.yaml and Markdown sources", WriteScope: "none", OutputFields: []string{"apiVersion", "theme", "capabilities"}},
	{Name: "init", Summary: "Create a minimal OXPIO Vault", SideEffects: "creates missing Vault files", ReadScope: "Vault boundary", WriteScope: "oxpio.yaml and _index.md", OutputFields: []string{"created"}},
	{Name: "validate", Summary: "Analyze a Vault without publishing", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}}, SideEffects: "read-only", ReadScope: "Vault and selected output boundary", WriteScope: "none", OutputFields: []string{"errors", "warnings"}},
	{Name: "publish", Summary: "Strictly generate and atomically publish the site", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "dry-run", Summary: "Generate without replacing output"}}, SideEffects: "publishes output unless dry-run", ReadScope: "Vault and selected theme", WriteScope: "output", OutputFields: []string{"output", "preview", "published", "artifacts"}},
	{Name: "serve", Summary: "Serve an existing output, optionally watching publish", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "listen", Value: "ADDRESS", Summary: "Listener bind address"}, {Name: "watch", Summary: "Publish on input changes"}}, SideEffects: "serves local output", ReadScope: "output", WriteScope: "output only with watch", OutputFields: []string{"address"}},
	{Name: "editor", Summary: "Run the explicitly enabled local editor", Flags: []FlagSpec{{Name: "setup", Summary: "Configure the editor account"}, {Name: "reset-password", Summary: "Reset the editor password"}, {Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "listen", Value: "ADDRESS", Summary: "Listener bind address"}}, SideEffects: "serves and may publish", ReadScope: "Vault and output", WriteScope: "source and output", OutputFields: []string{"address", "configured"}},
	{Name: "version", Summary: "Print the OXPIO version", SideEffects: "none", ReadScope: "none", WriteScope: "none", OutputFields: []string{"version"}},
	{Name: "config read", Summary: "Read a redacted configuration summary", SideEffects: "read-only", ReadScope: "oxpio.yaml", WriteScope: "none", OutputFields: []string{"configured", "hash", "title", "baseURL", "language", "theme", "navigation", "versions", "attachments", "editorConfigured"}},
	{Name: "config edit", Summary: "Patch configuration bytes with CAS", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "oxpio.yaml", WriteScope: "oxpio.yaml", OutputFields: []string{"changes", "next"}},
	{Name: "config write", Summary: "Replace configuration bytes with CAS", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true, Summary: "Vault-relative POSIX input file; use - for stdin; never read .oxpio, .obsidian, or formal output"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "oxpio.yaml", WriteScope: "oxpio.yaml", OutputFields: []string{"changes", "next"}},
	{Name: "read", Summary: "Read an original Markdown source", Arguments: "PATH", Flags: []FlagSpec{{Name: "offset", Value: "LINE", Default: "0", Summary: "0-based starting line"}, {Name: "limit", Value: "1..10000", Summary: "Maximum number of lines; omitted reads to EOF"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault-relative Markdown", WriteScope: "none", OutputFields: []string{"source", "content", "hash", "offset", "limit", "totalLines", "eof"}},
	{Name: "write", Summary: "Replace one Markdown source with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true, Summary: "Vault-relative POSIX input file; use - for stdin; never read .oxpio, .obsidian, or formal output"}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "publish", Summary: "Explicitly publish source"}, {Name: "unpublish", Summary: "Explicitly unpublish source"}, {Name: "dry-run", Summary: "Do not write"}}, Examples: []string{"printf '%s\\n' '---' 'title: New article' 'publish: true' 'type: doc' '---' 'Body' | oxpio write new.md --content-file - --if-hash absent"}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes", "publish"}},
	{Name: "edit", Summary: "Apply exact source text patches with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "publish", Summary: "Explicitly publish source"}, {Name: "unpublish", Summary: "Explicitly unpublish source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes", "publish"}},
	{Name: "move", Summary: "Move one Markdown article with CAS", Arguments: "SOURCE DEST", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "dest-if-hash", Value: "absent", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes"}},
	{Name: "delete", Summary: "Delete one Markdown article with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "include-unpublished", Summary: "Include unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "source mutation", ReadScope: "Vault-relative Markdown", WriteScope: "Markdown source", OutputFields: []string{"changes"}},
	{Name: "mkdir", Summary: "Create one Vault directory", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "dry-run", Summary: "Do not write"}}, Examples: []string{"oxpio mkdir docs", "oxpio mkdir attachments", "oxpio mkdir attachments/images"}, SideEffects: "directory mutation", ReadScope: "Vault-relative directory", WriteScope: "Vault directory", OutputFields: []string{"changes"}},
	{Name: "rename-dir", Summary: "Rename one empty Vault directory", Arguments: "SOURCE DEST", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "directory mutation", ReadScope: "Vault-relative directory", WriteScope: "Vault directory", OutputFields: []string{"changes"}},
	{Name: "recover", Summary: "Inspect or recover a publish transaction", Flags: []FlagSpec{{Name: "dry-run", Summary: "Only report recovery"}}, SideEffects: "transaction recovery", ReadScope: ".oxpio/transactions", WriteScope: "transaction state", OutputFields: []string{"found", "phase", "action", "changed", "dryRun"}},
	{Name: "upload", Summary: "Create one Markdown or attachment file (configure attachments.directory first)", Arguments: "DIR", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "file", Value: "FILE|-", Required: true, Summary: "Vault-relative input file or stdin; use - with --name for raw bytes"}, {Name: "name", Value: "NAME"}, {Name: "kind", Value: "article|section-index|attachment", Required: true}, {Name: "if-hash", Value: "absent", Required: true}, {Name: "include-unpublished", Summary: "Allow unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, Examples: []string{"oxpio mkdir docs", "printf '%s\\n' '---' 'title: Docs' 'publish: true' '---' 'Docs' | oxpio upload docs --file - --name _index.md --kind section-index --if-hash absent", "oxpio mkdir attachments"}, SideEffects: "source mutation", ReadScope: "Vault-relative directory", WriteScope: "new source or attachment", OutputFields: []string{"changes"}},
	{Name: "import", Summary: "Import mutually-referencing Markdown sources as one strict overlay", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "manifest-file", Value: "FILE|-", Required: true, Summary: "Vault-relative JSON manifest or - for stdin; all content is analyzed before commit"}, {Name: "include-unpublished", Summary: "Allow unpublished source"}, {Name: "dry-run", Summary: "Do not write"}}, Examples: []string{"printf '%s' '{\"files\":[{\"path\":\"new.md\",\"kind\":\"article\",\"content\":\"---\\ntitle: New\\npublish: true\\ntype: doc\\n---\\nBody\\n\"}]}' | oxpio import --manifest-file - --dry-run"}, SideEffects: "source mutation", ReadScope: "Vault and import manifest", WriteScope: "new Markdown sources", OutputFields: []string{"changes", "next"}},
	{Name: "find", Summary: "Find normalized Markdown sources", Flags: []FlagSpec{{Name: "path", Value: "PATH", Summary: "Vault-relative prefix"}, {Name: "kind", Value: "section|article|all", Summary: "Source kind"}, {Name: "section", Value: "PATH", Summary: "Section prefix"}, {Name: "publish", Value: "true|false", Summary: "Publication state"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "ls", Summary: "List Vault files and directories", Arguments: "PATH", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault boundary", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "tags", Summary: "List normalized published tags", Flags: []FlagSpec{{Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"items"}},
	{Name: "grep", Summary: "Search Markdown source with literal or RE2 matching", Arguments: "QUERY", Flags: []FlagSpec{{Name: "regex", Summary: "Use Go RE2; reject nullable/zero-width patterns"}, {Name: "case-sensitive", Summary: "Disable default Unicode simple folding"}, {Name: "scope", Value: "source|body|title|path|metadata|all", Default: "source", Summary: "Search one normalized source/field scope"}, {Name: "context", Value: "0..20", Default: "0", Summary: "Merged lines before and after; conflicts with before/after"}, {Name: "before", Value: "0..20", Default: "0", Summary: "Context lines before each match"}, {Name: "after", Value: "0..20", Default: "0", Summary: "Context lines after each match"}, {Name: "max-results", Value: "1..10000", Default: "1000", Summary: "Result limit; repeated filters are OR"}, {Name: "max-line-length", Value: "1..8388608", Default: "1048576", Summary: "UTF-8 bytes excluding line terminator"}, {Name: "max-scan-bytes", Value: "1..10737418240", Default: "1073741824", Summary: "Candidate source byte budget"}, {Name: "path", Value: "PATH", Repeatable: true, Summary: "Vault-relative prefix; repeated values are OR"}, {Name: "file", Value: "NAME", Repeatable: true, Summary: "Exact basename; repeated values are OR"}, {Name: "tag", Value: "TAG", Repeatable: true, Summary: "Normalized exact tag; repeated values are OR"}, {Name: "section", Value: "PATH", Repeatable: true, Summary: "Section prefix; repeated values are OR"}, {Name: "type", Value: "doc|post|page", Repeatable: true, Summary: "Normalized type filter"}, {Name: "status", Value: "stable|experimental|deprecated", Repeatable: true, Summary: "Normalized status filter"}, {Name: "publish", Value: "true|false", Repeatable: true, Summary: "Effective publication filter; false requires include-unpublished"}, {Name: "include-unpublished", Summary: "Include unpublished source"}}, SideEffects: "read-only", ReadScope: "Vault Markdown", WriteScope: "none", OutputFields: []string{"query", "options", "matches", "contexts", "truncated"}},
	{Name: "related", Summary: "Return production related-article recommendations", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}}, Examples: []string{"oxpio related PATH"}, SideEffects: "read-only", ReadScope: "Published article source", WriteScope: "none", OutputFields: []string{"source", "partition", "candidates"}},
	{Name: "theme context", Summary: "Describe the theme View Model contract", SideEffects: "read-only", ReadScope: "selected theme", WriteScope: "none", OutputFields: []string{"apiVersion", "theme", "availableThemes", "pageKinds", "layouts"}},
	{Name: "theme validate", Summary: "Validate a local or embedded theme", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "theme", Value: "ID", Summary: "Temporarily select a theme"}}, SideEffects: "read-only", ReadScope: "selected theme", WriteScope: "none", OutputFields: []string{"theme", "apiVersion", "manifest", "errors", "warnings", "valid"}},
	{Name: "theme init", Summary: "Create an editable local theme scaffold", Arguments: "[NAME]", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: ".oxpio/themes/<id>", OutputFields: []string{"changes", "theme", "published"}},
	{Name: "theme read", Summary: "Read a local theme file", Arguments: "PATH", Flags: []FlagSpec{{Name: "raw", Summary: "Write binary bytes in text mode"}}, SideEffects: "read-only", ReadScope: "theme boundary", WriteScope: "none", OutputFields: []string{"path", "theme", "content", "sha256", "size", "kind"}},
	{Name: "theme write", Summary: "Replace a local theme file with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256|absent", Required: true}, {Name: "content-file", Value: "FILE|-", Required: true, Summary: "Vault-relative POSIX input file outside .oxpio; use - for stdin"}, {Name: "dry-run", Summary: "Do not write"}}, Examples: []string{"printf '%s\\n' 'body { color: red; }' | oxpio theme write .oxpio/themes/minimal/layouts/example.html --content-file - --if-hash absent"}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: "theme file", OutputFields: []string{"changes", "published"}},
	{Name: "theme edit", Summary: "Patch a local theme text file with CAS", Arguments: "PATH", Flags: []FlagSpec{{Name: "output", Value: "PATH", Summary: "Formal output boundary"}, {Name: "if-hash", Value: "SHA256", Required: true}, {Name: "edits-file", Value: "FILE|-", Required: true, Summary: "Vault-relative POSIX JSON input outside .oxpio; use - for stdin"}, {Name: "dry-run", Summary: "Do not write"}}, SideEffects: "theme mutation", ReadScope: "theme boundary", WriteScope: "theme file", OutputFields: []string{"changes", "published"}},
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
	spec.Examples = append([]string(nil), spec.Examples...)
	addFlag := func(name, value, summary string) {
		for _, flag := range spec.Flags {
			if flag.Name == name {
				return
			}
		}
		spec.Flags = append(spec.Flags, FlagSpec{Name: name, Value: value, Summary: summary})
	}
	setDefault := func(name, value string) {
		for index := range spec.Flags {
			if spec.Flags[index].Name == name {
				spec.Flags[index].Default = value
				return
			}
		}
	}
	hasFlag := func(name string) bool {
		for _, flag := range spec.Flags {
			if flag.Name == name {
				return true
			}
		}
		return false
	}
	if spec.Name != "version" {
		addFlag("vault", "PATH", "Path to the Vault")
	}
	addFlag("format", "text|json", "Output format")
	addFlag("version", "", "Print the OXPIO version")
	if spec.Name == "context" || spec.Name == "find" || spec.Name == "ls" || spec.Name == "read" || spec.Name == "tags" || spec.Name == "grep" {
		addFlag("output", "PATH", "Formal output boundary")
	}
	if spec.Name == "editor" || spec.Name == "serve" {
		addFlag("port", "NUM", "Listener port")
	}
	if spec.Name == "editor" || spec.Name == "serve" {
		addFlag("listen", "ADDRESS", "Listener bind address")
	}
	if spec.Name == "theme preview" {
		addFlag("port", "NUM", "Listener port")
	}
	setDefault("format", "text")
	if spec.Name != "version" && spec.Name != "help" && spec.Name != "context" {
		setDefault("vault", ".")
	}
	if hasFlag("output") {
		setDefault("output", "<vault>/public")
	}
	if spec.Name == "serve" || spec.Name == "editor" || spec.Name == "theme preview" {
		setDefault("port", "8080")
	}
	if spec.Name == "editor" || spec.Name == "serve" {
		setDefault("listen", "127.0.0.1")
	}
	if len(spec.Examples) == 0 {
		spec.Examples = []string{"oxpio " + spec.Name}
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

// CommandGroup returns the discoverable subcommands under a registry parent.
func CommandGroup(name string) []CommandSpec {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return nil
	}
	prefix := name + " "
	result := make([]CommandSpec, 0)
	for _, raw := range commandSpecs {
		if strings.HasPrefix(raw.Name, prefix) {
			result = append(result, enrichCommandSpec(raw))
		}
	}
	return result
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
		"write or edit with an explicit CAS hash; use - for --content-file/--edits-file stdin and Vault-relative POSIX inputs outside .oxpio, .obsidian, and formal output",
		"configure attachments.directory first, initialize attachment/section directories and indexes from the root down, write navigation last, then validate, optionally publish --dry-run, and explicitly publish",
	}})
}

// HelpText returns a deterministic human-readable registry overview.
func HelpText(command string, agentOnly bool) string {
	if command != "" {
		if spec, ok := FindCommand(command); ok {
			var b strings.Builder
			b.WriteString(spec.Name + " - " + spec.Summary + "\nArguments: " + valueOrNone(spec.Arguments) + "\nSide effects: " + spec.SideEffects + "\nReads: " + spec.ReadScope + "\nWrites: " + spec.WriteScope + "\n")
			if len(spec.Flags) != 0 {
				b.WriteString("Flags:\n")
				for _, flag := range spec.Flags {
					b.WriteString("  --" + flag.Name)
					if flag.Value != "" {
						b.WriteString(" ")
						b.WriteString(flag.Value)
					}
					if flag.Required {
						b.WriteString(" (required)")
					}
					if flag.Repeatable {
						b.WriteString(" (repeatable)")
					}
					if flag.Default != "" {
						b.WriteString(" [default=")
						b.WriteString(flag.Default)
						b.WriteString("]")
					}
					if flag.Summary != "" {
						b.WriteString(" - ")
						b.WriteString(flag.Summary)
					}
					b.WriteByte('\n')
				}
			}
			if len(spec.Examples) != 0 {
				b.WriteString("Examples:\n")
				for _, example := range spec.Examples {
					b.WriteString("  ")
					b.WriteString(example)
					b.WriteByte('\n')
				}
			}
			return b.String()
		}
		if group := CommandGroup(command); len(group) != 0 {
			var b strings.Builder
			b.WriteString(command + " commands:\n")
			for _, spec := range group {
				b.WriteString("  " + spec.Name + " - " + spec.Summary + "\n")
			}
			return b.String()
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
		b.WriteString("Input files: use --content-file - or --edits-file - for stdin; otherwise use a Vault-relative POSIX path outside .oxpio, .obsidian, and the formal output.\n")
		b.WriteString("Import order: configure attachments.directory first; create attachment/section directories and section _index.md files from the root down, write navigation after all target sections exist, then validate.\n")
		if _, ok := FindCommand("grep"); ok {
			b.WriteString("grep QUERY: literal Unicode-folded source search by default; --regex uses RE2 and rejects zero-width patterns; --scope source|body|title|path|metadata|all; --context/--before/--after 0..20 (context is exclusive); --max-results 1..10000 (default 1000); --max-line-length 1..8388608 (default 1048576); --max-scan-bytes 1..10737418240 (default 1073741824); repeated path/file/tag/section/type/status/publish filters OR within a flag and AND across flags; --include-unpublished is required for hidden sources.\n")
		}
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
