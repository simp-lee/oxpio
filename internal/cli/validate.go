package cli

import (
	"encoding/json"
	"fmt"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
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
			if formatOf(cmd) == "json" {
				data, marshalErr := json.Marshal(validationSummary(result.Diagnostics))
				if marshalErr != nil {
					return marshalErr
				}
				if _, writeErr := cmd.OutOrStdout().Write(append(data, '\n')); writeErr != nil {
					return writeErr
				}
			}
			diagnostics := redactHiddenDiagnostics(diagnosticsFromBuild(result.Diagnostics), result.Plan)
			if analyzeErr != nil {
				return diagnosticError{err: analyzeErr, diagnostics: diagnostics}
			}
			failure := internalanalyze.Failure(result.Diagnostics)
			if failure == nil {
				return nil
			}
			return diagnosticError{err: failure, diagnostics: diagnostics}
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the OXPIO Vault")
	flags.StringVar(&outputPath, "output", "", "Formal site output boundary (default <vault>/public)")
	return cmd
}

type validationResult struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
}

func validationSummary(diagnostics []internaldiag.Diagnostic) validationResult {
	var summary validationResult
	for _, diagnostic := range diagnostics {
		switch string(diagnostic.Severity) {
		case "warning":
			summary.Warnings++
		case "error":
			summary.Errors++
		}
	}
	return summary
}
