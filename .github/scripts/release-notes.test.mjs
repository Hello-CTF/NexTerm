#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github", "scripts", "release-notes.mjs");

function git(directory, ...args) {
  return execFileSync("git", ["-C", directory, ...args], { encoding: "utf8" });
}

function makeRepo(t, entries) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-release-notes-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  fs.mkdirSync(path.join(directory, ".github", "scripts"), { recursive: true });
  fs.copyFileSync(SCRIPT, path.join(directory, ".github", "scripts", "release-notes.mjs"));
  git(directory, "init", "-q", "-b", "main");
  for (const entry of entries) {
    const args = ["-c", "user.email=notes@test", "-c", "user.name=Notes Test", "commit", "--allow-empty", "-q", "-m", entry.subject];
    if (entry.body) args.push("-m", entry.body);
    git(directory, ...args);
    if (entry.tag) git(directory, "tag", entry.tag);
  }
  return directory;
}

function runNotes(directory, args) {
  const result = spawnSync(process.execPath, [path.join(directory, ".github", "scripts", "release-notes.mjs"), ...args], { encoding: "utf8" });
  if (result.error) throw result.error;
  return result;
}

const releaseEntries = [
  { subject: "feat(zeta): retire zeta (#1)" },
  { subject: "fix(alpha): patch alpha (#2)", tag: "v1.0.0" },
  { subject: "feat(beta): add beta (#3)" },
  { subject: "docs: update readme (#4)" },
  { subject: "fix(beta): fix beta (#5)", tag: "v1.0.1-rc.1" },
  { subject: "perf(core): speed up render (#6)" },
  { subject: "miscellaneous tidy-up" },
  { subject: "feat(alpha): add alpha (#7)", tag: "v1.0.1" },
];

test("groups by type, excludes internal types and sorts by scope then subject", (t) => {
  const directory = makeRepo(t, releaseEntries);
  const result = runNotes(directory, ["v1.0.1"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `### v1.0.1 变化

#### 功能

- **alpha**: add alpha (#7)
- **beta**: add beta (#3)

#### 修复

- **beta**: fix beta (#5)

#### 性能

- **core**: speed up render (#6)

#### 其他

- miscellaneous tidy-up

`);
});

test("--prev-tag resolves the previous stable tag and skips prerelease tags", (t) => {
  const directory = makeRepo(t, releaseEntries);
  const result = runNotes(directory, ["--prev-tag", "v1.0.1"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "v1.0.0\n");
});

test("fails on an unknown tag", (t) => {
  const directory = makeRepo(t, releaseEntries);
  const result = runNotes(directory, ["v9.9.9"]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /fatal:/);
});

test("falls back to the full history when no previous stable tag exists", (t) => {
  const directory = makeRepo(t, [
    { subject: "feat(start): beginning (#1)" },
    { subject: "docs: notes (#2)", tag: "v0.1.0" },
    { subject: "docs: more notes (#3)", tag: "v0.2.0" },
  ]);
  const first = runNotes(directory, ["v0.1.0"]);
  assert.equal(first.status, 0, first.stderr);
  assert.match(first.stderr, /no previous v\* tag/);
  assert.equal(first.stdout, `### v0.1.0 变化

#### 功能

- **start**: beginning (#1)

`);
  const second = runNotes(directory, ["v0.2.0"]);
  assert.equal(second.status, 0, second.stderr);
  assert.equal(second.stdout, `### v0.2.0 变化

#### 其他

- more notes (#3)

`);
});

test("renders breaking changes from ! markers and BREAKING CHANGE footers", (t) => {
  const directory = makeRepo(t, [
    { subject: "feat: initial release", tag: "v1.0.0" },
    { subject: "feat(api)!: remove old endpoint" },
    { subject: "fix(core): require explicit configuration", body: "BREAKING CHANGE: the implicit default configuration has been removed" },
    { subject: "docs: final documentation", tag: "v2.0.0" },
  ]);
  const result = runNotes(directory, ["v2.0.0"]);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `### v2.0.0 变化

#### 破坏性变更

- **api**: remove old endpoint
- **core**: the implicit default configuration has been removed

#### 功能

- **api**: remove old endpoint

#### 修复

- **core**: require explicit configuration

`);
});

test("excludes merge commits while retaining changes merged into the release", (t) => {
  const directory = makeRepo(t, [{ subject: "feat: initial release", tag: "v1.0.0" }]);
  git(directory, "checkout", "-q", "-b", "feature");
  git(directory, "-c", "user.email=notes@test", "-c", "user.name=Notes Test", "commit", "--allow-empty", "-q", "-m", "feat(branch): add merged feature");
  git(directory, "checkout", "-q", "main");
  git(directory, "-c", "user.email=notes@test", "-c", "user.name=Notes Test", "commit", "--allow-empty", "-q", "-m", "docs: update main");
  git(directory, "-c", "user.email=notes@test", "-c", "user.name=Notes Test", "merge", "--no-ff", "-q", "feature", "-m", "Merge branch 'feature'");
  git(directory, "tag", "v1.1.0");
  const result = runNotes(directory, ["v1.1.0"]);
  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stdout, /Merge branch/);
  assert.match(result.stdout, /- \*\*branch\*\*: add merged feature/);
});
