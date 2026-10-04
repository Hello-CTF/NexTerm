import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { createBinaryManifest, createDistManifest } from "./build-manifest.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const BUILD = path.join(ROOT, "scripts", "build.mjs");
const DIST = path.join(ROOT, "dist");

function repoIdentity() {
  const version = JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info.version;
  const commit = spawnSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" });
  if (!version || commit.status !== 0) return null;
  return { version, commit: commit.stdout.trim() };
}

const identity = repoIdentity();

function runBuild(args) {
  return spawnSync(process.execPath, [BUILD, ...args], { cwd: ROOT, encoding: "utf8", timeout: 120_000 });
}

function makeTempDirectory(t, prefix) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return directory;
}

function preserveDist(t) {
  const backup = `${DIST}.cli-test-${process.pid}-${Date.now()}`;
  const existed = fs.existsSync(DIST);
  if (existed) fs.renameSync(DIST, backup);
  t.after(() => {
    fs.rmSync(DIST, { recursive: true, force: true });
    if (existed) fs.renameSync(backup, DIST);
  });
}

function makeDistFixture(t) {
  const directory = makeTempDirectory(t, "nexterm-dist-fixture-");
  fs.mkdirSync(path.join(directory, "assets"));
  fs.writeFileSync(path.join(directory, "index.html"), '<!doctype html>\n<html><body><script type="module" src="/assets/app-abc123.js"></script></body></html>\n');
  fs.writeFileSync(path.join(directory, "assets", "app-abc123.js"), "console.log('nexterm');\n");
  return directory;
}

function writeDistManifest(t, distDirectory) {
  const manifestPath = path.join(makeTempDirectory(t, "nexterm-dist-manifest-"), "dist-manifest.json");
  fs.writeFileSync(manifestPath, `${JSON.stringify(createDistManifest(distDirectory, identity), null, 2)}\n`);
  return manifestPath;
}

function writeBinaryManifest(t, binary, overrides = {}) {
  const manifestPath = path.join(makeTempDirectory(t, "nexterm-binary-manifest-"), "binary-manifest.json");
  const manifest = createBinaryManifest({
    file: binary,
    id: "desktop-darwin-arm64",
    kind: "desktop",
    goos: "darwin",
    goarch: "arm64",
    tags: ["production"],
    cgo: "1",
    stripped: true,
    version: identity.version,
    commit: identity.commit,
    source_date_epoch: 1_700_000_000,
    ...overrides,
  });
  fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return manifestPath;
}

function requireIdentity(t) {
  if (!identity) {
    t.skip("wails.json or git metadata is unavailable");
    return false;
  }
  return true;
}

test("help documents the reuse harness", () => {
  const result = runBuild(["help"]);
  assert.equal(result.status, 0);
  assert.match(result.stdout, /--consume-dist/);
  assert.match(result.stdout, /--package-only/);
});

test("frontend --consume-dist restores a verified dist without rebuilding", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const manifest = writeDistManifest(t, dist);
  const result = runBuild(["frontend", `--consume-dist=${dist}`, `--dist-manifest=${manifest}`]);
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /frontend dist consumed/);
  assert.equal(fs.readFileSync(path.join(DIST, "assets", "app-abc123.js"), "utf8"), "console.log('nexterm');\n");
  assert.ok(fs.existsSync(path.join(DIST, "index.html")));
});

test("frontend --consume-dist rejects a tampered dist", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const manifest = writeDistManifest(t, dist);
  fs.writeFileSync(path.join(dist, "assets", "app-abc123.js"), "console.log('tampered');\n");
  const result = runBuild(["frontend", `--consume-dist=${dist}`, `--dist-manifest=${manifest}`]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /verified dist consumption failed/);
  assert.match(result.stderr, /assets\/app-abc123\.js/);
});

test("frontend --consume-dist rejects a manifest from another commit", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const manifestPath = path.join(makeTempDirectory(t, "nexterm-dist-manifest-"), "dist-manifest.json");
  const manifest = createDistManifest(dist, { version: identity.version, commit: "ffffffffffffffffffffffffffffffffffffffff" });
  fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  const result = runBuild(["frontend", `--consume-dist=${dist}`, `--dist-manifest=${manifestPath}`]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /commit/);
});

test("frontend --consume-dist refuses --repro-check", (t) => {
  if (!requireIdentity(t)) return;
  const dist = makeDistFixture(t);
  const manifest = writeDistManifest(t, dist);
  const result = runBuild(["frontend", `--consume-dist=${dist}`, `--dist-manifest=${manifest}`, "--repro-check"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /--repro-check belongs to the producing job/);
});

test("server --consume-dist is rejected", (t) => {
  if (!requireIdentity(t)) return;
  const dist = makeDistFixture(t);
  const result = runBuild(["server", "--release", "--os=linux", "--arch=amd64", `--consume-dist=${dist}`]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /--consume-dist is only meaningful for the frontend command and desktop builds/);
});

test("desktop --package-only requires --package and --release", (t) => {
  if (!requireIdentity(t)) return;
  const withoutPackage = runBuild(["desktop", "--release", "--package-only", "--os=darwin", "--arch=arm64"]);
  assert.equal(withoutPackage.status, 1);
  assert.match(withoutPackage.stderr, /--package-only requires --package/);
  const withoutRelease = runBuild(["desktop", "--package-only", "--package", "--os=darwin", "--arch=arm64"]);
  assert.equal(withoutRelease.status, 1);
  assert.match(withoutRelease.stderr, /--package-only requires --release/);
});

test("desktop --package-only refuses --smoke and --repro-check", (t) => {
  if (!requireIdentity(t)) return;
  const withSmoke = runBuild(["desktop", "--release", "--package", "--package-only", "--smoke", "--consume-dist=unused", "--os=darwin", "--arch=arm64"]);
  assert.equal(withSmoke.status, 1);
  assert.match(withSmoke.stderr, /--package-only refuses --smoke/);
  const withRepro = runBuild(["desktop", "--release", "--package", "--package-only", "--repro-check", "--consume-dist=unused", "--os=darwin", "--arch=arm64"]);
  assert.equal(withRepro.status, 1);
  assert.match(withRepro.stderr, /--repro-check belongs to the producing job/);
});

test("desktop --package-only requires --consume-dist", (t) => {
  if (!requireIdentity(t)) return;
  const binary = path.join(makeTempDirectory(t, "nexterm-binary-"), "nexterm-desktop-darwin-arm64");
  fs.writeFileSync(binary, "fake-production-binary\n");
  const manifest = writeBinaryManifest(t, binary);
  const result = runBuild(["desktop", "--release", "--package", "--package-only", `--out=${binary}`, `--binary-manifest=${manifest}`, "--os=darwin", "--arch=arm64"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /--package-only requires --consume-dist/);
});

test("desktop --package-only requires the existing production binary", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const distManifest = writeDistManifest(t, dist);
  const binary = path.join(makeTempDirectory(t, "nexterm-binary-"), "nexterm-desktop-darwin-arm64");
  const result = runBuild(["desktop", "--release", "--package", "--package-only", `--out=${binary}`, `--consume-dist=${dist}`, `--dist-manifest=${distManifest}`, "--os=darwin", "--arch=arm64"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /--package-only requires the existing production binary/);
});

test("desktop --package-only rejects a smoke-tagged binary manifest", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const distManifest = writeDistManifest(t, dist);
  const binary = path.join(makeTempDirectory(t, "nexterm-binary-"), "nexterm-desktop-darwin-arm64");
  fs.writeFileSync(binary, "fake-smoke-binary\n");
  const manifest = writeBinaryManifest(t, binary, { tags: ["production", "smoke"] });
  const result = runBuild(["desktop", "--release", "--package", "--package-only", `--out=${binary}`, `--binary-manifest=${manifest}`, `--consume-dist=${dist}`, `--dist-manifest=${distManifest}`, "--os=darwin", "--arch=arm64"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /smoke-tagged test binaries must never be substituted for production release binaries/);
});

test("desktop --package-only rejects a binary that drifted from its manifest", (t) => {
  if (!requireIdentity(t)) return;
  preserveDist(t);
  const dist = makeDistFixture(t);
  const distManifest = writeDistManifest(t, dist);
  const binary = path.join(makeTempDirectory(t, "nexterm-binary-"), "nexterm-desktop-darwin-arm64");
  fs.writeFileSync(binary, "fake-production-binary\n");
  const manifest = writeBinaryManifest(t, binary);
  fs.writeFileSync(binary, "drifted-binary-content\n");
  const result = runBuild(["desktop", "--release", "--package", "--package-only", `--out=${binary}`, `--binary-manifest=${manifest}`, `--consume-dist=${dist}`, `--dist-manifest=${distManifest}`, "--os=darwin", "--arch=arm64"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /binary manifest validation failed/);
  assert.match(result.stderr, /sha256 mismatch/);
});

test("report keeps the release-evidence sha256 and size identity intact", (t) => {
  if (!requireIdentity(t)) return;
  const directory = makeTempDirectory(t, "nexterm-report-");
  const file = path.join(directory, "NexTerm_0.0.0_aarch64.dmg");
  fs.writeFileSync(file, crypto.randomBytes(4096));
  const reportPath = path.join(directory, "report.json");
  const result = runBuild(["report", "--kind=desktop-dmg", "--os=darwin", "--arch=arm64", `--file=${file}`, `--output=${reportPath}`, "--require-evidence"]);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
  const content = fs.readFileSync(file);
  assert.equal(report.schema_version, 2);
  assert.equal(report.id, "desktop-dmg-darwin-arm64");
  assert.equal(report.version, identity.version);
  assert.equal(report.commit, identity.commit);
  assert.equal(report.status, "passed");
  assert.equal(report.artifact.sha256, crypto.createHash("sha256").update(content).digest("hex"));
  assert.equal(report.artifact.size_bytes, content.length);
  assert.equal(report.artifact.stripped, true);
  assert.ok(report.assertions.length > 0);
  assert.ok(report.assertions.every((assertion) => assertion.status === "passed"));
});
