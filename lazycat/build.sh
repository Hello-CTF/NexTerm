#!/usr/bin/env bash
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
CALLER_CWD="$PWD"
CLI_ARGS=()

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  cat <<'EOF'
Usage: bash lazycat/build.sh [-o FILE|--output FILE]

Build the frontend, linux/amd64 server and nine SSH target helpers, then
package from a temporary directory. The tracked package.yml remains at 0.0.0.
NEXTERM_RELEASE_VERSION overrides version resolution; otherwise an exact git
tag is used, with 0.0.0 as the non-tag fallback. A relative output path is
resolved against the caller's working directory.
EOF
  exit 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    -o|--output)
      [ $# -ge 2 ] || { echo "[lzc-build] error: $1 requires a file path" >&2; exit 2; }
      OUTPUT="$2"
      shift 2
      ;;
    -o=*|--output=*)
      OUTPUT="${1#*=}"
      shift
      ;;
    *)
      echo "[lzc-build] error: unsupported argument: $1 (only -o/--output is accepted)" >&2
      exit 2
      ;;
  esac
  [ -n "$OUTPUT" ] || { echo "[lzc-build] error: output path must not be empty" >&2; exit 2; }
  case "$OUTPUT" in
    /*) ;;
    *) OUTPUT="$CALLER_CWD/$OUTPUT" ;;
  esac
  CLI_ARGS+=(--output "$OUTPUT")
done

if ! command -v lzc-cli >/dev/null 2>&1; then
  echo "[lzc-build] error: lzc-cli is required to assemble the package" >&2
  exit 1
fi

if [ -n "${NEXTERM_RELEASE_VERSION:-}" ] && [ -n "${LZC_VERSION:-}" ] && [ "$NEXTERM_RELEASE_VERSION" != "$LZC_VERSION" ]; then
  echo "[lzc-build] error: NEXTERM_RELEASE_VERSION and LZC_VERSION disagree" >&2
  exit 1
fi

LZC_VERSION="${NEXTERM_RELEASE_VERSION:-${LZC_VERSION:-}}"
if [ -z "$LZC_VERSION" ]; then
  TAG="$(git -C "$ROOT" describe --exact-match --tags HEAD 2>/dev/null || true)"
  LZC_VERSION="${TAG#v}"
fi
LZC_VERSION="${LZC_VERSION:-0.0.0}"
export LZC_VERSION
if [ "$LZC_VERSION" = "0.0.0" ]; then
  unset NEXTERM_RELEASE_VERSION
else
  export NEXTERM_RELEASE_VERSION="$LZC_VERSION"
fi

bash "$HERE/image/build-server.sh"

mkdir -p "$ROOT/.tmp"
STAGE="$(mktemp -d "$ROOT/.tmp/lazycat-package.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT

cp "$HERE/package.yml" "$STAGE/package.yml"
cp "$HERE/lzc-manifest.yml" "$STAGE/lzc-manifest.yml"
cp "$HERE/lzc-build.yml" "$STAGE/lzc-build.yml"
cp "$HERE/lzc-icon.png" "$STAGE/lzc-icon.png"
cp -R "$HERE/content" "$STAGE/content"
cp -R "$HERE/image-context" "$STAGE/image-context"

node - "$STAGE" "$LZC_VERSION" "$HERE" <<'NODE'
const fs = require("node:fs");
const path = require("node:path");

const [stage, version, output] = process.argv.slice(2);
function replaceOnce(file, pattern, replacement) {
  const source = fs.readFileSync(file, "utf8");
  const matches = source.match(new RegExp(pattern.source, "gm")) || [];
  if (matches.length !== 1) {
    throw new Error(`${file}: expected one ${pattern}, found ${matches.length}`);
  }
  fs.writeFileSync(file, source.replace(pattern, replacement));
}

replaceOnce(path.join(stage, "package.yml"), /^version:.*$/m, `version: ${JSON.stringify(version)}`);
replaceOnce(path.join(stage, "lzc-build.yml"), /^buildscript:.*$/m, 'buildscript: ""');
replaceOnce(path.join(stage, "lzc-build.yml"), /^pkgout:.*$/m, `pkgout: ${JSON.stringify(output)}`);
NODE

echo "[lzc-build] package version: ${LZC_VERSION} (temporary copy only)"
(
  cd "$STAGE"
  lzc-cli project build "${CLI_ARGS[@]}"
)
