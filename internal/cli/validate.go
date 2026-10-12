package cli

import (
	"encoding/json"
	"fmt"
	"io"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/workspace"
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
			if err := workspace.Check(boundary.VaultPath); err != nil {
				return err
			}
			if err := internalfsutil.ValidateOutputParent(boundary); err != nil {
				return err
			}
			if err := internalpublish.CheckOutputBoundary(boundary.VaultPath, boundary.OutputPath); err != nil {
				return err
			}
			if err := internalpublish.CheckExternalOutputLock(boundary.VaultPath, boundary.OutputPath); err != nil {
				return err
			}
			result, analyzeErr := internalanalyze.AnalyzeWithOutput(boundary.VaultPath, boundary.OutputPath)
			diagnostics := redactHiddenDiagnostics(diagnosticsFromBuild(result.Diagnostics), result.Plan)
			if formatOf(cmd) != "json" {
				if writeErr := writeValidationDiagnostics(cmd.ErrOrStderr(), diagnostics, analyzeErr); writeErr != nil {
					return writeErr
				}
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

func writeValidationDiagnostics(writer io.Writer, diagnostics []internalagent.Diagnostic, analyzeErr error) error {
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintf(writer, "%s: %s\n", diagnostic.Severity, diagnostic.Message); err != nil {
			return err
		}
	}
	if len(diagnostics) == 0 && analyzeErr != nil {
		_, err := fmt.Fprintf(writer, "error: %v\n", analyzeErr)
		return err
	}
	return nil
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
