#!/bin/sh
set -eu

output=$(mktemp "${TMPDIR:-/tmp}/oxpio-go-test.XXXXXX")
trap 'rm -f "$output"' EXIT

set +e
go test -json "$@" >"$output"
status=$?
set -e
cat "$output"

executed=$(awk '/"Action":"(pass|fail)"/ && /"Test":"Test[^" ]*"/ { count++ } END { print count + 0 }' "$output")
if [ "$executed" -eq 0 ]; then
  printf '%s\n' 'go-test-nonempty: no Test* action executed' >&2
  exit 1
fi
exit "$status"
