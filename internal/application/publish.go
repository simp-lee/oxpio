package application

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
)

// PublishResult is the stable summary shared by publish adapters.
type PublishOptions struct {
	DryRun                     bool
	DiagnosticsWriter          io.Writer
	TrackOutputTransactionPath func(string)
}

type PublishResult struct {
	OutputPath string
	DryRun     bool
	Published  bool
	Build      *internalbuild.BuildResult
}

// Publish runs the strict publication pipeline. A dry run uses a process
// temporary output while retaining the formal output boundary for analysis.
func Publish(vaultPath, outputPath string, options PublishOptions) (*PublishResult, error) {
	if options.DryRun {
		temporary, err := os.MkdirTemp("", "oxpio-publish-")
		if err != nil {
			return nil, fmt.Errorf("create publish preview: %w", err)
		}
		defer os.RemoveAll(temporary)
		result, buildErr := internalbuild.BuildWithOptions(vaultPath, temporary, internalbuild.Options{
			Strict:                     true,
			DiagnosticsWriter:          options.DiagnosticsWriter,
			SourceOutputPath:           outputPath,
			TrackOutputTransactionPath: options.TrackOutputTransactionPath,
		})
		return &PublishResult{OutputPath: outputPath, DryRun: true, Build: result}, buildErr
	}
	result, err := internalbuild.BuildWithOptions(vaultPath, outputPath, internalbuild.Options{
		Strict:                     true,
		DiagnosticsWriter:          options.DiagnosticsWriter,
		TrackOutputTransactionPath: options.TrackOutputTransactionPath,
	})
	return &PublishResult{OutputPath: outputPath, Published: err == nil, Build: result}, err
}

// OutputDescriptor returns the public output identity without exposing a host
// path for external outputs.
func OutputDescriptor(vaultPath, outputPath string) map[string]string {
	if filepath.IsAbs(outputPath) && !isWithin(vaultPath, outputPath) {
		return map[string]string{"kind": "external", "path": "<external-output>"}
	}
	relative, err := filepath.Rel(vaultPath, outputPath)
	if err != nil || relative == "." || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return map[string]string{"kind": "external", "path": "<external-output>"}
	}
	return map[string]string{"kind": "vault-relative", "path": filepath.ToSlash(relative)}
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
