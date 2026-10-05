#!/bin/sh
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 OXPIO_BINARY" >&2; exit 2; }
case "$1" in
  /*) BINARY=$1 ;;
  *) BINARY=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd)/$(basename -- "$1") ;;
esac
[ -x "$BINARY" ] || { echo "release binary is not executable: $BINARY" >&2; exit 1; }

TMP=${TMPDIR:-/tmp}/oxpio-release-smoke-$$
VAULT=$TMP/vault
SERVER_PID=
cleanup() {
  if [ -n "$SERVER_PID" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$VAULT"

VERSION=$("$BINARY" version)
VERSION_FLAG=$("$BINARY" --version)
[ "$VERSION" = "$VERSION_FLAG" ] || { echo "version forms differ" >&2; exit 1; }
printf '%s\n' "$VERSION" | grep -E '^oxpio version=[^ ]+ commit=[^ ]+ date=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z type=(snapshot|release)$' >/dev/null || {
  echo "invalid release-shaped version: $VERSION" >&2
  exit 1
}
case "$VERSION" in
  *' commit=unknown '*|*' date=unknown '*) echo "release-shaped metadata is unavailable: $VERSION" >&2; exit 1 ;;
esac

(
  cd "$VAULT"
  "$BINARY" init
  "$BINARY" build
)
[ -f "$VAULT/public/index.html" ] || { echo "default build did not create public/index.html" >&2; exit 1; }

PORT=$(python3 - <<'PY'
import socket
with socket.socket() as listener:
    listener.bind(('127.0.0.1', 0))
    print(listener.getsockname()[1])
PY
)
(
  cd "$VAULT"
  exec "$BINARY" serve --port "$PORT"
) >"$TMP/serve.log" 2>&1 &
SERVER_PID=$!

ready=false
attempt=0
while [ "$attempt" -lt 50 ]; do
  attempt=$((attempt + 1))
  if curl --fail --silent --show-error --connect-timeout 1 --max-time 2 "http://127.0.0.1:$PORT/" >"$TMP/index.html" 2>/dev/null; then
    ready=true
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    break
  fi
  sleep 0.2
done
if [ "$ready" != true ]; then
  echo "release serve smoke did not return HTTP 200" >&2
  cat "$TMP/serve.log" >&2
  exit 1
fi
grep -F '<html' "$TMP/index.html" >/dev/null || { echo "served response is not HTML" >&2; exit 1; }

printf 'release smoke verified: %s\n' "$VERSION"
