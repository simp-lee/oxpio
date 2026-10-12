// Package cli provides the OXPIO command-line entrypoint.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internaledit "github.com/simp-lee/oxpio/internal/edit"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/simp-lee/oxpio/internal/model"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	internalsiteplan "github.com/simp-lee/oxpio/internal/siteplan"
	"github.com/spf13/cobra"
)

const defaultConfigFilename = internalconfig.Filename

type previewServer interface {
	ListenAndServe() error
	EnableLiveReload()
	NotifyReload()
}

func closePreviewServer(server previewServer) error {
	if closer, ok := server.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func closeFileWatcher(watcher fileWatcher) error {
	if watcher == nil {
		return nil
	}
	return watcher.Close()
}

type commandDependencies struct {
	buildSiteWithOptions func(vaultPath string, outputPath string, options internalbuild.Options) (*internalbuild.BuildResult, error)
	publishSite          func(vaultPath string, outputPath string, options internalapplication.PublishOptions) (*internalapplication.PublishResult, error)
	newPreviewServer     func(outputPath string, port int) (previewServer, error)
	newEditServer        func(vaultPath string, outputPath string, port int, listenHost string, catalog *model.SourceCatalog) (previewServer, error)
	newFileWatcher       func() (fileWatcher, error)
}

// silentError marks an already-serialized JSON protocol failure. The process
// entrypoint must not print a second human error after the envelope.
type silentError struct {
	err      error
	exitCode int
}

func (e silentError) Error() string { return e.err.Error() }
func (e silentError) Unwrap() error { return e.err }
func (e silentError) Silent() bool  { return true }
func (e silentError) ExitCode() int { return e.exitCode }

type codedError struct {
	err      error
	exitCode int
}

func (e codedError) Error() string { return e.err.Error() }
func (e codedError) Unwrap() error { return e.err }
func (e codedError) ExitCode() int { return e.exitCode }

type diagnosticError struct {
	err         error
	diagnostics []internalagent.Diagnostic
}

func (e diagnosticError) Error() string { return e.err.Error() }
func (e diagnosticError) Unwrap() error { return e.err }
func (e diagnosticError) Diagnostics() []internalagent.Diagnostic {
	return append([]internalagent.Diagnostic(nil), e.diagnostics...)
}

// Execute is the single CLI entrypoint used by main.
func Execute(args []string) error {
	err := executeWithDeps(args, defaultCommandDependencies(), os.Stdout, os.Stderr)
	if err == nil {
		return nil
	}
	if _, ok := err.(interface{ ExitCode() int }); ok {
		return err
	}
	return codedError{err: errors.New(sanitizeCLIError(err, args)), exitCode: classifyCLIError(err)}
}

func executeWithDeps(args []string, deps commandDependencies, stdout io.Writer, stderr io.Writer) error {
	root := newRootCommand(deps)
	effectiveArgs := args
	if versionFlagRequested(args) {
		withoutVersion := removeVersionFlags(args)
		if versionRequestCanDispatch(withoutVersion) {
			effectiveArgs = []string{"version"}
			if jsonRequested(args) {
				effectiveArgs = append(effectiveArgs, "--format", "json")
			}
		}
	}
	if jsonRequested(args) && isStreamingServiceCommand(effectiveArgs) && formatArgumentsValid(args) {
		return executeStreamingServiceJSON(effectiveArgs, args, root, stdout, stderr)
	}
	if jsonRequested(args) {
		var commandOutput bytes.Buffer
		var commandErrors bytes.Buffer
		root.SetOut(&commandOutput)
		root.SetErr(&commandErrors)
		root.SetArgs(effectiveArgs)
		var err error
		if helpFlagRequested(args) && !formatArgumentsValid(args) {
			err = fmt.Errorf("--format must be text or json")
		} else {
			err = root.Execute()
		}
		command := registryCommandFromArgs(effectiveArgs)
		var data json.RawMessage
		if commandOutput.Len() != 0 {
			trimmed := bytes.TrimSpace(commandOutput.Bytes())
			if json.Valid(trimmed) {
				data = append(json.RawMessage(nil), trimmed...)
			} else {
				encoded, marshalErr := json.Marshal(map[string]string{"output": string(trimmed)})
				if marshalErr == nil {
					data = encoded
				}
			}
		}
		var envelope internalagent.Envelope
		if err != nil {
			diagnostics := structuredErrorDiagnostics(err)
			if len(diagnostics) == 0 {
				diagnostics = parseCLIErrorDiagnostics(commandErrors.String(), args)
			}
			if len(diagnostics) == 0 {
				diagnostics = []internalagent.Diagnostic{fallbackCLIDiagnostic(err, args)}
			}
			failureData := json.RawMessage(nil)
			if command == "validate" || command == "theme validate" {
				failureData = data
			}
			if len(diagnostics) == 0 {
				envelope = internalagent.FailureWithDataAndDiagnostics(command, failureData, []internalagent.Diagnostic{{Code: "COMMAND_ERROR", Severity: "error", Category: "command", Message: sanitizeCLIError(err, args)}})
			} else {
				envelope = internalagent.FailureWithDataAndDiagnostics(command, failureData, sanitizeDiagnostics(diagnostics, args))
			}
		} else {
			data, changes := splitChangesData(data)
			envelope = internalagent.SuccessWithChanges(command, data, changes)
		}
		if stdout != nil {
			if writeErr := internalagent.Write(stdout, envelope); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			return silentError{err: err, exitCode: classifyCLIError(err)}
		}
		return nil
	}
	if stdout != nil {
		root.SetOut(stdout)
	}
	if stderr != nil {
		root.SetErr(&sanitizingWriter{output: stderr, args: args})
	}
	root.SetArgs(effectiveArgs)
	if helpFlagRequested(args) && !formatArgumentsValid(args) {
		return fmt.Errorf("--format must be text or json")
	}
	return root.Execute()
}

type sanitizingWriter struct {
	output io.Writer
	args   []string
}

func (w *sanitizingWriter) Write(p []byte) (int, error) {
	parts := strings.SplitAfter(string(p), "\n")
	var sanitized strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		newline := strings.HasSuffix(part, "\n")
		if newline {
			part = strings.TrimSuffix(part, "\n")
		}
		sanitized.WriteString(sanitizeCLIText(part, w.args))
		if newline {
			sanitized.WriteByte('\n')
		}
	}
	if _, err := io.WriteString(w.output, sanitized.String()); err != nil {
		return 0, err
	}
	return len(p), nil
}

type streamingServiceJSONWriter struct {
	output  io.Writer
	command string
	buffer  bytes.Buffer
	sent    bool
}

func (w *streamingServiceJSONWriter) Write(p []byte) (int, error) {
	if w.sent {
		return 0, fmt.Errorf("service emitted more than one startup result")
	}
	_, _ = w.buffer.Write(p)
	if !bytes.Contains(w.buffer.Bytes(), []byte{'\n'}) {
		return len(p), nil
	}
	trimmed := bytes.TrimSpace(w.buffer.Bytes())
	if !json.Valid(trimmed) {
		return 0, fmt.Errorf("service startup result is not JSON")
	}
	if err := internalagent.Write(w.output, internalagent.Success(w.command, trimmed)); err != nil {
		return 0, err
	}
	w.sent = true
	return len(p), nil
}

func isStreamingServiceCommand(args []string) bool {
	command := registryCommandFromArgs(args)
	return command == "serve" || command == "editor" || command == "theme preview"
}

func executeStreamingServiceJSON(effectiveArgs, originalArgs []string, root *cobra.Command, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	writer := &streamingServiceJSONWriter{output: stdout, command: registryCommandFromArgs(effectiveArgs)}
	root.SetOut(writer)
	root.SetErr(&sanitizingWriter{output: stderr, args: originalArgs})
	root.SetArgs(effectiveArgs)
	err := root.Execute()
	if writer.sent {
		if err != nil {
			_, _ = fmt.Fprintln(stderr, sanitizeCLIError(err, originalArgs))
			return silentError{err: err, exitCode: classifyCLIError(err)}
		}
		return nil
	}
	if err == nil {
		err = fmt.Errorf("service did not announce startup")
	}
	diagnostic := fallbackCLIDiagnostic(err, originalArgs)
	if writeErr := internalagent.Write(stdout, internalagent.FailureWithDiagnostics(writer.command, []internalagent.Diagnostic{diagnostic})); writeErr != nil {
		return writeErr
	}
	return silentError{err: err, exitCode: classifyCLIError(err)}
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		buildSiteWithOptions: internalbuild.BuildWithOptions,
		publishSite: func(vaultPath string, outputPath string, options internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
			return internalapplication.Publish(vaultPath, outputPath, options)
		},
		newPreviewServer: func(outputPath string, port int) (previewServer, error) {
			return internalserver.New(outputPath, port)
		},
		newEditServer: func(vaultPath string, outputPath string, port int, listenHost string, catalog *model.SourceCatalog) (previewServer, error) {
			return internaledit.NewWithListen(vaultPath, outputPath, port, listenHost, catalog)
		},
		newFileWatcher: newFSNotifyWatcher,
	}
}

func publishWithDependencies(deps commandDependencies, vaultPath, outputPath string, options internalapplication.PublishOptions) (*internalapplication.PublishResult, error) {
	if deps.publishSite == nil {
		return nil, fmt.Errorf("publish service is unavailable")
	}
	return deps.publishSite(vaultPath, outputPath, options)
}

func newRootCommand(deps commandDependencies) *cobra.Command {
	var showVersion bool
	var format string
	cmd := &cobra.Command{
		Use:           "oxpio",
		Short:         "Generate and preview static sites from OXPIO Vaults",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				if format == "json" {
					data, err := json.Marshal(map[string]string{"version": formatVersion()})
					if err != nil {
						return err
					}
					_, err = cmd.OutOrStdout().Write(append(data, '\n'))
					return err
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), formatVersion())
				return err
			}
			if format == "json" {
				data, err := internalapplication.HelpJSON(false)
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			_, err := io.WriteString(cmd.OutOrStdout(), internalapplication.HelpText("", false))
			return err
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.PersistentFlags().BoolVar(&showVersion, "version", false, "Print the OXPIO version")
	cmd.PersistentFlags().StringVar(&format, "format", "text", "Output format: text or json")
	cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if format != "text" && format != "json" {
			return fmt.Errorf("--format must be text or json")
		}
		return nil
	}

	registered := make(map[string]*cobra.Command)
	parents := make(map[string]*cobra.Command)
	for _, spec := range internalapplication.CommandSpecs() {
		parts := strings.Split(spec.Name, " ")
		command := configureRegistryCommand(commandForSpec(spec, deps), spec)
		if len(parts) == 1 {
			registered[spec.Name] = command
			continue
		}
		parentName := parts[0]
		parent := parents[parentName]
		if parent == nil {
			name := parentName
			parent = &cobra.Command{
				Use:   name,
				Short: name + " commands",
				Args:  cobra.NoArgs,
				RunE: func(*cobra.Command, []string) error {
					return fmt.Errorf("%s requires a subcommand", name)
				},
			}
			parent.SetHelpFunc(registryParentHelpFunc(name))
			parents[parentName] = parent
		}
		parent.AddCommand(command)
	}
	for name, parent := range parents {
		registered[name] = parent
	}
	rootCommands := make([]*cobra.Command, 0, len(registered))
	for _, spec := range internalapplication.CommandSpecs() {
		if !strings.Contains(spec.Name, " ") {
			if command := registered[spec.Name]; command != nil {
				rootCommands = append(rootCommands, command)
			}
		}
	}
	addedParents := make(map[string]struct{}, len(parents))
	for _, spec := range internalapplication.CommandSpecs() {
		parts := strings.Split(spec.Name, " ")
		if len(parts) != 2 {
			continue
		}
		parent := parents[parts[0]]
		if _, added := addedParents[parts[0]]; added || parent == nil {
			continue
		}
		rootCommands = append(rootCommands, parent)
		addedParents[parts[0]] = struct{}{}
	}
	cmd.AddCommand(rootCommands...)
	cmd.SetHelpFunc(registryHelpFunc(""))
	if helpCommand := registered["help"]; helpCommand != nil {
		cmd.SetHelpCommand(helpCommand)
	}
	return cmd
}

func configureRegistryCommand(cmd *cobra.Command, spec internalapplication.CommandSpec) *cobra.Command {
	cmd.Short = spec.Summary
	cmd.SetHelpFunc(registryHelpFunc(spec.Name))
	return cmd
}

func registryHelpFunc(name string) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, _ []string) {
		if formatOf(cmd) == "json" {
			data, err := registryHelpJSON(name)
			if err != nil {
				_, _ = io.WriteString(cmd.OutOrStdout(), err.Error()+"\n")
				return
			}
			_, _ = cmd.OutOrStdout().Write(append(data, '\n'))
			return
		}
		_, _ = io.WriteString(cmd.OutOrStdout(), internalapplication.HelpText(name, false))
	}
}

func registryHelpJSON(name string) ([]byte, error) {
	if name == "" {
		return internalapplication.HelpJSON(false)
	}
	if spec, ok := internalapplication.FindCommand(name); ok {
		return json.Marshal(struct {
			Schema  string                          `json:"schema"`
			Command internalapplication.CommandSpec `json:"command"`
		}{Schema: "oxpio.agent/help/v1", Command: spec})
	}
	commands := make([]internalapplication.CommandSpec, 0)
	for _, spec := range internalapplication.CommandSpecs() {
		if strings.HasPrefix(spec.Name, name+" ") {
			commands = append(commands, spec)
		}
	}
	return json.Marshal(struct {
		Schema   string                            `json:"schema"`
		Commands []internalapplication.CommandSpec `json:"commands"`
	}{Schema: "oxpio.agent/help/v1", Commands: commands})
}

func registryParentHelpFunc(name string) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, _ []string) {
		if formatOf(cmd) == "json" {
			data, err := registryHelpJSON(name)
			if err != nil {
				_, _ = io.WriteString(cmd.OutOrStdout(), err.Error()+"\n")
				return
			}
			_, _ = cmd.OutOrStdout().Write(append(data, '\n'))
			return
		}
		var builder strings.Builder
		builder.WriteString(name + " commands:\n")
		for _, spec := range internalapplication.CommandSpecs() {
			if strings.HasPrefix(spec.Name, name+" ") {
				builder.WriteString("  " + spec.Name + " - " + spec.Summary + "\n")
			}
		}
		_, _ = io.WriteString(cmd.OutOrStdout(), builder.String())
	}
}

func commandForSpec(spec internalapplication.CommandSpec, deps commandDependencies) *cobra.Command {
	switch spec.Name {
	case "help":
		return newHelpCommand()
	case "context":
		return newContextCommand()
	case "init":
		return newInitCommand()
	case "validate":
		return newValidateCommand()
	case "publish":
		return newPublishCommand(deps)
	case "serve":
		return newServeCommand(deps)
	case "editor":
		return newEditorCommand(deps)
	case "version":
		return newVersionCommand()
	case "read":
		return newReadCommand()
	case "find":
		return newFindCommand()
	case "ls":
		return newLSCommand()
	case "tags":
		return newTagsCommand()
	case "grep":
		return newGrepCommand()
	case "theme init":
		return newThemeInitCommand()
	case "theme validate":
		return newThemeValidateCommand()
	case "theme context":
		return newThemeContextCommand()
	case "theme read":
		return newThemeReadCommand()
	case "theme write":
		return newThemeWriteCommand()
	case "theme edit":
		return newThemeEditCommand()
	case "theme preview":
		return newThemePreviewCommand(deps)
	default:
		return newRegistryPlaceholder(spec)
	}
}

func newVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the OXPIO version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if formatOf(cmd) == "json" {
				data, err := json.Marshal(map[string]string{"version": formatVersion()})
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), formatVersion())
			return err
		},
	}
	return cmd
}

func formatOf(cmd *cobra.Command) string {
	value, err := cmd.Root().PersistentFlags().GetString("format")
	if err != nil {
		return "text"
	}
	return value
}

func resolveOptionalVaultFlag(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return resolveVaultPath(value)
}

func versionFlagRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "--version=true" {
			return true
		}
	}
	return false
}

func removeVersionFlags(args []string) []string {
	result := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--version" || arg == "--version=true" {
			continue
		}
		result = append(result, arg)
	}
	return result
}

func versionRequestCanDispatch(args []string) bool {
	commandName := registryCommandFromArgs(args)
	var spec internalapplication.CommandSpec
	if commandName != "" {
		var ok bool
		spec, ok = internalapplication.FindCommand(commandName)
		if !ok {
			return false
		}
	}
	flags := map[string]internalapplication.FlagSpec{
		"--format": {Name: "format", Value: "FORMAT"},
	}
	for _, flag := range spec.Flags {
		if flag.Name != "" && flag.Name != "version" {
			flags["--"+flag.Name] = flag
		}
	}
	positionals := make([]string, 0)
	vaultProvided := false
	includeUnpublished := false
	boolFlags := make(map[string]bool)
	flagSeen := make(map[string]bool)
	flagValues := make(map[string][]string)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "-h" || arg == "--help" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		name, value, hasValue := arg, "", false
		if equal := strings.IndexByte(arg, '='); equal >= 0 {
			name, value, hasValue = arg[:equal], arg[equal+1:], true
		}
		flag, ok := flags[name]
		if !ok {
			return false
		}
		flagSeen[name] = true
		if flag.Value != "" {
			if !hasValue {
				if index+1 >= len(args) || (strings.HasPrefix(args[index+1], "-") && args[index+1] != "-") {
					return false
				}
				index++
				value, hasValue = args[index], true
			}
			if strings.TrimSpace(value) == "" || !validVersionFlagValue(name, value) {
				return false
			}
			flagValues[name] = append(flagValues[name], value)
		} else {
			if hasValue {
				return false
			}
			boolFlags[name] = true
		}
		if name == "--vault" {
			vaultProvided = true
		}
		if name == "--include-unpublished" {
			includeUnpublished = true
		}
	}
	for _, flag := range spec.Flags {
		if flag.Required && !flagSeen["--"+flag.Name] {
			return false
		}
	}
	if (boolFlags["--publish"] && boolFlags["--unpublish"]) || (boolFlags["--setup"] && boolFlags["--reset-password"]) {
		return false
	}
	if commandName == "editor" && (boolFlags["--setup"] || boolFlags["--reset-password"]) && (len(flagValues["--output"]) != 0 || len(flagValues["--port"]) != 0 || len(flagValues["--listen"]) != 0) {
		return false
	}
	if commandName == "grep" || commandName == "find" {
		if commandName == "grep" && len(flagValues["--context"]) != 0 && (len(flagValues["--before"]) != 0 || len(flagValues["--after"]) != 0) {
			return false
		}
		for _, value := range flagValues["--publish"] {
			if value == "false" && !includeUnpublished {
				return false
			}
		}
	}
	if commandName == "" {
		return len(positionals) == 0
	}
	commandWords := strings.Fields(commandName)
	if len(positionals) < len(commandWords) {
		return false
	}
	for index, word := range commandWords {
		if positionals[index] != word {
			return false
		}
	}
	positionals = positionals[len(commandWords):]
	if commandName == "help" {
		if len(positionals) > 2 {
			return false
		}
		if len(positionals) != 0 {
			target := strings.Join(positionals, " ")
			if _, ok := internalapplication.FindCommand(target); !ok && len(internalapplication.CommandGroup(target)) == 0 {
				return false
			}
		}
	} else {
		argumentWords := strings.Fields(spec.Arguments)
		switch {
		case len(argumentWords) == 0 && len(positionals) != 0:
			return false
		case len(argumentWords) == 1 && strings.HasPrefix(argumentWords[0], "[") && len(positionals) > 1:
			return false
		case len(argumentWords) > 0 && !strings.HasPrefix(argumentWords[0], "[") && len(positionals) != len(argumentWords):
			return false
		}
		if commandName == "read" {
			if _, err := internalapplication.NormalizeSourcePath(positionals[0]); err != nil {
				return false
			}
		}
	}
	if includeUnpublished && (commandName == "help" || commandName == "context") && !vaultProvided {
		return false
	}
	return true
}

func validVersionFlagValue(name, value string) bool {
	if name == "--format" {
		return value == "text" || value == "json"
	}
	if name == "--port" {
		number, err := strconv.Atoi(value)
		return err == nil && number >= 1 && number <= 65535
	}
	bounds := map[string][2]int64{
		"--offset":          {0, int64(^uint(0) >> 1)},
		"--limit":           {1, 10000},
		"--context":         {0, 20},
		"--before":          {0, 20},
		"--after":           {0, 20},
		"--max-results":     {1, 10000},
		"--max-line-length": {1, 8388608},
		"--max-scan-bytes":  {1, 10737418240},
	}
	if bound, ok := bounds[name]; ok {
		number, err := strconv.ParseInt(value, 10, 64)
		return err == nil && number >= int64(bound[0]) && number <= int64(bound[1])
	}
	switch name {
	case "--scope":
		return containsOption(value, "source", "body", "title", "path", "metadata", "all")
	case "--kind":
		return containsOption(value, "section", "article", "all")
	case "--type":
		return containsOption(value, "doc", "post", "page")
	case "--status":
		return containsOption(value, "stable", "experimental", "deprecated")
	case "--publish":
		return value == "true" || value == "false"
	default:
		return true
	}
}

func containsOption(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func helpFlagRequested(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func formatArgumentsValid(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--format" {
			if index+1 >= len(args) || (args[index+1] != "text" && args[index+1] != "json") {
				return false
			}
			index++
			continue
		}
		if strings.HasPrefix(arg, "--format=") {
			value := strings.TrimPrefix(arg, "--format=")
			if value != "text" && value != "json" {
				return false
			}
		}
	}
	return true
}

func jsonRequested(args []string) bool {
	for index, arg := range args {
		if arg == "--format=json" {
			return true
		}
		if arg == "--format" && index+1 < len(args) && args[index+1] == "json" {
			return true
		}
	}
	return false
}

func registryCommandFromArgs(args []string) string {
	parts := make([]string, 0, 2)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && (arg == "--format" || arg == "--vault" || arg == "--output") {
				index++
			}
			continue
		}
		parts = append(parts, arg)
		if len(parts) == 2 {
			break
		}
	}
	for len(parts) > 0 {
		candidate := strings.Join(parts, " ")
		if _, ok := internalapplication.FindCommand(candidate); ok {
			return candidate
		}
		parts = parts[:len(parts)-1]
	}
	return ""
}

func fallbackCLIDiagnostic(err error, args []string) internalagent.Diagnostic {
	code, category := "COMMAND_ERROR", "command"
	switch classifyCLIError(err) {
	case 2:
		code, category = "INVALID_ARGUMENT", "parameter"
	case 3:
		code, category = "CONFLICT", "conflict"
	case 4:
		code, category = "PERMISSION_DENIED", "permission"
	case 5:
		code, category = "IO_ERROR", "io"
	}
	return internalagent.Diagnostic{Code: code, Severity: "error", Category: category, Message: sanitizeCLIError(err, args)}
}

func classifyCLIError(err error) int {
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) && coded.ExitCode() != 0 {
		return coded.ExitCode()
	}
	message := strings.ToLower(err.Error())
	var networkError *net.OpError
	if errors.As(err, &networkError) {
		return 5
	}
	if errors.Is(err, os.ErrPermission) || strings.Contains(message, "permission") || strings.Contains(message, "not available in the selected read scope") {
		return 4
	}
	if strings.Contains(message, "conflict") || strings.Contains(message, "hash mismatch") {
		return 3
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrClosed) || errors.Is(err, internalapplication.ErrSourceNotFound) {
		return 5
	}
	if strings.Contains(message, "unknown command") || strings.Contains(message, "unknown flag") || strings.Contains(message, "accepts no") || strings.Contains(message, "accepts at most") || strings.Contains(message, "must be true or false") || strings.Contains(message, "--format must") || isCLIParameterError(err) {
		return 2
	}
	return 1
}

func isCLIParameterError(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"invalid argument", "invalid value", "invalid syntax", "flag needs an argument", "unknown shorthand", "unknown flag", "unknown command", "parsing error", "required flag", "accepts ", "requires ", "at least one arg", "exactly one arg", "mutually exclusive", "output path", "path must be", "outside the content source boundary", "non-portable path", "must name a markdown", "offset must", "limit must", "--kind must", "must be between"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func splitChangesData(data json.RawMessage) (json.RawMessage, []internalagent.Change) {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return data, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return data, nil
	}
	changesValue, ok := object["changes"]
	if !ok {
		return data, nil
	}
	var changes []internalagent.Change
	if err := json.Unmarshal(changesValue, &changes); err != nil {
		return data, nil
	}
	delete(object, "changes")
	withoutChanges, err := json.Marshal(object)
	if err != nil {
		return data, nil
	}
	return withoutChanges, changes
}

func structuredErrorDiagnostics(err error) []internalagent.Diagnostic {
	var structured interface {
		Diagnostics() []internalagent.Diagnostic
	}
	if errors.As(err, &structured) {
		return structured.Diagnostics()
	}
	var single interface {
		Diagnostic() internalagent.Diagnostic
	}
	if errors.As(err, &single) {
		return []internalagent.Diagnostic{single.Diagnostic()}
	}
	return nil
}

func sanitizeDiagnostics(diagnostics []internalagent.Diagnostic, args []string) []internalagent.Diagnostic {
	result := make([]internalagent.Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		diagnostic.Message = sanitizeCLIText(diagnostic.Message, args)
		diagnostic.Path = sanitizeDiagnosticPath(diagnostic.Path, args)
		diagnostic.Target = sanitizeDiagnosticPath(diagnostic.Target, args)
		result = append(result, diagnostic)
	}
	return result
}

func sanitizeDiagnosticPath(value *string, args []string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	candidate := filepath.ToSlash(strings.TrimSpace(*value))
	if filepath.IsAbs(candidate) || isWindowsAbsolutePath(candidate) {
		if relative, ok := relativeVaultPath(candidate, args); ok {
			return &relative
		}
		return nil
	}
	if candidate == "." || candidate == ".." || strings.HasPrefix(candidate, "../") || strings.Contains(candidate, "/../") || strings.Contains(candidate, "\\") {
		return nil
	}
	return &candidate
}

func relativeVaultPath(candidate string, args []string) (string, bool) {
	vaultValue := flagArgument(args, "--vault")
	if strings.TrimSpace(vaultValue) == "" {
		vaultValue = "."
	}
	vault, err := filepath.Abs(vaultValue)
	if err != nil {
		return "", false
	}
	if resolved, resolveErr := filepath.EvalSymlinks(vault); resolveErr == nil {
		vault = resolved
	}
	resolved := candidate
	if absolute, absErr := filepath.Abs(candidate); absErr == nil {
		resolved = absolute
	}
	if real, resolveErr := filepath.EvalSymlinks(resolved); resolveErr == nil {
		resolved = real
	}
	relative, err := filepath.Rel(vault, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	if relative == "" || strings.Contains(relative, "\\") {
		return "", false
	}
	return relative, true
}

func redactHiddenDiagnostics(diagnostics []internalagent.Diagnostic, planned *internalsiteplan.Result) []internalagent.Diagnostic {
	if planned == nil || planned.Plan == nil {
		return diagnostics
	}
	prefixes := make([]string, 0)
	sectionPublish := make(map[string]bool)
	for _, source := range planned.Sources.Sections {
		if source == nil {
			continue
		}
		sectionPath := filepath.ToSlash(source.SectionPath)
		if sectionPath == "." {
			sectionPath = ""
		}
		sectionPublish[sectionPath] = source.Frontmatter.Publish != nil && *source.Frontmatter.Publish
	}
	isSectionHidden := func(sectionPath string) bool {
		sectionPath = filepath.ToSlash(strings.Trim(sectionPath, "/"))
		if sectionPath == "." {
			sectionPath = ""
		}
		for {
			if published, ok := sectionPublish[sectionPath]; ok && !published {
				return true
			}
			if sectionPath == "" {
				return false
			}
			sectionPath = path.Dir(sectionPath)
			if sectionPath == "." {
				sectionPath = ""
			}
		}
	}
	for _, source := range planned.Sources.Sections {
		if source != nil && isSectionHidden(source.SectionPath) {
			prefixes = append(prefixes, filepath.ToSlash(source.SectionPath), filepath.ToSlash(source.RelPath))
		}
	}
	for _, source := range planned.Sources.Sources {
		if source.Article != nil && isSectionHidden(source.Article.SectionPath) {
			prefixes = append(prefixes, filepath.ToSlash(source.RelPath))
		}
	}
	return redactHiddenByPrefixes(diagnostics, prefixes)
}

func redactHiddenCatalogDiagnostics(diagnostics []internalagent.Diagnostic, catalog *model.SourceCatalog) []internalagent.Diagnostic {
	if catalog == nil {
		return diagnostics
	}
	prefixes := make([]string, 0)
	for _, entry := range catalog.Entries {
		if entry.EffectivePublish {
			continue
		}
		prefixes = append(prefixes, entry.RelPath, entry.SectionPath)
	}
	return redactHiddenByPrefixes(diagnostics, prefixes)
}

func redactHiddenByPrefixes(diagnostics []internalagent.Diagnostic, prefixes []string) []internalagent.Diagnostic {
	isHidden := func(value string) bool {
		value = filepath.ToSlash(strings.TrimSpace(value))
		for _, prefix := range prefixes {
			prefix = strings.Trim(strings.ReplaceAll(prefix, "\\\\", "/"), "/")
			if prefix == "." || prefix == "" || value == prefix || strings.HasPrefix(value, prefix+"/") {
				return true
			}
		}
		return false
	}
	result := make([]internalagent.Diagnostic, len(diagnostics))
	copy(result, diagnostics)
	for index := range result {
		hidden := result[index].Path != nil && isHidden(*result[index].Path)
		if result[index].Target != nil && isHidden(*result[index].Target) {
			hidden = true
		}
		if hidden {
			result[index].Path = nil
			result[index].Target = nil
			result[index].Message = "target is not available in the selected read scope"
		}
	}
	return result
}

func diagnosticsFromBuild(diagnostics []internaldiag.Diagnostic) []internalagent.Diagnostic {
	result := make([]internalagent.Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		severity := string(diagnostic.Severity)
		category := string(diagnostic.Kind)
		code := strings.ToUpper(strings.ReplaceAll(category, "-", "_"))
		var pathValue, fieldValue, targetValue *string
		if diagnostic.Location.Path != "" {
			value := diagnostic.Location.Path
			pathValue = &value
		}
		if diagnostic.Field != "" {
			value := diagnostic.Field
			fieldValue = &value
		}
		if diagnostic.Target != "" {
			value := diagnostic.Target
			targetValue = &value
		}
		var lineValue *int
		if diagnostic.Location.Line > 0 {
			value := diagnostic.Location.Line
			lineValue = &value
		}
		result = append(result, internalagent.Diagnostic{Code: code, Severity: severity, Category: category, Path: pathValue, Line: lineValue, Field: fieldValue, Target: targetValue, Message: diagnostic.Message})
	}
	return result
}

func sanitizeCLIError(err error, args []string) string {
	if err == nil {
		return ""
	}
	return sanitizeCLIText(err.Error(), args)
}

func sanitizeCLIText(message string, args []string) string {
	paths := make([]struct{ value, replacement string }, 0, 2)
	vaultValue := flagArgument(args, "--vault")
	if strings.TrimSpace(vaultValue) == "" {
		vaultValue, _ = filepath.Abs(".")
	}
	addPath := func(value, replacement string) {
		absolute, err := filepath.Abs(strings.TrimSpace(value))
		if err != nil {
			return
		}
		paths = append(paths, struct{ value, replacement string }{absolute, replacement})
		if resolved, resolveErr := filepath.EvalSymlinks(absolute); resolveErr == nil && resolved != absolute {
			paths = append(paths, struct{ value, replacement string }{resolved, replacement})
		}
	}
	addPath(vaultValue, "<vault>")
	if outputValue := flagArgument(args, "--output"); strings.TrimSpace(outputValue) != "" {
		addPath(outputValue, "<external-output>")
	}
	for _, item := range paths {
		message = strings.ReplaceAll(message, item.value, item.replacement)
	}
	words := strings.Fields(message)
	for index, word := range words {
		trimmed := strings.Trim(word, "\"'`()[]{}.,;:")
		if filepath.IsAbs(trimmed) || isWindowsAbsolutePath(trimmed) {
			words[index] = strings.Replace(word, trimmed, "<path>", 1)
		}
	}
	if len(words) != 0 {
		return strings.Join(words, " ")
	}
	return message
}

func isWindowsAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func flagArgument(args []string, name string) string {
	for index, arg := range args {
		if arg == name && index+1 < len(args) {
			return args[index+1]
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return ""
}

func parseCLIErrorDiagnostics(text string, args []string) []internalagent.Diagnostic {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	result := make([]internalagent.Diagnostic, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		safe := sanitizeCLIText(line, args)
		severity, category, message := "error", "command", safe
		parts := strings.SplitN(safe, " ", 3)
		if len(parts) == 3 && (parts[0] == "error" || parts[0] == "warning") {
			severity, category = parts[0], parts[1]
			message = parts[2]
			if index := strings.Index(message, ": "); index >= 0 {
				message = message[index+2:]
			}
		}
		result = append(result, internalagent.Diagnostic{Code: strings.ToUpper(category), Severity: severity, Category: category, Message: message})
	}
	return result
}

func resolveVaultPath(value string) (string, error) {
	vaultPath := strings.TrimSpace(value)
	if vaultPath == "" {
		vaultPath = "."
	}
	return internalfsutil.ResolveVaultPath(filepath.Clean(vaultPath))
}

func resolveVaultOutputPaths(vaultValue string, outputValue string) (internalfsutil.VaultOutputBoundary, error) {
	vaultPath, err := resolveVaultPath(vaultValue)
	if err != nil {
		return internalfsutil.VaultOutputBoundary{}, err
	}
	outputPath := strings.TrimSpace(outputValue)
	if outputPath == "" {
		outputPath = filepath.Join(vaultPath, "public")
	}
	return internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
}
