#!/usr/bin/env node
import fs from "node:fs";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { selectNativeMatrix } from "./helper-matrix.mjs";

export function planCI({ isPR, files = [], labels = [] }) {
  const full = isPR && labels.includes("full-ci");
  let go = full;
  let frontend = full;
  let tooling = full;
  let fullNative = full;
  for (const file of files) {
    if (/^[^/]+\.md$|(^|\/)(README(?:_[^/]+)?|AGENTS)\.md$/.test(file)
      || /^(docs\/|website\/|LICENSE$|\.gitignore$|\.github\/workflows\/pages\.yml$)/.test(file)) continue;
    if (/^(src\/|package\.json$|pnpm-lock\.yaml$|tsconfig|vite\.|vitest\.|eslint\.)/.test(file)) {
      frontend = true;
      if (file.startsWith("src/ipc/")) tooling = true;
    } else if (/\.go$|(^|\/)(go\.mod|go\.sum)$/.test(file)) {
      go = true;
    } else {
      // Unknown configuration and tooling changes get the complete PR gates.
      go = frontend = tooling = true;
    }
    if (/^build\/|^\.github\/|^scripts\/build|(^|\/)(go\.mod|go\.sum)$|_(darwin|linux|unix|windows)\.go$/.test(file)) fullNative = true;
  }
  const native = !isPR || go || frontend || tooling;
  const scope = {
    quality: isPR && (go || tooling),
    unit: isPR && go,
    database: isPR && go,
    frontend: !isPR || native || frontend,
    frontendTests: isPR && frontend,
    native,
    mobile: isPR && go,
    repro: !isPR || full,
  };
  const targets = native ? selectNativeMatrix({ isPR, fullPR: fullNative })
    .filter((entry) => !isPR || go || tooling || entry.kind === "desktop") : [];
  const groups = new Map();
  for (const target of targets) {
    const key = `${target.os}/${target.arch}`;
    if (!groups.has(key)) groups.set(key, { runner: target.runner, os: target.os, arch: target.arch, kinds: [], representative: false });
    const group = groups.get(key);
    group.kinds.push(target.kind);
    group.representative ||= target.representative === true;
  }
  return { scope, matrix: { include: [...groups.values()] } };
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
