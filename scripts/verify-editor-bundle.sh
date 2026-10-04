#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
BUNDLE="$ROOT/internal/edit/web/editor.bundle.js"
TEMP=$(mktemp "${TMPDIR:-/tmp}/obsite-editor-bundle.XXXXXX.js")
trap 'rm -f "$TEMP"' EXIT HUP INT TERM

"$ROOT/node_modules/.bin/esbuild" "$ROOT/internal/edit/web/editor-entry.js" \
  --bundle --minify --outfile="$TEMP"
if ! cmp -s "$TEMP" "$BUNDLE"; then
  echo "editor bundle is stale; run npm run build:editor" >&2
  exit 1
fi
printf 'editor bundle matches source\n'
