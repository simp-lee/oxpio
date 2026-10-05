#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mode=${1:-}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

run_and_capture() {
  output=$1
  shift
  if "$@" >"$output" 2>&1; then
    cat "$output"
    return 0
  else
    status=$?
    cat "$output" >&2
    return "$status"
  fi
}

case "$mode" in
  core)
    run_and_capture "$tmp/core.txt" env GOMAXPROCS=4 \
      go test ./internal/recommend -run '^$' \
      -bench '^BenchmarkRelatedBuildWarm/mixed-(500|1000|5000)$' \
      -benchtime=1x -count=5 -benchmem -timeout=15m
    python3 - "$tmp/core.txt" <<'PY'
import re, statistics, sys
rows={500:[],1000:[],5000:[]}
pat=re.compile(r'^BenchmarkRelatedBuildWarm/mixed-(500|1000|5000)-\d+\s+\d+\s+(\d+) ns/op\s+(\d+) B/op')
for line in open(sys.argv[1]):
    m=pat.search(line)
    if m: rows[int(m.group(1))].append((int(m.group(2)),int(m.group(3))))
for size, values in rows.items():
    if len(values)!=5: raise SystemExit(f'mixed-{size}: got {len(values)} rows, want 5')
if any(b>1_500_000_000 for _,b in rows[5000]): raise SystemExit('mixed-5000 core B/op exceeds 1,500,000,000')
med_ns={n:statistics.median(v for v,_ in x) for n,x in rows.items()}
med_b={n:statistics.median(v for _,v in x) for n,x in rows.items()}
if med_b[1000] > med_b[500]*2.5: raise SystemExit('500->1000 B/op growth exceeds 2.5x')
if med_ns[1000] > med_ns[500]*4.5: raise SystemExit('500->1000 ns/op growth exceeds 4.5x')
print('core performance budgets verified')
PY
    ;;
  adversarial)
    for case_name in sparse-posting term-40%-boundary term-49%-coverage tag-49%-coverage rejected-content; do
      run_and_capture "$tmp/${case_name}.txt" env GOMAXPROCS=4 \
        go test ./internal/recommend -run '^$' \
        -bench "^BenchmarkRelatedBuildWarm/${case_name}-5000$" \
        -benchtime=1x -count=3 -benchmem -timeout=15m
      count=$(grep -Ec "^BenchmarkRelatedBuildWarm/${case_name}-5000-[0-9]+[[:space:]]" "$tmp/${case_name}.txt")
      [ "$count" -eq 3 ] || { echo "$case_name: got $count rows, want 3" >&2; exit 1; }
    done
    printf 'adversarial completion budgets verified\n'
    ;;
  end-to-end)
    run_and_capture "$tmp/end.txt" env GOMAXPROCS=4 \
      go test ./internal/build -run '^$' \
      -bench '^BenchmarkRelatedEndToEndWarm/mixed-5000$' \
      -benchtime=1x -count=5 -benchmem -timeout=15m
    python3 - "$tmp/end.txt" <<'PY'
import re, statistics, sys
pat=re.compile(r'^BenchmarkRelatedEndToEndWarm/mixed-5000-\d+\s+\d+\s+(\d+) ns/op')
values=[]
for line in open(sys.argv[1]):
    m=pat.search(line)
    if m: values.append(int(m.group(1)))
if len(values)!=5: raise SystemExit(f'end-to-end: got {len(values)} rows, want 5')
if statistics.median(values)>30_000_000_000: raise SystemExit('end-to-end median exceeds 30 seconds')
print('end-to-end time budget verified')
PY
    ;;
  rss)
    for count in 500 1000 5000; do
      run_and_capture "$tmp/rss-${count}.txt" env \
        OXPIO_RELATED_END_TO_END_RSS_HELPER="mixed:${count}" GOMAXPROCS=4 \
        go test ./internal/build -run '^TestRSSHelperIsolation$' -count=1 -v -timeout=15m
    done
    python3 - "$tmp/rss-500.txt" "$tmp/rss-1000.txt" "$tmp/rss-5000.txt" <<'PY'
import re, sys
values={}
for path in sys.argv[1:]:
    text=open(path).read()
    m=re.search(r'RELATED_END_TO_END_RSS mixed (500|1000|5000) (\d+)',text)
    if not m: raise SystemExit(f'missing RSS marker in {path}')
    values[int(m.group(1))]=int(m.group(2))
if values[5000] > 1536*1024: raise SystemExit('mixed-5000 VmHWM exceeds 1536 MiB')
if values[1000] > values[500]*2.5: raise SystemExit('500->1000 VmHWM growth exceeds 2.5x')
print('RSS budgets verified')
PY
    ;;
  *)
    echo 'usage: test/verify-related-benchmarks.sh {core|adversarial|end-to-end|rss}' >&2
    exit 2
    ;;
esac
