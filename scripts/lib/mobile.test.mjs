import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { MOBILE_CONFIG_PATH, MOBILE_STAMP_CONFIG_PATH, sourceState, stampMobileConfig } from "./mobile.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

function ignored(relativePath) {
  return spawnSync("git", ["check-ignore", "-q", relativePath], { cwd: ROOT }).status === 0;
}

test("the mobile stamp and its temporary file never dirty the source tree", () => {
  assert.equal(ignored(MOBILE_STAMP_CONFIG_PATH), true);
  assert.equal(ignored(`${MOBILE_STAMP_CONFIG_PATH}.tmp-${process.pid}`), true);
  assert.equal(ignored(MOBILE_CONFIG_PATH), false);
});

test("stamping a release in a clean repository keeps it clean", () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-stamp-"));
  try {
    const git = (...args) => assert.equal(spawnSync("git", args, { cwd: dir }).status, 0, `git ${args.join(" ")}`);
    git("init", "-q");
    fs.mkdirSync(path.join(dir, "build"));
    fs.copyFileSync(path.join(ROOT, "build/.gitignore"), path.join(dir, "build/.gitignore"));
    fs.writeFileSync(path.join(dir, MOBILE_CONFIG_PATH), 'info:\n  version: "0.0.0"\n');
    git("add", ".");
    git("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-q", "-m", "init");
    // The temporary file exists between the write and the rename; a check at that moment must still see a clean tree.
    fs.writeFileSync(path.join(dir, `${MOBILE_STAMP_CONFIG_PATH}.tmp-1`), "partial");
    assert.equal(sourceState({ cwd: dir }).dirty, false);
    fs.rmSync(path.join(dir, `${MOBILE_STAMP_CONFIG_PATH}.tmp-1`));
    stampMobileConfig({ configPath: path.join(dir, MOBILE_CONFIG_PATH), outPath: path.join(dir, MOBILE_STAMP_CONFIG_PATH), version: "1.2.3-rc.1" });
    assert.match(fs.readFileSync(path.join(dir, MOBILE_STAMP_CONFIG_PATH), "utf8"), /version: "1\.2\.3-rc\.1"/);
    assert.equal(sourceState({ cwd: dir }).dirty, false);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});
