#!/usr/bin/env bash
set -euo pipefail

required=(
  NEXTERM_ANDROID_KEYSTORE_BASE64
  RUNNER_TEMP
  GITHUB_ENV
)
for name in "${required[@]}"; do
  if [[ -z "${!name:-}" ]]; then
    echo "Android release signing requires $name" >&2
    exit 1
  fi
done

umask 077
export ANDROID_KEYSTORE_FILE="$RUNNER_TEMP/nexterm-android-release.keystore"
setup_complete=false
trap 'if [[ "$setup_complete" != "true" ]]; then rm -f "$ANDROID_KEYSTORE_FILE"; fi' EXIT
node <<'NODE'
const crypto = require("node:crypto");
const fs = require("node:fs");
const encoded = process.env.NEXTERM_ANDROID_KEYSTORE_BASE64.replace(/\s/g, "");
if (!encoded || !/^[A-Za-z0-9+/]*={0,2}$/.test(encoded) || encoded.length % 4 === 1) {
  throw new Error("NEXTERM_ANDROID_KEYSTORE_BASE64 is not valid base64");
}
const bytes = Buffer.from(encoded, "base64");
if (!bytes.length || bytes.toString("base64").replace(/=+$/, "") !== encoded.replace(/=+$/, "")) {
  throw new Error("NEXTERM_ANDROID_KEYSTORE_BASE64 is not canonical base64");
}
const target = process.env.ANDROID_KEYSTORE_FILE;
const temporary = `${target}.${process.pid}.${crypto.randomBytes(16).toString("hex")}.tmp`;
let fd;
let renamed = false;
try {
  fd = fs.openSync(temporary, "wx", 0o600);
  fs.fchmodSync(fd, 0o600);
  fs.writeFileSync(fd, bytes);
  fs.fsyncSync(fd);
  fs.closeSync(fd);
  fd = undefined;
  fs.renameSync(temporary, target);
  renamed = true;
} finally {
  if (fd !== undefined) {
    try { fs.closeSync(fd); } catch {}
  }
  if (!renamed) {
    try { fs.rmSync(temporary, { force: true }); } catch {}
  }
}
NODE

if [[ "$ANDROID_KEYSTORE_FILE" == *$'\n'* || "$ANDROID_KEYSTORE_FILE" == *$'\r'* ]]; then
  echo "ANDROID_KEYSTORE_FILE must be a single-line value" >&2
  exit 1
fi
printf 'ANDROID_KEYSTORE_FILE=%s\n' "$ANDROID_KEYSTORE_FILE" >> "$GITHUB_ENV"
setup_complete=true
