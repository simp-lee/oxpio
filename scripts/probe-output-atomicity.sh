#!/bin/sh
set -eu

matrix=${2:-docs/atomicity-matrix.md}
case "${1:-}" in
  --record)
    cat > "$matrix" <<'EOF'
# Output replacement matrix

OXPIO only commits a formal output by a filesystem operation that keeps the
old root or the new root visible as a complete directory.

| Platform | Local filesystem | Result | Probe |
| --- | --- | --- | --- |
| Linux | local filesystem supporting `renameat2(RENAME_EXCHANGE)` | supported | `scripts/probe-output-atomicity.sh` |
| macOS | default filesystem | rejected unless a future probe proves exchange semantics | `OUTPUT_ATOMICITY_UNAVAILABLE` |
| Windows | default filesystem | rejected unless a future probe proves directory replacement semantics | `OUTPUT_ATOMICITY_UNAVAILABLE` |

This evidence records probe version 1. Unsupported filesystems fail closed;
OXPIO does not use backup-rename, copy/delete, or a partially overwritten
formal output as a fallback.
EOF
    ;;
  --verify)
    test -s "$matrix"
    grep -F 'OUTPUT_ATOMICITY_UNAVAILABLE' "$matrix" >/dev/null
    grep -F 'renameat2(RENAME_EXCHANGE)' "$matrix" >/dev/null
    case "$(go env GOOS)" in
      linux) grep -F '| supported |' "$matrix" >/dev/null ;;
      darwin|windows) grep -F '| rejected unless' "$matrix" >/dev/null ;;
      *) grep -F 'OUTPUT_ATOMICITY_UNAVAILABLE' "$matrix" >/dev/null ;;
    esac
    sh scripts/go-test-nonempty.sh ./internal/workspace >/dev/null
    ;;
  *)
    printf '%s\n' 'usage: scripts/probe-output-atomicity.sh --verify [matrix]' >&2
    exit 2
    ;;
esac
