package branding

import _ "embed"

const Name = "OXPIO"

//go:embed logo.svg
var logoSVG []byte

// LogoSVG returns a copy of the OXPIO logo SVG.
func LogoSVG() []byte {
	return append([]byte(nil), logoSVG...)
}
