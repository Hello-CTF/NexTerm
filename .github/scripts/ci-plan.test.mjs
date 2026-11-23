import assert from "node:assert/strict";
import test from "node:test";
import { planCI } from "./ci-plan.mjs";

const pr = (...files) => planCI({ isPR: true, files }).scope;

test("lockfile and package.json only changes run frontend tests and desktop builds, not the quality job", () => {
  for (const file of ["pnpm-lock.yaml", "package.json"]) {
    const { scope, matrix } = planCI({ isPR: true, files: [file] });
    assert.equal(scope.quality, false, file);
    assert.equal(scope.frontendTests, true, file);
    assert.ok(matrix.include.length > 0, file);
    for (const group of matrix.include) assert.deepEqual(group.kinds, ["desktop"], file);
  }
});

test("frontend source only changes skip the quality job", () => {
  const scope = pr("src/app/foo.tsx");
  assert.equal(scope.quality, false);
  assert.equal(scope.frontendTests, true);
});

test("documentation only changes skip quality and unit tests", () => {
  const scope = pr("README.md");
  assert.equal(scope.quality, false);
  assert.equal(scope.unit, false);
});

test("Go changes run quality and unit tests", () => {
  const scope = pr("internal/server/x.go");
  assert.equal(scope.quality, true);
  assert.equal(scope.unit, true);
});

const targets = (...files) => planCI({ isPR: true, files }).matrix.include.map((group) => `${group.os}/${group.arch}`).sort();
const mobilePlatforms = (...files) => planCI({ isPR: true, files }).mobileMatrix.include.map((entry) => entry.platform);
const SMOKE = ["darwin/arm64", "linux/amd64", "windows/amd64"];

test("an ordinary Go change builds only the smoke targets and compiles Android but not iOS", () => {
  assert.deepEqual(targets("internal/server/x.go"), SMOKE);
  assert.deepEqual(mobilePlatforms("internal/server/x.go"), ["android"]);
  assert.equal(pr("internal/server/x.go").mobile, true);
});

test("operating-system specific Go files add both architectures of those systems only", () => {
  assert.deepEqual(targets("internal/pty/x_windows.go"), [...SMOKE, "windows/arm64"].sort());
  assert.deepEqual(targets("internal/pty/x_darwin_test.go"), [...SMOKE, "darwin/amd64"].sort());
  const linux = targets("internal/pty/x_linux.go");
  assert.ok(linux.includes("linux/arm64") && linux.includes("linux/riscv64"));
  assert.ok(!linux.includes("windows/arm64") && !linux.includes("darwin/amd64"));
  const unix = targets("internal/pty/x_unix.go");
  assert.ok(unix.includes("linux/arm64") && unix.includes("darwin/amd64") && !unix.includes("windows/arm64"));
});

test("fallback platform files, modules and build configuration build every native target", () => {
  const all = targets("go.mod");
  assert.ok(all.length > SMOKE.length);
  for (const file of ["internal/pty/x_other.go", "go.sum", "build/config.yml", ".github/workflows/ci.yml", ".github/native-matrix.json", ".github/actions/go-toolchain/action.yml", "scripts/lib/build-package.mjs", "scripts/pack-linux-server.sh"]) {
    assert.deepEqual(targets(file), all, file);
  }
});

test("templates, test weights and release-only scripts keep the smoke native targets", () => {
  for (const file of [".github/pull_request_template.md", ".github/go-test-weights.json", ".github/scripts/release-notes.mjs"]) {
    assert.deepEqual(targets(file), SMOKE, file);
  }
});

test("script-only changes run the quality job and native smoke builds but not the Go, database or frontend suites", () => {
  for (const file of ["scripts/check-semver.mjs", ".github/scripts/release-notes.mjs", "scripts/lib/retry-busy.test.mjs"]) {
    const scope = pr(file);
    assert.equal(scope.quality, true, file);
    assert.equal(scope.native, true, file);
    assert.equal(scope.unit, false, file);
    assert.equal(scope.database, false, file);
    assert.equal(scope.frontendTests, false, file);
    assert.equal(scope.mobile, false, file);
  }
});

test("scripts that feed other suites keep those suites", () => {
  assert.equal(pr("scripts/i18n.mjs").frontendTests, true);
  assert.equal(pr("scripts/lib/store-subscription-rule.mjs").frontendTests, true);
  assert.deepEqual(mobilePlatforms("scripts/mobile-source.mjs"), ["android", "ios"]);
  assert.equal(pr("scripts/lib/mobile.mjs").mobile, true);
  // Go code under scripts/ still runs the Go suites.
  assert.equal(pr("scripts/test-database/main.go").unit, true);
});

test("iOS compiles for iOS, Darwin and module changes, unknown configuration and full-ci", () => {
  for (const file of ["internal/app/x_ios.go", "internal/app/x_darwin.go", "build/ios/Taskfile.yml", "go.sum", "Taskfile.yml"]) {
    assert.deepEqual(mobilePlatforms(file), ["android", "ios"], file);
  }
  assert.deepEqual(planCI({ isPR: true, files: ["README.md"], labels: ["full-ci"] }).mobileMatrix.include.map((entry) => entry.platform), ["android", "ios"]);
});
