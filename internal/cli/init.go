package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	"github.com/spf13/cobra"
)

var (
	initConfigTemplate = internalconfig.InitialStrictYAML()
	initRootTemplate   = "---\ntitle: Home\npublish: true\n---\n"
)

func newInitCommand() *cobra.Command {
	var vaultPath string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create an oxpio.yaml template in a vault directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedVault, created, err := prepareInitVault(vaultPath)
			if err != nil {
				return err
			}
			if err := writeInitFiles(resolvedVault); err != nil {
				if created {
					_ = os.RemoveAll(resolvedVault)
				}
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Created %s. Replace baseURL before publishing.\n", filepath.Join(resolvedVault, defaultConfigFilename))
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the Obsidian vault (default current directory)")

	return cmd
}

func prepareInitVault(rawPath string) (string, bool, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		trimmed = "."
	}
	absolute, err := filepath.Abs(filepath.Clean(trimmed))
	if err != nil {
		return "", false, fmt.Errorf("resolve vault path %q: %w", rawPath, err)
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("vault path %q must not be a symbolic link", absolute)
		}
		if !info.IsDir() {
			return "", false, fmt.Errorf("vault path %q is not a directory", absolute)
		}
		entries, err := os.ReadDir(absolute)
		if err != nil {
			return "", false, fmt.Errorf("read vault path %q: %w", absolute, err)
		}
		if len(entries) != 0 {
			return "", false, fmt.Errorf("vault path %q must be nonexistent or completely empty", absolute)
		}
		resolved, err := internalfsutil.ResolveVaultPath(absolute)
		if err != nil {
			return "", false, err
		}
		return resolved, false, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", false, fmt.Errorf("inspect vault path %q: %w", absolute, err)
	}

	// A valid target may have more than one missing parent directory. Create
	// the parent chain before resolving the vault so init does not require the
	// immediate parent to exist already.
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return "", false, fmt.Errorf("create parent for vault path %q: %w", absolute, err)
	}
	parent, err := internalfsutil.ResolveVaultPath(filepath.Dir(absolute))
	if err != nil {
		return "", false, fmt.Errorf("resolve parent for vault path %q: %w", absolute, err)
	}
	createdPath := filepath.Join(parent, filepath.Base(absolute))
	if err := os.Mkdir(createdPath, 0o755); err != nil {
		return "", false, fmt.Errorf("create vault path %q: %w", createdPath, err)
	}
	return createdPath, true, nil
}

func writeInitFiles(vaultPath string) error {
	created := make([]string, 0, 2)
	write := func(name, contents string) error {
		filename := filepath.Join(vaultPath, name)
		file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return fmt.Errorf("init target %q already exists", filename)
			}
			return fmt.Errorf("create init file %q: %w", filename, err)
		}
		created = append(created, filename)
		if _, err := file.WriteString(contents); err != nil {
			_ = file.Close()
			return fmt.Errorf("write init file %q: %w", filename, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close init file %q: %w", filename, err)
		}
		return nil
	}
	if err := write(defaultConfigFilename, initConfigTemplate); err != nil {
		for _, filename := range created {
			_ = os.Remove(filename)
		}
		return err
	}
	if err := write("_index.md", initRootTemplate); err != nil {
		for _, filename := range created {
			_ = os.Remove(filename)
		}
		return err
	}
	return nil
}
