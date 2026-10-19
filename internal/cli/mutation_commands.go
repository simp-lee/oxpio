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
	"unicode/utf8"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/publishpath"
	"github.com/spf13/cobra"
)

func newWriteCommand() *cobra.Command {
	var vault, output, contentFile, expected string
	var includeUnpublished, publish, unpublish, dryRun bool
	cmd := &cobra.Command{Use: "write PATH", Short: "Replace one Markdown source with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		if publish && unpublish {
			return errors.New("--publish and --unpublish are mutually exclusive")
		}
		data, err := readAgentInput(cmd, boundary.VaultPath, boundary.OutputPath, contentFile, false)
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
		result, err := internalapplication.MutateSource(boundary.VaultPath, boundary.OutputPath, internalapplication.SourceMutationRequest{Operation: internalapplication.MutationWrite, Path: args[0], ExpectedHash: expected, Content: data, DryRun: dryRun, IncludeUnpublished: includeUnpublished, Publish: state})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "Vault-relative POSIX source file or - for stdin; file must stay inside the Agent input boundary")
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
	var vault, output, editsFile, expected string
	var includeUnpublished, publish, unpublish, dryRun bool
	cmd := &cobra.Command{Use: "edit PATH", Short: "Apply exact source text patches with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		if publish && unpublish {
			return errors.New("--publish and --unpublish are mutually exclusive")
		}
		data, err := readAgentInput(cmd, boundary.VaultPath, boundary.OutputPath, editsFile, true)
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
		result, err := internalapplication.MutateSource(boundary.VaultPath, boundary.OutputPath, internalapplication.SourceMutationRequest{Operation: internalapplication.MutationEdit, Path: args[0], ExpectedHash: expected, Content: data, DryRun: dryRun, IncludeUnpublished: includeUnpublished, Publish: state})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&editsFile, "edits-file", "", "Vault-relative POSIX PatchRequest JSON file or - for stdin; file must stay inside the Agent input boundary")
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
	var vault, output, expected, destinationExpected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "move SOURCE DEST", Short: "Move one Markdown article with CAS", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateSource(boundary.VaultPath, boundary.OutputPath, internalapplication.SourceMutationRequest{Operation: internalapplication.MutationMove, Path: args[0], Destination: args[1], ExpectedHash: expected, DestinationExpectedHash: destinationExpected, DryRun: dryRun, IncludeUnpublished: includeUnpublished})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().StringVar(&destinationExpected, "dest-if-hash", "", "Destination hash; must be absent")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("if-hash")
	_ = cmd.MarkFlagRequired("dest-if-hash")
	return cmd
}

func newDeleteCommand() *cobra.Command {
	var vault, output, expected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "delete PATH", Short: "Delete one Markdown article with CAS", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateSource(boundary.VaultPath, boundary.OutputPath, internalapplication.SourceMutationRequest{Operation: internalapplication.MutationDelete, Path: args[0], ExpectedHash: expected, DryRun: dryRun, IncludeUnpublished: includeUnpublished})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newUploadCommand() *cobra.Command {
	var vault, output, file, name, kind, expected string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "upload DIR", Short: "Create one Markdown or attachment file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		if kind != internalapplication.UploadArticle && kind != internalapplication.UploadSectionIndex && kind != internalapplication.UploadAttachment {
			return errors.New("--kind must be article, section-index, or attachment")
		}
		if file == "" {
			return errors.New("--file is required")
		}
		var data []byte
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
			data, err = readUploadInput(boundary.VaultPath, boundary.OutputPath, file)
		}
		if err != nil {
			return err
		}
		result, mutationErr := internalapplication.MutateUpload(boundary.VaultPath, boundary.OutputPath, internalapplication.UploadMutationRequest{
			Directory: args[0], Name: name, Kind: kind, ExpectedHash: expected,
			Content: data, IncludeUnpublished: includeUnpublished, DryRun: dryRun,
		})
		return writeMutationCommandResult(cmd, result, mutationErr)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&file, "file", "", "Vault-relative input file or - for stdin; use --name with -; file must stay inside the Agent input boundary")
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

func readUploadInput(vaultPath, outputPath, value string) ([]byte, error) {
	vault, err := resolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	if err := validateAgentInputPath(vault, outputPath, value); err != nil {
		return nil, fmt.Errorf("upload input: %w", err)
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

func newImportCommand() *cobra.Command {
	var vault, output, manifestFile string
	var includeUnpublished, dryRun bool
	cmd := &cobra.Command{Use: "import", Short: "Import mutually-referencing Markdown sources as one strict overlay", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		data, err := readAgentInput(cmd, boundary.VaultPath, boundary.OutputPath, manifestFile, false)
		if err != nil {
			return err
		}
		if len(data) > 64<<20 {
			return errors.New("import manifest exceeds 64 MiB")
		}
		manifest, err := decodeImportManifest(data)
		if err != nil {
			return err
		}
		items := make([]internalapplication.UploadBatchItem, 0, len(manifest.Files))
		for _, file := range manifest.Files {
			if file.Path == nil || file.Kind == nil || file.Content == nil {
				return errors.New("invalid import manifest: each file requires non-null path, kind, and content")
			}
			items = append(items, internalapplication.UploadBatchItem{Path: *file.Path, Kind: *file.Kind, Content: []byte(*file.Content)})
		}
		result, mutationErr := internalapplication.MutateUploadBatch(boundary.VaultPath, boundary.OutputPath, internalapplication.UploadBatchMutationRequest{Items: items, IncludeUnpublished: includeUnpublished, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, mutationErr)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&manifestFile, "manifest-file", "", "Vault-relative JSON manifest or - for stdin; all content is analyzed before commit")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Allow unpublished source")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("manifest-file")
	return cmd
}

type importManifest struct {
	Files []importManifestFile `json:"files"`
}

type importManifestFile struct {
	Path    *string `json:"path"`
	Kind    *string `json:"kind"`
	Content *string `json:"content"`
}

func decodeImportManifest(data []byte) (importManifest, error) {
	if len(data) == 0 {
		return importManifest{}, errors.New("import manifest is empty")
	}
	if !utf8.Valid(data) {
		return importManifest{}, errors.New("invalid import manifest: input is not valid UTF-8")
	}
	if err := rejectDuplicateImportJSONKeys(data); err != nil {
		return importManifest{}, fmt.Errorf("invalid import manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest importManifest
	if err := decoder.Decode(&manifest); err != nil {
		return importManifest{}, fmt.Errorf("invalid import manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return importManifest{}, errors.New("invalid import manifest: trailing data")
		}
		return importManifest{}, fmt.Errorf("invalid import manifest: %w", err)
	}
	if manifest.Files == nil {
		return importManifest{}, errors.New("invalid import manifest: files must be an array")
	}
	for _, file := range manifest.Files {
		if file.Path == nil || file.Kind == nil || file.Content == nil {
			return importManifest{}, errors.New("invalid import manifest: each file requires non-null path, kind, and content")
		}
	}
	return manifest, nil
}

func rejectDuplicateImportJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(objectDepth int) error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			var allowed map[string]struct{}
			switch objectDepth {
			case 0:
				allowed = map[string]struct{}{"files": {}}
			case 2:
				allowed = map[string]struct{}{"path": {}, "kind": {}, "content": {}}
			}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				if _, known := allowed[key]; !known {
					return fmt.Errorf("unknown import manifest key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(objectDepth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(objectDepth + 1); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing data")
		}
		return err
	}
	return nil
}

func newMkdirCommand() *cobra.Command {
	var vault, output string
	var dryRun bool
	cmd := &cobra.Command{Use: "mkdir PATH", Short: "Create one Vault directory", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateDirectory(boundary.VaultPath, boundary.OutputPath, internalapplication.DirectoryMutationRequest{Operation: internalapplication.MutationMkdir, Path: args[0], ExpectedHash: internalapplication.AbsentHash, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	return cmd
}

func newRenameDirCommand() *cobra.Command {
	var vault, output, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "rename-dir SOURCE DEST", Short: "Rename one empty Vault directory", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateDirectory(boundary.VaultPath, boundary.OutputPath, internalapplication.DirectoryMutationRequest{Operation: internalapplication.MutationRenameDir, Path: args[0], Destination: args[1], ExpectedHash: expected, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
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
	var vault, output, contentFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "write", Short: "Replace configuration bytes with CAS", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		data, err := readAgentInput(cmd, boundary.VaultPath, boundary.OutputPath, contentFile, false)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateConfig(boundary.VaultPath, boundary.OutputPath, internalapplication.ConfigMutationRequest{Operation: internalapplication.MutationConfigWrite, ExpectedHash: expected, Content: data, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "Vault-relative POSIX YAML file or - for stdin; file must stay inside the Agent input boundary")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash or absent")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("content-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newConfigEditCommand() *cobra.Command {
	var vault, output, editsFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "edit", Short: "Patch configuration bytes with CAS", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		boundary, err := resolveVaultOutputPaths(vault, output)
		if err != nil {
			return err
		}
		data, err := readAgentInput(cmd, boundary.VaultPath, boundary.OutputPath, editsFile, true)
		if err != nil {
			return err
		}
		result, err := internalapplication.MutateConfig(boundary.VaultPath, boundary.OutputPath, internalapplication.ConfigMutationRequest{Operation: internalapplication.MutationConfigEdit, ExpectedHash: expected, Content: data, DryRun: dryRun})
		return writeMutationCommandResult(cmd, result, err)
	}}
	cmd.Flags().StringVar(&vault, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&output, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&editsFile, "edits-file", "", "Vault-relative POSIX PatchRequest JSON file or - for stdin; file must stay inside the Agent input boundary")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("edits-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

const maxAgentInput = 64 << 20

func readAgentInput(cmd *cobra.Command, vaultPath, outputPath, value string, jsonInput bool) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("input file is required")
	}
	if value == "-" {
		return readBoundedAgentInput(cmd.InOrStdin())
	}
	vault, err := resolveVaultPath(vaultPath)
	if err != nil {
		return nil, err
	}
	if err := validateAgentInputPath(vault, outputPath, value); err != nil {
		return nil, fmt.Errorf("input file: %w; use the command's FILE|- option for stdin (for example --content-file -, --edits-file -, or --manifest-file -), or a Vault-relative POSIX path outside .oxpio, .obsidian, and the formal output", err)
	}
	if jsonInput && strings.HasSuffix(strings.ToLower(value), ".md") {
		return nil, errors.New("edits-file must be JSON")
	}
	absolute := filepath.Join(vault, filepath.FromSlash(value))
	_, file, _, err := internalfsutil.OpenContainedRegularFile(vault, absolute)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return readBoundedAgentInput(file)
}

func readBoundedAgentInput(input io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxAgentInput+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAgentInput {
		return nil, fmt.Errorf("input file exceeds %d MiB", maxAgentInput>>20)
	}
	return data, nil
}

func validateAgentInputPath(vaultPath, outputPath, value string) error {
	if value == "" || filepath.IsAbs(value) || isWindowsAbsolutePath(value) || strings.Contains(value, `\`) || path.Clean(value) != value || strings.ContainsAny(value, "?\x00") || !internalfsutil.IsPortableSitePath(value) {
		return errors.New("must be a Vault-relative POSIX path or -")
	}
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("must be a Vault-relative POSIX file path")
	}
	if publishpath.IsReservedPath(value) {
		return errors.New("is outside the Agent input boundary")
	}
	candidate := filepath.Join(vaultPath, filepath.FromSlash(value))
	if strings.TrimSpace(outputPath) != "" && internalfsutil.PathWithinRootAfterSymlinks(outputPath, candidate) {
		return errors.New("must not read the formal output boundary")
	}
	return nil
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
