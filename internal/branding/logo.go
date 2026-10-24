package branding

import _ "embed"

const Name = "OXPIO"

//go:embed logo.svg
var logoSVG []byte

//go:embed favicon.svg
var faviconSVG []byte

// LogoSVG returns a copy of the OXPIO logo SVG.
func LogoSVG() []byte {
	return append([]byte(nil), logoSVG...)
}

// FaviconSVG returns a copy of the OXPIO favicon SVG.
func FaviconSVG() []byte {
	return append([]byte(nil), faviconSVG...)
}
