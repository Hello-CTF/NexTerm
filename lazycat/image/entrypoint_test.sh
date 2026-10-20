#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ] || [ ! -f /.dockerenv ]; then
  echo "FAIL: run this test as root inside a disposable Docker container" >&2
  exit 1
fi

TEST_ROOT="$(mktemp -d /tmp/nexterm-entrypoint-test.XXXXXX)"
chmod 0755 "$TEST_ROOT"
mkdir -p "$TEST_ROOT/bin"
MOUNTED_PATH=''
cleanup() {
  if [ -n "$MOUNTED_PATH" ]; then
    umount "$MOUNTED_PATH" >/dev/null 2>&1 || true
  fi
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_eq() {
  [ "$1" = "$2" ] || fail "$3: got $1, want $2"
}

assert_owner() {
  actual="$(stat -c '%u:%g' "$1")"
  assert_eq "$actual" "$2" "owner of $1"
}

assert_log_contains() {
  grep -F "$2" "$1" >/dev/null || {
    cat "$1" >&2
    fail "$3"
  }
}

find_count() {
  wc -l < "$FIND_LOG" | tr -d ' '
}

chown_count() {
  wc -l < "$CHOWN_LOG" | tr -d ' '
}

cat > /usr/local/bin/nexterm-server <<'EOF'
#!/bin/sh
set -eu
key_state=absent
if [ "${NEXTERM_LAZYCAT_VAULT_MASTER_KEY+x}" = x ]; then
  key_state=present
fi
data_readable=no
if [ "$(cat "$NEXTERM_DATA_DIR/nested/db.sqlite")" = legacy-db ]; then
  data_readable=yes
fi
printf '%s\n' \
  "uid=$(id -u)" \
  "gid=$(id -g)" \
  "groups=$(id -G)" \
  "key_transport=$key_state" \
  "key_file=$NEXTERM_MASTER_KEY_FILE" \
  "data_readable=$data_readable" > "$REPORT_FILE"
EOF
chmod 0755 /usr/local/bin/nexterm-server

cat > "$TEST_ROOT/bin/find" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FIND_LOG"
exec /usr/bin/find "$@"
EOF
cat > "$TEST_ROOT/bin/chown" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CHOWN_LOG"
exec /usr/bin/chown "$@"
EOF
chmod 0755 "$TEST_ROOT/bin/find" "$TEST_ROOT/bin/chown"
export PATH="$TEST_ROOT/bin:$PATH"
export FIND_LOG="$TEST_ROOT/find.log"
export CHOWN_LOG="$TEST_ROOT/chown.log"
: > "$FIND_LOG"
: > "$CHOWN_LOG"

DATA="$TEST_ROOT/data"
OUTSIDE="$TEST_ROOT/outside"
REPORT="$TEST_ROOT/report"
mkdir -p "$DATA/nested" "$OUTSIDE"
printf 'legacy-db' > "$DATA/nested/db.sqlite"
printf 'old-key' > "$DATA/master.key"
printf 'outside-data' > "$OUTSIDE/target"
printf 'arbitrary-old-state' > "$DATA/.nexterm-entrypoint-owner-v1"
: > "$REPORT"
/usr/bin/chown 10001:10001 "$REPORT" "$DATA" "$DATA/.nexterm-entrypoint-owner-v1"
chmod 0644 "$DATA/.nexterm-entrypoint-owner-v1"
chmod 0600 "$DATA/master.key" "$DATA/nested/db.sqlite"
ln -s "$OUTSIDE/target" "$DATA/target-link"

run_main() {
  NEXTERM_DATA_DIR="$DATA" \
  NEXTERM_LAZYCAT_VAULT_MASTER_KEY='test-only-key' \
  REPORT_FILE="$REPORT" \
    /usr/local/bin/nexterm-server-entrypoint
}

run_main_expect_failure() {
  log="$1"
  if NEXTERM_DATA_DIR="$DATA" \
    NEXTERM_LAZYCAT_VAULT_MASTER_KEY='test-only-key' \
    REPORT_FILE="$REPORT" \
      /usr/local/bin/nexterm-server-entrypoint > "$log" 2>&1; then
    fail "entrypoint unexpectedly succeeded; see $log"
  fi
}

run_main
assert_eq "$(id -u)" 0 'test must start as root'
grep -qx 'uid=10001' "$REPORT" || fail 'server uid'
grep -qx 'gid=10001' "$REPORT" || fail 'server gid'
grep -qx 'groups=10001' "$REPORT" || fail 'server supplementary groups'
grep -qx 'key_transport=absent' "$REPORT" || fail 'master key transport remained in server environment'
grep -qx "key_file=$DATA/master.key" "$REPORT" || fail 'master key file path'
grep -qx 'data_readable=yes' "$REPORT" || fail 'legacy data was not readable by server'
assert_owner "$DATA/nested/db.sqlite" 10001:10001
assert_owner "$DATA/master.key" 10001:10001
assert_owner "$DATA/target-link" 10001:10001
assert_owner "$OUTSIDE/target" 0:0
assert_eq "$(stat -c '%a' "$DATA/master.key")" 600 'master key mode'
assert_eq "$(cat "$DATA/master.key")" test-only-key 'master key payload'
assert_eq "$(cat "$DATA/.nexterm-entrypoint-owner-v1")" arbitrary-old-state 'old marker content must remain untouched'
assert_eq "$(stat -c '%a' "$DATA/.nexterm-entrypoint-owner-v1")" 644 'old marker mode must remain untouched'
assert_eq "$(cat "$OUTSIDE/target")" outside-data 'symlink target payload'
assert_eq "$(find_count)" 2 'every root start must run hardlink and ownership finds'
printf 'CASE restored-marker-root-file: PASS\n'

before_chown="$(chown_count)"
run_main
assert_eq "$(find_count)" 4 'ordinary restart must repeat both finds'
assert_eq "$(chown_count)" "$before_chown" 'ordinary restart must not chown matching nodes'
grep -qx 'data_readable=yes' "$REPORT" || fail 'data was not readable after ordinary restart'
assert_eq "$(stat -c '%a' "$DATA/master.key")" 600 'master key mode after ordinary restart'
printf 'CASE ordinary-restart: PASS\n'

rm "$DATA/.nexterm-entrypoint-owner-v1"
/usr/bin/chown 0:0 "$DATA/nested/db.sqlite"
run_main
assert_eq "$(find_count)" 6 'deleting an old marker must not change scan behavior'
assert_owner "$DATA/nested/db.sqlite" 10001:10001
grep -qx 'data_readable=yes' "$REPORT" || fail 'restored root-owned file was not repaired with old marker absent'
printf 'CASE deleted-marker-and-root-file: PASS\n'

MOUNT_DATA="$TEST_ROOT/mount-data"
DATA="$MOUNT_DATA"
mkdir -p "$DATA/nested" "$DATA/nested-mount"
printf 'legacy-db' > "$DATA/nested/db.sqlite"
chmod 0600 "$DATA/nested/db.sqlite"
mount -t tmpfs tmpfs "$DATA/nested-mount"
MOUNTED_PATH="$DATA/nested-mount"
printf 'mounted-data' > "$DATA/nested-mount/file"
before_chown="$(chown_count)"
run_main_expect_failure "$TEST_ROOT/nested-tmpfs.log"
assert_log_contains "$TEST_ROOT/nested-tmpfs.log" 'nested mounts inside NexTerm data directory are unsupported' 'tmpfs error contract'
assert_log_contains "$TEST_ROOT/nested-tmpfs.log" "$DATA/nested-mount" 'tmpfs path must be listed'
assert_owner "$DATA/nested/db.sqlite" 0:0
assert_owner "$DATA/nested-mount/file" 0:0
assert_eq "$(chown_count)" "$before_chown" 'tmpfs failure must happen before any chown'
cat "$TEST_ROOT/nested-tmpfs.log"
umount "$MOUNTED_PATH"
MOUNTED_PATH=''
printf 'CASE nested-tmpfs-fails-before-chown: PASS\n'

mkdir -p "$OUTSIDE/bind-source" "$DATA/bind-mount"
printf 'bind-data' > "$OUTSIDE/bind-source/file"
mount --bind "$OUTSIDE/bind-source" "$DATA/bind-mount"
MOUNTED_PATH="$DATA/bind-mount"
run_main_expect_failure "$TEST_ROOT/nested-bind.log"
assert_log_contains "$TEST_ROOT/nested-bind.log" "$DATA/bind-mount" 'same-device bind mount path must be listed'
assert_owner "$DATA/nested/db.sqlite" 0:0
assert_eq "$(chown_count)" "$before_chown" 'bind mount failure must happen before any chown'
cat "$TEST_ROOT/nested-bind.log"
umount "$MOUNTED_PATH"
MOUNTED_PATH=''
printf 'CASE nested-bind-fails-before-chown: PASS\n'

DATA="$TEST_ROOT/data"
printf 'marker-alias-data' > "$OUTSIDE/marker-alias"
ln "$OUTSIDE/marker-alias" "$DATA/.nexterm-entrypoint-owner-v1"
/usr/bin/chown 0:0 "$DATA/nested/db.sqlite"
before_chown="$(chown_count)"
run_main_expect_failure "$TEST_ROOT/hardlink-marker.log"
assert_log_contains "$TEST_ROOT/hardlink-marker.log" 'hardlinked non-directories' 'hardlinked marker error contract'
assert_log_contains "$TEST_ROOT/hardlink-marker.log" "$DATA/.nexterm-entrypoint-owner-v1" 'hardlinked marker path must be listed'
assert_owner "$OUTSIDE/marker-alias" 0:0
assert_owner "$DATA/nested/db.sqlite" 0:0
assert_eq "$(cat "$OUTSIDE/marker-alias")" marker-alias-data 'hardlinked marker payload'
assert_eq "$(chown_count)" "$before_chown" 'hardlinked marker must fail before any chown'
cat "$TEST_ROOT/hardlink-marker.log"
printf 'CASE hardlinked-old-marker-fails-before-chown: PASS\n'
rm "$DATA/.nexterm-entrypoint-owner-v1" "$OUTSIDE/marker-alias"

printf 'migration-alias-data' > "$OUTSIDE/migration-alias"
ln "$OUTSIDE/migration-alias" "$DATA/nested/hardlinked-file"
run_main_expect_failure "$TEST_ROOT/hardlink-migration.log"
assert_log_contains "$TEST_ROOT/hardlink-migration.log" "$DATA/nested/hardlinked-file" 'migration hardlink path must be listed'
assert_owner "$OUTSIDE/migration-alias" 0:0
assert_owner "$DATA/nested/db.sqlite" 0:0
assert_eq "$(cat "$OUTSIDE/migration-alias")" migration-alias-data 'migration hardlink payload'
assert_eq "$(chown_count)" "$before_chown" 'migration hardlink must fail before any chown'
cat "$TEST_ROOT/hardlink-migration.log"
printf 'CASE migration-hardlink-fails-before-chown: PASS\n'
rm "$DATA/nested/hardlinked-file" "$OUTSIDE/migration-alias"

printf 'key-hardlink-data' > "$OUTSIDE/key-hardlink"
/usr/bin/chown 10001:10001 "$OUTSIDE/key-hardlink"
chmod 0600 "$OUTSIDE/key-hardlink"
rm "$DATA/master.key"
ln "$OUTSIDE/key-hardlink" "$DATA/master.key"
run_main_expect_failure "$TEST_ROOT/hardlink-key.log"
assert_log_contains "$TEST_ROOT/hardlink-key.log" "$DATA/master.key" 'master key hardlink path must be listed'
assert_owner "$OUTSIDE/key-hardlink" 10001:10001
assert_eq "$(cat "$OUTSIDE/key-hardlink")" key-hardlink-data 'master key hardlink root preflight payload'
assert_eq "$(stat -c '%a' "$OUTSIDE/key-hardlink")" 600 'master key hardlink root preflight mode'
assert_eq "$(chown_count)" "$before_chown" 'master key hardlink must fail before any chown'
cat "$TEST_ROOT/hardlink-key.log"
printf 'CASE master-key-hardlink-root-preflight: PASS\n'

/usr/bin/chown 10001:10001 "$DATA/nested/db.sqlite"
chmod 0640 "$DATA/nested/db.sqlite"
NEXTERM_DATA_DIR="$DATA" \
NEXTERM_LAZYCAT_VAULT_MASTER_KEY='atomic-key' \
REPORT_FILE="$REPORT" \
  /usr/bin/setpriv --reuid=10001 --regid=10001 --clear-groups \
  /usr/local/bin/nexterm-server-entrypoint
assert_owner "$DATA/master.key" 10001:10001
assert_eq "$(stat -c '%h' "$DATA/master.key")" 1 'atomic master key must be an independent inode'
assert_eq "$(cat "$DATA/master.key")" atomic-key 'atomic master key payload'
assert_eq "$(cat "$OUTSIDE/key-hardlink")" key-hardlink-data 'outside hardlink alias payload must remain unchanged'
assert_owner "$OUTSIDE/key-hardlink" 10001:10001
assert_eq "$(stat -c '%a' "$OUTSIDE/key-hardlink")" 600 'outside hardlink alias mode must remain unchanged'
if [ "$(stat -c '%i' "$DATA/master.key")" = "$(stat -c '%i' "$OUTSIDE/key-hardlink")" ]; then
  fail 'atomic master key replacement retained the old hardlink inode'
fi
printf 'CASE master-key-atomic-rename-preserves-outside-alias: PASS\n'

printf 'key-symlink-data' > "$OUTSIDE/key-symlink"
/usr/bin/chown 10001:10001 "$OUTSIDE/key-symlink"
chmod 0600 "$OUTSIDE/key-symlink"
rm "$DATA/master.key"
ln -s "$OUTSIDE/key-symlink" "$DATA/master.key"
run_main_expect_failure "$TEST_ROOT/key-symlink.log"
assert_log_contains "$TEST_ROOT/key-symlink.log" 'master key must not be a symbolic link' 'master key symlink error contract'
[ -L "$DATA/master.key" ] || fail 'rejected master key symlink was removed'
assert_eq "$(cat "$OUTSIDE/key-symlink")" key-symlink-data 'master key symlink target payload'
assert_owner "$OUTSIDE/key-symlink" 10001:10001
printf 'CASE master-key-symlink-target-unchanged: PASS\n'

rm "$DATA/master.key"
mkdir "$DATA/master.key"
run_main_expect_failure "$TEST_ROOT/key-rename-failure.log"
[ -d "$DATA/master.key" ] || fail 'failed atomic rename removed the pre-existing directory'
leftover_key_tmp="$(/usr/bin/find "$DATA" -maxdepth 1 -name '.master.key.*' -print -quit)"
assert_eq "$leftover_key_tmp" '' 'failed atomic rename must clean its controlled temporary file'
printf 'CASE master-key-rename-failure-cleanup: PASS\n'
rmdir "$DATA/master.key"

ln -s "$DATA" "$TEST_ROOT/data-link"
DATA="$TEST_ROOT/data-link"
run_main_expect_failure "$TEST_ROOT/data-symlink.log"
assert_log_contains "$TEST_ROOT/data-symlink.log" 'data directory must not be a symbolic link' 'data directory symlink error contract'
printf 'CASE data-directory-symlink-rejected: PASS\n'

printf 'entrypoint R1/R2 ownership, mount, hardlink, atomic-key and restart contracts: PASS\n'
