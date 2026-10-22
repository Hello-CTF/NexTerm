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
const SERVER_PACK = path.join(ROOT, "scripts/pack-linux-server.sh");
const LAZYCAT_BUILD = path.join(ROOT, "lazycat/image/build-server.sh");
const HEAD = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
const VERSION = "1.2.3";
const CURRENT_METADATA = readHostHelperProtocolMetadata({ cwd: ROOT });

function fixture(t, edit) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-helper-consumer-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const helpers = path.join(directory, "helpers");
  const web = path.join(directory, "web");
  const out = path.join(directory, "out");
  fs.mkdirSync(helpers, { recursive: true });
  fs.mkdirSync(web, { recursive: true });
  fs.writeFileSync(path.join(web, "index.html"), "<html>server packaging fixture</html>\n");
  const server = path.join(directory, "server");
  fs.writeFileSync(server, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  for (const target of helperTargets()) {
    const file = path.join(helpers, target.name);
    const bytes = Buffer.from(`fixture ${target.name}\n`);
    fs.writeFileSync(file, bytes, { mode: 0o755 });
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
    edit(manifest);
    fs.writeFileSync(`${file}.manifest.json`, JSON.stringify(manifest));
  }
  return { directory, helpers, web, out, server };
}

function runServer({ helpers, web, out, server }) {
  return spawnSync("bash", [SERVER_PACK, "--arch=amd64", `--bin=${server}`, `--helpers-dir=${helpers}`, `--web=${web}`, `--out=${out}`, "--require-evidence"], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: VERSION },
    encoding: "utf8",
  });
}

function runLazyCat({ helpers }) {
  return spawnSync("bash", [LAZYCAT_BUILD], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: VERSION, LZC_VERSION: VERSION, LZC_ARCH: "amd64", NEXTERM_HELPERS_DIR: helpers },
    encoding: "utf8",
  });
}

const CASES = [
  ["wrong protocol", (manifest) => { manifest.protocol = 1; }, /protocol .* does not match/],
  ["wrong features", (manifest) => { manifest.features = ["exec"]; }, /features do not match this build/],
  ["missing features", (manifest) => { delete manifest.features; }, /features must be non-empty strings/],
];

for (const [name, edit, expected] of CASES) {
  test(`server pack rejects ${name} in reused helper evidence`, (t) => {
    const current = fixture(t, edit);
    const result = runServer(current);
    assert.notEqual(result.status, 0, result.stdout);
    assert.match(result.stderr, expected);
    assert.equal(fs.existsSync(path.join(current.out, `NexTerm-server_${VERSION}_linux_amd64.tar.gz`)), false);
  });

  test(`LazyCat rejects ${name} in supplied helper evidence`, (t) => {
    const current = fixture(t, edit);
    const result = runLazyCat(current);
    assert.notEqual(result.status, 0, result.stdout);
    assert.match(result.stderr, expected);
  });
}
