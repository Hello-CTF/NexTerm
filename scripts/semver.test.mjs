import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseReleaseVersion, parseSemVer } from "./lib/semver.mjs";
import { stampVersion } from "./stamp-version.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const CHECK = path.join(ROOT, "scripts", "check-semver.mjs");
const BUILD = path.join(ROOT, "scripts", "build.mjs");

function run(script, version, environment = {}) {
  return spawnSync(process.execPath, [script, ...(script === CHECK ? [version] : ["help"])], {
    cwd: ROOT,
    env: { ...process.env, SOURCE_DATE_EPOCH: "1", NEXTERM_RELEASE_VERSION: version, ...environment },
    encoding: "utf8",
  });
}

for (const [version, core, prerelease, build] of [
  ["1.2.3", "1.2.3", [], []],
  ["1.2.3-rc.1", "1.2.3", ["rc", "1"], []],
  ["1.2.3+build.5", "1.2.3", [], ["build", "5"]],
  ["1.2.3-rc.1+build.5", "1.2.3", ["rc", "1"], ["build", "5"]],
  ["1.2.3-0.0.7+001", "1.2.3", ["0", "0", "7"], ["001"]],
]) {
  test(`release version ${version} parses and passes the CLI and build entry`, () => {
    const parsed = parseReleaseVersion(version);
    assert.ok(parsed);
    assert.equal(parsed.core, core);
    assert.deepEqual(parsed.prerelease, prerelease);
    assert.deepEqual(parsed.build, build);
    assert.equal(run(CHECK, version).status, 0);
    const buildEntry = run(BUILD, version);
    assert.equal(buildEntry.status, 0, buildEntry.stderr);
  });
}

for (const version of [
  "",
  "0.0.0",
  "v1.2.3",
  "01.2.3",
  "1.2.3-",
  "1.2.3+",
  "1.2.3-rc..1",
  "1.2.3+build..5",
  "1.2.3-01",
  "1.2.3-rc_1",
  "1.2.3+build_5",
  "1.2.3-rc.1+build.5 ",
]) {
  test(`invalid release version ${JSON.stringify(version)} is rejected`, () => {
    assert.equal(parseReleaseVersion(version), null);
    assert.equal(run(CHECK, version).status, 1);
  });
}

test("the local build placeholder is not accepted as an explicit release", () => {
  assert.ok(parseSemVer("0.0.0"));
  assert.equal(parseReleaseVersion("0.0.0"), null);
  const local = spawnSync(process.execPath, [BUILD, "help"], {
    cwd: ROOT,
    env: { ...process.env, SOURCE_DATE_EPOCH: "1", NEXTERM_RELEASE_VERSION: "" },
    encoding: "utf8",
  });
  assert.equal(local.status, 0, local.stderr);
  const release = run(BUILD, "0.0.0");
  assert.equal(release.status, 1);
  assert.match(release.stderr, /release version is not a semver version/);
});

test("version stamping accepts combined suffixes and rejects invalid versions without rewriting", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-semver-stamp-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const config = path.join(directory, "config.yml");
  const version = "1.2.3-rc.1+build.5";
  fs.writeFileSync(config, 'info:\n  version: "0.0.0"\n');
  stampVersion([config], version);
  assert.equal(fs.readFileSync(config, "utf8"), `info:\n  version: "${version}"\n`);
  const before = fs.readFileSync(config, "utf8");
  for (const invalid of ["0.0.0", "1.2.3-01", "1.2.3-rc..1", "1.2.3+build..5", "1.2.3-rc_1"]) {
    assert.throws(() => stampVersion([config], invalid), /non-placeholder semver/);
    assert.equal(fs.readFileSync(config, "utf8"), before);
  }
});
