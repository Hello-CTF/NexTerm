#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/target/release-assets"
BIN=""
WEB="$ROOT/dist"
ARCH="amd64"
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
    --require-evidence) REQUIRE_EVIDENCE=1; shift ;;
    -h|--help)
      cat <<'PACK_HELP_EOF'
Usage: scripts/pack-linux-server.sh [options]

Package one full CGO_ENABLED=0 Go server tar.gz with both runtime units.
scripts/build.mjs owns compilation/versioning; e2e-sync-local.py retains
--sync-only acceptance.

Options:
  --out DIR           output directory (default target/release-assets)
  --bin PATH          server binary (default target/go-build/nexterm-server-linux-<arch>)
  --web DIR           frontend dist directory (default dist)
  --arch ARCH         Linux server architecture: amd64 or arm64 (default amd64)
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
  echo "server --version ($BINARY_VERSION) does not match release version ($VERSION)" >&2; exit 1;
}

for unit in nexterm-server.service nexterm-onlyserver.service; do
  [ -f "$ROOT/deploy/systemd/$unit" ] || { echo "missing systemd unit: deploy/systemd/$unit" >&2; exit 1; }
done
[ -f "$ROOT/LICENSE" ] || { echo "missing LICENSE" >&2; exit 1; }

mkdir -p "$OUT"
rm -f \
  "$OUT/NexTerm-server_${VERSION}_linux_${ARCH}.tar.gz" \
  "$OUT/NexTerm-server_${VERSION}_linux_${ARCH}.tar.gz.artifact.json"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if tar --version 2>/dev/null | grep -qi 'gnu tar'; then
  TAR_OWNER=(--owner=0 --group=0 --numeric-owner)
else
  TAR_OWNER=(--uid 0 --gid 0)
fi
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct HEAD)}"

tar_cz() {
  tar ${TAR_OWNER[@]+"${TAR_OWNER[@]}"} --sort=name --mtime="@$SOURCE_DATE_EPOCH" -czf "$1" -C "$2" "$3"
}

write_env() {
  local target="$1" env_name="$2"
  cat > "$target" <<ENVEOF
# Copy to /etc/nexterm/$env_name and chmod 0600.
# The vault root key lives in its own 0600 file owned by the service user:
#   install -m 0600 -o nexterm -g nexterm /dev/null /etc/nexterm/master.key
#   printf '%s' '<root-key>' > /etc/nexterm/master.key
NEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key
ENVEOF
}

write_readme() {
  local target="$1"
  cat > "$target" <<MDEOF
# NexTerm Linux Server $VERSION (linux/$ARCH)

This is the only Linux server archive. It contains the stripped, static Go
server, the real Vite frontend, both systemd units and both environment-file
examples. Its version comes from the release tag (NEXTERM_RELEASE_VERSION).

## Choose one runtime (do not enable both units)

- Full browser server: install nexterm-server.service and use nexterm.env.
  It serves the browser UI with a built-in init/login page: on first start
  with no account, the server prints a one-time init code to the console
  (journalctl -u nexterm-server under systemd); open the UI and finish
  superadmin setup. /rpc, /ws and /files/blob always require an account
  session with --auth on. Public deployments still belong behind a
  TLS-terminating reverse proxy.
- Restricted runtime: install nexterm-onlyserver.service instead and use
  onlyserver.env. The same binary runs with --sync-only and mounts only the
  account, admin and sync routes (/auth/*, /admin/*, /sync/v2/*) plus
  /healthz; there is no browser UI and /rpc returns 404. This is an optional
  runtime in this full archive, not a separate onlyServer package. Public
  deployments still require TLS.

Install nexterm-server as /opt/nexterm/nexterm-server. The env examples point
NEXTERM_MASTER_KEY_FILE at /etc/nexterm/master.key; create that root-key file
owned by the nexterm service user with chmod 0600, keep it secret and back it
up with /var/lib/nexterm.

## Verify

- Full: curl http://127.0.0.1:8080/healthz and check syncOnly=false.
- Restricted: check syncOnly=true and commands=0; /rpc must return 404.
- Account sign-in: POST /auth/login with username and password returns a
  session cookie. Desktop clients sign in under Settings -> Account Sync with
  server address + username + password.

LazyCat package assembly and real LazyCat/box acceptance are separate external
steps. This archive is an input contract, not evidence that those external
targets have passed.
MDEOF
}

package_full() {
  local name="NexTerm-server_${VERSION}_linux_${ARCH}"
  local dir="$STAGE/$name"
  local unit env_file member
  mkdir -p "$dir"
  install -m 0755 "$BIN" "$dir/nexterm-server"
  for unit in nexterm-server.service nexterm-onlyserver.service; do
    install -m 0644 "$ROOT/deploy/systemd/$unit" "$dir/$unit"
  done
  for env_file in nexterm.env onlyserver.env; do
    write_env "$dir/$env_file.example" "$env_file"
  done
  install -m 0644 "$ROOT/LICENSE" "$dir/LICENSE"
  write_readme "$dir/README.md"
  mkdir -p "$dir/web"
  (cd "$WEB" && find . -type f -print0 | sort -z | while IFS= read -r -d '' file; do
    install -D -m 0644 "$file" "$dir/web/$file"
  done)
  tar_cz "$OUT/$name.tar.gz" "$STAGE" "$name"

  local archive_members
  archive_members="$(tar -tzf "$OUT/$name.tar.gz")"
  for member in \
    nexterm-server \
    nexterm-server.service \
    nexterm-onlyserver.service \
    nexterm.env.example \
    onlyserver.env.example \
    LICENSE \
    README.md \
    web/index.html; do
    grep -q "^$name/$member$" <<<"$archive_members"
  done
}

ARTIFACT="$OUT/NexTerm-server_${VERSION}_linux_${ARCH}.tar.gz"
(cd "$ROOT" && package_full)

report_args=(--kind=server-archive --os=linux "--arch=$ARCH" "--file=$ARTIFACT")
if [ "$REQUIRE_EVIDENCE" -ne 0 ]; then
  report_args+=(--require-evidence)
fi
(cd "$ROOT" && node scripts/build.mjs report "${report_args[@]}")

if command -v sha256sum >/dev/null 2>&1; then digest="$(sha256sum "$ARTIFACT" | cut -d' ' -f1)"; else digest="$(shasum -a 256 "$ARTIFACT" | cut -d' ' -f1)"; fi
printf 'Linux %s full server archive: %s  %s bytes  %s\n' "$ARCH" "$(basename "$ARTIFACT")" "$(stat -c %s "$ARTIFACT" 2>/dev/null || stat -f %z "$ARTIFACT")" "$digest"
