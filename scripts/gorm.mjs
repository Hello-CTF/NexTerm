#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const check = process.argv.length === 3 && process.argv[2] === "--check";
if (!check && process.argv.length !== 2) {
  throw new Error("usage: node scripts/gorm.mjs [--check]");
}

const args = ["run", "./scripts/gen-gorm"];
if (check) args.push("--check");
const result = spawnSync("go", args, {
  cwd: ROOT,
  env: process.env,
  stdio: "inherit",
});
if (result.error) throw result.error;
if (result.status !== 0) throw new Error(`GORM generator exited with ${result.status ?? result.signal}`);
