#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=${TMPDIR:-/tmp}/oxpio-version-builds-$$
EPOCH=${SOURCE_DATE_EPOCH:-1700000000}
trap 'chmod -R u+w "$TMP" 2>/dev/null || true; rm -rf "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin-a" "$TMP/bin-b"

EXPECTED_DATE=$(python3 - "$EPOCH" <<'PY'
from datetime import datetime, timezone
import sys
print(datetime.fromtimestamp(int(sys.argv[1]), timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'))
PY
)
LDFLAGS="-s -w -X github.com/simp-lee/oxpio/internal/cli.releaseMetadata=oxpio-release-metadata:1.2.3|0123456789abcdef|$EXPECTED_DATE|release"
(
  cd "$ROOT"
  go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/bin-a/oxpio" ./cmd/oxpio
  go build -trimpath -ldflags "$LDFLAGS" -o "$TMP/bin-b/oxpio" ./cmd/oxpio
)
cmp -s "$TMP/bin-a/oxpio" "$TMP/bin-b/oxpio" || {
  echo "fixed-epoch builds differ" >&2
  exit 1
}
VERSION_A=$($TMP/bin-a/oxpio version)
VERSION_B=$($TMP/bin-b/oxpio --version)
EXPECTED="oxpio version=1.2.3 commit=0123456789abcdef date=$EXPECTED_DATE type=release"
[ "$VERSION_A" = "$EXPECTED" ] || { echo "version metadata mismatch: $VERSION_A" >&2; exit 1; }
[ "$VERSION_B" = "$EXPECTED" ] || { echo "--version metadata mismatch: $VERSION_B" >&2; exit 1; }

PROXY="$TMP/proxy"
VERSION=v0.0.1
MODULE=github.com/simp-lee/oxpio
VERSION_DIR="$PROXY/github.com/simp-lee/oxpio/@v"
mkdir -p "$VERSION_DIR" "$TMP/gobin" "$TMP/gomodcache"
cp "$ROOT/go.mod" "$VERSION_DIR/$VERSION.mod"
printf '{"Version":"%s","Time":"%s"}\n' "$VERSION" "$EXPECTED_DATE" > "$VERSION_DIR/$VERSION.info"
python3 - "$ROOT" "$VERSION_DIR/$VERSION.zip" "$MODULE@$VERSION/" <<'PY'
from pathlib import Path
import sys, zipfile
root = Path(sys.argv[1])
out = Path(sys.argv[2])
prefix = sys.argv[3]
with zipfile.ZipFile(out, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
    for top in ('cmd', 'internal'):
        for path in sorted((root / top).rglob('*')):
            if not path.is_file() or path.name.endswith('_test.go'):
                continue
            archive.write(path, prefix + path.relative_to(root).as_posix())
    for name in ('go.mod', 'go.sum', 'LICENSE', 'THIRD_PARTY.md'):
        archive.write(root / name, prefix + name)
PY
GOBIN="$TMP/gobin" GOMODCACHE="$TMP/gomodcache" GONOSUMDB='*' GOPROXY="file://$PROXY,https://proxy.golang.org" \
  go install "$MODULE/cmd/oxpio@$VERSION"
INSTALLED=$($TMP/gobin/oxpio version)
INSTALLED_FLAG=$($TMP/gobin/oxpio --version)
EXPECTED_INSTALLED="oxpio version=$VERSION commit=unknown date=unknown type=go-install"
[ "$INSTALLED" = "$EXPECTED_INSTALLED" ] || { echo "go-install metadata mismatch: $INSTALLED" >&2; exit 1; }
[ "$INSTALLED_FLAG" = "$EXPECTED_INSTALLED" ] || { echo "go-install --version mismatch: $INSTALLED_FLAG" >&2; exit 1; }

printf 'version metadata verified: %s\n' "$VERSION_A"
printf 'go-install metadata verified: %s\n' "$INSTALLED"
