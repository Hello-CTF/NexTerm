#!/usr/bin/env bash
set -euo pipefail

SOURCE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_PREFIX="/"
ROLLBACK=0
ENABLE=1

usage() {
  cat <<'INSTALL_HELP_EOF'
Usage: install.sh [options]

Install or upgrade the NexTerm server from an extracted release archive.

First install creates the nexterm system user, /opt/nexterm, /etc/nexterm
and /var/lib/nexterm, installs binaries, web assets, units and environment
files with safe defaults (loopback listen, auth on), generates
/etc/nexterm/master.key, then enables and starts nexterm-server.

An upgrade replaces binaries, web assets and units, but preserves
/etc/nexterm/nexterm.env, /etc/nexterm/onlyserver.env and
/etc/nexterm/master.key, backfills missing environment keys with the safe
defaults from the shipped examples, migrates legacy units (inline
ExecStart flags, copies under /usr/lib/systemd/system or
/lib/systemd/system) to the canonical env-driven units, saves the previous
payload to /opt/nexterm/.rollback, and restarts only units that were
active.

Options:
  --source DIR   release payload directory (default: this script's directory)
  --root DIR     filesystem prefix for a staged install (default: /)
  --rollback     restore the payload saved by the previous upgrade
  --no-enable    do not enable and start nexterm-server on first install
  -h, --help     show this help

Test hooks (see scripts/test-systemd-install.py):
  NEXTERM_INSTALL_SYSTEMCTL   systemctl executable to call
  NEXTERM_INSTALL_FAIL_AT     abort at the named step
INSTALL_HELP_EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --source) SOURCE_DIR="$2"; shift 2 ;;
    --source=*) SOURCE_DIR="${1#*=}"; shift ;;
    --root) ROOT_PREFIX="$2"; shift 2 ;;
    --root=*) ROOT_PREFIX="${1#*=}"; shift ;;
    --rollback) ROLLBACK=1; shift ;;
    --no-enable) ENABLE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -d "$SOURCE_DIR" ] || { echo "install: error: payload directory not found: $SOURCE_DIR" >&2; exit 1; }
SOURCE_DIR="$(cd "$SOURCE_DIR" && pwd)"
case "$ROOT_PREFIX" in /|/*) ;; *) echo "install: error: --root must be an absolute path: $ROOT_PREFIX" >&2; exit 2 ;; esac
[ "$ROOT_PREFIX" = "/" ] || ROOT_PREFIX="${ROOT_PREFIX%/}"

OPT_DIR="$ROOT_PREFIX/opt/nexterm"
ETC_DIR="$ROOT_PREFIX/etc/nexterm"
DATA_DIR="$ROOT_PREFIX/var/lib/nexterm"
UNIT_DIR="$ROOT_PREFIX/etc/systemd/system"
BACKUP_DIR="$OPT_DIR/.rollback"
FAILED_DIR="$OPT_DIR/.rollback.failed"
SERVER_UNIT="nexterm-server.service"
ONLYSERVER_UNIT="nexterm-onlyserver.service"
UNITS="$SERVER_UNIT $ONLYSERVER_UNIT"
BINARIES="nexterm-server nexterm-helper-linux-amd64 nexterm-helper-linux-arm64"
ENV_KEYS="NEXTERM_LISTEN NEXTERM_AUTH NEXTERM_DATA_DIR NEXTERM_WEB_ROOT NEXTERM_MASTER_KEY_FILE NEXTERM_DB"
REAL_ROOT=0
[ "$ROOT_PREFIX" = "/" ] && REAL_ROOT=1
SYSTEMCTL="${NEXTERM_INSTALL_SYSTEMCTL:-systemctl}"
MODE="first"
ACTIVE_UNITS=""

log() { printf 'install: %s\n' "$*"; }
die() { printf 'install: error: %s\n' "$*" >&2; exit 1; }
fail_at() {
  if [ "${NEXTERM_INSTALL_FAIL_AT:-}" = "$1" ]; then
    die "injected failure at step: $1"
  fi
}

require_systemctl() {
  command -v "$SYSTEMCTL" >/dev/null 2>&1 || die "systemctl not found (set NEXTERM_INSTALL_SYSTEMCTL or run on a systemd host)"
}

require_root_when_real() {
  if [ "$REAL_ROOT" -eq 1 ] && [ "$(id -u)" -ne 0 ]; then
    die "must run as root for a system install (use --root DIR for a staged install)"
  fi
}

unit_active() { "$SYSTEMCTL" is-active --quiet "$1" >/dev/null 2>&1; }

capture_active_units() {
  ACTIVE_UNITS=""
  local unit
  for unit in $UNITS; do
    if unit_active "$unit"; then ACTIVE_UNITS="$ACTIVE_UNITS $unit"; fi
  done
}

is_legacy_unit() {
  grep -qE '^EnvironmentFile=-' "$1" ||
    grep -qE '^[[:space:]]*--(listen|auth|data-dir|web-root)([[:space:]=]|$)' "$1"
}

ensure_user() {
  fail_at user
  if [ "$REAL_ROOT" -ne 1 ]; then
    log "skipping service user creation (staged root $ROOT_PREFIX)"
    return 0
  fi
  if id -u nexterm >/dev/null 2>&1; then return 0; fi
  useradd --system --user-group --home-dir /var/lib/nexterm --shell /usr/sbin/nologin nexterm
  log "created system user nexterm"
}

ensure_dirs() {
  fail_at dirs
  install -d -m 0755 "$OPT_DIR" "$ETC_DIR" "$UNIT_DIR"
  if [ "$REAL_ROOT" -eq 1 ]; then
    install -d -m 0700 -o nexterm -g nexterm "$DATA_DIR"
  else
    install -d -m 0700 "$DATA_DIR"
  fi
}

backup_current() {
  fail_at backup
  rm -rf "$BACKUP_DIR"
  install -d -m 0700 "$BACKUP_DIR"
  local f unit d rel
  for f in $BINARIES; do
    if [ -f "$OPT_DIR/$f" ]; then cp -p "$OPT_DIR/$f" "$BACKUP_DIR/$f"; fi
  done
  if [ -d "$OPT_DIR/web" ]; then cp -R "$OPT_DIR/web" "$BACKUP_DIR/web"; fi
  for unit in $UNITS; do
    for d in "$UNIT_DIR" "$ROOT_PREFIX/usr/lib/systemd/system" "$ROOT_PREFIX/lib/systemd/system"; do
      if [ -f "$d/$unit" ]; then
        rel="${d#$ROOT_PREFIX}/$unit"
        mkdir -p "$BACKUP_DIR/units$(dirname "$rel")"
        cp -p "$d/$unit" "$BACKUP_DIR/units/$rel"
      fi
    done
  done
  : > "$BACKUP_DIR/active-units"
  for unit in $ACTIVE_UNITS; do
    printf '%s\n' "$unit" >> "$BACKUP_DIR/active-units"
  done
  log "saved previous payload to $BACKUP_DIR"
}

install_binaries() {
  fail_at binaries
  local f
  for f in $BINARIES; do
    install -m 0755 "$SOURCE_DIR/$f" "$OPT_DIR/.$f.new"
    mv -f "$OPT_DIR/.$f.new" "$OPT_DIR/$f"
  done
}

install_web() {
  fail_at web
  rm -rf "$OPT_DIR/web.new"
  cp -R "$SOURCE_DIR/web" "$OPT_DIR/web.new"
  rm -rf "$OPT_DIR/web.old"
  if [ -d "$OPT_DIR/web" ]; then mv "$OPT_DIR/web" "$OPT_DIR/web.old"; fi
  mv "$OPT_DIR/web.new" "$OPT_DIR/web"
  rm -rf "$OPT_DIR/web.old"
}

install_units() {
  fail_at units
  local unit d
  for unit in $UNITS; do
    if [ -f "$UNIT_DIR/$unit" ] && is_legacy_unit "$UNIT_DIR/$unit"; then
      log "migrating legacy unit $unit (inline ExecStart flags move to the $ETC_DIR env files)"
    fi
    install -m 0644 "$SOURCE_DIR/$unit" "$UNIT_DIR/$unit"
    for d in "$ROOT_PREFIX/usr/lib/systemd/system" "$ROOT_PREFIX/lib/systemd/system"; do
      if [ -f "$d/$unit" ]; then
        rm -f "$d/$unit"
        log "removed legacy unit copy ${d#$ROOT_PREFIX}/$unit (canonical location is ${UNIT_DIR#$ROOT_PREFIX})"
      fi
    done
  done
}

ensure_env_files() {
  fail_at env
  local name env_file example key line
  for name in nexterm onlyserver; do
    env_file="$ETC_DIR/$name.env"
    example="$SOURCE_DIR/$name.env.example"
    if [ -f "$env_file" ]; then
      log "preserving existing ${env_file#$ROOT_PREFIX}"
    else
      install -m 0600 "$example" "$env_file"
      log "installed ${env_file#$ROOT_PREFIX} with safe defaults"
    fi
    for key in $ENV_KEYS; do
      line="$(grep -m1 "^$key=" "$example" || true)"
      [ -n "$line" ] || continue
      if ! grep -q "^$key=" "$env_file"; then
        printf '%s\n' "$line" >> "$env_file"
        log "backfilled $key in ${env_file#$ROOT_PREFIX} (was absent; applied the example default)"
      fi
    done
  done
}

ensure_master_key() {
  fail_at masterkey
  local key_file="$ETC_DIR/master.key"
  if [ -f "$key_file" ]; then
    log "preserving existing ${key_file#$ROOT_PREFIX}"
    return 0
  fi
  command -v openssl >/dev/null 2>&1 || die "openssl is required to generate ${key_file#$ROOT_PREFIX}"
  (umask 077; openssl rand -hex 32 > "$key_file")
  if [ "$REAL_ROOT" -eq 1 ]; then
    chown nexterm:nexterm "$key_file"
  fi
  chmod 0600 "$key_file"
  if [ "$MODE" = "upgrade" ]; then
    log "WARNING: ${key_file#$ROOT_PREFIX} was missing and has been regenerated; credentials encrypted under an older key cannot be recovered"
  fi
  log "generated ${key_file#$ROOT_PREFIX} (back it up with ${DATA_DIR#$ROOT_PREFIX}; it cannot be recreated)"
}

reload_systemd() {
  fail_at daemon-reload
  "$SYSTEMCTL" daemon-reload
}

manage_services() {
  fail_at services
  if [ "$MODE" = "first" ]; then
    if [ "$ENABLE" -eq 1 ]; then
      "$SYSTEMCTL" enable --now "$SERVER_UNIT"
      log "enabled and started $SERVER_UNIT"
    else
      log "skipped enabling $SERVER_UNIT (--no-enable)"
    fi
    return 0
  fi
  local unit restarted=0
  for unit in $ACTIVE_UNITS; do
    "$SYSTEMCTL" restart "$unit"
    restarted=1
    log "restarted $unit"
  done
  if [ "$restarted" -ne 1 ]; then
    log "no unit was active before this run; start one with: systemctl enable --now $SERVER_UNIT"
  fi
}

run_install() {
  fail_at preflight
  local f
  for f in $BINARIES "$SERVER_UNIT" "$ONLYSERVER_UNIT" nexterm.env.example onlyserver.env.example; do
    [ -f "$SOURCE_DIR/$f" ] || die "payload is missing $f (run this script from an extracted NexTerm server archive, or pass --source DIR)"
  done
  [ -f "$SOURCE_DIR/web/index.html" ] || die "payload is missing web/index.html"
  require_systemctl
  require_root_when_real

  MODE=first
  if [ -f "$OPT_DIR/nexterm-server" ] || [ -f "$ETC_DIR/nexterm.env" ] || [ -f "$ETC_DIR/onlyserver.env" ] ||
     [ -f "$UNIT_DIR/$SERVER_UNIT" ] || [ -f "$UNIT_DIR/$ONLYSERVER_UNIT" ]; then
    MODE=upgrade
  fi
  log "mode: $MODE install"

  ensure_user
  ensure_dirs
  if [ "$MODE" = "upgrade" ]; then
    capture_active_units
    backup_current
  else
    ACTIVE_UNITS=""
  fi
  install_binaries
  install_web
  install_units
  ensure_env_files
  ensure_master_key
  reload_systemd
  manage_services
  log "done"
}

run_rollback() {
  fail_at preflight
  [ -d "$BACKUP_DIR" ] || die "no rollback backup found at ${BACKUP_DIR#$ROOT_PREFIX}"
  [ -f "$BACKUP_DIR/nexterm-server" ] || die "rollback backup at ${BACKUP_DIR#$ROOT_PREFIX} is incomplete"
  require_systemctl
  require_root_when_real

  MODE=upgrade
  capture_active_units
  fail_at rollback
  install -d -m 0755 "$OPT_DIR"
  rm -rf "$FAILED_DIR"
  install -d -m 0700 "$FAILED_DIR"
  local f rel unit
  for f in $BINARIES; do
    if [ -f "$OPT_DIR/$f" ]; then mv "$OPT_DIR/$f" "$FAILED_DIR/$f"; fi
  done
  if [ -d "$OPT_DIR/web" ]; then mv "$OPT_DIR/web" "$FAILED_DIR/web"; fi
  for unit in $UNITS; do
    for rel in "etc/systemd/system" "usr/lib/systemd/system" "lib/systemd/system"; do
      if [ -f "$ROOT_PREFIX/$rel/$unit" ] && [ ! -f "$BACKUP_DIR/units/$rel/$unit" ]; then
        rm -f "$ROOT_PREFIX/$rel/$unit"
        log "removed $rel/$unit (not part of the previous payload)"
      fi
    done
  done
  for f in $BINARIES; do
    if [ -f "$BACKUP_DIR/$f" ]; then install -m 0755 "$BACKUP_DIR/$f" "$OPT_DIR/$f"; fi
  done
  if [ -d "$BACKUP_DIR/web" ]; then cp -R "$BACKUP_DIR/web" "$OPT_DIR/web"; fi
  if [ -d "$BACKUP_DIR/units" ]; then
    (cd "$BACKUP_DIR/units" && find . -type f | sed 's|^\./||' | while read -r rel; do
      mkdir -p "$ROOT_PREFIX/$(dirname "$rel")"
      install -m 0644 "$BACKUP_DIR/units/$rel" "$ROOT_PREFIX/$rel"
    done)
  fi
  reload_systemd
  manage_services
  rm -rf "$BACKUP_DIR"
  log "rollback complete; replaced payload kept in ${FAILED_DIR#$ROOT_PREFIX}"
  log "done"
}

if [ "$ROLLBACK" -eq 1 ]; then
  run_rollback
else
  run_install
fi
