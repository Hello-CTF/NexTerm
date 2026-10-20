#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const embedded = path.join(ROOT, "cmd/nexterm-desktop/dist");

function run(command, args) {
  const result = spawnSync(command, args, { cwd: ROOT, env: process.env, stdio: "inherit" });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} exited with ${result.status ?? result.signal}`);
}

try {
  fs.rmSync(embedded, { recursive: true, force: true });
  run(process.execPath, [path.join(ROOT, "scripts/build.mjs"), "frontend", "--repro-check"]);
  fs.cpSync(path.join(ROOT, "dist"), embedded, { recursive: true });
  run("go", ["test", "-tags", "production", "./cmd/nexterm-desktop"]);
} catch (error) {
  console.error(`test:production: ${error.message}`);
  process.exitCode = 1;
} finally {
  fs.rmSync(embedded, { recursive: true, force: true });
}
