import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./verify-server-archive.mjs", import.meta.url));

function sha256(bytes) {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

function writeArchive(directory, root, mainBytes) {
  const stage = path.join(directory, `stage-${root}`);
  fs.mkdirSync(path.join(stage, root), { recursive: true });
  fs.writeFileSync(path.join(stage, root, "nexterm-server"), mainBytes);
  fs.writeFileSync(path.join(stage, root, "nexterm-helper-linux-amd64"), Buffer.from("helper fixture"));
  fs.writeFileSync(path.join(stage, root, "web-index-placeholder"), Buffer.from("placeholder"));
  const archive = path.join(directory, `${root}.tar.gz`);
  execFileSync("tar", ["-czf", archive, "-C", stage, root]);
  return archive;
}

test("server archive byte evidence accepts the intact archive and rejects tampering", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  fs.writeFileSync(`${binary}.manifest.json`, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));
  const archive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  const run = () => spawnSync(process.execPath, [script, archive, binary], { cwd: directory, encoding: "utf8" });

  const accepted = run();
  assert.equal(accepted.status, 0, accepted.stderr);
  assert.match(accepted.stderr, /carries the staged main binary/);

  const tamperedArchive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64-tampered", Buffer.from("server main binary fixture!"));
  const tampered = spawnSync(process.execPath, [script, tamperedArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(tampered.status, 0);
  assert.match(tampered.stderr, /does not match the staged CI binary/);

  fs.appendFileSync(binary, "tampered");
  const stagedMismatch = run();
  assert.notEqual(stagedMismatch.status, 0);
  assert.match(stagedMismatch.stderr, /does not match its CI manifest/);
});

test("server archive byte evidence rejects missing members, manifests and layouts", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-neg-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  const manifestPath = `${binary}.manifest.json`;
  fs.writeFileSync(manifestPath, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));

  const stage = path.join(directory, "stage-empty");
  fs.mkdirSync(path.join(stage, "NexTerm-server_1.2.3_linux_amd64"), { recursive: true });
  const emptyArchive = path.join(directory, "empty.tar.gz");
  execFileSync("tar", ["-czf", emptyArchive, "-C", stage, "NexTerm-server_1.2.3_linux_amd64"]);
  const missing = spawnSync(process.execPath, [script, emptyArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /missing the main binary member/);

  const goodArchive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  fs.rmSync(manifestPath);
  const noManifest = spawnSync(process.execPath, [script, goodArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(noManifest.status, 0);
  assert.match(noManifest.stderr, /manifest is missing/);
  fs.writeFileSync(manifestPath, JSON.stringify({ artifact: { size_bytes: mainBytes.length, sha256: "0".repeat(64) } }));
  const wrongManifest = spawnSync(process.execPath, [script, goodArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(wrongManifest.status, 0);
  assert.match(wrongManifest.stderr, /does not match its CI manifest/);
});
