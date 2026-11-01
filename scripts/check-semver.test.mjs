import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import test from "node:test";
import { androidVersionCode, parseReleaseVersion, parseSemVer, releaseVersionProblem } from "./lib/semver.mjs";

const OLD_SCHEME_LATEST = 1 * 1000000 + 3 * 1000 + 6; // v1.3.6

const code = (version) => androidVersionCode(parseSemVer(version));

test("versionCode strictly increases rc.1 < rc.2 < rc.10 < final < next core", () => {
  const ordered = ["1.3.6", "1.4.0-rc.1", "1.4.0-rc.2", "1.4.0-rc.10", "1.4.0-rc.98", "1.4.0", "1.4.1-rc.1", "1.4.1", "1.5.0-rc.1", "1.5.0", "2.0.0-rc.1", "2.0.0"];
  const codes = ordered.map(code);
  for (let index = 1; index < codes.length; index += 1) {
    assert.ok(codes[index] > codes[index - 1], `${ordered[index]} (${codes[index]}) must exceed ${ordered[index - 1]} (${codes[index - 1]})`);
  }
});

test("versionCode exceeds every code of the old scheme issued up to v1.3.6", () => {
  assert.ok(code("1.4.0-rc.1") > OLD_SCHEME_LATEST);
  assert.ok(code("1.3.6") > OLD_SCHEME_LATEST);
});

test("versionCode values and bounds", () => {
  assert.equal(code("1.4.0-rc.1"), 100400001);
  assert.equal(code("1.4.0"), 100400099);
  assert.equal(code("20.999.999"), 2099999999);
  assert.ok(code("20.999.999") <= 2100000000);
  assert.throws(() => code("21.0.0"), /major/);
  assert.throws(() => code("1.1000.0"), /999/);
  assert.throws(() => code("1.0.1000"), /999/);
  assert.throws(() => code("1.4.0-rc.99"), /between 1 and 98/);
});

test("only x.y.z and x.y.z-rc.N are release versions", () => {
  for (const ok of ["1.4.0", "1.4.0-rc.1", "1.4.0-rc.12"]) assert.equal(releaseVersionProblem(ok), null, ok);
  for (const bad of ["0.0.0", "v1.4.0", "1.4", "1.4.0-beta.1", "1.4.0-alpha", "1.4.0-rc", "1.4.0-rc.0", "1.4.0-rc.01", "1.4.0-rc.1.2", "1.4.0-rc.x", "1.4.0-RC.1"]) {
    assert.notEqual(releaseVersionProblem(bad), null, bad);
    assert.equal(parseReleaseVersion(bad), null, bad);
  }
});

function run(...args) {
  return spawnSync(process.execPath, [new URL("./check-semver.mjs", import.meta.url).pathname, ...args], { encoding: "utf8" });
}

test("check-semver CLI", () => {
  assert.equal(run("1.4.0-rc.1").status, 0);
  const beta = run("1.4.0-beta.1");
  assert.equal(beta.status, 1);
  assert.match(beta.stderr, /only x\.y\.z or x\.y\.z-rc\.N/);
  const android = run("--android-json", "1.4.0-rc.2");
  assert.equal(android.status, 0);
  assert.deepEqual(JSON.parse(android.stdout), { versionName: "1.4.0-rc.2", versionCode: 100400002 });
  assert.equal(run("--android-json", "1.4.0-rc.99").status, 1);
  assert.equal(run("--android-json", "21.0.0").status, 1);
  assert.equal(run().status, 1);
});
