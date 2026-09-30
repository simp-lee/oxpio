#!/bin/sh
set -eu

mode=${1:-all}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=
cleanup() {
  if [ -n "$tmp" ]; then
    rm -rf "$tmp"
  fi
}
trap cleanup EXIT HUP INT TERM

check_search() {
  matches=$(git grep -in -E 'pagefind|_pagefind|HasSearch|SearchConfig|data-obsite-search-ui|obsite-search-root|install-pagefind|cfg\.Search' -- . \
    ':(exclude)internal/render/vendor/**' \
    ':(exclude)scripts/verify-product-boundary.sh' \
    ':(exclude)scripts/verify-release.sh' || true)
  unexpected=$(printf '%s\n' "$matches" | grep -Ev \
    '^internal/(build/build_integration_test|cli/init_test|config/config_test)\.go:[0-9]+:[[:space:]]*for _, forbidden := range \[\]string\{.*pagefind|^internal/config/config_test\.go:[0-9]+:[[:space:]]*\{name: "pagefind (path|version)", field: "pagefind(Path|Version)", yaml:' || true)
  if [ -n "$unexpected" ]; then
    printf 'Pagefind/search product contract remains:\n%s\n' "$unexpected" >&2
    exit 1
  fi
}

check_packages() {
  packages=$(go list -f '{{.ImportPath}} {{.Name}}' ./...)
  public=$(printf '%s\n' "$packages" | awk '$1 !~ /\/internal\// && $1 != "github.com/simp-lee/obsite/cmd/obsite" {print}')
  if [ -n "$public" ]; then
    printf 'public product packages remain:\n%s\n' "$public" >&2
    exit 1
  fi

  imports=$(go list -f '{{.ImportPath}}|{{join .Imports " "}}' ./...)
  cmd_product_imports=$(printf '%s\n' "$imports" | awk -F'|' '$1 == "github.com/simp-lee/obsite/cmd/obsite" {print $2}' | tr ' ' '\n' | grep '^github.com/simp-lee/obsite/' || true)
  if [ "$cmd_product_imports" != "github.com/simp-lee/obsite/internal/cli" ]; then
    printf 'cmd/obsite product imports must contain only internal/cli, got:\n%s\n' "$cmd_product_imports" >&2
    exit 1
  fi

  invalid_cli=$(printf '%s\n' "$imports" | awk -F'|' '$1 != "github.com/simp-lee/obsite/cmd/obsite" && $2 ~ /github.com\/simp-lee\/obsite\/internal\/cli/ {print $1}')
  if [ -n "$invalid_cli" ]; then
    printf 'packages outside cmd/obsite import internal/cli:\n%s\n' "$invalid_cli" >&2
    exit 1
  fi

  invalid_build=$(printf '%s\n' "$imports" | awk -F'|' '$1 != "github.com/simp-lee/obsite/internal/build" && $1 != "github.com/simp-lee/obsite/internal/cli" && $1 != "github.com/simp-lee/obsite/internal/edit" && $2 ~ /github.com\/simp-lee\/obsite\/internal\/build/ {print $1}')
  if [ -n "$invalid_build" ]; then
    printf 'lower packages import internal/build:\n%s\n' "$invalid_build" >&2
    exit 1
  fi
}

check_embed_inventory() {
  embed_files=$(git grep -l '//go:embed' -- '*.go' ':(exclude)**/*_test.go' | sort)
  expected_embed_files=$(printf '%s\n' internal/edit/web.go internal/recommend/chinese/embed.go internal/render/site_assets.go internal/social/card.go | sort)
  if [ "$embed_files" != "$expected_embed_files" ]; then
    printf 'production embed owners changed:\n%s\n' "$embed_files" >&2
    exit 1
  fi

  site_files=$(find internal/render/site -type f -print | sed 's#^internal/render/site/##' | sort)
  expected_site_files=$(printf '%s\n' runtime.js style.css | sort)
  if [ "$site_files" != "$expected_site_files" ]; then
    printf 'embedded site inventory changed:\n%s\n' "$site_files" >&2
    exit 1
  fi

  chinese_files=$(find internal/recommend/chinese/data -type f -print | sed 's#^internal/recommend/chinese/data/##' | sort)
  expected_chinese_files=$(printf '%s\n' s_1.txt stop_tokens.txt t_1.txt | sort)
  if [ "$chinese_files" != "$expected_chinese_files" ]; then
    printf 'embedded Chinese resource inventory changed:\n%s\n' "$chinese_files" >&2
    exit 1
  fi

  vendor_files=$(find internal/render/vendor -type f -print | sort)
  expected_vendor_files=$(
    {
      sed -n 's/^- `[0-9a-f]\{64\}  \(internal\/render\/vendor\/[^`]*\)`$/\1/p' THIRD_PARTY.md
      printf '%s\n' internal/render/vendor/katex/LICENSE internal/render/vendor/mermaid/LICENSE
    } | sort
  )
  if [ "$vendor_files" != "$expected_vendor_files" ]; then
    printf 'embedded vendor inventory differs from THIRD_PARTY.md:\n%s\n' "$vendor_files" >&2
    exit 1
  fi
}

check_workflows() {
  workflows=$(find .github/workflows -type f \( -name '*.yml' -o -name '*.yaml' \) -print | sort)
  [ -n "$workflows" ] || { echo "workflow files are missing" >&2; exit 1; }

  uses=$(grep -h -E '^[[:space:]]*uses:' $workflows || true)
  unpinned=$(printf '%s\n' "$uses" | grep -Ev 'uses: [^[:space:]]+@[0-9a-f]{40}([[:space:]]*#.*)?$' || true)
  if [ -n "$unpinned" ]; then
    printf 'workflow actions must use full commit OIDs:\n%s\n' "$unpinned" >&2
    exit 1
  fi

  for workflow in $workflows; do
    grep -Eq '^[[:space:]]*GO_VERSION: [0-9]+\.[0-9]+\.[0-9]+$' "$workflow" || { echo "$workflow has no exact Go version" >&2; exit 1; }
    grep -Eq '^[[:space:]]*NODE_VERSION: [0-9]+\.[0-9]+\.[0-9]+$' "$workflow" || { echo "$workflow has no exact Node version" >&2; exit 1; }
    grep -Eq '^[[:space:]]*GORELEASER_VERSION: v[0-9]+\.[0-9]+\.[0-9]+$' "$workflow" || { echo "$workflow has no exact GoReleaser version" >&2; exit 1; }
  done

  if grep -n -H -E 'runs-on: .*latest|runner: .*latest|VERSION[[:space:]]*(:|:?=)[[:space:]]*(latest|main|master)|@(latest|main|master)([[:space:]]|$)' $workflows Makefile README.md scripts/*.sh | grep -v '^scripts/verify-product-boundary.sh:'; then
    echo "floating runner, action, or tool version remains" >&2
    exit 1
  fi
  scripts/actionlint.sh $workflows
}

check_artifacts() {
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/obsite-boundary-XXXXXX")
  binary=$tmp/obsite
  vault=$tmp/vault
  mkdir -p "$vault"
  (
    cd "$root"
    go build -trimpath -o "$binary" ./cmd/obsite
  )

  for marker in '_pagefind' 'testdata/quality' 'Rebuilding State from an Event Log' 'cal-zh-hans-00'; do
    if grep -a -F "$marker" "$binary" >/dev/null; then
      echo "forbidden release marker embedded in binary: $marker" >&2
      exit 1
    fi
  done

  (
    cd "$vault"
    "$binary" init >/dev/null
    cat > audit.md <<'MARKDOWN'
---
title: Boundary audit
publish: true
type: doc
---
# Boundary audit

Inline math: $x^2$.

```mermaid
flowchart LR
  A --> B
```
MARKDOWN
    "$binary" build >/dev/null
  )
  output=$vault/public

  if find "$output" -type f -print | grep -i 'pagefind\|testdata' >/dev/null; then
    echo "forbidden generated framework path remains" >&2
    exit 1
  fi

  python3 - "$output" <<'PY'
from html.parser import HTMLParser
from pathlib import Path
import re, sys

root = Path(sys.argv[1])
violations = []
class AssetParser(HTMLParser):
    def handle_starttag(self, tag, attrs):
        values = {name.lower(): (value or '') for name, value in attrs}
        if tag.lower() == 'script' and values.get('src', '').lower().startswith(('http://', 'https://')):
            violations.append((self.getpos(), values['src']))
        rel = values.get('rel', '').lower().split()
        if tag.lower() == 'link' and 'stylesheet' in rel and values.get('href', '').lower().startswith(('http://', 'https://')):
            violations.append((self.getpos(), values['href']))

for html in root.rglob('*.html'):
    parser = AssetParser()
    parser.feed(html.read_text(encoding='utf-8'))
for css in root.rglob('*.css'):
    text = css.read_text(encoding='utf-8')
    if re.search(r'(?:url\(|@import\s+(?:url\()?)\s*["\']?https?://', text, re.IGNORECASE):
        violations.append((css, 'external CSS resource'))
if violations:
    raise SystemExit(f'generated framework has external runtime resources: {violations}')
PY

  runtimes=$(find "$output/assets/obsite" -type f -name 'runtime.*.js' -print)
  if [ "$(printf '%s\n' "$runtimes" | awk 'NF {count++} END {print count+0}')" -ne 1 ]; then
    printf 'shared runtime count is not one:\n%s\n' "$runtimes" >&2
    exit 1
  fi
  if printf '%s\n' "$runtimes" | xargs grep -a -i -E 'https?://|cdn|npm' >/dev/null; then
    echo "shared runtime contains an external downloader target" >&2
    exit 1
  fi
}

case "$mode" in
  --search-only) check_search ;;
  --packages-only) check_packages ;;
  all) check_search; check_packages; check_embed_inventory; check_workflows; check_artifacts ;;
  *) echo "usage: $0 [--search-only|--packages-only]" >&2; exit 2 ;;
esac
