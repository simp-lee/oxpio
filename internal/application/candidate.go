package application

import (
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	"github.com/simp-lee/oxpio/internal/model"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
)

// CandidateBuildOptions describes an isolated build used by editor and theme
// previews. The application layer owns this adapter so interface packages do
// not select a second build or renderer implementation themselves.
type CandidateBuildOptions struct {
	SourceOverlay     map[string][]byte
	SourceDeleted     map[string]bool
	SourceOutputPath  string
	PreviewSourcePath string
	AllowWarnings     bool
}

// CandidatePublishOptions is the transaction handoff for a previously built
// candidate. The application service remains the only adapter entry point for
// editor source/output commits.
type CandidatePublishOptions = internalpublish.Options

// BuildCandidate runs the canonical strict renderer against an isolated
// output directory. It never replaces formal output or writes source files.
func BuildCandidate(vaultPath, outputPath string, options CandidateBuildOptions) (*internalbuild.BuildResult, error) {
	return internalbuild.BuildWithOptions(vaultPath, outputPath, internalbuild.Options{
		Strict:            true,
		AllowWarnings:     options.AllowWarnings,
		Ephemeral:         true,
		SourceOverlay:     options.SourceOverlay,
		SourceDeleted:     options.SourceDeleted,
		SourceOutputPath:  options.SourceOutputPath,
		PreviewSourcePath: options.PreviewSourcePath,
	})
}

// BuildCandidateWithConfig is the same isolated application service with an
// explicit in-memory theme/config selection used by theme preview.
func BuildCandidateWithConfig(vaultPath, outputPath string, cfg model.SiteConfig, sourceOutputPath string) (*internalbuild.BuildResult, error) {
	return internalbuild.BuildWithConfig(vaultPath, outputPath, cfg, internalbuild.Options{
		Strict:           true,
		Ephemeral:        true,
		SourceOutputPath: sourceOutputPath,
	})
}

// AdoptCandidateStaging hands a fully validated candidate to the canonical
// output transaction implementation without exposing that adapter to editor
// HTTP/file-operation code.
func AdoptCandidateStaging(vaultPath, outputPath, stagingPath string, options CandidatePublishOptions) (*internalpublish.Transaction, error) {
	return internalpublish.AdoptStaging(vaultPath, outputPath, stagingPath, options)
}
