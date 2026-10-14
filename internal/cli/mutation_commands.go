package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/spf13/cobra"
)

func newWriteCommand() *cobra.Command {
	var vault, contentFile, expected string
	var includeUnpublished, publish, unpublish, dryRun bool
	cmd := &cobra.Command{Use: "write PATH", Short: "Replace one Markdown source with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if publish && unpublish {
			return errors.New("--publish and --unpublish are mutually exclusive")
		}
		data, err := readAgentInput(cmd, vault, contentFile, false)
		if err != nil {
			return err
		}
		var state *bool
		if publish {
			value := true
			state = &value
		}
		if unpublish {
			value := false
			state = &value
		}
		result, err := internalapplication.MutateSource(vault, "", internalapplication.SourceMutationRequest{Operation: internalapplication.MutationWrite, Path: args[0], ExpectedHash: expected, Content: data, DryRun: dryRun, IncludeUnpublished: includeUnpublished, Publish: state})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "Complete UTF-8 source or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash or absent")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&publish, "publish", false, "Explicitly publish the source")
	cmd.Flags().BoolVar(&unpublish, "unpublish", false, "Explicitly unpublish the source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("content-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newEditCommand() *cobra.Command {
	var vault, editsFile, expected string
	var includeUnpublished, publish, unpublish, dryRun bool
	cmd := &cobra.Command{Use: "edit PATH", Short: "Apply exact source text patches with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if publish && unpublish {
			return errors.New("--publish and --unpublish are mutually exclusive")
		}
		data, err := readAgentInput(cmd, vault, editsFile, true)
		if err != nil {
			return err
		}
		var state *bool
		if publish {
			value := true
			state = &value
		}
		if unpublish {
			value := false
			state = &value
		}
		result, err := internalapplication.MutateSource(vault, "", internalapplication.SourceMutationRequest{Operation: internalapplication.MutationEdit, Path: args[0], ExpectedHash: expected, Content: data, DryRun: dryRun, IncludeUnpublished: includeUnpublished, Publish: state})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&editsFile, "edits-file", "", "Strict PatchRequest JSON or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&publish, "publish", false, "Explicitly publish the source")
	cmd.Flags().BoolVar(&unpublish, "unpublish", false, "Explicitly unpublish the source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("edits-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newMoveCommand() *cobra.Command {
	var vault, expected, destinationExpected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "move SOURCE DEST", Short: "Move one Markdown article with CAS", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := internalapplication.MutateSource(vault, "", internalapplication.SourceMutationRequest{Operation: internalapplication.MutationMove, Path: args[0], Destination: args[1], ExpectedHash: expected, DestinationExpectedHash: destinationExpected, DryRun: dryRun, IncludeUnpublished: includeUnpublished})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().StringVar(&destinationExpected, "dest-if-hash", "", "Destination hash; must be absent")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("if-hash")
	_ = cmd.MarkFlagRequired("dest-if-hash")
	return cmd
}

func newDeleteCommand() *cobra.Command {
	var vault, expected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "delete PATH", Short: "Delete one Markdown article with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := internalapplication.MutateSource(vault, "", internalapplication.SourceMutationRequest{Operation: internalapplication.MutationDelete, Path: args[0], ExpectedHash: expected, DryRun: dryRun, IncludeUnpublished: includeUnpublished})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newUploadCommand() *cobra.Command {
	var vault, file, name, kind, expected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "upload DIR", Short: "Create one Markdown or attachment file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if kind != internalapplication.UploadArticle && kind != internalapplication.UploadSectionIndex && kind != internalapplication.UploadAttachment {
			return errors.New("--kind must be article, section-index, or attachment")
		}
		if file == "" {
			return errors.New("--file is required")
		}
		var data []byte
		var err error
		if file == "-" {
			if name == "" {
				return errors.New("--name is required when --file is -")
			}
			data, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), (64<<20)+1))
			if err == nil && len(data) > 64<<20 {
				err = errors.New("upload exceeds 64 MiB")
			}
		} else {
			if name != "" {
				return errors.New("--name is only valid with --file -")
			}
			name = path.Base(file)
			data, err = readUploadInput(vault, file)
		}
		if err != nil {
			return err
		}
		result, mutationErr := internalapplication.MutateUpload(vault, "", internalapplication.UploadMutationRequest{
			Directory: args[0], Name: name, Kind: kind, ExpectedHash: expected,
			Content: data, IncludeUnpublished: includeUnpublished, DryRun: dryRun,
		})
		return writeMutationCommandResult(cmd, result, mutationErr)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&file, "file", "", "Input file or -")
	cmd.Flags().StringVar(&name, "name", "", "Safe destination basename for stdin")
	cmd.Flags().StringVar(&kind, "kind", "", "Upload kind: article, section-index, or attachment")
	cmd.Flags().StringVar(&expected, "if-hash", "", "Must be absent")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished Markdown source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func readUploadInput(vaultPath, value string) ([]byte, error) {
	vault, err := resolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(value) || strings.Contains(value, `\`) || path.Clean(value) != value || strings.HasPrefix(value, ".") || strings.ContainsAny(value, "?#\x00") {
		return nil, errors.New("upload input must be a Vault-relative POSIX path")
	}
	_, file, info, err := internalfsutil.OpenContainedRegularFile(vault, value)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("upload input must be a regular non-symlink file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, errors.New("upload exceeds 64 MiB")
	}
	return data, nil
}

func newMkdirCommand() *cobra.Command {
	var vault string
	var dryRun bool
	cmd := &cobra.Command{Use: "mkdir PATH", Short: "Create one Vault directory", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := internalapplication.MutateDirectory(vault, "", internalapplication.DirectoryMutationRequest{Operation: internalapplication.MutationMkdir, Path: args[0], ExpectedHash: internalapplication.AbsentHash, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	return cmd
}

func newRenameDirCommand() *cobra.Command {
	var vault, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "rename-dir SOURCE DEST", Short: "Rename one empty Vault directory", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		result, err := internalapplication.MutateDirectory(vault, "", internalapplication.DirectoryMutationRequest{Operation: internalapplication.MutationRenameDir, Path: args[0], Destination: args[1], ExpectedHash: expected, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&expected, "if-hash", "", "Directory SHA-256 hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newRecoverCommand() *cobra.Command {
	var vault string
	var dryRun bool
	cmd := &cobra.Command{Use: "recover", Short: "Inspect or recover a transaction", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		resolved, err := resolveVaultPath(vault)
		if err != nil {
			return err
		}
		result, err := internalpublish.Recover(resolved, dryRun)
		if err != nil {
			return err
		}
		data, err := json.Marshal(struct {
			Found   bool   `json:"found"`
			Phase   string `json:"phase,omitempty"`
			Action  string `json:"action"`
			Changed bool   `json:"changed"`
			DryRun  bool   `json:"dryRun"`
		}{result.Found, result.Phase, result.Action, result.Changed, dryRun})
		if err != nil {
			return err
		}
		return writeCommandData(cmd, data, nil)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only report recovery")
	return cmd
}

func newConfigReadCommand() *cobra.Command {
	var vault string
	cmd := &cobra.Command{Use: "read", Short: "Read a redacted configuration summary", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		result, err := internalapplication.ReadConfig(vault)
		if err != nil {
			return err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return writeCommandData(cmd, data, nil)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	return cmd
}

func newConfigWriteCommand() *cobra.Command {
	var vault, contentFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "write", Short: "Replace configuration bytes with CAS", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := readAgentInput(cmd, vault, contentFile, false)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateConfig(vault, internalapplication.ConfigMutationRequest{Operation: internalapplication.MutationConfigWrite, ExpectedHash: expected, Content: data, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "Complete YAML or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash or absent")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("content-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newConfigEditCommand() *cobra.Command {
	var vault, editsFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "edit", Short: "Patch configuration bytes with CAS", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := readAgentInput(cmd, vault, editsFile, true)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateConfig(vault, internalapplication.ConfigMutationRequest{Operation: internalapplication.MutationConfigEdit, ExpectedHash: expected, Content: data, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&editsFile, "edits-file", "", "Strict PatchRequest JSON or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("edits-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func readAgentInput(cmd *cobra.Command, vaultPath, value string, jsonInput bool) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("input file is required")
	}
	if value == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	vault, err := resolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	if filepath.IsAbs(value) || strings.Contains(value, `\`) || path.Clean(value) != value || strings.HasPrefix(value, ".") || strings.ContainsAny(value, "?#\x00") {
		return nil, errors.New("input file must be a Vault-relative POSIX path or -")
	}
	if jsonInput && strings.HasSuffix(strings.ToLower(value), ".md") {
		return nil, errors.New("edits-file must be JSON")
	}
	absolute := filepath.Join(vault, filepath.FromSlash(value))
	if internalfsutil.PathWithinRoot(filepath.Join(vault, ".oxpio"), absolute) || internalfsutil.PathWithinRoot(filepath.Join(vault, ".obsidian"), absolute) {
		return nil, errors.New("input file is outside the Agent input boundary")
	}
	_, data, _, err := internalfsutil.ReadContainedRegularFile(vault, absolute)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func writeMutationCommandResult(cmd *cobra.Command, result internalapplication.MutationResult, operationErr error) error {
	if operationErr != nil {
		return operationErr
	}
	diagnostics := diagnosticsFromBuild(result.Diagnostics)
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if len(diagnostics) != 0 {
		object["diagnostics"] = diagnostics
	}
	data, err = json.Marshal(object)
	if err != nil {
		return err
	}
	return writeCommandData(cmd, data, diagnostics)
}

func writeCommandData(cmd *cobra.Command, data []byte, diagnostics []internalagent.Diagnostic) error {
	if formatOf(cmd) == "json" {
		_, err := cmd.OutOrStdout().Write(append(data, '\n'))
		return err
	}
	var object struct {
		Operation   string                 `json:"operation"`
		Path        string                 `json:"path"`
		Destination string                 `json:"destination"`
		Changes     []internalagent.Change `json:"changes"`
		Next        string                 `json:"next"`
	}
	_ = json.Unmarshal(data, &object)
	if object.Operation == "" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, data, "", "  "); err == nil {
			pretty.WriteByte('\n')
			_, err = io.WriteString(cmd.OutOrStdout(), pretty.String())
			return err
		}
		_, err := cmd.OutOrStdout().Write(append(data, '\n'))
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", object.Operation, object.Path); err != nil {
		return err
	}
	for _, change := range object.Changes {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", change.Kind, change.Domain, change.Path); err != nil {
			return err
		}
	}
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", diagnostic.Severity, diagnostic.Message); err != nil {
			return err
		}
	}
	if object.Next != "" {
		_, _ = io.WriteString(cmd.OutOrStdout(), "Next: "+object.Next+"\n")
	}
	return nil
}
