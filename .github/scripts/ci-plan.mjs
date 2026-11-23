#!/usr/bin/env node
import fs from "node:fs";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { selectNativeMatrix } from "./helper-matrix.mjs";

// The Go tests are split into this many shards, each on its own runner. The race run costs about 3.5 times the plain
// run, so it gets more shards; see .github/go-test-weights.json and .github/scripts/go-shards.mjs.
export const PLAIN_UNIT_SHARDS = 3;
export const RACE_UNIT_SHARDS = 5;

// Script and workflow files whose change must build every native target: they drive the native jobs or the
// artifacts those jobs produce. Templates and the Go test weights do not.
const FULL_NATIVE_FILES = /^(build\/|\.github\/(workflows\/(ci|release)\.yml$|actions\/|native-matrix\.json$|scripts\/(build-native|helper-matrix|helper-metadata|verify-reused-binary|verify-packaged-runtime|stage-server-evidence)\.mjs$|scripts\/windows-real-tests\.ps1$)|scripts\/(build|test-production)\.mjs$|scripts\/lib\/build-|scripts\/pack-)|(^|\/)(go\.mod|go\.sum)$/;
// Go files compiled only for some operating systems build those systems' native targets (both architectures).
// Fallback files for other systems (_other, _unsupported, _default, _stub) build every target.
const OS_FILES = [
  [/_windows(_test)?\.go$/, ["windows"]],
  [/_(darwin|apple)(_test)?\.go$/, ["darwin"]],
  [/_linux(_test)?\.go$/, ["linux"]],
  [/_(unix|nonwindows)(_test)?\.go$/, ["linux", "darwin"]],
];
const FALLBACK_OS_FILE = /_(other|unsupported|default|stub)(_test)?\.go$/;
// Node and shell tooling: script tests run in the quality job and native builds use the build scripts, so these
// changes skip the Go, database and frontend test suites. Scripts that feed other suites say so here.
const SCRIPT_FILE = /^(scripts|\.github\/scripts)\/.*\.(mjs|sh)$/;
const FRONTEND_SCRIPT = /^scripts\/(i18n\.mjs|lib\/(vite-watch-ignores|store-subscription-rule)\.mjs)$/;
const MOBILE_SCRIPT = /^scripts\/(mobile-source\.mjs|lib\/(mobile|semver)\.mjs)$/;
// The iOS compile runs on a macOS runner, so PRs run it only for changes that can break iOS specifically.
const IOS_FILES = /_(ios|darwin|apple)(_test)?\.go$|^build\/ios\/|(^|\/)(go\.mod|go\.sum)$/;

export function planCI({ isPR, files = [], labels = [] }) {
  const full = isPR && labels.includes("full-ci");
  let go = full;
  let frontend = full;
  let tooling = full;
  let mobile = full;
  let ios = full;
  let fullNative = full;
  const nativeOS = new Set();
  for (const file of files) {
    if (/^[^/]+\.md$|(^|\/)(README(?:_[^/]+)?|AGENTS)\.md$/.test(file)
      || /^(docs\/|website\/|LICENSE$|\.gitignore$|\.github\/workflows\/pages\.yml$)/.test(file)) continue;
    if (/^(src\/|package\.json$|pnpm-lock\.yaml$|tsconfig|vite\.|vitest\.|eslint\.)/.test(file)) {
      frontend = true;
      if (file.startsWith("src/ipc/")) tooling = true;
    } else if (/\.go$|(^|\/)(go\.mod|go\.sum)$/.test(file)) {
      go = mobile = true;
    } else if (SCRIPT_FILE.test(file)) {
      tooling = true;
      if (FRONTEND_SCRIPT.test(file)) frontend = true;
      if (MOBILE_SCRIPT.test(file)) mobile = ios = true;
    } else {
      // Unknown configuration and tooling changes get the complete PR gates.
      go = frontend = tooling = mobile = ios = true;
    }
    if (IOS_FILES.test(file)) ios = true;
    if (FULL_NATIVE_FILES.test(file) || FALLBACK_OS_FILE.test(file)) fullNative = true;
    for (const [pattern, systems] of OS_FILES) if (pattern.test(file)) for (const os of systems) nativeOS.add(os);
  }
  const native = !isPR || go || frontend || tooling;
  const scope = {
    quality: isPR && (go || tooling),
    unit: isPR && go,
    // The race detector multiplies test time, so ordinary PRs skip it; full-ci opts in.
    race: isPR && go && full,
    database: isPR && go,
    frontend: !isPR || native || frontend,
    frontendTests: isPR && frontend,
    native,
    mobile: isPR && mobile,
    repro: !isPR || full,
  };
  const targets = native ? selectNativeMatrix({ isPR, fullPR: fullNative, systems: nativeOS })
    .filter((entry) => !isPR || go || tooling || entry.kind === "desktop") : [];
  const groups = new Map();
  for (const target of targets) {
    const key = `${target.os}/${target.arch}`;
    if (!groups.has(key)) groups.set(key, { runner: target.runner, os: target.os, arch: target.arch, kinds: [], representative: false });
    const group = groups.get(key);
    group.kinds.push(target.kind);
    group.representative ||= target.representative === true;
  }
  const shardCount = scope.race ? RACE_UNIT_SHARDS : PLAIN_UNIT_SHARDS;
  // number is the 1-based shard shown in the job name: workflow expressions cannot do arithmetic.
  const unitMatrix = { include: Array.from({ length: shardCount }, (_, shard) => ({ shard, number: shard + 1, count: shardCount })) };
  const mobileMatrix = { include: [{ platform: "android", runner: "ubuntu-latest" }, ...(ios ? [{ platform: "ios", runner: "macos-26" }] : [])] };
  return { scope, matrix: { include: [...groups.values()] }, unitMatrix, mobileMatrix };
}

function main() {
  const isPR = process.env.IS_PR === "true";
  let files = [];
  if (isPR) {
    if (!process.env.BASE_SHA) throw new Error("BASE_SHA is required for PR change detection");
    const diff = spawnSync("git", ["diff", "--name-only", "-z", `${process.env.BASE_SHA}...HEAD`], { encoding: "utf8" });
    if (diff.status !== 0) throw new Error(diff.stderr || "git diff failed");
    files = diff.stdout.split("\0").filter(Boolean);
  }
  const plan = planCI({ isPR, files, labels: JSON.parse(process.env.PR_LABELS || "[]") });
  for (const [key, value] of Object.entries(plan)) fs.appendFileSync(process.env.GITHUB_OUTPUT, `${key}=${JSON.stringify(value)}\n`);
  const summary = `## CI scope\n\n${Object.entries(plan.scope).map(([key, value]) => `- ${key}: ${value ? "run" : "skip"}`).join("\n")}\n\nNative runner jobs: ${plan.matrix.include.length}\n`;
  if (process.env.GITHUB_STEP_SUMMARY) fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY, summary);
  process.stdout.write(summary);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
