#!/bin/sh
# 平台只能把 stable_secret 注入环境变量；这里把它落成 0600 密钥文件，
# 让服务端走受支持的 NEXTERM_MASTER_KEY_FILE 流程，进程环境里不留密钥。
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
