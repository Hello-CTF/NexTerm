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
