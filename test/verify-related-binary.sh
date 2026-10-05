#!/bin/sh
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 BINARY" >&2; exit 2; }
binary=$1
[ -f "$binary" ] || { echo "binary not found: $binary" >&2; exit 1; }
[ "$(stat -c %s "$binary")" -le 41943040 ] || { echo 'stripped binary exceeds 40 MiB' >&2; exit 1; }
! grep -Fq 'github.com/go-ego/gse' go.mod go.sum || { echo 'gse remains in module files' >&2; exit 1; }

grep -Eq '^[[:space:]]*github.com/vcaesar/cedar v0\.30\.0$' go.mod || { echo 'cedar v0.30.0 is not a direct dependency' >&2; exit 1; }

python3 - "$binary" <<'PY'
import json, pathlib, subprocess, sys
binary=pathlib.Path(sys.argv[1]).read_bytes()
for name in ('s_1.txt','t_1.txt','stop_tokens.txt'):
    data=pathlib.Path('internal/recommend/chinese/data',name).read_bytes()
    window=data[:64]
    count=binary.count(window)
    if count != 1:
        raise SystemExit(f'{name} byte window count = {count}, want exactly 1')
negative={
    'Japanese dictionary': '81bee38198e3818be3828d203639353620e5bda2e5aeb9e8a99e0ae38282e381aee58784e381bee38198e3818be381a3203639353620e5bda2e5aeb9e8a99e0a',
    'generic gse IDF': '3637373635320ae5ba93e982a62031332e323037353330343731340ae99d9ee6ada3e4b9892031312e333335373238323934350ae7bc96e8aea22031302e3238',
}
for label, encoded in negative.items():
    if bytes.fromhex(encoded) in binary:
        raise SystemExit(f'{label} bytes found in production binary')
for marker in (b'testdata/quality', b'Rebuilding State from an Event Log', b'cal-zh-hans-00'):
    if marker in binary:
        raise SystemExit(f'test-only marker found in production binary: {marker!r}')

raw=subprocess.check_output(['go','list','-deps','-json','./cmd/oxpio'],text=True)
decoder=json.JSONDecoder(); offset=0; packages=[]
while offset < len(raw):
    while offset < len(raw) and raw[offset].isspace(): offset += 1
    if offset >= len(raw): break
    value, offset=decoder.raw_decode(raw,offset); packages.append(value)
for package in packages:
    import_path=package.get('ImportPath','')
    if import_path == 'github.com/go-ego/gse' or import_path.startswith('github.com/go-ego/gse/'):
        raise SystemExit('gse appears in production dependency closure')
    for embedded in package.get('EmbedFiles') or []:
        if 'testdata/quality' in embedded or '/jp/' in embedded or embedded.endswith('/idf.txt'):
            raise SystemExit(f'excluded embedded file in production closure: {import_path}: {embedded}')
chinese=next((p for p in packages if p.get('ImportPath')=='github.com/simp-lee/oxpio/internal/recommend/chinese'),None)
if chinese is None:
    raise SystemExit('Chinese tokenizer package missing from production closure')
if sorted(chinese.get('EmbedFiles') or []) != ['data/s_1.txt','data/stop_tokens.txt','data/t_1.txt']:
    raise SystemExit(f'unexpected Chinese tokenizer embeds: {chinese.get("EmbedFiles")}')
print('related production binary verified')
PY
