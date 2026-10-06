#!/usr/bin/env node
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fetchServerSyncToken } from "./server-token.mjs";

const SENTINEL = ["nx", "sentinel", "sync", "token", "do", "not", "leak"].join("-") + "-9f4c2a";

function fakeBinary(t, script) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-fake-server-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const file = path.join(directory, "nexterm-server");
  fs.writeFileSync(file, `#!/bin/sh\n${script}\n`, { mode: 0o755 });
  return file;
}

function tempDir(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-fake-data-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return directory;
}

function captureError(fn) {
  try {
    fn();
  } catch (error) {
    return error;
  }
  assert.fail("expected the call to throw");
}

function assertNoLeak(error, pattern) {
  assert.match(error.message, pattern);
  const recorded = String(error?.stack || error);
  const report = JSON.stringify({ harness_errors: [recorded] });
  assert.ok(!recorded.includes(SENTINEL), "recorded harness error must not contain the token stdout");
  assert.ok(!report.includes(SENTINEL), "report.json must not contain the token stdout");
}

test("happy path returns the single-line token", (t) => {
  const binary = fakeBinary(t, `printf '%s\\n' '${SENTINEL}'`);
  assert.equal(fetchServerSyncToken(binary, tempDir(t), process.env), SENTINEL);
});

test("multi-line stdout fails with a fixed message and no token leak", (t) => {
  const binary = fakeBinary(t, `printf '%s\\n' '${SENTINEL}' 'extra-line'`);
  assertNoLeak(captureError(() => fetchServerSyncToken(binary, tempDir(t), process.env)), /single non-empty line/);
});

test("non-zero exit reports only the exit status, never stdout", (t) => {
  const binary = fakeBinary(t, `printf '%s\\n' '${SENTINEL}'\nexit 3`);
  assertNoLeak(captureError(() => fetchServerSyncToken(binary, tempDir(t), process.env)), /exit status 3/);
});

test("non-zero exit never relays stderr content", (t) => {
  const binary = fakeBinary(t, `printf '%s\\n' '${SENTINEL}' >&2\nexit 1`);
  assertNoLeak(captureError(() => fetchServerSyncToken(binary, tempDir(t), process.env)), /exit status 1/);
});

test("empty stdout fails with a fixed message", (t) => {
  const binary = fakeBinary(t, "exit 0");
  assertNoLeak(captureError(() => fetchServerSyncToken(binary, tempDir(t), process.env)), /single non-empty line/);
});

test("spawn failure reports only the error code", (t) => {
  const directory = tempDir(t);
  const error = captureError(() => fetchServerSyncToken(path.join(directory, "missing-binary"), directory, process.env));
  assert.match(error.message, /could not be spawned: ENOENT/);
});
