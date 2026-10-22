#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const MATRIX = JSON.parse(fs.readFileSync(path.join(ROOT, ".github/native-matrix.json"), "utf8"));
const TARGETS = [
  { os: "linux", arch: "amd64", goarch: "amd64", goarm: "" },
  { os: "linux", arch: "arm64", goarch: "arm64", goarm: "" },
  { os: "linux", arch: "armv7", goarch: "arm", goarm: "7" },
  { os: "linux", arch: "riscv64", goarch: "riscv64", goarm: "" },
  { os: "linux", arch: "loong64", goarch: "loong64", goarm: "" },
  { os: "darwin", arch: "amd64", goarch: "amd64", goarm: "" },
  { os: "darwin", arch: "arm64", goarch: "arm64", goarm: "" },
  { os: "windows", arch: "amd64", goarch: "amd64", goarm: "" },
  { os: "windows", arch: "arm64", goarch: "arm64", goarm: "" },
];

export function helperTargets() {
  const rows = MATRIX.filter((entry) => entry.kind === "helper");
  const byTarget = new Map();
  for (const row of rows) {
    const key = `${row.os}/${row.arch}`;
    if (byTarget.has(key)) throw new Error(`duplicate helper native matrix entry: ${key}`);
    byTarget.set(key, row);
  }
  const targets = TARGETS.map((target) => {
    const key = `${target.os}/${target.arch}`;
    const row = byTarget.get(key);
    if (!row) throw new Error(`native matrix is missing helper target ${key}`);
    byTarget.delete(key);
    return {
      ...target,
      runner: row.runner,
      smoke: row.smoke === true,
      cross: row.cross === true,
      name: `nexterm-helper-${target.os}-${target.arch}${target.os === "windows" ? ".exe" : ""}`,
    };
  });
  if (byTarget.size > 0) throw new Error(`unexpected helper native matrix entries: ${[...byTarget.keys()].join(", ")}`);
  return targets;
}

export function helperTarget(goos, goarch) {
  return helperTargets().find((target) => target.os === goos && target.arch === goarch) || null;
}

export function selectNativeMatrix({ isPR, fullPR }) {
  helperTargets();
  return MATRIX.filter((entry) => !isPR || fullPR || entry.smoke === true);
}

export function canExecuteHelperMetadata(host, target) {
  return host.os === target.os && host.arch === target.arch;
}

export function helperArchiveMembers(prefix = "") {
  return helperTargets().map((target) => `${prefix}${target.name}`);
}

export function missingHelperArchiveMembers(members, prefix = "") {
  const present = new Set(members);
  return helperArchiveMembers(prefix).filter((name) => !present.has(name));
}

function main() {
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === "--shell") {
    process.stdout.write(`${helperTargets().map((target) => `${target.os}|${target.arch}|${target.name}`).join("\n")}\n`);
    return;
  }
  if (args.length === 1 && args[0] === "--matrix") {
    const include = selectNativeMatrix({ isPR: process.env.IS_PR === "true", fullPR: process.env.FULL_NATIVE === "true" });
    process.stdout.write(`${JSON.stringify({ include })}\n`);
    return;
  }
  throw new Error("usage: helper-matrix.mjs --shell | --matrix");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
