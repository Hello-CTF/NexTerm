#!/bin/sh
# The platform can only inject stable_secret into environment variables; here
# it is landed as a 0600 key file so the server uses the supported
# NEXTERM_MASTER_KEY_FILE flow and no key remains in the process environment.
set -eu

if [ -n "${NEXTERM_LAZYCAT_VAULT_MASTER_KEY:-}" ]; then
  if [ -z "${NEXTERM_MASTER_KEY_FILE:-}" ]; then
    data_dir="${NEXTERM_DATA_DIR:-/lzcapp/var/nexterm}"
    umask 077
    mkdir -p "$data_dir"
    printf '%s' "$NEXTERM_LAZYCAT_VAULT_MASTER_KEY" > "$data_dir/master.key"
    chmod 0600 "$data_dir/master.key"
    NEXTERM_MASTER_KEY_FILE="$data_dir/master.key"
    export NEXTERM_MASTER_KEY_FILE
  fi
  unset NEXTERM_LAZYCAT_VAULT_MASTER_KEY
fi

exec /usr/local/bin/nexterm-server "$@"
