#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUNDLE="$ROOT/internal/edit/web/editor.bundle.js"
WASM="$ROOT/internal/edit/web/live-preview.wasm"
WASM_EXEC="$ROOT/internal/edit/web/wasm_exec.js"
BUNDLE_TEMP=$(mktemp "${TMPDIR:-/tmp}/oxpio-editor-bundle.XXXXXX.js")
WASM_TEMP=$(mktemp "${TMPDIR:-/tmp}/oxpio-live-preview.XXXXXX.wasm")
WASM_EXEC_TEMP=$(mktemp "${TMPDIR:-/tmp}/oxpio-wasm-exec.XXXXXX.js")
trap 'rm -f "$BUNDLE_TEMP" "$WASM_TEMP" "$WASM_EXEC_TEMP"' EXIT HUP INT TERM

cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$WASM_EXEC_TEMP"
if ! cmp -s "$WASM_EXEC_TEMP" "$WASM_EXEC"; then
  echo "wasm_exec.js is stale; run npm run build:live-preview" >&2
  exit 1
fi

"$ROOT/node_modules/.bin/esbuild" "$ROOT/internal/edit/web/editor-entry.js" \
  --bundle --minify --outfile="$BUNDLE_TEMP"
if ! cmp -s "$BUNDLE_TEMP" "$BUNDLE"; then
  echo "editor bundle is stale; run npm run build:editor" >&2
  exit 1
fi

(
  cd "$ROOT"
  GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w -buildid=' -o "$WASM_TEMP" ./cmd/livepreview-wasm
)
if ! cmp -s "$WASM_TEMP" "$WASM"; then
  echo "live-preview WASM is stale; run npm run build:live-preview" >&2
  exit 1
fi

printf 'editor bundle and live-preview WASM match source\n'
