package cli

import (
	"encoding/json"
	"fmt"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

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
					return diagnosticError{err: publishErr, diagnostics: redactHiddenCatalogDiagnostics(diagnosticsFromBuild(result.Build.Diagnostics), result.Build.Catalog)}
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
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary (default <vault>/public)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Generate and validate without replacing output")
	return cmd
}
