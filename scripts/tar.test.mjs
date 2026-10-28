import assert from "node:assert/strict";
import test from "node:test";
import { resolveTar } from "./lib/tar.mjs";

test("resolveTar uses System32 bsdtar on Windows when present", () => {
  const found = resolveTar("win32", { SystemRoot: "D:\\Win" }, () => true);
  assert.equal(found, "D:\\Win\\System32\\tar.exe");
  assert.equal(resolveTar("win32", {}, () => true), "C:\\Windows\\System32\\tar.exe");
});

test("resolveTar falls back to PATH tar", () => {
  assert.equal(resolveTar("win32", {}, () => false), "tar");
  assert.equal(resolveTar("linux", {}, () => true), "tar");
  assert.equal(resolveTar("darwin", {}, () => true), "tar");
});
