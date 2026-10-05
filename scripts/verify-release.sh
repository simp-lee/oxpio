#!/bin/sh
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 DIST_DIR" >&2; exit 2; }
DIST=$1
[ -d "$DIST" ] || { echo "dist directory not found: $DIST" >&2; exit 1; }
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
METADATA=$DIST/metadata.json
[ -f "$METADATA" ] || { echo "release metadata missing: $METADATA" >&2; exit 1; }

EXPECTED_VERSION=$(python3 - "$METADATA" <<'PY'
import json, sys
print(json.load(open(sys.argv[1], encoding='utf-8'))['version'])
PY
)
EXPECTED_COMMIT=$(python3 - "$METADATA" <<'PY'
import json, sys
print(json.load(open(sys.argv[1], encoding='utf-8'))['commit'])
PY
)
case "$EXPECTED_VERSION" in
  *snapshot*) EXPECTED_TYPE=snapshot ;;
  *) EXPECTED_TYPE=release ;;
esac
COMMIT_DATE=$(git -C "$ROOT" show -s --format=%cI "$EXPECTED_COMMIT")
EXPECTED_DATE=$(python3 - "$COMMIT_DATE" <<'PY'
from datetime import datetime, timezone
import sys
value = datetime.fromisoformat(sys.argv[1]).astimezone(timezone.utc)
print(value.strftime('%Y-%m-%dT%H:%M:%SZ'))
PY
)
EXPECTED_OUTPUT="oxpio version=$EXPECTED_VERSION commit=$EXPECTED_COMMIT date=$EXPECTED_DATE type=$EXPECTED_TYPE"

python3 - "$ROOT" "$DIST" "$EXPECTED_VERSION" "$EXPECTED_COMMIT" "$EXPECTED_DATE" "$EXPECTED_TYPE" <<'PY'
from pathlib import Path
import hashlib, json, platform, re, subprocess, sys, tarfile, tempfile, zipfile

root, dist = map(Path, sys.argv[1:3])
expected_version, expected_commit, expected_date, expected_type = sys.argv[3:]
expected_output = f'oxpio version={expected_version} commit={expected_commit} date={expected_date} type={expected_type}'
expected_marker = f'oxpio-release-metadata:{expected_version}|{expected_commit}|{expected_date}|{expected_type}'.encode()
archives = sorted(list(dist.glob('*.tar.gz')) + list(dist.glob('*.zip')))
if len(archives) != 6:
    raise SystemExit(f'archive count = {len(archives)}, want 6: {[p.name for p in archives]}')

notice_paths = {
    'LICENSE',
    'THIRD_PARTY.md',
    'internal/markdown/PASSTHROUGH_LICENSE',
    'internal/recommend/chinese/LICENSE-APACHE-2.0',
    'internal/recommend/chinese/LICENSE-CEDAR-BSD-2-CLAUSE',
    'internal/recommend/chinese/LICENSE-JIEBA-MIT',
    'internal/recommend/chinese/THIRD_PARTY.md',
    'internal/render/runtime-assets/katex/LICENSE',
    'internal/render/runtime-assets/mermaid/LICENSE',
    'internal/social/assets/DroidSansFallbackFull.LICENSE',
}
expected_matrix = {(os_name, arch) for os_name in ('linux', 'darwin', 'windows') for arch in ('amd64', 'arm64')}

# Verify the audited source resources by their documented digest rather than
# relying on a distinctive string that could survive a changed asset.
notice_hashes = {}
for line in (root / 'THIRD_PARTY.md').read_text(encoding='utf-8').splitlines():
    match = re.match(r'^- `?([0-9a-f]{64})  ([^`]+)`?$', line)
    if match:
        notice_hashes[match.group(2)] = match.group(1)
if not notice_hashes:
    raise SystemExit('no documented third-party hashes found')
for relative, expected in notice_hashes.items():
    source = root / relative
    if not source.is_file():
        raise SystemExit(f'documented resource missing: {relative}')
    actual = hashlib.sha256(source.read_bytes()).hexdigest()
    if actual != expected:
        raise SystemExit(f'{relative}: source hash {actual} != documented {expected}')
embedded_resources = [
    'internal/social/assets/DroidSansFallbackFull.ttf',
    'internal/social/assets/KaTeX_Main-Regular.ttf',
    'internal/render/runtime-assets/katex/katex.min.js',
    'internal/render/runtime-assets/mermaid/mermaid.min.js',
]
embedded_bytes = {relative: (root / relative).read_bytes() for relative in embedded_resources}

def matrix_key(name):
    lowered = name.lower()
    os_name = next((item for item in ('linux', 'darwin', 'windows') if item in lowered), None)
    arch = next((item for item in ('amd64', 'arm64') if item in lowered), None)
    if not os_name or not arch:
        raise SystemExit(f'artifact matrix name is not auditable: {name}')
    return os_name, arch


def host_matrix_key():
    os_name = {'linux': 'linux', 'darwin': 'darwin', 'windows': 'windows'}.get(platform.system().lower())
    arch = {'x86_64': 'amd64', 'amd64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(platform.machine().lower())
    return (os_name, arch) if os_name and arch else None


# The release marker is injected into the executable and is also the source of
# the version command's output. This makes the exact metadata check portable to
# targets that cannot run on the verifier's host; the matching native target is
# executed below as an additional behavioral check.
def verify_version_metadata(binary, path):
    if binary.count(expected_marker + b'\x00') != 1:
        raise SystemExit(f'{path}: release version metadata marker does not match {expected_marker.decode()!r}')


def verify_native_version(binary, path):
    with tempfile.TemporaryDirectory(prefix='oxpio-release-version-') as directory:
        executable = Path(directory) / ('oxpio.exe' if path[0] == 'windows' else 'oxpio')
        executable.write_bytes(binary)
        executable.chmod(0o755)
        for argument in ('version', '--version'):
            try:
                result = subprocess.run(
                    [str(executable), argument],
                    check=True,
                    capture_output=True,
                    text=True,
                )
            except (OSError, subprocess.CalledProcessError) as error:
                raise SystemExit(f'{path}: unable to execute archived binary for version check: {error}')
            output = result.stdout.rstrip('\n')
            if output != expected_output:
                raise SystemExit(f'{path} {argument}: {output!r} != {expected_output!r}')

raw_binaries = {}
for path in dist.rglob('*'):
    if path.is_file() and path.name in ('oxpio', 'oxpio.exe'):
        key = matrix_key(path.parent.name)
        if key in raw_binaries:
            raise SystemExit(f'duplicate raw binary for {key}: {path}, {raw_binaries[key]}')
        raw_binaries[key] = path
if set(raw_binaries) != expected_matrix:
    raise SystemExit(f'binary matrix = {sorted(raw_binaries)}, want {sorted(expected_matrix)}')

# Verify the build provenance and object format for every target. Linux also
# exposes its static-link status through file(1); on Darwin and Windows,
# CGO_ENABLED=0 is the platform-appropriate check that cgo was disabled (their
# native formats retain normal operating-system loader dependencies).
format_markers = {
    ('linux', 'amd64'): ('ELF 64-bit', 'x86-64'),
    ('linux', 'arm64'): ('ELF 64-bit', 'ARM aarch64'),
    ('darwin', 'amd64'): ('Mach-O 64-bit', 'x86_64'),
    ('darwin', 'arm64'): ('Mach-O 64-bit', 'arm64'),
    ('windows', 'amd64'): ('PE32+', 'x86-64'),
    ('windows', 'arm64'): ('PE32+', ('ARM64', 'Aarch64')),
}
for key, binary in sorted(raw_binaries.items()):
    os_name, arch = key
    try:
        build_info = subprocess.run(
            ['go', 'version', '-m', '-json', str(binary)],
            check=True,
            capture_output=True,
            text=True,
        )
        settings = {
            item['Key']: item['Value']
            for item in json.loads(build_info.stdout).get('Settings', [])
        }
    except (FileNotFoundError, subprocess.CalledProcessError, KeyError, json.JSONDecodeError) as error:
        raise SystemExit(f'{binary}: unable to read Go build metadata: {error}')
    for setting, expected in (('CGO_ENABLED', '0'), ('GOOS', os_name), ('GOARCH', arch)):
        if settings.get(setting) != expected:
            actual = settings.get(setting, '<missing>')
            raise SystemExit(f'{binary}: {setting} = {actual}, want {expected}')

    try:
        file_info = subprocess.run(
            ['file', '-b', str(binary)],
            check=True,
            capture_output=True,
            text=True,
        ).stdout
    except (FileNotFoundError, subprocess.CalledProcessError) as error:
        raise SystemExit(f'{binary}: unable to inspect executable format: {error}')
    for marker in format_markers[key]:
        alternatives = marker if isinstance(marker, tuple) else (marker,)
        if not any(candidate in file_info for candidate in alternatives):
            raise SystemExit(f'{binary}: file format = {file_info.strip()!r}, missing one of {alternatives!r}')
    if os_name == 'linux' and not re.search(r'(?:statically linked|not a dynamic executable)', file_info):
        raise SystemExit(f'{binary}: not statically linked: {file_info.strip()}')

seen = set()
native_key = host_matrix_key()
for archive in archives:
    key = matrix_key(archive.name)
    seen.add(key)
    expected_binary = 'oxpio.exe' if key[0] == 'windows' else 'oxpio'
    if key[0] == 'windows':
        if archive.suffix != '.zip':
            raise SystemExit(f'{archive.name}: Windows archive must be zip')
        with zipfile.ZipFile(archive) as bundle:
            infos = {info.filename.rstrip('/'): info for info in bundle.infolist() if not info.is_dir()}
            members = {name: bundle.read(info) for name, info in infos.items()}
            modes = {name: (info.external_attr >> 16) & 0o777 for name, info in infos.items()}
    else:
        if not archive.name.endswith('.tar.gz'):
            raise SystemExit(f'{archive.name}: {key[0]} archive must be tar.gz')
        with tarfile.open(archive, 'r:gz') as bundle:
            infos = {member.name.rstrip('/'): member for member in bundle.getmembers() if member.isfile()}
            members = {name: bundle.extractfile(info).read() for name, info in infos.items()}
            modes = {name: info.mode & 0o777 for name, info in infos.items()}
            for name, info in infos.items():
                if (info.uid, info.gid, info.uname, info.gname) != (0, 0, 'root', 'root'):
                    raise SystemExit(f'{archive.name}: noncanonical ownership for {name}')
    expected_members = notice_paths | {expected_binary}
    if set(members) != expected_members:
        raise SystemExit(f'{archive.name} members = {sorted(members)}, want {sorted(expected_members)}')
    for name, mode in modes.items():
        expected_mode = 0o755 if name == expected_binary else 0o644
        if mode != expected_mode:
            raise SystemExit(f'{archive.name}: mode for {name} = {mode:o}, want {expected_mode:o}')
    verify_version_metadata(members[expected_binary], f'{archive.name}:{expected_binary}')
    if native_key == key:
        verify_native_version(members[expected_binary], key)
    if members[expected_binary] != raw_binaries[key].read_bytes():
        raise SystemExit(f'{archive.name} executable differs from the audited raw binary')
if seen != expected_matrix:
    raise SystemExit(f'archive matrix = {sorted(seen)}, want {sorted(expected_matrix)}')

checksum = dist / 'checksums.txt'
if not checksum.is_file():
    raise SystemExit('checksums.txt missing')
listed = {}
for line in checksum.read_text(encoding='utf-8').splitlines():
    digest, name = line.split(None, 1)
    listed[name.strip()] = digest
for archive in archives:
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if listed.get(archive.name) != digest:
        raise SystemExit(f'checksum mismatch for {archive.name}')
if set(listed) != {archive.name for archive in archives}:
    raise SystemExit(f'checksum members = {sorted(listed)}')

for key, binary in sorted(raw_binaries.items()):
    data = binary.read_bytes()
    for marker in (b'testdata/quality', b'Rebuilding State from an Event Log', b'cal-zh-hans-00', b'_pagefind'):
        if marker in data:
            raise SystemExit(f'{binary}: forbidden marker {marker!r}')
    for name in ('s_1.txt', 't_1.txt', 'stop_tokens.txt'):
        window = (root / 'internal/recommend/chinese/data' / name).read_bytes()[:64]
        if data.count(window) != 1:
            raise SystemExit(f'{binary}: {name} resource count != 1')
    for relative, resource in embedded_bytes.items():
        if data.count(resource) < 1:
            raise SystemExit(f'{binary}: embedded resource bytes missing: {relative}')
    print(f'{binary.relative_to(dist)} {len(data)} bytes')
PY

printf 'release metadata verified for six archives: %s\n' "$EXPECTED_OUTPUT"
echo "release artifacts verified"
