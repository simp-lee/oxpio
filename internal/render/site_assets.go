package render

import "embed"

//go:embed site/style.css site/runtime.js runtime-assets/**/*
var embeddedSiteFS embed.FS

func embeddedSiteAssetPath(name string) string {
	return "site/" + name
}
