import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const FIXTURE_FILES = [
  "go.mod",
  "package.json",
  "wails.json",
  ".github/native-matrix.json",
  ".github/scripts/helper-matrix.mjs",
  ".github/scripts/helper-metadata.mjs",
  "scripts/build.mjs",
  "scripts/test-production.mjs",
  "scripts/lib/build-manifest.mjs",
  "scripts/lib/privacy-runtime.mjs",
  "scripts/lib/makensis.mjs",
  "scripts/lib/mobile.mjs",
  "scripts/lib/semver.mjs",
  "scripts/lib/spawn-spec.mjs",
];

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-production-test-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const name of FIXTURE_FILES) {
    const destination = path.join(root, name);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(path.join(ROOT, name), destination);
  }
  fs.mkdirSync(path.join(root, "cmd/nexterm-desktop"), { recursive: true });
  const bin = path.join(root, "bin");
  fs.mkdirSync(bin);
  writeExecutable(bin, "pnpm", `#!/bin/sh
set -eu
printf '%s\\n' "$*" >> "$PNPM_LOG"
if [ "$1" = "exec" ] && [ "$2" = "tsc" ]; then exit 0; fi
if [ "$1" = "exec" ] && [ "$2" = "vite" ] && [ "$3" = "build" ]; then
  rm -rf dist
  mkdir -p dist/assets
  printf '%s\\n' '<html><script type="module" src="/assets/app.js"></script></html>' > dist/index.html
  printf '%s\\n' 'console.log("production fixture");' > dist/assets/app.js
  exit 0
fi
printf 'unexpected pnpm arguments: %s\\n' "$*" >&2
exit 2
`);
  writeExecutable(bin, "go", `#!/bin/sh
set -eu
printf '%s\\n' "$*" >> "$GO_LOG"
if [ "$*" != "test -tags production ./cmd/nexterm-desktop" ]; then
  printf 'unexpected go arguments: %s\\n' "$*" >&2
  exit 2
fi
test -f cmd/nexterm-desktop/dist/index.html
test -f cmd/nexterm-desktop/dist/assets/app.js
`);
  const env = {
    ...process.env,
    PATH: `${bin}${path.delimiter}${process.env.PATH || ""}`,
    SOURCE_DATE_EPOCH: "1",
    NEXTERM_RELEASE_VERSION: "1.2.3",
    PNPM_LOG: path.join(root, "pnpm.log"),
    GO_LOG: path.join(root, "go.log"),
    DIST_SOURCE: "",
    DIST_MANIFEST: "",
  };
  return { root, env };
}

function writeExecutable(directory, name, content) {
  const file = path.join(directory, name);
  fs.writeFileSync(file, content, { mode: 0o755 });
}

function runProduction(root, env) {
  return spawnSync(process.execPath, [path.join(root, "scripts/test-production.mjs")], {
    cwd: root,
    env,
    encoding: "utf8",
  });
}

function logLines(file) {
  if (!fs.existsSync(file)) return [];
  return fs.readFileSync(file, "utf8").trim().split("\n").filter(Boolean);
}

function prepareVerifiedDist(root, env) {
  const build = spawnSync(process.execPath, [path.join(root, "scripts/build.mjs"), "frontend", "--repro-check"], {
    cwd: root,
    env,
    encoding: "utf8",
  });
  assert.equal(build.status, 0, build.stderr || build.stdout);
  fs.mkdirSync(path.join(root, "verified/target"), { recursive: true });
  fs.renameSync(path.join(root, "dist"), path.join(root, "verified/dist"));
  fs.renameSync(path.join(root, "target/dist-manifest.json"), path.join(root, "verified/target/dist-manifest.json"));
  fs.rmSync(env.PNPM_LOG, { force: true });
  fs.rmSync(env.GO_LOG, { force: true });
  return {
    ...env,
    DIST_SOURCE: "verified/dist",
    DIST_MANIFEST: "verified/target/dist-manifest.json",
  };
}

test("standalone production test keeps the fresh reproducible frontend fallback", (t) => {
  const { root, env } = fixture(t);
  const result = runProduction(root, env);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  assert.deepEqual(logLines(env.PNPM_LOG), [
    "exec tsc -p tsconfig.json --noEmit",
    "exec vite build",
    "exec vite build",
  ]);
  assert.deepEqual(logLines(env.GO_LOG), ["test -tags production ./cmd/nexterm-desktop"]);
  assert.equal(fs.existsSync(path.join(root, "cmd/nexterm-desktop/dist")), false);
});

test("production test consumes a verified dist without rerunning pnpm", (t) => {
  const { root, env: baseEnv } = fixture(t);
  const env = prepareVerifiedDist(root, baseEnv);
  const result = runProduction(root, env);
  assert.equal(result.status, 0, result.stderr || result.stdout);
  assert.deepEqual(logLines(env.PNPM_LOG), []);
  assert.deepEqual(logLines(env.GO_LOG), ["test -tags production ./cmd/nexterm-desktop"]);
  assert.equal(
    fs.readFileSync(path.join(root, "dist/index.html"), "utf8"),
    fs.readFileSync(path.join(root, "verified/dist/index.html"), "utf8"),
  );
  assert.equal(fs.existsSync(path.join(root, "cmd/nexterm-desktop/dist")), false);
});

test("production test rejects a tampered consumed dist before running Go tests", (t) => {
  const { root, env: baseEnv } = fixture(t);
  const env = prepareVerifiedDist(root, baseEnv);
  fs.appendFileSync(path.join(root, "verified/dist/assets/app.js"), "tampered\n");
  const result = runProduction(root, env);
  assert.equal(result.status, 1, result.stdout);
  assert.match(result.stderr, /verified dist consumption failed/);
  assert.deepEqual(logLines(env.PNPM_LOG), []);
  assert.deepEqual(logLines(env.GO_LOG), []);
  assert.equal(fs.existsSync(path.join(root, "cmd/nexterm-desktop/dist")), false);
});

test("production test rejects incomplete dist consumption settings", (t) => {
  const { root, env: baseEnv } = fixture(t);
  for (const override of [
    { DIST_SOURCE: "verified/dist", DIST_MANIFEST: "" },
    { DIST_SOURCE: "", DIST_MANIFEST: "verified/target/dist-manifest.json" },
  ]) {
    const result = runProduction(root, { ...baseEnv, ...override });
    assert.equal(result.status, 1, result.stdout);
    assert.match(result.stderr, /DIST_SOURCE and DIST_MANIFEST must be set together/);
    assert.deepEqual(logLines(baseEnv.PNPM_LOG), []);
    assert.deepEqual(logLines(baseEnv.GO_LOG), []);
  }
});
