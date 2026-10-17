import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./stage-server-evidence.mjs", import.meta.url));

test("server evidence stages a clean report and rejects failed, missing and mismatched evidence", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const source = path.join(directory, "download");
  const reportPath = path.join(source, "e2e-sync", "report.json");
  fs.mkdirSync(path.dirname(reportPath), { recursive: true });
  const binary = path.join(directory, "server");
  const bytes = Buffer.from("server fixture");
  fs.writeFileSync(binary, bytes);
  fs.writeFileSync(`${binary}.manifest.json`, JSON.stringify({ artifact: { sha256: crypto.createHash("sha256").update(bytes).digest("hex") } }));
  const passed = JSON.stringify({ status: "passed", failed: 0 });
  fs.writeFileSync(reportPath, passed);
  fs.writeFileSync(path.join(source, "e2e-sync", "server.log"), "acceptance passed");
  const run = () => spawnSync(process.execPath, [script, source, binary], { cwd: directory, encoding: "utf8" });
  const result = run();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.readFileSync(path.join(directory, "target", "e2e-sync", "report.json"), "utf8"), passed);
  assert.equal(fs.readFileSync(path.join(directory, "target", "e2e-sync", "server.log"), "utf8"), "acceptance passed");
  fs.rmSync(path.join(directory, "target"), { recursive: true });
  for (const report of [{ status: "failed", failed: 2 }, { status: "passed", failed: 1 }]) {
    fs.writeFileSync(reportPath, JSON.stringify(report));
    assert.notEqual(run().status, 0);
    assert.equal(fs.existsSync(path.join(directory, "target")), false);
  }
  fs.rmSync(reportPath);
  assert.notEqual(run().status, 0);
  assert.equal(fs.existsSync(path.join(directory, "target")), false);
  fs.writeFileSync(reportPath, passed);
  fs.appendFileSync(binary, "tampered");
  assert.notEqual(run().status, 0);
  assert.equal(fs.existsSync(path.join(directory, "target")), false);
});
