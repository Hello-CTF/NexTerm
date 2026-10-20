#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const goMod = fs.readFileSync(path.join(ROOT, "go.mod"), "utf8");
const goToolchain = goMod.match(/^toolchain\s+(go\d+\.\d+\.\d+)\s*$/m)?.[1];
const wailsVersion = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)\s*$/m)?.[1];
if (!goToolchain || !wailsVersion) throw new Error("cannot read the Go toolchain and Wails versions from go.mod");
const packageJSON = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8"));
const pnpmRequirement = packageJSON.packageManager?.match(/^pnpm@(\d+)/)?.[1] || "11";
let failures = 0;

function versionOutput(command, args, env = process.env) {
  const result = spawnSync(command, args, { cwd: ROOT, env, encoding: "utf8" });
  if (result.error || result.status !== 0) return { ok: false, output: (result.error?.message || result.stderr || result.stdout || `exit ${result.status}`).trim() };
  return { ok: true, output: `${result.stdout || ""}${result.stderr || ""}`.trim() };
}

function report(name, result, valid, expected) {
  const version = result.output.match(/(?:go|v)?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?/)?.[0] || result.output.split("\n")[0] || "missing";
  const ok = result.ok && valid(version);
  console.log(`${ok ? "ok" : "FAIL"} - ${name}: ${version}${ok ? "" : ` (expected ${expected})`}`);
  if (!ok) failures += 1;
}

if (process.platform !== "darwin" && process.platform !== "linux") {
  console.log(`FAIL - host: ${process.platform} (expected Linux, macOS, or WSL)`);
  failures += 1;
} else {
  console.log(`ok - host: ${process.platform}; Linux, macOS and WSL are supported development hosts`);
}
report("go", versionOutput("go", ["env", "GOVERSION"], { ...process.env, GOTOOLCHAIN: goToolchain }), (version) => version === goToolchain, goToolchain);
const nodeMajor = Number(process.versions.node.split(".")[0]);
report("node", { ok: true, output: process.version }, () => nodeMajor >= 24, "Node 24 or newer");
report("pnpm", versionOutput("pnpm", ["--version"]), (version) => version.split(".")[0] === pnpmRequirement, `pnpm ${pnpmRequirement}.x`);
report("task", versionOutput("task", ["--version"]), (version) => version.replace(/^v/, "").split(".")[0] === "3", "Task v3 (CI uses v3.45.5)");
report("python3", versionOutput("python3", ["--version"]), (version) => Number(version.split(".")[0]) === 3, "Python 3");
report("wails3 (pinned go run)", versionOutput("go", ["run", `github.com/wailsapp/wails/v3/cmd/wails3@${wailsVersion}`, "version"], { ...process.env, GOTOOLCHAIN: goToolchain }), (version) => version === wailsVersion, wailsVersion);
report("docker", versionOutput("docker", ["--version"]), (version) => Number(version.split(".")[0]) > 0, "Docker CLI");
report("docker compose", versionOutput("docker", ["compose", "version"]), (version) => Number(version.replace(/^v/, "").split(".")[0]) >= 2, "Docker Compose v2");

if (failures) {
  console.error(`doctor: ${failures} requirement${failures === 1 ? "" : "s"} failed; see deploy/dev/README.md`);
  process.exitCode = 1;
} else {
  console.log("doctor: development toolchain is ready");
}
