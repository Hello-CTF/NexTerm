import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createBinaryManifest } from "../../scripts/lib/build-manifest.mjs";
import { readHostHelperProtocolMetadata, requiresHelperExecutionEvidence } from "./helper-metadata.mjs";
import { helperTargets } from "./helper-matrix.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SERVER_PACK = path.join(ROOT, "scripts/pack-linux-server.sh");
const LAZYCAT_BUILD = path.join(ROOT, "lazycat/image/build-server.sh");
const HEAD = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
const VERSION = "1.2.3";
const CURRENT_METADATA = readHostHelperProtocolMetadata({ cwd: ROOT });

function fixture(t, edit, { nonRepresentativeNotRun = false } = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-helper-consumer-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const helpers = path.join(directory, "helpers");
  const web = path.join(directory, "web");
  const out = path.join(directory, "out");
  const bin = path.join(directory, "bin");
  const log = path.join(directory, "consumer.log");
  fs.mkdirSync(helpers, { recursive: true });
  fs.mkdirSync(web, { recursive: true });
  fs.mkdirSync(bin, { recursive: true });
  fs.writeFileSync(path.join(web, "index.html"), "<html>server packaging fixture</html>\n");
  const server = path.join(directory, "server");
  fs.writeFileSync(server, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
  fs.writeFileSync(path.join(bin, "file"), "#!/bin/sh\nprintf 'server-helpers-accepted\\n' >> \"$CONSUMER_LOG\"\nexit 42\n", { mode: 0o755 });
  fs.writeFileSync(path.join(bin, "node"), `#!/bin/sh
if [ "$1" = "scripts/build.mjs" ] && [ "$2" = "frontend" ]; then
  printf 'lazycat-helpers-accepted\\n' >> "$CONSUMER_LOG"
  exit 42
fi
exec "$REAL_NODE" "$@"
`, { mode: 0o755 });
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
    if (nonRepresentativeNotRun && !requiresHelperExecutionEvidence(target.os, target.arch)) {
      Object.assign(manifest, { binary_metadata_verified: false, metadata_status: "not-run", cross_compiled: true, untested: true });
      delete manifest.protocol;
      delete manifest.features;
      delete manifest.sha256;
      delete manifest.metadata_host;
    }
    fs.writeFileSync(`${file}.manifest.json`, JSON.stringify(manifest));
  }
  const env = { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH || ""}`, CONSUMER_LOG: log, REAL_NODE: process.execPath };
  return { directory, helpers, web, out, server, log, env };
}

function runServer({ helpers, web, out, server, env }) {
  return spawnSync("bash", [SERVER_PACK, "--arch=amd64", `--bin=${server}`, `--helpers-dir=${helpers}`, `--web=${web}`, `--out=${out}`, "--require-evidence"], {
    cwd: ROOT,
    env: { ...env, NEXTERM_RELEASE_VERSION: VERSION },
    encoding: "utf8",
  });
}

function runLazyCat({ helpers, env }) {
  return spawnSync("bash", [LAZYCAT_BUILD], {
    cwd: ROOT,
    env: { ...env, NEXTERM_RELEASE_VERSION: VERSION, LZC_VERSION: VERSION, LZC_ARCH: "amd64", NEXTERM_HELPERS_DIR: helpers },
    encoding: "utf8",
  });
}

test("validator fixture: server helper gate accepts three verified and six not-run", (t) => {
  const current = fixture(t, () => {}, { nonRepresentativeNotRun: true });
  const result = runServer(current);
  assert.equal(result.status, 42, result.stderr || result.stdout);
  assert.equal(fs.readFileSync(current.log, "utf8"), "server-helpers-accepted\n");
  assert.equal((result.stderr.match(/verified reused production binary/g) || []).length, 3);
  assert.equal((result.stderr.match(/metadata not-run/g) || []).length, 6);
});

test("validator fixture: LazyCat helper gate accepts three verified and six not-run", (t) => {
  const current = fixture(t, () => {}, { nonRepresentativeNotRun: true });
  const result = runLazyCat(current);
  assert.equal(result.status, 42, result.stderr || result.stdout);
  assert.equal(fs.readFileSync(current.log, "utf8"), "lazycat-helpers-accepted\n");
  assert.equal((result.stderr.match(/verified reused production binary/g) || []).length, 3);
  assert.equal((result.stderr.match(/metadata not-run/g) || []).length, 6);
});

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
