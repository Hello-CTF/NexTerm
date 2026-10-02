#!/usr/bin/env bash
# Package a CGO_ENABLED=0 Go server as full and sync-only Linux tar.gz files.
# This script packages only; scripts/build.mjs owns compilation and version injection.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/target/release-assets"
BIN=""
WEB="$ROOT/dist"
ARCH="amd64"
REQUIRE_SIZE=0

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --bin) BIN="$2"; shift 2 ;;
    --web) WEB="$2"; shift 2 ;;
    --arch) ARCH="$2"; shift 2 ;;
    --require-size) REQUIRE_SIZE=1; shift ;;
    -h|--help) sed -n '1,16p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

case "$ARCH" in amd64|arm64) ;; *) echo "unsupported Linux server architecture: $ARCH" >&2; exit 2 ;; esac
[ -n "$BIN" ] || BIN="$ROOT/target/go-build/nexterm-server-linux-$ARCH"
VERSION="$(node -e 'const c=require("./wails.json"); process.stdout.write(c.info.version)' 2>/dev/null)" || {
  echo "cannot read the sole release version from wails.json" >&2; exit 1;
}
[ -f "$BIN" ] || {
  echo "missing Go server binary: $BIN" >&2
  echo "build it with: node scripts/build.mjs server --release --os=linux --arch=$ARCH" >&2
  exit 1
}
[ -f "$WEB/index.html" ] || { echo "missing real frontend: $WEB/index.html" >&2; exit 1; }
[ -x "$BIN" ] || { echo "server binary is not executable: $BIN" >&2; exit 1; }

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
BINARY_VERSION="$("$BIN" --version)"
[ "$BINARY_VERSION" = "$VERSION" ] || {
  echo "server --version ($BINARY_VERSION) does not match wails.json ($VERSION)" >&2; exit 1;
}

for unit in nexterm-server.service nexterm-onlyserver.service; do
  [ -f "$ROOT/deploy/systemd/$unit" ] || { echo "missing systemd unit: deploy/systemd/$unit" >&2; exit 1; }
done
[ -f "$ROOT/LICENSE" ] || { echo "missing LICENSE" >&2; exit 1; }

mkdir -p "$OUT"
rm -f \
  "$OUT/NexTerm-$VERSION-linux-$ARCH.tar.gz" \
  "$OUT/NexTerm-$VERSION-linux-$ARCH.tar.gz.artifact.json" \
  "$OUT/NexTerm-onlyServer-$VERSION-linux-$ARCH.tar.gz" \
  "$OUT/NexTerm-onlyServer-$VERSION-linux-$ARCH.tar.gz.artifact.json"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if tar --version 2>/dev/null | grep -qi 'gnu tar'; then
  TAR_OWNER=(--owner=0 --group=0 --numeric-owner)
else
  TAR_OWNER=(--uid 0 --gid 0)
fi
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct HEAD)}"

tar_cz() {
  # Fixed metadata and sorted names make identical inputs produce identical archives.
  tar ${TAR_OWNER[@]+"${TAR_OWNER[@]}"} --sort=name --mtime="@$SOURCE_DATE_EPOCH" -czf "$1" -C "$2" "$3"
}

write_env() {
  local target="$1" env_name="$2"
  cat > "$target" <<ENVEOF
# Copy to /etc/nexterm/$env_name and chmod 0600.
NEXTERM_MASTER_KEY=
ENVEOF
}

write_readme() {
  local target="$1" mode="$2"
  cat > "$target" <<MDEOF
# NexTerm $mode $VERSION (linux/$ARCH)

Built from the Go/Wails delivery chain. The server binary is stripped and static;
its version comes only from wails.json. The full package serves the real Vite
frontend from web/; the sync-only package intentionally has no browser or /rpc
route and exposes only /sync/rpc plus /healthz.

## Install

Install nexterm-server as /opt/nexterm/nexterm-server and the supplied systemd
unit. Keep NEXTERM_MASTER_KEY secret and back it up with /var/lib/nexterm.
The full server has no built-in login page: keep it on loopback or behind an
authenticating reverse proxy. Public sync-only deployments still require TLS.

## Verify

- Full: curl http://127.0.0.1:8080/healthz and check syncOnly=false.
- Sync only: check syncOnly=true and commands=3; /rpc must return 404.
- Token CLI: nexterm-server token --data-dir /var/lib/nexterm

LazyCat assembly and real LazyCat/box acceptance belong to M47. This archive is
an input contract, not evidence that those external targets have passed.
MDEOF
}

package_one() {
  local flavor="$1" name dir unit env_file
  if [ "$flavor" = full ]; then
    name="NexTerm-$VERSION-linux-$ARCH"
    unit="nexterm-server.service"
    env_file="nexterm.env"
  else
    name="NexTerm-onlyServer-$VERSION-linux-$ARCH"
    unit="nexterm-onlyserver.service"
    env_file="onlyserver.env"
  fi
  dir="$STAGE/$name"
  mkdir -p "$dir"
  install -m 0755 "$BIN" "$dir/nexterm-server"
  install -m 0644 "$ROOT/deploy/systemd/$unit" "$dir/$unit"
  install -m 0644 "$ROOT/LICENSE" "$dir/LICENSE"
  write_env "$dir/$env_file.example" "$env_file"
  write_readme "$dir/README.md" "$flavor"
  if [ "$flavor" = full ]; then
    mkdir -p "$dir/web"
    (cd "$WEB" && find . -type f -print0 | sort -z | while IFS= read -r -d '' file; do
      install -D -m 0644 "$file" "$dir/web/$file"
    done)
  fi
  tar_cz "$OUT/$name.tar.gz" "$STAGE" "$name"

  tar -tzf "$OUT/$name.tar.gz" | grep -q "^$name/nexterm-server$"
  tar -tzf "$OUT/$name.tar.gz" | grep -q "^$name/$unit$"
  tar -tzf "$OUT/$name.tar.gz" | grep -q "^$name/$env_file.example$"
  if [ "$flavor" = full ]; then
    tar -tzf "$OUT/$name.tar.gz" | grep -q "^$name/web/index.html$"
  elif tar -tzf "$OUT/$name.tar.gz" | grep -q '^.*\/web\/'; then
    echo "sync-only archive unexpectedly contains browser assets" >&2; exit 1
  fi

  local kind="server-archive"
  [ "$flavor" = full ] || kind="sync-archive"
  node scripts/build.mjs report "--kind=$kind" "--flavor=$flavor" --os=linux "--arch=$ARCH" "--file=$OUT/$name.tar.gz"
}

(cd "$ROOT" && package_one full)
(cd "$ROOT" && package_one sync)

# Produce every candidate before a missing/failed size baseline stops publication.
if [ "$REQUIRE_SIZE" -ne 0 ]; then
  for artifact in "$OUT"/*.tar.gz; do
    case "$(basename "$artifact")" in
      NexTerm-onlyServer-*) kind="sync-archive"; flavor="sync" ;;
      *) kind="server-archive"; flavor="full" ;;
    esac
    (cd "$ROOT" && node scripts/build.mjs report "--kind=$kind" "--flavor=$flavor" --os=linux "--arch=$ARCH" "--file=$artifact" --require-size)
  done
fi

echo "Linux $ARCH packages:"
for artifact in "$OUT"/*.tar.gz; do
  if command -v sha256sum >/dev/null 2>&1; then digest="$(sha256sum "$artifact" | cut -d' ' -f1)"; else digest="$(shasum -a 256 "$artifact" | cut -d' ' -f1)"; fi
  printf '  %s  %s bytes  %s\n' "$(basename "$artifact")" "$(stat -c %s "$artifact" 2>/dev/null || stat -f %z "$artifact")" "$digest"
done
