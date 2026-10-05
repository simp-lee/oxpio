package cli

import (
	"fmt"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	"github.com/spf13/cobra"
)

func newValidateCommand() *cobra.Command {
	var vaultPath string
	var outputPath string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a vault without writing site output",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			result, analyzeErr := internalanalyze.AnalyzeWithOutput(boundary.VaultPath, boundary.OutputPath)
			if writeErr := internalanalyze.WriteDiagnostics(cmd.ErrOrStderr(), result.Diagnostics); writeErr != nil {
				return fmt.Errorf("write diagnostics: %w", writeErr)
			}
			if analyzeErr != nil {
				return analyzeErr
			}
			return internalanalyze.Failure(result.Diagnostics)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the Obsidian vault")
	flags.StringVar(&outputPath, "output", "", "Formal site output boundary (default <vault>/public)")
	return cmd
}
