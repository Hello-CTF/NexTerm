#!/bin/sh
# The platform can only inject stable_secret into environment variables; here
# it is landed as a 0600 key file so the server uses the supported
# NEXTERM_MASTER_KEY_FILE flow and no key remains in the process environment.
set -eu

data_dir="${NEXTERM_DATA_DIR:-/lzcapp/var/nexterm}"

if [ "$(id -u)" -eq 0 ]; then
  if [ -L "$data_dir" ]; then
    echo "FATAL: NexTerm data directory must not be a symbolic link: $data_dir" >&2
    exit 1
  fi
  mkdir -p "$data_dir"

  echo "NexTerm: checking data directory ownership before dropping privileges"
  data_dir_physical="$(cd "$data_dir" && pwd -P)"
  case "$data_dir_physical" in
    */) mount_prefix="$data_dir_physical" ;;
    *) mount_prefix="$data_dir_physical/" ;;
  esac
  if ! mount_table="$(findmnt -ln -o TARGET)"; then
    echo "FATAL: cannot inspect mounts before migrating NexTerm data" >&2
    exit 1
  fi
  nested_mounts="$(printf '%s\n' "$mount_table" | awk -v prefix="$mount_prefix" 'index($0, prefix) == 1 { print }')"
  if [ -n "$nested_mounts" ]; then
    printf 'FATAL: nested mounts inside NexTerm data directory are unsupported; unmount them before startup:\n%s\n' "$nested_mounts" >&2
    exit 1
  fi

  if ! hardlinks="$(find -P "$data_dir" -xdev ! -type d -links +1 -print)"; then
    echo "FATAL: cannot inspect hardlinks before migrating NexTerm data" >&2
    exit 1
  fi
  if [ -n "$hardlinks" ]; then
    printf 'FATAL: hardlinked non-directories inside NexTerm data directory are unsupported; replace them with independent files:\n%s\n' "$hardlinks" >&2
    exit 1
  fi

  if ! find -P "$data_dir" -xdev \( ! -uid 10001 -o ! -gid 10001 \) \
    -exec chown -h 10001:10001 -- {} +; then
    echo "FATAL: cannot migrate NexTerm data directory ownership" >&2
    exit 1
  fi

  exec /usr/bin/setpriv --reuid=10001 --regid=10001 --clear-groups \
    /usr/local/bin/nexterm-server-entrypoint "$@"
fi

if [ -n "${NEXTERM_LAZYCAT_VAULT_MASTER_KEY:-}" ]; then
  if [ -z "${NEXTERM_MASTER_KEY_FILE:-}" ]; then
    umask 077
    mkdir -p "$data_dir"
    if [ -L "$data_dir/master.key" ]; then
      echo "FATAL: NexTerm master key must not be a symbolic link: $data_dir/master.key" >&2
      exit 1
    fi
    key_tmp="$(mktemp "$data_dir/.master.key.XXXXXX")"
    trap 'rm -f -- "$key_tmp"' EXIT
    trap 'exit 1' HUP INT TERM
    printf '%s' "$NEXTERM_LAZYCAT_VAULT_MASTER_KEY" > "$key_tmp"
    chmod 0600 "$key_tmp"
    mv -fT -- "$key_tmp" "$data_dir/master.key"
    trap - EXIT HUP INT TERM
    NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"
    export NEXTERM_MASTER_KEY_FILE
  fi
  unset NEXTERM_LAZYCAT_VAULT_MASTER_KEY
fi

exec /usr/local/bin/nexterm-server "$@"
