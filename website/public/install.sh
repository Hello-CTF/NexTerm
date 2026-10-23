#!/usr/bin/env bash
# NexTerm server one-line installer: downloads the Linux server archive from a
# GitHub release, verifies it against the release SHA256SUMS, then runs the
# install.sh shipped inside the archive (first install or upgrade).
set -euo pipefail

REPO="Hello-CTF/NexTerm"
RELEASE_BASE="${NEXTERM_RELEASE_BASE:-https://github.com/$REPO/releases}"
VERSION=""
FORWARD=()

usage() {
  cat <<'USAGE_EOF'
Usage: curl -fsSL https://nexterm.work/install.sh | sudo bash -s -- [options]

Options:
  --version X.Y.Z   install this release instead of the latest one
  -h, --help        show this help

Any other option is passed to the archive's install.sh, for example
--no-enable or --rollback.

Environment:
  NEXTERM_RELEASE_BASE    release URL prefix for mirrors
                          (default https://github.com/Hello-CTF/NexTerm/releases)
  NEXTERM_ADMIN_PASSWORD  initial super-admin password on first install
  NEXTERM_ADMIN_USERNAME  super-admin name on first install (default admin)
USAGE_EOF
}

fail() { echo "nexterm-install: error: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || fail "--version needs a value"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#*=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) FORWARD+=("$1"); shift ;;
  esac
done

[ "$(uname -s)" = "Linux" ] || fail "the NexTerm server installer supports Linux with systemd only"
[ "$(id -u)" = "0" ] || fail "run as root, e.g. curl -fsSL https://nexterm.work/install.sh | sudo bash"
for tool in curl tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || fail "missing required command: $tool"
done

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) fail "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
esac

if [ -z "$VERSION" ]; then
  latest="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$RELEASE_BASE/latest")" ||
    fail "cannot resolve the latest release from $RELEASE_BASE/latest"
  VERSION="${latest##*/}"
fi
VERSION="${VERSION#v}"
case "$VERSION" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) fail "invalid version: $VERSION" ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

curl -fsSL --retry 3 -o "$WORK/SHA256SUMS" "$RELEASE_BASE/download/v$VERSION/SHA256SUMS" ||
  fail "download failed: SHA256SUMS for v$VERSION"
ROOT_NAME="nexterm-server_linux_${ARCH}_${VERSION}"
ASSET="$ROOT_NAME.tar.gz"

echo "Downloading NexTerm server $VERSION ($ARCH)..."
curl -fL --progress-bar --retry 3 -o "$WORK/$ASSET" "$RELEASE_BASE/download/v$VERSION/$ASSET" ||
  fail "download failed: $RELEASE_BASE/download/v$VERSION/$ASSET"

expected="$(awk -v name="$ASSET" '$2 == name || $2 == "*" name { print $1; exit }' "$WORK/SHA256SUMS")"
[ -n "$expected" ] || fail "$ASSET is not listed in SHA256SUMS"
actual="$(sha256sum "$WORK/$ASSET" | awk '{ print $1 }')"
[ "$expected" = "$actual" ] || fail "checksum mismatch for $ASSET"
echo "Checksum verified."

tar -xzf "$WORK/$ASSET" -C "$WORK"
PAYLOAD="$WORK/$ROOT_NAME"
[ -x "$PAYLOAD/install.sh" ] || fail "install.sh missing from $ASSET"

"$PAYLOAD/install.sh" --source "$PAYLOAD" ${FORWARD[@]+"${FORWARD[@]}"}
