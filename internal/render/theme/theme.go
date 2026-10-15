// Package theme exposes the renderer's theme contract at the rendering
// boundary. The implementation remains in internal/theme so planning and CLI
// adapters use the same compiler rather than a second renderer.
package theme

import core "github.com/simp-lee/oxpio/internal/theme"

type (
	File     = core.File
	Source   = core.Source
	Manifest = core.Manifest
	Renderer = core.Renderer
)

const (
	APIVersion = core.APIVersion
	VMVersion  = core.VMVersion
	BaseID     = core.BaseID
)

var (
	BaseFiles          = core.BaseFiles
	BaseSource         = core.BaseSource
	LoadLocal          = core.LoadLocal
	Validate           = core.Validate
	ValidateWithAssets = core.ValidateWithAssets
	Compile            = core.Compile
	ManifestOf         = core.ManifestOf
	InitFiles          = core.InitFiles
	Files              = core.Files
	FileKind           = core.FileKind
	ValidationAssets   = core.ValidationAssets
	ValidID            = core.ValidID
	ValidRelativePath  = core.ValidRelativePath
)
