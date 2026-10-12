#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/target/release-assets"
BIN_DIR="$ROOT/target/go-build"
REQUIRE_EVIDENCE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --out=*) OUT="${1#*=}"; shift ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --bin-dir=*) BIN_DIR="${1#*=}"; shift ;;
    --require-evidence) REQUIRE_EVIDENCE=1; shift ;;
    -h|--help)
      cat <<'PACK_HELP_EOF'
Usage: scripts/pack-agent.sh [options]

Package the five device-agent release archives from verified CI binaries:
NexTerm-agent_<version>_<linux|darwin>_<arch>.tar.gz (tar root: nexterm-agent)
and NexTerm-agent_<version>_windows_amd64.zip (zip root: nexterm-agent.exe).
scripts/build.mjs owns compilation/versioning.

Options:
  --out DIR           output directory (default target/release-assets)
  --bin-dir DIR       agent binary directory (default target/go-build)
  --require-evidence  fail unless every artifact report passes every assertion
  -h, --help          show this help
PACK_HELP_EOF
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

VERSION="${NEXTERM_RELEASE_VERSION:-$(node -e 'const fs=require("fs"); const c=JSON.parse(fs.readFileSync(process.argv[1],"utf8")); process.stdout.write(c.info.version)' "$ROOT/wails.json" 2>/dev/null)}" || {
  echo "cannot read the release version from NEXTERM_RELEASE_VERSION or wails.json" >&2; exit 1;
}
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct HEAD)}"

if tar --version 2>/dev/null | grep -qi 'gnu tar'; then
  TAR_OWNER=(--owner=0 --group=0 --numeric-owner)
else
  TAR_OWNER=(--uid 0 --gid 0)
fi

tar_cz() {
  tar ${TAR_OWNER[@]+"${TAR_OWNER[@]}"} --sort=name --mtime="@$SOURCE_DATE_EPOCH" -czf "$1" -C "$2" "$3"
}

normalize_mtime() {
  touch -d "@$SOURCE_DATE_EPOCH" "$1" 2>/dev/null || touch -t "$(date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" "$1"
}

zip_pack() {
  python3 - "$1" "$2/$3" "$3" "$SOURCE_DATE_EPOCH" <<'PY'
import os
import sys
import time
import zipfile

artifact, source, member, epoch = sys.argv[1], sys.argv[2], sys.argv[3], int(sys.argv[4])
info = zipfile.ZipInfo(member, date_time=time.gmtime(epoch)[:6])
info.compress_type = zipfile.ZIP_DEFLATED
info.external_attr = (0o100000 | 0o755) << 16
with open(source, "rb") as handle:
    data = handle.read()
with zipfile.ZipFile(artifact, "w") as archive:
    archive.writestr(info, data)
PY
}

zip_members() {
  python3 - "$1" <<'PY'
import sys
import zipfile

with zipfile.ZipFile(sys.argv[1]) as archive:
    for name in archive.namelist():
        print(name)
PY
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p "$OUT"
for spec in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64; do
  os="${spec%-*}"
  arch="${spec#*-}"
  suffix=""
  if [ "$os" = "windows" ]; then suffix=".exe"; fi
  bin="$BIN_DIR/nexterm-agent-${os}-${arch}${suffix}"
  [ -f "$bin" ] || {
    echo "missing agent binary: $bin" >&2
    echo "build it with: node scripts/build.mjs agent --release --os=$os --arch=$arch" >&2
    exit 1
  }
  member="nexterm-agent${suffix}"
  stage="$ROOT/target/package-work/agent/${os}-${arch}"
  rm -rf "$stage"
  mkdir -p "$stage"
  install -m 0755 "$bin" "$stage/$member"
  normalize_mtime "$stage/$member"
  if [ "$os" = "windows" ]; then
    artifact="$OUT/NexTerm-agent_${VERSION}_windows_${arch}.zip"
    rm -f "$artifact" "$artifact.artifact.json"
    zip_pack "$artifact" "$stage" "$member"
    members="$(zip_members "$artifact")"
    [ "$members" = "$member" ] || { echo "zip root must contain only $member, got: $members" >&2; exit 1; }
    kind="agent-zip"
  else
    artifact="$OUT/NexTerm-agent_${VERSION}_${os}_${arch}.tar.gz"
    rm -f "$artifact" "$artifact.artifact.json"
    tar_cz "$artifact" "$stage" "$member"
    tar_members="$(tar -tzf "$artifact")"
    [ "$tar_members" = "$member" ] || { echo "tar root must contain only $member, got: $tar_members" >&2; exit 1; }
    kind="agent-archive"
  fi
  report_args=(--kind="$kind" --os="$os" --arch="$arch" --file="$artifact")
  if [ "$REQUIRE_EVIDENCE" -ne 0 ]; then
    report_args+=(--require-evidence)
  fi
  (cd "$ROOT" && node scripts/build.mjs report "${report_args[@]}")
  printf 'agent %s/%s archive: %s  %s bytes  %s\n' "$os" "$arch" "$(basename "$artifact")" "$(stat -c %s "$artifact" 2>/dev/null || stat -f %z "$artifact")" "$(sha256_of "$artifact")"
done
