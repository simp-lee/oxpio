package cli

import (
	"encoding/json"
	"fmt"
	"io"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

func writePublishSummary(stdout, stderr io.Writer, vaultPath, outputPath string, result *internalapplication.PublishResult, dryRun bool) error {
	if result == nil {
		return fmt.Errorf("publish returned no result")
	}
	descriptor := internalapplication.OutputDescriptor(vaultPath, outputPath)
	phase := "Published"
	if dryRun {
		phase = "Dry-run"
	}
	if _, err := fmt.Fprintf(stdout, "%s output: %s (%s)\n", phase, descriptor["path"], descriptor["kind"]); err != nil {
		return err
	}
	if dryRun {
		if _, err := fmt.Fprintf(stdout, "No output changes published; preview changes: %d\n", len(result.Changes)); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(stdout, "Output changes: %d\n", len(result.Changes)); err != nil {
		return err
	}
	if result.Build != nil {
		if _, err := fmt.Fprintf(stdout, "Pages: %d, tag pages: %d, assets: %d, warnings: %d, errors: %d\n", result.Build.NotePages, result.Build.TagPages, len(result.Build.Assets), result.Build.WarningCount, result.Build.ErrorCount); err != nil {
			return err
		}
		if err := writePublishDiagnostics(stderr, result, vaultPath); err != nil {
			return err
		}
		if result.Build.OutputCleanupError != nil {
			if _, err := fmt.Fprintf(stderr, "POST_COMMIT_WARNING: %v\n", result.Build.OutputCleanupError); err != nil {
				return err
			}
		}
	}
	return nil
}

func writePublishDiagnostics(stderr io.Writer, result *internalapplication.PublishResult, vaultPath string) error {
	if result == nil || result.Build == nil {
		return nil
	}
	diagnostics := redactHiddenCatalogDiagnostics(diagnosticsFromBuild(result.Build.Diagnostics), result.Build.Catalog, vaultPath)
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintf(stderr, "%s\n", formatCLIDiagnostic(diagnostic)); err != nil {
			return err
		}
	}
	return nil
}

func newPublishCommand(deps commandDependencies) *cobra.Command {
	var vaultPath, outputPath string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Strictly generate and publish the site",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			result, publishErr := publishWithDependencies(deps, boundary.VaultPath, boundary.OutputPath, internalapplication.PublishOptions{DryRun: dryRun})
			if publishErr != nil {
				if result != nil && result.Build != nil {
					diagnostics := redactHiddenCatalogDiagnostics(diagnosticsFromBuild(result.Build.Diagnostics), result.Build.Catalog, boundary.VaultPath)
					if formatOf(cmd) != "json" {
						for _, diagnostic := range diagnostics {
							if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "%s\n", formatCLIDiagnostic(diagnostic)); writeErr != nil {
								return writeErr
							}
						}
					}
					return diagnosticError{err: publishErr, diagnostics: diagnostics}
				}
				return publishErr
			}
			if formatOf(cmd) == "json" {
				var changes []internalapplication.PublishChange
				if result != nil {
					changes = result.Changes
				}
				data := map[string]any{
					"output":    internalapplication.OutputDescriptor(boundary.VaultPath, boundary.OutputPath),
					"phase":     map[bool]string{true: "validated", false: "published"}[dryRun],
					"preview":   dryRun,
					"published": !dryRun,
					"next":      map[bool]string{true: "publish", false: "serve"}[dryRun],
					"changes":   changes,
				}
				if result != nil && result.Build != nil {
					data["artifacts"] = map[string]int{
						"pages":    result.Build.NotePages,
						"tagPages": result.Build.TagPages,
						"assets":   len(result.Build.Assets),
						"warnings": result.Build.WarningCount,
						"errors":   result.Build.ErrorCount,
					}
					data["impactedPages"] = result.Build.NotePages + result.Build.TagPages
					if result.Build.OutputCleanupError != nil {
						data["postCommitWarning"] = true
					}
				}
				encoded, marshalErr := json.Marshal(data)
				if marshalErr != nil {
					return fmt.Errorf("encode publish result: %w", marshalErr)
				}
				_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))
				return err
			}
			return writePublishSummary(cmd.OutOrStdout(), cmd.ErrOrStderr(), boundary.VaultPath, boundary.OutputPath, result, dryRun)
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary (default <vault>/public)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Generate and validate without replacing output")
	return cmd
}
