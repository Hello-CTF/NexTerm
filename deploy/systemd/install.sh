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

Requires a Linux distribution with systemd; the platform is detected from
explicit evidence before anything is changed, and unsupported systems
(non-Linux kernels, non-systemd init systems) fail with a visible error.
macOS and Windows are not supported.

First install creates the nexterm system user, /opt/nexterm, /etc/nexterm
and /var/lib/nexterm, installs binaries, web assets, units and environment
files with safe defaults (loopback listen, auth on), generates
/etc/nexterm/master.key, creates the super-admin account through the
server's account initialization contract, then enables and starts
nexterm-server.

The super-admin bootstrap runs only on a first install to a real root and
prints the initial password to this console only after the account was
actually created. By default the password is cryptographically random and
must be changed at first login. Set NEXTERM_ADMIN_PASSWORD in the
installer's environment to inject a specific initial password instead (no
forced rotation), e.g. for LazyCat-style install systems;
NEXTERM_ADMIN_USERNAME overrides the default name "admin". The password
travels via the process environment only — never via argv, which is
world-readable through /proc/<pid>/cmdline — and is never written to
env files, disk or logs. Nothing account-related is printed on upgrades, existing
accounts or re-runs. The bootstrap runs as the nexterm service user, so
everything it creates is born with the right ownership and a data
directory the service cannot write fails closed; pre-existing files in a
custom data directory are never touched. The bootstrap environment is
scrubbed to a whitelist that mirrors the service's effective
configuration — the unit's HOME and every key from the env file,
including PG* values the file sets explicitly — so ambient variables
such as PGDATABASE or PGPASSWORD can never point it at a different
database or credential than the service uses. The bootstrap executes in
the unit's WorkingDirectory, so relative paths such as
NEXTERM_DB_PASSWORD_FILE resolve exactly like the service resolves
them. When the bootstrap is skipped
(staged --root installs, auth off/platform modes) finish setup with the
one-time code from the server log instead. If an init code was already
issued and cannot be proven usable, the first install keeps failing
(with recovery guidance) rather than starting an uninitialized service.

An upgrade replaces binaries, web assets and units, but preserves
/etc/nexterm/nexterm.env, /etc/nexterm/onlyserver.env and the effective
master key (an explicit NEXTERM_MASTER_KEY_FILE, an existing
<data-dir>/master.key, or /etc/nexterm/master.key, in that order; a new
key is generated only when none exists). Missing environment keys are
backfilled with the safe defaults from the shipped examples, explicit
legacy unit flags (--listen, --auth, --data-dir, --web-root,
--master-key-file) are migrated into the env files instead of being
discarded, legacy unit copies under /usr/lib/systemd/system or
/lib/systemd/system are removed, the previous payload is saved to
/opt/nexterm/.rollback, and only units that were active are restarted.
An interrupted upgrade keeps its original rollback backup, env-origin
record, active-unit list, and pending legacy migrations across retries;
an interrupted first install records the same env origin and resumes,
finishing with enable --now.

Options:
  --source DIR   release payload directory (default: this script's directory)
  --root DIR     filesystem prefix for a staged install (default: /)
  --rollback     restore the payload saved by the previous upgrade
  --no-enable    do not enable and start nexterm-server on first install
  -h, --help     show this help

Test hooks (see scripts/test-systemd-install.py):
  NEXTERM_INSTALL_SYSTEMCTL   systemctl executable to call
  NEXTERM_INSTALL_UNAME       uname executable to call
  NEXTERM_INSTALL_INIT_ADMIN  replacement for the server binary during the
                              super-admin bootstrap (staged-root tests)
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
BINARIES="nexterm-server nexterm-helper-linux-amd64 nexterm-helper-linux-arm64 nexterm-helper-linux-armv7 nexterm-helper-linux-riscv64 nexterm-helper-linux-loong64 nexterm-helper-darwin-amd64 nexterm-helper-darwin-arm64 nexterm-helper-windows-amd64.exe nexterm-helper-windows-arm64.exe"
ENV_KEYS="NEXTERM_LISTEN NEXTERM_AUTH NEXTERM_DATA_DIR NEXTERM_WEB_ROOT NEXTERM_MASTER_KEY_FILE NEXTERM_DB"
REAL_ROOT=0
[ "$ROOT_PREFIX" = "/" ] && REAL_ROOT=1
SYSTEMCTL="${NEXTERM_INSTALL_SYSTEMCTL:-systemctl}"
UNAME="${NEXTERM_INSTALL_UNAME:-uname}"
SUPPORTED_DISTRO_IDS="debian ubuntu raspbian rhel fedora centos rocky almalinux ol amzn arch manjaro sles opensuse-leap opensuse-tumbleweed"
MODE="first"
ACTIVE_UNITS=""
SERVER_LEGACY_VALUES=""
ONLYSERVER_LEGACY_VALUES=""
NEXTERM_ENV_PREEXISTED=0
ONLYSERVER_ENV_PREEXISTED=0
INCOMPLETE_MARKER="$OPT_DIR/.install-incomplete"
FIRST_INSTALL_STATE="$OPT_DIR/.install-transaction"
MIGRATED_KEY_FILE_NEXTERM=0
MIGRATED_KEY_FILE_ONLYSERVER=0
WORKING_DIR_NEXTERM="/var/lib/nexterm"
WORKING_DIR_ONLYSERVER="/var/lib/nexterm"

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

systemd_booted() {
  # explicit, filesystem-only evidence that systemd is the init system, so a
  # staged (--root) target is checked identically to the live host: the
  # sd_booted() probe (/run/systemd/system) or an init symlink to systemd
  [ -d "$ROOT_PREFIX/run/systemd/system" ] && return 0
  local link
  for link in sbin/init usr/sbin/init; do
    if [ -L "$ROOT_PREFIX/$link" ]; then
      case "$(readlink "$ROOT_PREFIX/$link")" in
        *systemd*) return 0 ;;
      esac
    fi
  done
  return 1
}

os_release_file() {
  if [ -f "$ROOT_PREFIX/etc/os-release" ]; then
    printf '%s\n' "$ROOT_PREFIX/etc/os-release"
  elif [ -f "$ROOT_PREFIX/usr/lib/os-release" ]; then
    printf '%s\n' "$ROOT_PREFIX/usr/lib/os-release"
  else
    return 1
  fi
}

os_release_get() {
  # print one KEY's value from an os-release file with matching quotes
  # stripped; the file is parsed, never sourced
  local file="$1" key="$2" line
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "$key"=*)
        line="${line#"$key"=}"
        case "$line" in
          '"'*'"') line="${line#\"}"; line="${line%\"}" ;;
          "'"*"'") line="${line#\'}"; line="${line%\'}" ;;
        esac
        printf '%s' "$line"
        return 0
        ;;
    esac
  done < "$file"
  return 1
}

distro_supported() {
  local token
  for token in $1 $2; do
    case " $SUPPORTED_DISTRO_IDS " in
      *" $token "*) return 0 ;;
    esac
  done
  return 1
}

detect_platform() {
  fail_at platform
  local kernel file id id_like pretty
  command -v "$UNAME" >/dev/null 2>&1 || die "uname not found (set NEXTERM_INSTALL_UNAME)"
  kernel="$("$UNAME" -s)"
  if [ "$kernel" != "Linux" ]; then
    die "unsupported operating system: $kernel (this installer requires a Linux distribution with systemd; macOS and Windows are not supported)"
  fi
  if ! systemd_booted; then
    die "systemd is required but was not detected (no /run/systemd/system and no systemd /sbin/init under $ROOT_PREFIX); this installer does not support non-systemd init systems"
  fi
  if file="$(os_release_file)"; then
    id="$(os_release_get "$file" ID || true)"
    id_like="$(os_release_get "$file" ID_LIKE || true)"
    pretty="$(os_release_get "$file" PRETTY_NAME || true)"
    if distro_supported "$id" "$id_like"; then
      log "platform: Linux, init: systemd, distribution: ${pretty:-$id} (supported)"
    else
      log "WARNING: distribution ${pretty:-${id:-unknown}} is not in the supported list ($SUPPORTED_DISTRO_IDS); continuing because systemd was detected"
    fi
  else
    log "WARNING: cannot identify the distribution (no /etc/os-release); continuing because systemd was detected"
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
    grep -qE '^[[:space:]]*--(listen|auth|data-dir|web-root|master-key-file)([[:space:]=]|$)' "$1"
}

legacy_exec_line() {
  local file="$1" line acc="" continuing=0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%"${line##*[![:blank:]]}"}"
    case "$line" in
      ExecStart=*) acc="${line#ExecStart=}"; continuing=1 ;;
      *) if [ "$continuing" -eq 1 ]; then acc="$acc $line"; fi ;;
    esac
    if [ "$continuing" -eq 1 ]; then
      case "$acc" in
        *\\) acc="${acc%\\}" ;;
        *) continuing=0 ;;
      esac
    fi
    if [ "$continuing" -eq 0 ] && [ -n "$acc" ]; then break; fi
  done < <(tr -d '\r' < "$file")
  printf '%s' "$acc"
}

utf8_encode() {
  local cp="$1"
  if [ "$cp" -lt 128 ]; then
    printf "\\$(printf '%03o' "$cp")"
  elif [ "$cp" -lt 2048 ]; then
    printf "\\$(printf '%03o' $((192 | (cp >> 6))))\\$(printf '%03o' $((128 | (cp & 63))))"
  elif [ "$cp" -lt 65536 ]; then
    printf "\\$(printf '%03o' $((224 | (cp >> 12))))\\$(printf '%03o' $((128 | ((cp >> 6) & 63))))\\$(printf '%03o' $((128 | (cp & 63))))"
  else
    printf "\\$(printf '%03o' $((240 | (cp >> 18))))\\$(printf '%03o' $((128 | ((cp >> 12) & 63))))\\$(printf '%03o' $((128 | ((cp >> 6) & 63))))\\$(printf '%03o' $((128 | (cp & 63))))"
  fi
}

codepoint_valid() {
  # mirrors systemd's unichar_is_valid: reject NUL, out-of-range, UTF-16
  # surrogates, the U+FDD0..U+FDEF noncharacters, and U+xFFFE/U+xFFFF
  local val="$1"
  [ "$val" -ne 0 ] &&
    [ "$val" -le 1114111 ] &&
    { [ "$val" -lt 55296 ] || [ "$val" -gt 57343 ]; } &&
    { [ "$val" -lt 64976 ] || [ "$val" -gt 65007 ]; } &&
    [ $((val & 65534)) -ne 65534 ]
}

systemd_split() {
  local input="$1" token="" state=plain return_state=plain c hex oct val present=0
  local i=0 len=${#input}
  while [ "$i" -lt "$len" ]; do
    c="${input:$i:1}"
    case "$state" in
      plain)
        case "$c" in
          ' '|$'\t')
            if [ "$present" -eq 1 ]; then printf '%s\n' "$token"; token=""; present=0; fi
            ;;
          "'") state=single; present=1 ;;
          '"') state=double; present=1 ;;
          \\) return_state=plain; state=escape; present=1 ;;
          *) token="$token$c"; present=1 ;;
        esac
        ;;
      single)
        case "$c" in
          "'") state=plain ;;
          \\) return_state=single; state=escape ;;
          *) token="$token$c" ;;
        esac
        ;;
      double)
        case "$c" in
          '"') state=plain ;;
          \\) return_state=double; state=escape ;;
          *) token="$token$c" ;;
        esac
        ;;
      escape)
        case "$c" in
          a) token="$token"$'\a'; state="$return_state" ;;
          b) token="$token"$'\b'; state="$return_state" ;;
          f) token="$token"$'\f'; state="$return_state" ;;
          n) return 3 ;;
          r) return 3 ;;
          t) token="$token"$'\t'; state="$return_state" ;;
          v) token="$token"$'\v'; state="$return_state" ;;
          \\) token="$token\\"; state="$return_state" ;;
          '"') token="$token\""; state="$return_state" ;;
          "'") token="$token'"; state="$return_state" ;;
          s) token="$token "; state="$return_state" ;;
          x)
            if [ $((i + 2)) -lt "$len" ]; then
              hex="${input:$((i + 1)):2}"
              case "$hex" in
                *[!0-9a-fA-F]*|'') token="$token\\x" ;;
                *)
                  val=$((16#$hex))
                  if [ "$val" -eq 10 ] || [ "$val" -eq 13 ]; then
                    return 3
                  elif [ "$val" -ne 0 ]; then
                    token="$token$(printf "\\$(printf '%03o' "$val")")"
                    i=$((i + 2))
                  else
                    token="$token\\x"
                  fi
                  ;;
              esac
            else
              token="$token\\x"
            fi
            state="$return_state"
            ;;
          u)
            if [ $((i + 4)) -lt "$len" ]; then
              hex="${input:$((i + 1)):4}"
              case "$hex" in
                *[!0-9a-fA-F]*|'') token="$token\\u" ;;
                *)
                  val=$((16#$hex))
                  if [ "$val" -eq 10 ] || [ "$val" -eq 13 ]; then
                    return 3
                  elif [ "$val" -ne 0 ]; then
                    token="$token$(utf8_encode "$val")"
                    i=$((i + 4))
                  else
                    token="$token\\u"
                  fi
                  ;;
              esac
            else
              token="$token\\u"
            fi
            state="$return_state"
            ;;
          U)
            if [ $((i + 8)) -lt "$len" ]; then
              hex="${input:$((i + 1)):8}"
              case "$hex" in
                *[!0-9a-fA-F]*|'') token="$token\\U" ;;
                *)
                  val=$((16#$hex))
                  if [ "$val" -eq 10 ] || [ "$val" -eq 13 ]; then
                    return 3
                  elif codepoint_valid "$val"; then
                    token="$token$(utf8_encode "$val")"
                    i=$((i + 8))
                  else
                    token="$token\\U"
                  fi
                  ;;
              esac
            else
              token="$token\\U"
            fi
            state="$return_state"
            ;;
          [0-7])
            if [ $((i + 2)) -lt "$len" ]; then
              oct="${input:$i:3}"
              case "$oct" in
                *[!0-7]*|'') token="$token\\$c" ;;
                *)
                  val=$((8#$oct))
                  if [ "$val" -eq 10 ] || [ "$val" -eq 13 ]; then
                    return 3
                  elif [ "$val" -le 255 ] && [ "$val" -ne 0 ]; then
                    token="$token$(printf "\\$(printf '%03o' "$val")")"
                    i=$((i + 2))
                  else
                    token="$token\\$c"
                  fi
                  ;;
              esac
            else
              token="$token\\$c"
            fi
            state="$return_state"
            ;;
          *) token="$token\\$c"; state="$return_state" ;;
        esac
        ;;
    esac
    i=$((i + 1))
  done
  if [ "$state" = "escape" ]; then
    if [ "$return_state" = "plain" ]; then
      token="$token\\"
      state=plain
    else
      return 1
    fi
  fi
  if [ "$state" != "plain" ]; then return 1; fi
  if [ "$present" -eq 1 ]; then printf '%s\n' "$token"; fi
  return 0
}

legacy_flag_value() {
  local exec_line="$1" want="--$2" rc
  # Feed awk straight from systemd_split: command substitution would strip the
  # trailing newlines and lose a final empty argument's record boundary.
  systemd_split "$exec_line" | awk -v want="$want" '
    $0 == want { if ((getline nxt) > 0) { value = nxt; found = 1 }; next }
    index($0, want "=") == 1 { value = substr($0, length(want) + 2); found = 1; next }
    END { if (found) { print value; exit 0 }; exit 1 }
  '
  rc=("${PIPESTATUS[@]}")
  if [ "${rc[0]}" -eq 3 ]; then return 3; fi
  if [ "${rc[0]}" -ne 0 ]; then return 2; fi
  return "${rc[1]}"
}

capture_legacy_values() {
  local file="$1" exec_line flag key value probe rc
  exec_line="$(legacy_exec_line "$file")"
  [ -n "$exec_line" ] || return 0
  for flag in listen auth data-dir web-root master-key-file; do
    if value="$(legacy_flag_value "$exec_line" "$flag")"; then
      :
    else
      rc=$?
      if [ "$rc" -eq 1 ]; then
        continue
      fi
      return "$rc"
    fi
    case "$value" in
      *%*)
        probe="$(printf '%s' "$value" | sed 's/%%//g')"
        case "$probe" in
          *%*) return 4 ;;
        esac
        value="$(printf '%s' "$value" | sed 's/%%/%/g')"
        ;;
    esac
    if ! env_utf8_valid "$value"; then
      return 5
    fi
    if [ -z "$value" ] && [ "$flag" != "master-key-file" ]; then
      continue
    fi
    case "$flag" in
      listen) key=NEXTERM_LISTEN ;;
      auth) key=NEXTERM_AUTH ;;
      data-dir) key=NEXTERM_DATA_DIR ;;
      web-root) key=NEXTERM_WEB_ROOT ;;
      master-key-file) key=NEXTERM_MASTER_KEY_FILE ;;
    esac
    printf '%s=%s\n' "$key" "$value"
  done
}

env_join_continuations() {
  # join lines ending with an odd number of backslashes: the backslash and
  # the newline are eaten, per systemd's VALUE_ESCAPE rules. Comment lines
  # only lose the trailing backslash (COMMENT_ESCAPE returns to PRE_KEY, so
  # the next line is parsed fresh, never swallowed into the comment).
  awk '
    {
      n = 0
      while (substr($0, length($0) - n, 1) == "\\") n++
      if ($0 ~ /^[[:blank:]]*[#;]/) {
        if (n % 2 == 1) {
          $0 = substr($0, 1, length($0) - 1)
        }
        print
        next
      }
      while (n % 2 == 1 && (getline nxt) > 0) {
        $0 = substr($0, 1, length($0) - 1) nxt
        n = 0
        while (substr($0, length($0) - n, 1) == "\\") n++
      }
      print
    }
  '
}

env_parse_value() {
  # $1 = raw text after the first '=' on a logical line; prints the parsed
  # value per systemd env-file semantics (quotes, escapes, concatenation)
  local value="$1" out="" state=pre c
  local i=0 len=${#value} trail=0
  while [ "$i" -lt "$len" ]; do
    c="${value:$i:1}"
    case "$state" in
      pre)
        case "$c" in
          ' '|$'\t') ;;
          '"') state=dquote ;;
          "'") state=squote ;;
          \\) state=plain_escape ;;
          *) out="$out$c"; state=plain; trail=0 ;;
        esac
        ;;
      plain)
        case "$c" in
          \\) state=plain_escape ;;
          ' '|$'\t') out="$out$c"; trail=$((trail + 1)) ;;
          *) out="$out$c"; trail=0 ;;
        esac
        ;;
      plain_escape)
        out="$out$c"; state=plain ;;
      dquote)
        case "$c" in
          '"') state=after_quote ;;
          \\) state=dquote_escape ;;
          *) out="$out$c"; trail=0 ;;
        esac
        ;;
      dquote_escape)
        case "$c" in
          '"'|\\|'$'|'`') out="$out$c" ;;
          *) out="$out\\$c" ;;
        esac
        state=dquote ;;
      squote)
        if [ "$c" = "'" ]; then
          state=after_quote
        else
          out="$out$c"; trail=0
        fi
        ;;
      after_quote)
        case "$c" in
          ' '|$'\t') ;;
          '"') state=dquote ;;
          "'") state=squote ;;
          \\) state=plain_escape ;;
          *) out="$out$c"; state=plain; trail=0 ;;
        esac
        ;;
    esac
    i=$((i + 1))
  done
  if [ "$trail" -gt 0 ]; then
    out="${out:0:${#out}-$trail}"
  fi
  printf '%s' "$out"
}

env_quote_if_needed() {
  local value="$1"
  case "$value" in
    *\\*|*\"*|*\$*|*\`*|[[:blank:]]*|*[[:blank:]]|"'"*|'"'*|"")
      printf '"%s"' "$(printf '%s' "$value" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\$/\\$/g' -e 's/`/\\`/g')"
      ;;
    *) printf '%s' "$value" ;;
  esac
}

env_utf8_valid() {
  # mirrors systemd's utf8_is_valid as used by load_env_file: the value must
  # be structurally valid UTF-8 (iconv rejects overlongs, surrogate encodings,
  # lone continuations, and invalid lead bytes) and every codepoint must pass
  # unichar_is_valid (no surrogates, no U+FDD0..U+FDEF or U+xFFFE/U+xFFFF
  # noncharacters, nothing above U+10FFFF)
  local value="$1"
  printf '%s' "$value" | iconv -f UTF-8 -t UTF-8 >/dev/null 2>&1 || return 1
  printf '%s' "$value" | iconv -f UTF-8 -t UTF-32BE 2>/dev/null | od -An -v -tx1 | awk '
    BEGIN { for (i = 0; i < 256; i++) hex[sprintf("%02x", i)] = i }
    {
      for (i = 1; i + 3 <= NF; i += 4) {
        cp = hex[$(i)] * 16777216 + hex[$(i + 1)] * 65536 + hex[$(i + 2)] * 256 + hex[$(i + 3)]
        if (cp >= 1114112) exit 1
        if (cp >= 55296 && cp <= 57343) exit 1
        if (cp >= 64976 && cp <= 65007) exit 1
        if (cp % 65536 >= 65534) exit 1
      }
    }
  '
}

env_get() {
  local file="$1" key="$2" line rest value="" found=0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:blank:]]*}"}"
    case "$line" in
      "$key"=*)
        value="${line#"$key"=}"; found=1 ;;
      "$key"[[:blank:]]*=*)
        rest="${line#"$key"}"
        value="${rest#*=}"; found=1 ;;
    esac
  done < <(tr '\r' '\n' < "$file" | env_join_continuations)
  [ "$found" -eq 1 ] || return 0
  env_parse_value "$value"
}

env_key_valid() {
  # a full key-name check (not a glob): a letter or underscore followed by
  # any number of letters, digits or underscores — single-character keys
  # are valid, empty keys and invalid trailing characters are not
  local key="$1" i char
  [ -n "$key" ] || return 1
  case "$key" in
    [A-Za-z_]*) ;;
    *) return 1 ;;
  esac
  i=2
  while [ "$i" -le "${#key}" ]; do
    char="${key:$((i - 1)):1}"
    case "$char" in
      [A-Za-z0-9_]) ;;
      *) return 1 ;;
    esac
    i=$((i + 1))
  done
  return 0
}

env_file_keys() {
  # print the assignment key of every non-comment logical line, in order;
  # used to mirror the service's effective environment exactly
  local file="$1" line key
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:blank:]]*}"}"
    case "$line" in
      ''|'#'*|';'*) continue ;;
      *[=]*) ;;
      *) continue ;;
    esac
    key="${line%%=*}"
    key="${key%"${key##*[![:blank:]]}"}"
    if env_key_valid "$key"; then
      printf '%s\n' "$key"
    fi
  done < <(tr '\r' '\n' < "$file" | env_join_continuations)
}

transaction_pending() { [ -f "$BACKUP_DIR/pending" ]; }

transaction_state_get() {
  local file="$1" key="$2" line
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      "$key"=*) printf '%s' "${line#"$key"=}"; return 0 ;;
    esac
  done < "$file"
  return 1
}

env_origin_record_file() {
  if transaction_pending; then
    printf '%s' "$BACKUP_DIR/transaction"
  elif [ -f "$INCOMPLETE_MARKER" ] && [ -f "$FIRST_INSTALL_STATE" ]; then
    printf '%s' "$FIRST_INSTALL_STATE"
  else
    return 1
  fi
}

load_saved_active_units() {
  ACTIVE_UNITS=""
  local unit
  [ -f "$BACKUP_DIR/active-units" ] || return 1
  while IFS= read -r unit; do
    if [ -n "$unit" ]; then ACTIVE_UNITS="$ACTIVE_UNITS $unit"; fi
  done < "$BACKUP_DIR/active-units"
  return 0
}

pick_effective_unit() {
  local unit="$1" base="$2" d
  for d in "etc/systemd/system" "usr/lib/systemd/system" "lib/systemd/system"; do
    if [ -f "$base/$d/$unit" ]; then
      printf '%s' "$base/$d/$unit"
      return 0
    fi
  done
  return 1
}

unit_working_dir() {
  local file="$1" line value
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      WorkingDirectory=*)
        value="${line#WorkingDirectory=}"
        value="$(printf '%s' "$value" | sed -e 's/^[[:blank:]]*//' -e 's/[[:blank:]]*$//')"
        if [ -z "$value" ]; then
          printf '/'
        elif [ "${value#/}" = "$value" ]; then
          return 1
        else
          lex_clean "$value"
        fi
        return 0
        ;;
    esac
  done < <(tr -d '\r' < "$file")
  printf '/'
}

join_path() {
  case "$1" in
    /) printf '/%s' "$2" ;;
    *) printf '%s/%s' "$1" "$2" ;;
  esac
}

lex_clean() {
  # lexical path normalization (like Go's filepath.Clean): resolves . and ..
  # textually without touching the filesystem; input must be absolute
  local path="$1" out="" part
  case "$path" in /*) ;; *) return 1 ;; esac
  local IFS='/'
  for part in $path; do
    case "$part" in
      ""|.) ;;
      ..) out="${out%/*}" ;;
      *) out="$out/$part" ;;
    esac
  done
  [ -n "$out" ] || out="/"
  printf '%s' "$out"
}

resolve_path() {
  # $1 = logical path, $2 = effective unit working directory; the logical
  # path is lexically cleaned first, then mapped under the staged root, and
  # the result must stay contained
  local logical="$1" wd="$2" physical cleaned
  case "$logical" in
    /*) cleaned="$(lex_clean "$logical")" ;;
    *) cleaned="$(lex_clean "$(join_path "$wd" "$logical")")" ;;
  esac
  physical="$ROOT_PREFIX$cleaned"
  case "$physical" in
    "$ROOT_PREFIX"/*|"$ROOT_PREFIX") ;;
    *) die "resolved path escapes the staged root: $logical" ;;
  esac
  case "$physical" in //*) physical="${physical#?}" ;; esac
  printf '%s' "$physical"
}

dir_has_content() {
  [ -d "$1" ] && [ -n "$(ls -A "$1" 2>/dev/null)" ]
}

env_has_key() {
  local file="$1" key="$2" line
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:blank:]]*}"}"
    case "$line" in
      "$key"=*|"$key"[[:blank:]]*=*)
        return 0 ;;
    esac
  done < <(tr '\r' '\n' < "$file" | env_join_continuations)
  return 1
}

env_append() {
  local file="$1" key="$2" value="$3"
  if [ -s "$file" ] && [ -n "$(tail -c 1 "$file")" ]; then
    printf '\n' >> "$file"
  fi
  printf '%s=%s\n' "$key" "$(env_quote_if_needed "$value")" >> "$file"
}

env_set() {
  local file="$1" key="$2" value="$3" tmp="$1.nexterm-new" line trimmed
  if env_has_key "$file" "$key"; then
    while IFS= read -r line || [ -n "$line" ]; do
      trimmed="${line#"${line%%[![:blank:]]*}"}"
      case "$trimmed" in
        "$key"=*|"$key"[[:blank:]]*=*)
          printf '%s=%s\n' "$key" "$(env_quote_if_needed "$value")" ;;
        *) printf '%s\n' "$line" ;;
      esac
    done < <(tr '\r' '\n' < "$file" | env_join_continuations) > "$tmp"
    cat "$tmp" > "$file"
    rm -f "$tmp"
  else
    env_append "$file" "$key" "$value"
  fi
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
  if [ -f "$BACKUP_DIR/pending" ]; then
    log "preserving rollback backup from the interrupted upgrade (retry keeps the original payload and active-unit state)"
    return 0
  fi
  rm -rf "$BACKUP_DIR"
  install -d -m 0700 "$BACKUP_DIR"
  local f unit d rel
  for f in $BINARIES; do
    if [ -f "$OPT_DIR/$f" ]; then cp -p "$OPT_DIR/$f" "$BACKUP_DIR/$f"; fi
  done
  if [ -d "$OPT_DIR/web" ]; then cp -R "$OPT_DIR/web" "$BACKUP_DIR/web"; fi
  if [ -d "$OPT_DIR/ai-runtime" ]; then cp -R "$OPT_DIR/ai-runtime" "$BACKUP_DIR/ai-runtime"; fi
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
  : > "$BACKUP_DIR/transaction"
  for d in nexterm onlyserver; do
    if [ -f "$ETC_DIR/$d.env" ]; then
      printf 'env_preexisted.%s=1\n' "$d" >> "$BACKUP_DIR/transaction"
    else
      printf 'env_preexisted.%s=0\n' "$d" >> "$BACKUP_DIR/transaction"
    fi
  done
  : > "$BACKUP_DIR/pending"
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

install_ai_runtime() {
  fail_at ai-runtime
  rm -rf "$OPT_DIR/ai-runtime.new"
  if [ -d "$SOURCE_DIR/ai-runtime" ]; then
    cp -R "$SOURCE_DIR/ai-runtime" "$OPT_DIR/ai-runtime.new"
  fi
  rm -rf "$OPT_DIR/ai-runtime.old"
  if [ -d "$OPT_DIR/ai-runtime" ]; then mv "$OPT_DIR/ai-runtime" "$OPT_DIR/ai-runtime.old"; fi
  if [ -d "$OPT_DIR/ai-runtime.new" ]; then
    mv "$OPT_DIR/ai-runtime.new" "$OPT_DIR/ai-runtime"
  else
    log "payload has no AI privacy runtime; optional local AI redaction will be unavailable"
  fi
  rm -rf "$OPT_DIR/ai-runtime.old"
}

legacy_capture_failed() {
  local file="$1" rc="$2"
  case "$rc" in
    3) die "legacy ExecStart values in ${file#$ROOT_PREFIX} contain a newline, which cannot be represented in an environment file; migrate the settings to the ${ETC_DIR#$ROOT_PREFIX} env files manually" ;;
    4) die "legacy ExecStart values in ${file#$ROOT_PREFIX} contain systemd specifiers, which this installer cannot expand safely; migrate the settings to the ${ETC_DIR#$ROOT_PREFIX} env files manually" ;;
    5) die "legacy ExecStart values in ${file#$ROOT_PREFIX} decode to bytes that cannot be represented in an environment file (invalid UTF-8 or Unicode noncharacters); migrate the settings to the ${ETC_DIR#$ROOT_PREFIX} env files manually" ;;
    *) die "cannot parse the legacy ExecStart in ${file#$ROOT_PREFIX} (unsupported quoting); fix the unit or restore it from the rollback backup and re-run" ;;
  esac
}

install_units() {
  fail_at units
  local unit d effective base wd
  if transaction_pending; then
    base="$BACKUP_DIR/units"
  else
    base="$ROOT_PREFIX"
  fi
  for unit in $UNITS; do
    effective="$(pick_effective_unit "$unit" "$base" || true)"
    [ -n "$effective" ] || continue
    if ! wd="$(unit_working_dir "$effective")"; then
      die "cannot parse WorkingDirectory in ${effective#$ROOT_PREFIX} (systemd requires an absolute path)"
    fi
    if [ "$unit" = "$SERVER_UNIT" ]; then
      WORKING_DIR_NEXTERM="$wd"
    else
      WORKING_DIR_ONLYSERVER="$wd"
    fi
    if is_legacy_unit "$effective"; then
      if transaction_pending; then
        log "recovering legacy unit settings for $unit from the rollback backup (interrupted upgrade)"
      fi
      log "migrating legacy unit $unit (inline ExecStart flags move to the ${ETC_DIR#$ROOT_PREFIX} env files)"
      if [ "$unit" = "$SERVER_UNIT" ]; then
        SERVER_LEGACY_VALUES="$(capture_legacy_values "$effective")" || legacy_capture_failed "$effective" $?
      else
        ONLYSERVER_LEGACY_VALUES="$(capture_legacy_values "$effective")" || legacy_capture_failed "$effective" $?
      fi
    fi
  done
  for unit in $UNITS; do
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
  local name env_file example legacy line key pre_existed record_file wd data_dir_migrated data_dir_value configured_key_value path_value
  for name in nexterm onlyserver; do
    env_file="$ETC_DIR/$name.env"
    example="$SOURCE_DIR/$name.env.example"
    if record_file="$(env_origin_record_file)"; then
      [ -f "$record_file" ] || die "transaction state is missing ($record_file); inspect the rollback backup or remove it and re-run"
      pre_existed="$(transaction_state_get "$record_file" "env_preexisted.$name")" ||
        die "transaction state $record_file lacks env_preexisted.$name; inspect it or remove and re-run"
    elif [ -f "$env_file" ]; then
      pre_existed=1
    else
      pre_existed=0
    fi
    if [ "$name" = "nexterm" ]; then NEXTERM_ENV_PREEXISTED="$pre_existed"; else ONLYSERVER_ENV_PREEXISTED="$pre_existed"; fi
    if [ "$pre_existed" -eq 1 ]; then
      log "preserving existing ${env_file#$ROOT_PREFIX}"
    else
      install -m 0600 "$example" "$env_file"
      log "installed ${env_file#$ROOT_PREFIX} with safe defaults"
    fi
    if [ "$name" = "nexterm" ]; then legacy="$SERVER_LEGACY_VALUES"; else legacy="$ONLYSERVER_LEGACY_VALUES"; fi
    if [ -n "$legacy" ]; then
      while IFS= read -r line || [ -n "$line" ]; do
        [ -n "$line" ] || continue
        key="${line%%=*}"
        env_set "$env_file" "$key" "${line#*=}"
        if [ "$key" = "NEXTERM_MASTER_KEY_FILE" ]; then
          if [ "$name" = "nexterm" ]; then MIGRATED_KEY_FILE_NEXTERM=1; else MIGRATED_KEY_FILE_ONLYSERVER=1; fi
        fi
        log "migrated legacy unit setting $key=${line#*=} into ${env_file#$ROOT_PREFIX}"
      done <<EOF
$legacy
EOF
    fi
    for key in $ENV_KEYS; do
      if [ "$key" = "NEXTERM_MASTER_KEY_FILE" ] || [ "$key" = "NEXTERM_DATA_DIR" ]; then continue; fi
      line="$(grep -m1 "^$key=" "$example" || true)"
      [ -n "$line" ] || continue
      if ! env_has_key "$env_file" "$key"; then
        env_append "$env_file" "$key" "${line#*=}"
        log "backfilled $key in ${env_file#$ROOT_PREFIX} (was absent; applied the example default)"
      fi
    done
    if [ "$name" = "nexterm" ]; then wd="$WORKING_DIR_NEXTERM"; else wd="$WORKING_DIR_ONLYSERVER"; fi
    case "$legacy" in *NEXTERM_DATA_DIR=*) data_dir_migrated=1 ;; *) data_dir_migrated=0 ;; esac
    if [ "$MODE" = "upgrade" ] && [ "$data_dir_migrated" -eq 0 ]; then
      if [ "$pre_existed" -eq 1 ] && ! env_has_key "$env_file" NEXTERM_DATA_DIR; then
        env_set "$env_file" NEXTERM_DATA_DIR "$(join_path "$wd" data)"
        log "pinned NEXTERM_DATA_DIR=$(join_path "$wd" data) in ${env_file#$ROOT_PREFIX} (the previous effective location)"
      elif [ "$pre_existed" -eq 0 ] && dir_has_content "$(resolve_path data "$wd")"; then
        env_set "$env_file" NEXTERM_DATA_DIR "$(join_path "$wd" data)"
        log "pinned NEXTERM_DATA_DIR=$(join_path "$wd" data) in ${env_file#$ROOT_PREFIX} (existing data at the previous fallback location)"
      fi
    fi
    if [ "$MODE" = "first" ] && dir_has_content "$(resolve_path data "$wd")"; then
      if [ "$pre_existed" -eq 0 ] || ! env_has_key "$env_file" NEXTERM_DATA_DIR; then
        env_set "$env_file" NEXTERM_DATA_DIR "$(join_path "$wd" data)"
        log "pinned NEXTERM_DATA_DIR=$(join_path "$wd" data) in ${env_file#$ROOT_PREFIX} (existing data at the fallback location)"
      fi
    fi
    if [ "$MODE" = "upgrade" ] && [ "$data_dir_migrated" -eq 0 ] && [ "$wd" != "/var/lib/nexterm" ]; then
      if ! data_dir_value="$(env_get "$env_file" NEXTERM_DATA_DIR)"; then
        die "cannot parse NEXTERM_DATA_DIR in ${env_file#$ROOT_PREFIX} (unsupported EnvironmentFile syntax); fix the file and re-run"
      fi
      if [ -z "$data_dir_value" ] && env_has_key "$env_file" NEXTERM_DATA_DIR; then
        env_set "$env_file" NEXTERM_DATA_DIR "$(join_path "$wd" data)"
        log "pinned empty NEXTERM_DATA_DIR to the previous fallback $(join_path "$wd" data) (working directory changed)"
      fi
    fi
    if ! data_dir_value="$(env_get "$env_file" NEXTERM_DATA_DIR)"; then
      die "cannot parse NEXTERM_DATA_DIR in ${env_file#$ROOT_PREFIX} (unsupported EnvironmentFile syntax); fix the file and re-run"
    fi
    if [ -n "$data_dir_value" ] && [ "${data_dir_value#/}" = "$data_dir_value" ] && [ "$wd" != "/var/lib/nexterm" ]; then
      env_set "$env_file" NEXTERM_DATA_DIR "$(lex_clean "$(join_path "$wd" "$data_dir_value")")"
      log "absolutized relative NEXTERM_DATA_DIR against the previous working directory $wd"
    fi
    if ! configured_key_value="$(env_get "$env_file" NEXTERM_MASTER_KEY_FILE)"; then
      die "cannot parse NEXTERM_MASTER_KEY_FILE in ${env_file#$ROOT_PREFIX} (unsupported EnvironmentFile syntax); fix the file and re-run"
    fi
    if [ -n "$configured_key_value" ] && [ "${configured_key_value#/}" = "$configured_key_value" ] && [ "$wd" != "/var/lib/nexterm" ]; then
      env_set "$env_file" NEXTERM_MASTER_KEY_FILE "$(lex_clean "$(join_path "$wd" "$configured_key_value")")"
      log "absolutized relative NEXTERM_MASTER_KEY_FILE against the previous working directory $wd"
    fi
    if [ "$wd" != "/var/lib/nexterm" ]; then
      for key in NEXTERM_WEB_ROOT NEXTERM_DB_PASSWORD_FILE; do
        path_value="$(env_get "$env_file" "$key")"
        if [ -n "$path_value" ] && [ "${path_value#/}" = "$path_value" ]; then
          env_set "$env_file" "$key" "$(lex_clean "$(join_path "$wd" "$path_value")")"
          log "absolutized relative $key against the previous working directory $wd"
        fi
      done
    fi
  done
}

ensure_master_key() {
  fail_at masterkey
  local env_file configured data_dir data_dir_key key_file physical parent name pre_existed migrated_key wd
  for name in nexterm onlyserver; do
    env_file="$ETC_DIR/$name.env"
    if [ "$name" = "nexterm" ]; then
      pre_existed="$NEXTERM_ENV_PREEXISTED"
      migrated_key="$MIGRATED_KEY_FILE_NEXTERM"
      wd="$WORKING_DIR_NEXTERM"
    else
      pre_existed="$ONLYSERVER_ENV_PREEXISTED"
      migrated_key="$MIGRATED_KEY_FILE_ONLYSERVER"
      wd="$WORKING_DIR_ONLYSERVER"
    fi
    if ! configured="$(env_get "$env_file" NEXTERM_MASTER_KEY_FILE)"; then
      die "cannot parse NEXTERM_MASTER_KEY_FILE in ${env_file#$ROOT_PREFIX} (unsupported EnvironmentFile syntax); fix the file and re-run"
    fi
    if ! data_dir="$(env_get "$env_file" NEXTERM_DATA_DIR)"; then
      die "cannot parse NEXTERM_DATA_DIR in ${env_file#$ROOT_PREFIX} (unsupported EnvironmentFile syntax); fix the file and re-run"
    fi
    if [ -z "$data_dir" ]; then
      data_dir="$(join_path "$wd" data)"
    elif [ "${data_dir#/}" = "$data_dir" ]; then
      data_dir="$(join_path "$wd" "$data_dir")"
    fi
    data_dir="$(lex_clean "$data_dir")"
    data_dir_key="$data_dir/master.key"
    if [ -n "$configured" ] && { [ "$pre_existed" -eq 1 ] || [ "$migrated_key" -eq 1 ]; }; then
      key_file="$configured"
    elif [ -f "$(resolve_path "$data_dir_key" "$wd")" ]; then
      key_file="$data_dir_key"
      log "adopting existing data-directory master key $data_dir_key for ${env_file#$ROOT_PREFIX}"
    else
      key_file="/etc/nexterm/master.key"
    fi
    physical="$(resolve_path "$key_file" "$wd")"
    if [ -f "$physical" ]; then
      log "preserving existing master key $key_file"
    else
      command -v openssl >/dev/null 2>&1 || die "openssl is required to generate $key_file"
      parent="$(dirname "$physical")"
      if [ ! -d "$parent" ]; then install -d -m 0700 "$parent"; fi
      (umask 077; openssl rand -hex 32 > "$physical")
      if [ "$REAL_ROOT" -eq 1 ]; then
        chown nexterm:nexterm "$physical"
      fi
      chmod 0600 "$physical"
      if [ "$MODE" = "upgrade" ]; then
        log "WARNING: $key_file was missing and has been regenerated; credentials encrypted under an older key cannot be recovered"
      fi
      log "generated $key_file (back it up with ${DATA_DIR#$ROOT_PREFIX}; it cannot be recreated)"
    fi
    if [ "$key_file" != "$configured" ] || ! env_has_key "$env_file" NEXTERM_MASTER_KEY_FILE; then
      env_set "$env_file" NEXTERM_MASTER_KEY_FILE "$key_file"
      log "configured NEXTERM_MASTER_KEY_FILE=$key_file in ${env_file#$ROOT_PREFIX}"
    fi
  done
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
      if unit_active "$SERVER_UNIT"; then
        log "$SERVER_UNIT is active"
      else
        log "WARNING: $SERVER_UNIT did not report active after enable --now; inspect it with: systemctl status $SERVER_UNIT; journalctl -u $SERVER_UNIT"
      fi
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

bootstrap_admin() {
  fail_at bootstrap
  [ "$MODE" = "first" ] || return 0
  if [ "$REAL_ROOT" -ne 1 ] && [ -z "${NEXTERM_INSTALL_INIT_ADMIN:-}" ]; then
    log "skipping super-admin bootstrap (staged root $ROOT_PREFIX)"
    return 0
  fi
  local env_file="$ETC_DIR/nexterm.env" auth data_dir db dsn dbpw effective rc env_keys server_home admin_user admin_pass key bootstrap_wd
  auth="$(env_get "$env_file" NEXTERM_AUTH)"
  case "$auth" in
    ""|on|loopback) ;;
    *) log "skipping super-admin bootstrap (NEXTERM_AUTH=$auth manages accounts itself)"; return 0 ;;
  esac
  data_dir="$(env_get "$env_file" NEXTERM_DATA_DIR)"
  db="$(env_get "$env_file" NEXTERM_DB)"
  dsn="$(env_get "$env_file" NEXTERM_DB_DSN)"
  dbpw="$(env_get "$env_file" NEXTERM_DB_PASSWORD_FILE)"
  # Resolve the data directory the way the server resolves it (an explicit
  # value, relative values against the unit working directory, otherwise the
  # <working-dir>/data fallback) so the bootstrap opens the same database the
  # service will open.
  if [ -z "$data_dir" ]; then
    effective="$WORKING_DIR_NEXTERM/data"
  elif [ "${data_dir#/}" = "$data_dir" ]; then
    effective="$WORKING_DIR_NEXTERM/$data_dir"
  else
    effective="$data_dir"
  fi
  effective="$(lex_clean "$effective")"
  [ "$effective" != "/" ] || die "refusing to bootstrap with dangerous data directory $effective"
  # The service's own HOME comes from the unit's Environment= line.
  server_home="$(grep -m1 '^Environment=HOME=' "$UNIT_DIR/$SERVER_UNIT" | sed 's/^Environment=HOME=//' || true)"
  [ -n "$server_home" ] || server_home="/var/lib/nexterm"
  # The bootstrap runs in the unit's WorkingDirectory (mapped under the
  # staged root when there is one), so relative paths such as
  # NEXTERM_DB_PASSWORD_FILE resolve exactly like the service resolves them.
  bootstrap_wd="$(resolve_path "$WORKING_DIR_NEXTERM" /)"
  # The whitelist mirrors the service's effective environment: every key of
  # the env file (systemd applies EnvironmentFile to the service verbatim),
  # the unit's HOME, and the intentionally injected admin variables.
  env_keys="$(env_file_keys "$env_file")"
  admin_user="${NEXTERM_ADMIN_USERNAME:-}"
  admin_pass="${NEXTERM_ADMIN_PASSWORD:-}"
  local server_bin="$OPT_DIR/nexterm-server"
  [ -n "${NEXTERM_INSTALL_INIT_ADMIN:-}" ] && server_bin="$NEXTERM_INSTALL_INIT_ADMIN"
  if [ "$REAL_ROOT" -eq 1 ]; then
    # Run the bootstrap as the service user: everything it creates is then
    # born with the right ownership, and a data directory the nexterm user
    # cannot write fails closed here instead of breaking the service later.
    command -v runuser >/dev/null 2>&1 || die "runuser not found (required to run the super-admin bootstrap as the nexterm user)"
  fi
  log "creating the super-admin account (first install)"
  rc=0
  # The bootstrap runs in a scrubbed environment: ambient variables (PG*,
  # LD_*, ...) never reach the child, so it sees exactly what the service
  # would see and cannot land in a different database. Secrets travel
  # through the process environment only: argv is world-readable via
  # /proc/<pid>/cmdline. The scrub and exports live in a subshell so the
  # rest of the installer never carries the admin variables.
  (
    while IFS= read -r line; do
      case "$line" in
        "declare -x "*) ;;
        *) continue ;;
      esac
      name="${line#declare -x }"
      name="${name%%=*}"
      case "$name" in
        PATH|INIT_ADMIN_*) ;;
        *) unset "$name" 2>/dev/null || true ;;
      esac
    done < <(export -p)
    export HOME="$server_home"
    for key in $env_keys; do
      export "$key=$(env_get "$env_file" "$key")"
    done
    if [ -n "$admin_user" ]; then export NEXTERM_ADMIN_USERNAME="$admin_user"; fi
    if [ -n "$admin_pass" ]; then export NEXTERM_ADMIN_PASSWORD="$admin_pass"; fi
    cd "$bootstrap_wd" || die "cannot enter the service working directory $bootstrap_wd"
    if [ "$REAL_ROOT" -eq 1 ]; then
      exec runuser --preserve-environment -u nexterm -- "$server_bin" init-admin
    fi
    exec "$server_bin" init-admin
  ) || rc=$?
  case "$rc" in
    0) log "super-admin created; the initial password printed above is shown only once" ;;
    3) log "super-admin bootstrap skipped: the account system is already initialized" ;;
    4) die "super-admin bootstrap found an already-issued one-time init code it cannot prove usable (leftover from an interrupted attempt, or issued by a running server); if a server printed the code, finish initialization in the browser with it, otherwise clear auth.init_code in the server setting table, then re-run (the first install resumes)" ;;
    *) die "super-admin bootstrap failed; fix the problem reported above and re-run (the first install resumes)" ;;
  esac
}

# Run the payload binary before mutating anything so an unsupported C library or
# CPU fails the install instead of leaving a service that cannot start.
preflight_server_binary() {
  local output rc=0 missing
  output="$("$SOURCE_DIR/nexterm-server" --version 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    die "payload nexterm-server cannot run on this host (exit $rc): ${output:-no output}; it requires glibc 2.28 or newer (RHEL 8, Debian 11, Ubuntu 20.04 or later) and a matching CPU architecture"
  fi
  log "payload server version: ${output:-unknown}"
  if [ -f "$SOURCE_DIR/ai-runtime/libonnxruntime.so" ] && command -v ldd >/dev/null 2>&1; then
    missing="$(ldd "$SOURCE_DIR/ai-runtime/libonnxruntime.so" 2>/dev/null | grep 'not found' || true)"
    if [ -n "$missing" ]; then
      log "WARNING: optional local AI redaction will be unavailable until these ONNX Runtime dependencies are installed: $(printf '%s' "$missing" | tr -s ' \t\n' ' ')"
    fi
  fi
}

run_install() {
  fail_at preflight
  local f
  for f in $BINARIES "$SERVER_UNIT" "$ONLYSERVER_UNIT" nexterm.env.example onlyserver.env.example; do
    [ -f "$SOURCE_DIR/$f" ] || die "payload is missing $f (run this script from an extracted NexTerm server archive, or pass --source DIR)"
  done
  [ -f "$SOURCE_DIR/web/index.html" ] || die "payload is missing web/index.html"
  if [ -d "$SOURCE_DIR/ai-runtime" ]; then
    [ -f "$SOURCE_DIR/ai-runtime/libonnxruntime.so" ] || die "payload is missing ai-runtime/libonnxruntime.so"
  fi
  preflight_server_binary
  detect_platform
  require_systemctl
  require_root_when_real

  MODE=first
  if [ -f "$INCOMPLETE_MARKER" ]; then
    log "resuming incomplete first install (previous attempt did not finish)"
  elif [ -f "$OPT_DIR/nexterm-server" ] || [ -f "$ETC_DIR/nexterm.env" ] || [ -f "$ETC_DIR/onlyserver.env" ] ||
       [ -f "$UNIT_DIR/$SERVER_UNIT" ] || [ -f "$UNIT_DIR/$ONLYSERVER_UNIT" ]; then
    MODE=upgrade
  fi
  log "mode: $MODE install"

  ensure_user
  ensure_dirs
  if [ "$MODE" = "upgrade" ]; then
    if transaction_pending && load_saved_active_units; then
      log "reusing the active-unit state saved by the interrupted upgrade"
    else
      capture_active_units
    fi
    backup_current
  else
    if [ ! -f "$INCOMPLETE_MARKER" ]; then
      : > "$INCOMPLETE_MARKER"
      : > "$FIRST_INSTALL_STATE"
      for f in nexterm onlyserver; do
        if [ -f "$ETC_DIR/$f.env" ]; then
          printf 'env_preexisted.%s=1\n' "$f" >> "$FIRST_INSTALL_STATE"
        else
          printf 'env_preexisted.%s=0\n' "$f" >> "$FIRST_INSTALL_STATE"
        fi
      done
    fi
    ACTIVE_UNITS=""
  fi
  install_binaries
  install_web
  install_ai_runtime
  install_units
  ensure_env_files
  ensure_master_key
  bootstrap_admin
  reload_systemd
  manage_services
  rm -f "$INCOMPLETE_MARKER" "$FIRST_INSTALL_STATE" "$BACKUP_DIR/pending"
  print_next_steps
  log "done"
}

# print_next_steps tells the operator how to reach the server it just
# configured; the default loopback listener is unreachable from other machines.
print_next_steps() {
  local env_file="$ETC_DIR/nexterm.env" listen port
  [ -f "$env_file" ] || return 0
  listen="$(env_get "$env_file" NEXTERM_LISTEN)"
  [ -n "$listen" ] || listen="0.0.0.0:8080"
  port="${listen##*:}"
  log "next steps:"
  case "$listen" in
    127.*|localhost:*|"[::1]":*)
      log "  the web UI listens on $listen, reachable from this machine only"
      log "  from your computer: ssh -L $port:$listen <user>@<this-server>, then open http://localhost:$port"
      log "  for public access, put a TLS reverse proxy in front and list its source ranges in NEXTERM_TRUSTED_PROXY_CIDRS"
      ;;
    *)
      log "  the web UI listens on $listen; open http://<this-server>:$port (use a TLS reverse proxy for public access)"
      ;;
  esac
  log "  settings: ${env_file#$ROOT_PREFIX} (apply with: systemctl restart $SERVER_UNIT)"
  log "  logs: journalctl -u $SERVER_UNIT"
  log "  upgrade: run the installer again; undo the last upgrade with --rollback"
}

run_rollback() {
  fail_at preflight
  [ -d "$BACKUP_DIR" ] || die "no rollback backup found at ${BACKUP_DIR#$ROOT_PREFIX}"
  [ -f "$BACKUP_DIR/nexterm-server" ] || die "rollback backup at ${BACKUP_DIR#$ROOT_PREFIX} is incomplete"
  detect_platform
  require_systemctl
  require_root_when_real

  MODE=upgrade
  if ! load_saved_active_units; then
    capture_active_units
  fi
  fail_at rollback
  install -d -m 0755 "$OPT_DIR"
  rm -rf "$FAILED_DIR"
  install -d -m 0700 "$FAILED_DIR"
  local f rel unit
  for f in $BINARIES; do
    if [ -f "$OPT_DIR/$f" ]; then mv "$OPT_DIR/$f" "$FAILED_DIR/$f"; fi
  done
  if [ -d "$OPT_DIR/web" ]; then mv "$OPT_DIR/web" "$FAILED_DIR/web"; fi
  if [ -d "$OPT_DIR/ai-runtime" ]; then mv "$OPT_DIR/ai-runtime" "$FAILED_DIR/ai-runtime"; fi
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
  if [ -d "$BACKUP_DIR/ai-runtime" ]; then cp -R "$BACKUP_DIR/ai-runtime" "$OPT_DIR/ai-runtime"; fi
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
