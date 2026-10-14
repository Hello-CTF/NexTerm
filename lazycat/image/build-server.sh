#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTENT="$ROOT/lazycat/content"
OUT_BIN="$HERE/nexterm-server"

LZC_ARCH="${LZC_ARCH:-amd64}"
case "$LZC_ARCH" in
  amd64) ELF_ARCH="x86-64" ;;
  arm64) ELF_ARCH="aarch64" ;;
  *)
    echo "[lzc-build] error: LZC_ARCH must be amd64 or arm64, got: $LZC_ARCH" >&2
    exit 1
    ;;
esac

echo "[lzc-build] repository root: $ROOT"
echo "[lzc-build] target architecture: ${LZC_ARCH} (linux/${LZC_ARCH}, Go cross-compilation)"

# Version resolution: NEXTERM_RELEASE_VERSION first, else the current commit
# sitting exactly on a tag; when resolved, inject it into both package.yml and
# build.mjs (binary stamped with the same version), otherwise explicitly
# restore the 0.0.0 placeholder.
LZC_VERSION="${NEXTERM_RELEASE_VERSION:-}"
if [ -z "$LZC_VERSION" ]; then
  LZC_VERSION="$(git -C "$ROOT" describe --exact-match --tags HEAD 2>/dev/null | sed 's/^v//' || true)"
fi
if [ -n "$LZC_VERSION" ]; then
  export NEXTERM_RELEASE_VERSION="$LZC_VERSION"
  sed -i "s/^version: .*/version: ${LZC_VERSION}/" "$ROOT/lazycat/package.yml"
  echo "[lzc-build] package version: ${LZC_VERSION} (tag/env injection; package.yml and binary match)"
else
  sed -i "s/^version: .*/version: 0.0.0/" "$ROOT/lazycat/package.yml"
  echo "[lzc-build] package version: restored the 0.0.0 placeholder (non-tag build)"
fi

BASE_REF="$(grep -E '^FROM ' "$HERE/Dockerfile" | tail -1 | awk '{print $NF}')"
echo "[lzc-build] 1/4 base image: ${BASE_REF}"
case "$BASE_REF" in
  registry.lazycat.cloud/*) ;;
  *)
    echo "[lzc-build] warning: FROM is not an official Lazycat registry reference; upstream matching will fail and the entire base layer will be embedded" >&2
    echo "[lzc-build]    fix: first run  lzc-cli appstore copy-image <base-image> --arch ${LZC_ARCH}" >&2
    echo "[lzc-build]    then change FROM to the registry.lazycat.cloud/... reference it prints." >&2
    echo "[lzc-build]    (not fatal: the LPK can still be built and installed, but embedding the full base layer significantly increases its size.)" >&2
    ;;
esac

echo "[lzc-build] 2/4 building the frontend (node scripts/build.mjs frontend -> dist/)"
cd "$ROOT"

if [ -n "${NEXTERM_FORCE_INSTALL:-}" ] || [ ! -d node_modules ]; then
  if [ -f pnpm-lock.yaml ]; then
    pnpm install --frozen-lockfile
  else
    pnpm install
  fi
else
  echo "[lzc-build]     node_modules exists; skipping pnpm install (force reinstall: NEXTERM_FORCE_INSTALL=1)"
fi

node scripts/build.mjs frontend

rm -rf "$CONTENT/web"
mkdir -p "$CONTENT"
cp -R "$ROOT/dist" "$CONTENT/web"
echo "[lzc-build]     frontend ready: $CONTENT/web"

rm -rf "$CONTENT/lazycat-injects"
mkdir -p "$CONTENT/lazycat-injects"
cp "$HERE"/../injects/*.js "$CONTENT/lazycat-injects/"
echo "[lzc-build]     inject scripts ready: $CONTENT/lazycat-injects"

echo "[lzc-build] 3/4 building the server binary (linux/${LZC_ARCH}, node scripts/build.mjs server; this may take several minutes)"
node scripts/build.mjs server --release --os=linux --arch="$LZC_ARCH"

GO_BIN="$ROOT/target/go-build/nexterm-server-linux-${LZC_ARCH}"
install -m 0755 "$GO_BIN" "$OUT_BIN"
echo "[lzc-build]     binary ready: $OUT_BIN"

echo "[lzc-build] 4/4 self-check"
FILE_OUT="$(file -b "$OUT_BIN" || true)"
case "$FILE_OUT" in
  *ELF*"$ELF_ARCH"*) ;;
  *)
    echo "[lzc-build] error: output is not a Linux/${ELF_ARCH} ELF: $FILE_OUT" >&2
    echo "[lzc-build]   target architecture is LZC_ARCH=${LZC_ARCH}." >&2
    exit 1
    ;;
esac
case "$FILE_OUT" in
  *statically\ linked*) ;;
  *)
    echo "[lzc-build] error: output is not statically linked: $FILE_OUT" >&2
    echo "[lzc-build]   the server must use CGO_ENABLED=0 (already enforced by the shared delivery entry point; this should not happen)." >&2
    exit 1
    ;;
esac
if command -v go >/dev/null 2>&1; then
  if ! go version -m "$OUT_BIN" | grep -q 'CGO_ENABLED=0'; then
    echo "[lzc-build] error: go version -m does not contain the CGO_ENABLED=0 marker" >&2
    exit 1
  fi
fi
if [ ! -f "$CONTENT/web/index.html" ]; then
  echo "[lzc-build] error: $CONTENT/web/index.html does not exist: did the frontend build fail?" >&2
  exit 1
fi

echo "[lzc-build] done. Next: lzc-cli project build"
