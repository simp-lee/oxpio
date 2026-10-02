package edit

import "embed"

// editorWeb contains the self-contained editor UI. The JavaScript bundle is
// built locally and embedded so edit mode does not depend on a CDN.
//
//go:embed web/editor.html web/editor.css web/editor.bundle.js web/content-preview.html web/content-preview.css web/login.html web/login.css
var editorWeb embed.FS
