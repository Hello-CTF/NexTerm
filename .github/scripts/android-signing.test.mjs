#!/usr/bin/env node
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const CHECK_SEMVER = path.join(ROOT, "scripts", "check-semver.mjs");
const SETUP_SIGNING = path.join(ROOT, "build", "android", "scripts", "setup-signing.sh");

function makeSetupSigningFixture(t, { existingMode = 0o600, failure = "" } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-signing-r2-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const runner = path.join(root, "runner");
  fs.mkdirSync(runner);
  const envFile = path.join(root, "github.env");
  const target = path.join(runner, "nexterm-android-release.keystore");
  if (existingMode !== null) {
    fs.writeFileSync(target, "previous keystore");
    fs.chmodSync(target, existingMode);
  }
  let nodeOptions = "";
  if (failure) {
    const preload = path.join(root, "failure.cjs");
    const source = failure === "write"
      ? `const fs = require("node:fs"); const original = fs.writeFileSync; fs.writeFileSync = function(file, ...args) { if (typeof file === "number") throw new Error("injected write failure"); return original.call(fs, file, ...args); };`
      : `const fs = require("node:fs"); fs.closeSync = function() { throw new Error("injected close failure"); };`;
    fs.writeFileSync(preload, source);
    nodeOptions = `--require=${preload}`;
  }
  const result = spawnSync("bash", [SETUP_SIGNING], {
    env: {
      ...process.env,
      RUNNER_TEMP: runner,
      GITHUB_ENV: envFile,
      NEXTERM_ANDROID_KEYSTORE_BASE64: Buffer.from("replacement keystore").toString("base64"),
      ANDROID_KEYSTORE_PASSWORD: "dummy-password",
      ANDROID_KEY_ALIAS: "dummy-alias",
      ANDROID_KEY_PASSWORD: "dummy-key-password",
      NODE_OPTIONS: nodeOptions,
    },
    encoding: "utf8",
    timeout: 5000,
  });
  return { result, runner, envFile, target };
}

for (const existingMode of [0o600, 0o644]) {
  test(`setup-signing leaves a pre-existing ${existingMode.toString(8)} keystore at 0600 without exporting secrets`, (t) => {
    const { result, runner, envFile, target } = makeSetupSigningFixture(t, { existingMode });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(fs.statSync(target).mode & 0o777, 0o600);
    assert.equal(fs.readFileSync(target, "utf8"), "replacement keystore");
    assert.deepEqual(fs.readdirSync(runner), ["nexterm-android-release.keystore"]);
    const environment = fs.readFileSync(envFile, "utf8");
    assert.equal(environment, `ANDROID_KEYSTORE_FILE=${target}\n`);
    assert.ok(!environment.includes("PASSWORD") && !environment.includes("ALIAS") && !environment.includes("dummy-"));
  });
}

for (const failure of ["write", "close"]) {
  test(`setup-signing cleans temporary and pre-existing material after an injected ${failure} failure`, (t) => {
    const { result, runner, envFile, target } = makeSetupSigningFixture(t, { existingMode: 0o644, failure });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, new RegExp(`injected ${failure} failure`));
    assert.equal(fs.existsSync(target), false);
    assert.deepEqual(fs.readdirSync(runner), []);
    assert.equal(fs.existsSync(envFile), false);
  });
}

test("shared Android version CLI emits tag-derived values and rejects invalid prerelease numbers", () => {
  const valid = spawnSync(process.execPath, [CHECK_SEMVER, "--android-json", "1.2.3-rc.1+build.5"], { encoding: "utf8" });
  assert.equal(valid.status, 0, valid.stderr);
  assert.deepEqual(JSON.parse(valid.stdout), { versionName: "1.2.3-rc.1+build.5", versionCode: 1002003 });
  const invalid = spawnSync(process.execPath, [CHECK_SEMVER, "--android-json", "1.2.3-01"], { encoding: "utf8" });
  assert.equal(invalid.status, 1);
});
