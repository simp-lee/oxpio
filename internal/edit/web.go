package edit

import "embed"

// editorWeb contains the self-contained editor UI and the shared Markdown WASM
// runtime. All browser assets are built locally so edit mode does not depend on a CDN.
//
//go:embed web/editor.html web/editor.css web/editor.bundle.js web/login.html web/login.css web/wasm_exec.js web/live-preview.wasm
var editorWeb embed.FS
