#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
CONTENT="$ROOT/lazycat/content"
IMAGE_CONTEXT="$ROOT/lazycat/image-context"

LZC_ARCH="${LZC_ARCH:-amd64}"
case "$LZC_ARCH" in
  amd64) ELF_ARCH="x86-64" ;;
  arm64)
    echo "[lzc-build] error: LZC_ARCH=arm64 is unsupported: this build configures only a linux/amd64 base image" >&2
    echo "[lzc-build]   use LZC_ARCH=amd64, or configure and verify a linux/arm64 base image before enabling arm64" >&2
    exit 1
    ;;
  *)
    echo "[lzc-build] error: LZC_ARCH must be amd64, got: $LZC_ARCH" >&2
    exit 1
    ;;
esac

if [ -n "${NEXTERM_RELEASE_VERSION:-}" ] && [ "${LZC_VERSION:-}" != "$NEXTERM_RELEASE_VERSION" ]; then
  echo "[lzc-build] error: LZC_VERSION and NEXTERM_RELEASE_VERSION must match; use bash lazycat/build.sh to stamp the generated package safely" >&2
  exit 1
fi
LZC_VERSION="${LZC_VERSION:-0.0.0}"
export LZC_VERSION
if [ "$LZC_VERSION" = "0.0.0" ]; then
  unset NEXTERM_RELEASE_VERSION
else
  export NEXTERM_RELEASE_VERSION="$LZC_VERSION"
fi

echo "[lzc-build] repository root: $ROOT"
echo "[lzc-build] target architecture: ${LZC_ARCH} (linux/${LZC_ARCH}, Go cross-compilation)"
echo "[lzc-build] binary version: ${LZC_VERSION}"

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

rm -rf "$CONTENT/web" "$CONTENT/image"
mkdir -p "$CONTENT"
cp -R "$ROOT/dist" "$CONTENT/web"
echo "[lzc-build]     frontend ready: $CONTENT/web"

rm -rf "$CONTENT/lazycat-injects"
mkdir -p "$CONTENT/lazycat-injects"
cp "$HERE"/../injects/*.js "$CONTENT/lazycat-injects/"
echo "[lzc-build]     inject scripts ready: $CONTENT/lazycat-injects"

echo "[lzc-build] 3/4 building the server and nine SSH target helpers"
node scripts/build.mjs server --release --os=linux --arch="$LZC_ARCH"
HELPERS_DIR="${NEXTERM_HELPERS_DIR:-$ROOT/target/go-build}"
if [ -n "${NEXTERM_HELPERS_DIR:-}" ]; then
  echo "[lzc-build]     using matching-host helper evidence from: $HELPERS_DIR"
else
  node scripts/build.mjs helpers --release
fi
[ -d "$HELPERS_DIR" ] || { echo "[lzc-build] error: helpers directory does not exist: $HELPERS_DIR" >&2; exit 1; }
rm -rf "$IMAGE_CONTEXT"
mkdir -p "$IMAGE_CONTEXT"
install -m 0755 "$ROOT/target/go-build/nexterm-server-linux-${LZC_ARCH}" "$IMAGE_CONTEXT/nexterm-server"
while IFS='|' read -r HELPER_OS HELPER_ARCH HELPER_NAME; do
  HELPER="$HELPERS_DIR/$HELPER_NAME"
  node .github/scripts/verify-reused-binary.mjs "$HELPER" "helper-$HELPER_OS-$HELPER_ARCH" "$HELPER_OS" "$HELPER_ARCH" 0
  install -m 0755 "$HELPER" "$IMAGE_CONTEXT/$HELPER_NAME"
done < <(node .github/scripts/helper-matrix.mjs --shell)
install -m 0644 "$HERE/Dockerfile" "$IMAGE_CONTEXT/Dockerfile"
install -m 0755 "$HERE/entrypoint.sh" "$IMAGE_CONTEXT/entrypoint.sh"
echo "[lzc-build]     image context ready: $IMAGE_CONTEXT"

check_static_elf() {
  local binary="$1" expected_arch="$2" file_out
  file_out="$(file -b "$binary" || true)"
  case "$file_out" in
    *ELF*"$expected_arch"*) ;;
    *)
      echo "[lzc-build] error: $binary is not a Linux/${expected_arch} ELF: $file_out" >&2
      exit 1
      ;;
  esac
  case "$file_out" in
    *statically\ linked*) ;;
    *)
      echo "[lzc-build] error: $binary is not statically linked: $file_out" >&2
      exit 1
      ;;
  esac
  if command -v go >/dev/null 2>&1 && ! go version -m "$binary" | grep -q 'CGO_ENABLED=0'; then
    echo "[lzc-build] error: go version -m $binary does not contain the CGO_ENABLED=0 marker" >&2
    exit 1
  fi
}

echo "[lzc-build] 4/4 self-check"
check_static_elf "$IMAGE_CONTEXT/nexterm-server" "$ELF_ARCH"
while IFS='|' read -r HELPER_OS HELPER_ARCH HELPER_NAME; do
  [ -x "$IMAGE_CONTEXT/$HELPER_NAME" ] || { echo "[lzc-build] error: helper is not executable: $HELPER_NAME" >&2; exit 1; }
  if [ "$HELPER_OS" = "linux" ]; then
    case "$HELPER_ARCH" in
      amd64) HELPER_ELF_ARCH="x86-64" ;;
      arm64) HELPER_ELF_ARCH="aarch64" ;;
      armv7) HELPER_ELF_ARCH="ARM" ;;
      riscv64) HELPER_ELF_ARCH="RISC-V" ;;
      loong64) HELPER_ELF_ARCH="LoongArch" ;;
    esac
    check_static_elf "$IMAGE_CONTEXT/$HELPER_NAME" "$HELPER_ELF_ARCH"
  fi
done < <(node .github/scripts/helper-matrix.mjs --shell)
if [ ! -f "$CONTENT/web/index.html" ]; then
  echo "[lzc-build] error: $CONTENT/web/index.html does not exist: did the frontend build fail?" >&2
  exit 1
fi

echo "[lzc-build] binaries and content ready"
