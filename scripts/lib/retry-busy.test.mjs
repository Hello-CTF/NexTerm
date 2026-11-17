import assert from "node:assert/strict";
import test from "node:test";
import { runRetryingBusy } from "./retry-busy.mjs";

function scripted(results) {
  const calls = [];
  const sleeps = [];
  const attempt = (n) => {
    calls.push(n);
    return results[n - 1];
  };
  return { calls, sleeps, attempt, sleep: (ms) => sleeps.push(ms) };
}

test("retries a busy resource with growing waits until it succeeds", () => {
  const run = scripted([
    { ok: false, output: "hdiutil: create failed - Resource busy\n" },
    { ok: false, output: "hdiutil: create failed - Resource busy\n" },
    { ok: true, output: "created: x.dmg" },
  ]);
  const result = runRetryingBusy(run.attempt, { delayMs: 100, sleep: run.sleep });
  assert.deepEqual(result, { ok: true, tries: 3 });
  assert.deepEqual(run.sleeps, [100, 200]);
});

test("does not retry other failures", () => {
  const run = scripted([{ ok: false, output: "hdiutil: create failed - No space left on device" }]);
  const result = runRetryingBusy(run.attempt, { sleep: run.sleep });
  assert.equal(result.ok, false);
  assert.equal(result.busy, false);
  assert.deepEqual(run.calls, [1]);
  assert.deepEqual(run.sleeps, []);
});

test("gives up after the last attempt while still busy", () => {
  const busy = { ok: false, output: "Resource busy" };
  const run = scripted([busy, busy, busy]);
  const result = runRetryingBusy(run.attempt, { attempts: 3, delayMs: 1, sleep: run.sleep });
  assert.equal(result.ok, false);
  assert.equal(result.busy, true);
  assert.equal(result.tries, 3);
  assert.deepEqual(run.sleeps, [1, 2]);
});
