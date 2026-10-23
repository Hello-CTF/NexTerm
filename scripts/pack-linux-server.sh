#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/target/release-assets"
BIN=""
WEB="$ROOT/dist"
ARCH="amd64"
HELPERS_DIR="$ROOT/target/go-build"
REQUIRE_EVIDENCE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --out=*) OUT="${1#*=}"; shift ;;
    --bin) BIN="$2"; shift 2 ;;
    --bin=*) BIN="${1#*=}"; shift ;;
    --web) WEB="$2"; shift 2 ;;
    --web=*) WEB="${1#*=}"; shift ;;
    --arch) ARCH="$2"; shift 2 ;;
    --arch=*) ARCH="${1#*=}"; shift ;;
    --helpers-dir) HELPERS_DIR="$2"; shift 2 ;;
    --helpers-dir=*) HELPERS_DIR="${1#*=}"; shift ;;
    --require-evidence) REQUIRE_EVIDENCE=1; shift ;;
    -h|--help)
      cat <<'PACK_HELP_EOF'
Usage: scripts/pack-linux-server.sh [options]

Package one full CGO_ENABLED=0 Go server tar.gz with both runtime units and
all nine SSH target helpers. scripts/build.mjs owns compilation/versioning;
scripts/e2e-harness retains --sync-only acceptance.

Options:
  --out DIR           output directory (default target/release-assets)
  --bin PATH          server binary (default target/go-build/nexterm-server-linux-<arch>)
  --web DIR           frontend dist directory (default dist)
  --arch ARCH         Linux server architecture: amd64 or arm64 (default amd64)
  --helpers-dir DIR   nine-target helper binaries and metadata records
  --require-evidence  fail unless the artifact report passes every assertion
  -h, --help          show this help
PACK_HELP_EOF
      exit 0
      ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$ARCH" in amd64|arm64) ;; *) echo "unsupported Linux server architecture: $ARCH" >&2; exit 2 ;; esac
[ -n "$BIN" ] || BIN="$ROOT/target/go-build/nexterm-server-linux-$ARCH"
VERSION="${NEXTERM_RELEASE_VERSION:-$(node -e 'const fs=require("fs"); const c=JSON.parse(fs.readFileSync(process.argv[1],"utf8")); process.stdout.write(c.info.version)' "$ROOT/wails.json" 2>/dev/null)}" || {
  echo "cannot read the release version from NEXTERM_RELEASE_VERSION or wails.json" >&2; exit 1;
}
[ -f "$BIN" ] || {
  echo "missing Go server binary: $BIN" >&2
  echo "build it with: node scripts/build.mjs server --release --os=linux --arch=$ARCH" >&2
  exit 1
}
[ -f "$WEB/index.html" ] || { echo "missing real frontend: $WEB/index.html" >&2; exit 1; }
[ -x "$BIN" ] || { echo "server binary is not executable: $BIN" >&2; exit 1; }
while IFS='|' read -r helper_os helper_arch helper_name; do
  helper="$HELPERS_DIR/$helper_name"
  [ -x "$helper" ] || { echo "missing executable helper: $helper" >&2; exit 1; }
  node "$ROOT/.github/scripts/verify-reused-binary.mjs" "$helper" "helper-$helper_os-$helper_arch" "$helper_os" "$helper_arch" 0
done < <(node "$ROOT/.github/scripts/helper-matrix.mjs" --shell)

FILE_INFO="$(file -b "$BIN")"
case "$FILE_INFO" in *ELF*64-bit*) ;; *) echo "not a 64-bit ELF binary: $FILE_INFO" >&2; exit 1 ;; esac
case "$ARCH:$FILE_INFO" in
  amd64:*x86-64*|amd64:*x86_64*|arm64:*aarch64*|arm64:*ARM\ aarch64*) ;;
  *) echo "ELF architecture does not match $ARCH: $FILE_INFO" >&2; exit 1 ;;
esac
case "$FILE_INFO" in *statically\ linked*) ;; *) echo "Linux server must be statically linked: $FILE_INFO" >&2; exit 1 ;; esac
if command -v readelf >/dev/null 2>&1 && readelf -l "$BIN" | grep -q 'DYNAMIC'; then
  echo "Linux server contains a PT_DYNAMIC segment" >&2; exit 1
fi
GO_INFO="$(go version -m "$BIN")"
printf '%s' "$GO_INFO" | grep -q 'CGO_ENABLED=0' || { echo "server was not built with CGO_ENABLED=0" >&2; exit 1; }
export NEXTERM_RELEASE_VERSION="$VERSION"
node "$ROOT/.github/scripts/verify-reused-binary.mjs" "$BIN" "server-linux-$ARCH" linux "$ARCH" 0

for unit in nexterm-server.service nexterm-onlyserver.service; do
  [ -f "$ROOT/deploy/systemd/$unit" ] || { echo "missing systemd unit: deploy/systemd/$unit" >&2; exit 1; }
done
[ -f "$ROOT/deploy/systemd/install.sh" ] || { echo "missing systemd installer: deploy/systemd/install.sh" >&2; exit 1; }
[ -f "$ROOT/LICENSE" ] || { echo "missing LICENSE" >&2; exit 1; }

mkdir -p "$OUT"
rm -f \
  "$OUT/nexterm-server_linux_${ARCH}_${VERSION}.tar.gz" \
  "$OUT/nexterm-server_linux_${ARCH}_${VERSION}.tar.gz.artifact.json"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct HEAD)}"

tar_cz() {
  python3 "$ROOT/scripts/lib/portable-archive.py" tar "$1" "$2" "$3" "$SOURCE_DATE_EPOCH"
}

package_full() {
  local name="nexterm-server_linux_${ARCH}_${VERSION}"
  local dir="$STAGE/$name"
  local unit env_file member helper_os helper_arch helper_name
  mkdir -p "$dir"
  install -m 0755 "$BIN" "$dir/nexterm-server"
  while IFS='|' read -r helper_os helper_arch helper_name; do
    install -m 0755 "$HELPERS_DIR/$helper_name" "$dir/$helper_name"
  done < <(node "$ROOT/.github/scripts/helper-matrix.mjs" --shell)
  for unit in nexterm-server.service nexterm-onlyserver.service; do
    install -m 0644 "$ROOT/deploy/systemd/$unit" "$dir/$unit"
  done
  install -m 0755 "$ROOT/deploy/systemd/install.sh" "$dir/install.sh"
  for env_file in nexterm.env onlyserver.env; do
    install -m 0644 "$ROOT/deploy/systemd/$env_file.example" "$dir/$env_file.example"
  done
  install -m 0644 "$ROOT/LICENSE" "$dir/LICENSE"
  install -m 0644 "$ROOT/deploy/systemd/README.md" "$dir/README.md"
  python3 "$ROOT/scripts/lib/portable-archive.py" copy-tree "$WEB" "$dir/web"
  tar_cz "$OUT/$name.tar.gz" "$STAGE" "$name"
  chmod 0644 "$OUT/$name.tar.gz"

  local archive_members
  archive_members="$(tar -tzf "$OUT/$name.tar.gz")"
  for member in \
    nexterm-server \
    nexterm-server.service \
    nexterm-onlyserver.service \
    install.sh \
    nexterm.env.example \
    onlyserver.env.example \
    LICENSE \
    README.md \
    web/index.html; do
    grep -Fxq "$name/$member" <<<"$archive_members"
  done
  while IFS='|' read -r helper_os helper_arch helper_name; do
    grep -Fxq "$name/$helper_name" <<<"$archive_members"
  done < <(node "$ROOT/.github/scripts/helper-matrix.mjs" --shell)
}

ARTIFACT="$OUT/nexterm-server_linux_${ARCH}_${VERSION}.tar.gz"
(cd "$ROOT" && package_full)

report_args=(--kind=server-archive --os=linux "--arch=$ARCH" "--file=$ARTIFACT" "--helpers-dir=$HELPERS_DIR")
if [ "$REQUIRE_EVIDENCE" -ne 0 ]; then
  report_args+=(--require-evidence)
fi
(cd "$ROOT" && node scripts/build.mjs report "${report_args[@]}")

if command -v sha256sum >/dev/null 2>&1; then digest="$(sha256sum "$ARTIFACT" | cut -d' ' -f1)"; else digest="$(shasum -a 256 "$ARTIFACT" | cut -d' ' -f1)"; fi
printf 'Linux %s full server archive: %s  %s bytes  %s\n' "$ARCH" "$(basename "$ARTIFACT")" "$(stat -c %s "$ARTIFACT" 2>/dev/null || stat -f %z "$ARTIFACT")" "$digest"
