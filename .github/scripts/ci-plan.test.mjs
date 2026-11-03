import assert from "node:assert/strict";
import test from "node:test";
import { planCI } from "./ci-plan.mjs";

const pr = (...files) => planCI({ isPR: true, files }).scope;

test("lockfile and package.json changes run the quality gates and frontend tests", () => {
  for (const file of ["pnpm-lock.yaml", "package.json"]) {
    const scope = pr(file);
    assert.equal(scope.quality, true, file);
    assert.equal(scope.frontendTests, true, file);
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
