import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createBinaryManifest } from "../../scripts/lib/build-manifest.mjs";
import { readHostHelperProtocolMetadata } from "./helper-metadata.mjs";
import { helperTargets } from "./helper-matrix.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github/scripts/verify-reused-binary.mjs");
const HEAD = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
const VERSION = "1.2.3";
const CURRENT_METADATA = readHostHelperProtocolMetadata({ cwd: ROOT });

function fixture(t, target) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-reused-helper-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const file = path.join(directory, target.name);
  const bytes = Buffer.from(`fixture ${target.name}\n`);
  fs.writeFileSync(file, bytes);
  const manifest = createBinaryManifest({
    file,
    id: `helper-${target.os}-${target.arch}`,
    kind: "helper",
    goos: target.os,
    goarch: target.arch,
    goarm: target.goarm,
    tags: ["production"],
    cgo: "0",
    stripped: true,
    version: VERSION,
    commit: HEAD,
    source_date_epoch: 1,
  });
  Object.assign(manifest, {
    protocol: CURRENT_METADATA.protocol,
    features: [...CURRENT_METADATA.features],
    sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
    binary_metadata_verified: true,
    metadata_host: { os: target.os, arch: target.arch },
  });
  fs.writeFileSync(`${file}.manifest.json`, JSON.stringify(manifest));
  return { file, manifest };
}

function verify(file) {
  const name = path.basename(file).replace(/\.exe$/, "");
  const match = /^nexterm-helper-(linux|darwin|windows)-([^.]+)$/.exec(name);
  return spawnSync(process.execPath, [SCRIPT, file, `helper-${match[1]}-${match[2]}`, match[1], match[2], "0"], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: VERSION },
    encoding: "utf8",
  });
}

for (const target of helperTargets()) {
  test(`reused ${target.os}/${target.arch} helper accepts matching manifest evidence`, (t) => {
    const { file } = fixture(t, target);
    const result = verify(file);
    assert.equal(result.status, 0, result.stderr);
    assert.match(result.stderr, /verified reused production binary sha256:/);
  });
}

for (const [name, edit, expected] of [
  ["wrong protocol", (manifest) => { manifest.protocol = 1; }, /protocol/],
  ["wrong features", (manifest) => { manifest.features = ["exec"]; }, /features/],
  ["missing features", (manifest) => { delete manifest.features; }, /features/],
  ["stale placeholder version", (manifest) => { manifest.version = "0.0.0"; }, /version/],
  ["placeholder runtime sha256", (manifest) => { manifest.sha256 = "0".repeat(64); }, /sha256/],
  ["unexecuted cross metadata", (manifest) => {
    manifest.binary_metadata_verified = false;
    delete manifest.sha256;
    delete manifest.protocol;
    delete manifest.features;
    delete manifest.metadata_host;
  }, /not verified/],
]) {
  test(`reused helper rejects ${name}`, (t) => {
    const target = helperTargets().find((entry) => entry.os === "linux" && entry.arch === "armv7");
    const { file, manifest } = fixture(t, target);
    edit(manifest);
    fs.writeFileSync(`${file}.manifest.json`, JSON.stringify(manifest));
    const result = verify(file);
    assert.equal(result.status, 1);
    assert.match(result.stderr, expected);
  });
}

test("reused helper rejects an unknown target identity before trusting its manifest", (t) => {
  const target = helperTargets().find((entry) => entry.os === "linux" && entry.arch === "armv7");
  const { file } = fixture(t, target);
  const result = spawnSync(process.execPath, [SCRIPT, file, "helper-linux-madeup", "linux", "madeup", "0"], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: VERSION },
    encoding: "utf8",
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /unsupported helper identity/);
});
