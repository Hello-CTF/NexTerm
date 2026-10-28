import assert from "node:assert/strict";
import test from "node:test";
import { planCI } from "./ci-plan.mjs";
import { selectNativeMatrix } from "./helper-matrix.mjs";

test("documentation and website changes do not allocate application build runners", () => {
  const { scope, matrix } = planCI({ isPR: true, files: ["README.md", "website/src/App.tsx"] });
  assert.ok(Object.values(scope).every((value) => value === false));
  assert.deepEqual(matrix.include, []);
});

test("frontend PRs run coverage and production builds without Go race or database suites", () => {
  const { scope, matrix } = planCI({ isPR: true, files: ["src/App.tsx"] });
  assert.equal(scope.frontendTests, true);
  assert.equal(scope.native, true);
  assert.equal(scope.unit, false);
  assert.equal(scope.database, false);
  assert.equal(scope.mobile, false);
  assert.equal(scope.repro, false);
  assert.ok(matrix.include.some(({ os }) => os === "darwin"));
});

test("Go changes run the full Go suite, real database contracts and both mobile compiles", () => {
  const { scope } = planCI({ isPR: true, files: ["internal/session/session.go"] });
  for (const gate of ["quality", "unit", "database", "mobile", "native", "frontend"]) assert.equal(scope[gate], true, gate);
  assert.equal(scope.frontendTests, false);
  // The race detector is opt-in: it multiplies the suite time.
  assert.equal(scope.race, false);
  assert.equal(planCI({ isPR: true, files: ["internal/session/session.go"], labels: ["full-ci"] }).scope.race, true);
});

test("release builds retain every target and reproducibility without rerunning PR checks", () => {
  const { scope, matrix } = planCI({ isPR: false });
  assert.equal(scope.repro, true);
  for (const gate of ["quality", "unit", "database", "mobile", "frontendTests"]) assert.equal(scope[gate], false, gate);
  const targets = matrix.include.flatMap(({ os, arch, runner, kinds }) => kinds.map((kind) => ({ os, arch, runner, kind })));
  const expected = selectNativeMatrix({ isPR: false }).map(({ os, arch, runner, kind }) => ({ os, arch, runner, kind }));
  const sort = (items) => items.map((item) => JSON.stringify(item)).sort();
  assert.deepEqual(sort(targets), sort(expected));
  assert.ok(matrix.include.length < targets.length);
  for (const group of matrix.include) {
    if (group.representative) assert.ok(group.kinds.includes("helper"));
  }
});

test("full-ci opts into every gate even for a docs PR", () => {
  const plan = planCI({ isPR: true, files: ["README.md"], labels: ["full-ci"] });
  assert.ok(Object.values(plan.scope).every(Boolean));
  assert.deepEqual(plan.matrix, planCI({ isPR: false }).matrix);
});

test("platform files, module manifests, build tooling and unknown configuration fail closed", () => {
  for (const file of ["internal/pty/pty_windows.go", "go.mod", "go.sum", "build/config.yml", ".github/workflows/ci.yml",
    "internal/ai/privacy/inference/engine.go", "internal/ai/privacy/inference/ort_api.go", "scripts/ai-privacy-runtime.mjs", "scripts/lib/privacy-runtime.mjs"]) {
    const plan = planCI({ isPR: true, files: [file] });
    assert.deepEqual(plan.matrix, planCI({ isPR: false }).matrix, file);
  }
  const { scope } = planCI({ isPR: true, files: ["new-tool.config"] });
  assert.equal(scope.quality, true);
  assert.equal(scope.unit, true);
  assert.equal(scope.frontendTests, true);
});

test("IPC generated changes retain the generated bindings gate", () => {
  assert.equal(planCI({ isPR: true, files: ["src/ipc/bindings/generated.ts"] }).scope.quality, true);
});

test("embedded Markdown prompts are application inputs, not documentation-only changes", () => {
  const { scope } = planCI({ isPR: true, files: ["internal/ai/prompts/assistant.md"] });
  assert.equal(scope.unit, true);
  assert.equal(scope.native, true);
});
