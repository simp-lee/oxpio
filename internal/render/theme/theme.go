// Package theme exposes the renderer's theme contract at the rendering
// boundary. The implementation remains in internal/theme so planning and CLI
// adapters use the same compiler rather than a second renderer.
package theme

import core "github.com/simp-lee/oxpio/internal/theme"

type File = core.File
type Source = core.Source
type Manifest = core.Manifest
type Renderer = core.Renderer

const (
	APIVersion = core.APIVersion
	VMVersion  = core.VMVersion
	BaseID     = core.BaseID
)

var BaseFiles = core.BaseFiles
var BaseSource = core.BaseSource
var LoadLocal = core.LoadLocal
var Validate = core.Validate
var ValidateWithAssets = core.ValidateWithAssets
var Compile = core.Compile
var ManifestOf = core.ManifestOf
var InitFiles = core.InitFiles
var Files = core.Files
var FileKind = core.FileKind
var ValidationAssets = core.ValidationAssets
var ValidID = core.ValidID
var ValidRelativePath = core.ValidRelativePath
