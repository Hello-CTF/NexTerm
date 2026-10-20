import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  MOBILE_CONFIG_PATH,
  MOBILE_STAMP_CONFIG_PATH,
  commitLabel,
  mobileLdflags,
  mobileVersion,
  renderStampedMobileConfig,
  resolveMobileConfigPath,
  sourceState,
  stampMobileConfig,
} from "./lib/mobile.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const CLI = path.join(ROOT, "scripts", "mobile-source.mjs");

const TRACKED_CONFIG = `version: '3'

info:
  companyName: "NexTerm contributors"
  productName: "NexTerm"
  productIdentifier: "com.nexterm.desktop"
  version: "0.0.0"

ios:
  bundleID: "work.nexterm.app"
  version: "0.0.0"
`;

function fixture(t, prefix) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

function git(cwd, ...args) {
  const result = spawnSync("git", [
    "-c", "user.name=NexTerm Test",
    "-c", "user.email=nexterm-test@example.com",
    "-c", "commit.gpgsign=false",
    ...args,
  ], { cwd, encoding: "utf8" });
  assert.equal(result.status, 0, `git ${args.join(" ")}: ${result.stderr}`);
  return result.stdout;
}

// Mirrors the repository layout: a tracked build/config.yml with 0.0.0
// placeholders and build/.gitignore keeping the generated stamp config out of
// the index.
function gitRepo(t) {
  const root = fixture(t, "nexterm-mobile-git-");
  git(root, "init", "-q");
  fs.mkdirSync(path.join(root, "build"));
  fs.writeFileSync(path.join(root, "build", ".gitignore"), "/config.stamp.yml\n");
  fs.writeFileSync(path.join(root, MOBILE_CONFIG_PATH), TRACKED_CONFIG);
  git(root, "add", "-A");
  git(root, "commit", "-q", "-m", "init");
  return root;
}

function cli(root, args, version) {
  return spawnSync(process.execPath, [CLI, ...args], {
    cwd: root,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: version || "" },
    encoding: "utf8",
  });
}

test("mobile version falls back to dev and rejects invalid release versions", () => {
  assert.equal(mobileVersion({}), "dev");
  assert.equal(mobileVersion({ NEXTERM_RELEASE_VERSION: "" }), "dev");
  assert.equal(mobileVersion({ NEXTERM_RELEASE_VERSION: "1.2.3-rc.1" }), "1.2.3-rc.1");
  assert.throws(() => mobileVersion({ NEXTERM_RELEASE_VERSION: "0.0.0" }), /non-placeholder semver/);
  assert.throws(() => mobileVersion({ NEXTERM_RELEASE_VERSION: "v1.2.3" }), /non-placeholder semver/);
});

test("stamp rendering replaces every placeholder and rejects invalid input", () => {
  const { updated, count } = renderStampedMobileConfig(TRACKED_CONFIG, "1.2.3-rc.1");
  assert.equal(count, 2);
  assert.equal(updated, TRACKED_CONFIG.replaceAll('version: "0.0.0"', 'version: "1.2.3-rc.1"'));
  assert.equal(updated.includes("version: '3'"), true);
  assert.throws(() => renderStampedMobileConfig(TRACKED_CONFIG, "0.0.0"), /non-placeholder semver/);
  assert.throws(() => renderStampedMobileConfig(TRACKED_CONFIG, ""), /non-placeholder semver/);
  assert.throws(() => renderStampedMobileConfig('info:\n  version: "1.0.0"\n', "1.2.3"), /replaced nothing/);
});

test("stamped release config is ignored and keeps the tree clean", (t) => {
  const root = gitRepo(t);
  const head = git(root, "rev-parse", "HEAD").trim();
  assert.equal(resolveMobileConfigPath({ root }), MOBILE_CONFIG_PATH);
  const count = stampMobileConfig({
    configPath: path.join(root, MOBILE_CONFIG_PATH),
    outPath: path.join(root, MOBILE_STAMP_CONFIG_PATH),
    version: "1.2.3",
  });
  assert.equal(count, 2);
  const stamped = fs.readFileSync(path.join(root, MOBILE_STAMP_CONFIG_PATH), "utf8");
  assert.equal(stamped.includes('version: "1.2.3"'), true);
  assert.equal(stamped.includes("0.0.0"), false);
  assert.equal(fs.readFileSync(path.join(root, MOBILE_CONFIG_PATH), "utf8"), TRACKED_CONFIG);
  assert.equal(resolveMobileConfigPath({ root }), MOBILE_STAMP_CONFIG_PATH);
  assert.deepEqual(sourceState({ cwd: root }), { commit: head, dirty: false });
  assert.equal(commitLabel(sourceState({ cwd: root })), head);
});

test("unrelated tracked and untracked changes still mark the tree dirty", (t) => {
  const root = gitRepo(t);
  const head = git(root, "rev-parse", "HEAD").trim();
  fs.appendFileSync(path.join(root, MOBILE_CONFIG_PATH), "\n# local edit\n");
  assert.deepEqual(sourceState({ cwd: root }), { commit: head, dirty: true });
  assert.equal(commitLabel(sourceState({ cwd: root })), `${head}+dirty`);
  git(root, "checkout", "--", MOBILE_CONFIG_PATH);
  fs.writeFileSync(path.join(root, "stray.txt"), "stray\n");
  assert.equal(sourceState({ cwd: root }).dirty, true);
  assert.equal(commitLabel(sourceState({ cwd: root })), `${head}+dirty`);
  fs.rmSync(path.join(root, "stray.txt"));
  assert.deepEqual(sourceState({ cwd: root }), { commit: head, dirty: false });
});

test("source state reports unknown outside a git work tree", (t) => {
  const root = fixture(t, "nexterm-mobile-nogit-");
  assert.deepEqual(sourceState({ cwd: root }), { commit: "unknown", dirty: false });
  assert.equal(commitLabel(sourceState({ cwd: root })), "unknown");
});

test("stamp CLI refreshes the generated config and removes it when the version is unset", (t) => {
  const root = gitRepo(t);
  const head = git(root, "rev-parse", "HEAD").trim();
  const stamped = cli(root, ["stamp"], "1.2.3");
  assert.equal(stamped.status, 0, stamped.stderr);
  assert.match(stamped.stdout, /stamped 2 version fields with 1\.2\.3/);
  assert.equal(fs.readFileSync(path.join(root, MOBILE_CONFIG_PATH), "utf8"), TRACKED_CONFIG);
  const consumed = cli(root, ["config"], "1.2.3");
  assert.equal(consumed.status, 0, consumed.stderr);
  assert.equal(consumed.stdout.trim(), MOBILE_STAMP_CONFIG_PATH);
  const commit = cli(root, ["commit"], "1.2.3");
  assert.equal(commit.status, 0, commit.stderr);
  assert.equal(commit.stdout.trim(), head);
  const removed = cli(root, ["stamp"], "");
  assert.equal(removed.status, 0, removed.stderr);
  assert.equal(fs.existsSync(path.join(root, MOBILE_STAMP_CONFIG_PATH)), false);
  const fallback = cli(root, ["config"], "");
  assert.equal(fallback.stdout.trim(), MOBILE_CONFIG_PATH);
});

test("stamp CLI rejects a placeholder release version without writing", (t) => {
  const root = gitRepo(t);
  const result = cli(root, ["stamp"], "0.0.0");
  assert.equal(result.status, 1);
  assert.match(result.stderr, /non-placeholder semver/);
  assert.equal(fs.existsSync(path.join(root, MOBILE_STAMP_CONFIG_PATH)), false);
  assert.equal(fs.readFileSync(path.join(root, MOBILE_CONFIG_PATH), "utf8"), TRACKED_CONFIG);
});

test("commit CLI prints the exact commit on a clean tree and +dirty after a tracked edit", (t) => {
  const root = gitRepo(t);
  const head = git(root, "rev-parse", "HEAD").trim();
  const clean = cli(root, ["commit"], "");
  assert.equal(clean.status, 0, clean.stderr);
  assert.equal(clean.stdout.trim(), head);
  fs.appendFileSync(path.join(root, MOBILE_CONFIG_PATH), "\n# local edit\n");
  const dirty = cli(root, ["commit"], "");
  assert.equal(dirty.status, 0, dirty.stderr);
  assert.equal(dirty.stdout.trim(), `${head}+dirty`);
});

test("ldflags stamp version and commit for both platforms in debug and release", () => {
  for (const platform of ["android", "ios"]) {
    const release = mobileLdflags({ platform, production: true, version: "1.2.3", commit: "abc+dirty" });
    assert.ok(release.includes(`-tags production,${platform} `), release);
    assert.ok(release.includes(" -trimpath "), release);
    assert.ok(release.includes('-ldflags="-s -w '), release);
    assert.ok(release.includes("-X github.com/Hello-CTF/NexTerm/internal/version.Version=1.2.3"), release);
    assert.ok(release.includes("-X github.com/Hello-CTF/NexTerm/internal/version.Commit=abc+dirty"), release);
    assert.ok(!release.includes("-gcflags"), release);
    const debug = mobileLdflags({ platform, production: false, version: "dev", commit: "abc" });
    assert.ok(debug.includes(`-tags ${platform},debug `), debug);
    assert.ok(debug.includes('-gcflags=all="-l"'), debug);
    assert.ok(debug.includes("-X github.com/Hello-CTF/NexTerm/internal/version.Version=dev"), debug);
    assert.ok(debug.includes("-X github.com/Hello-CTF/NexTerm/internal/version.Commit=abc"), debug);
    assert.ok(!debug.includes("-trimpath"), debug);
    assert.ok(!debug.includes('"-s -w '), debug);
  }
  assert.throws(() => mobileLdflags({ platform: "windows", production: true, version: "1.2.3", commit: "x" }), /unsupported mobile platform/);
  assert.throws(() => mobileLdflags({ production: true, version: "1.2.3", commit: "x" }), /unsupported mobile platform/);
});

test("ldflags CLI resolves the release version and dirty commit from the environment", (t) => {
  const root = gitRepo(t);
  const head = git(root, "rev-parse", "HEAD").trim();
  const release = cli(root, ["ldflags", "--platform=android", "--production=true"], "1.2.3");
  assert.equal(release.status, 0, release.stderr);
  assert.equal(release.stdout.trim(), `-tags production,android -trimpath -buildvcs=false -ldflags="-s -w -X github.com/Hello-CTF/NexTerm/internal/version.Version=1.2.3 -X github.com/Hello-CTF/NexTerm/internal/version.Commit=${head}"`);
  fs.writeFileSync(path.join(root, "stray.txt"), "stray\n");
  const dirty = cli(root, ["ldflags", "--platform=ios", "--production=true"], "1.2.3");
  assert.equal(dirty.status, 0, dirty.stderr);
  assert.ok(dirty.stdout.includes(`version.Commit=${head}+dirty`), dirty.stdout);
  const debug = cli(root, ["ldflags", "--platform=ios"], "");
  assert.equal(debug.status, 0, debug.stderr);
  assert.ok(debug.stdout.includes("-tags ios,debug "), debug.stdout);
  assert.ok(debug.stdout.includes("version.Version=dev"), debug.stdout);
  const invalid = cli(root, ["ldflags", "--platform=android"], "0.0.0");
  assert.equal(invalid.status, 1);
  assert.match(invalid.stderr, /non-placeholder semver/);
  const missing = cli(root, ["ldflags"], "1.2.3");
  assert.equal(missing.status, 1);
  assert.match(missing.stderr, /--platform/);
});


// --- go-task level checks ---------------------------------------------------
// The CLI tests above bypass go-task: variable propagation, deps and the
// sources/generates up-to-date cache only exist at the task level.

function taskRunner() {
  for (const runner of [["task"], ["wails3", "task"]]) {
    const probe = spawnSync(runner[0], runner[0] === "task" ? ["--version"] : ["version"], { encoding: "utf8" });
    if (!probe.error && probe.status === 0) return runner;
  }
  return null;
}

function runTask(cwd, args, version) {
  const runner = taskRunner();
  assert.ok(runner, "a go-task runner is required (install task or the pinned wails3 CLI)");
  return spawnSync(runner[0], [...runner.slice(1), ...args], {
    cwd,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: version || "" },
    encoding: "utf8",
  });
}

function requireWails3() {
  const probe = spawnSync("wails3", ["version"], { encoding: "utf8" });
  assert.ok(!probe.error && probe.status === 0, "the pinned wails3 CLI is required (go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.27)");
}

// An isolated checkout with just the files the generation tasks touch, so the
// real task graph (deps, up-to-date cache, wails3 generators) runs without
// touching the repository work tree.
function isolatedRepo(t) {
  requireWails3();
  const root = fixture(t, "nexterm-mobile-task-");
  for (const item of ["Taskfile.yml", ".gitignore", "build", "scripts"]) {
    fs.cpSync(path.join(ROOT, item), path.join(root, item), { recursive: true });
  }
  // generate:ios:xcode is internal; reach it through a harness task.
  fs.appendFileSync(path.join(root, "Taskfile.yml"), `  harness:ios:xcode:
    deps:
      - task: ios:generate:ios:xcode
    cmds:
      - echo "harness done"
`);
  for (const generated of [MOBILE_STAMP_CONFIG_PATH, "build/android/overlay.json", "build/android/gen", "build/ios/xcode"]) {
    fs.rmSync(path.join(root, generated), { recursive: true, force: true });
  }
  git(root, "init", "-q");
  git(root, "add", "-A");
  git(root, "commit", "-q", "-m", "init");
  return root;
}

function stampVersion(root) {
  const stamp = path.join(root, MOBILE_STAMP_CONFIG_PATH);
  if (!fs.existsSync(stamp)) return null;
  return fs.readFileSync(stamp, "utf8").match(/version: "([^"]+)"/)?.[1] ?? null;
}

function plistShortVersion(plist) {
  const text = fs.readFileSync(plist, "utf8");
  return text.match(/<key>CFBundleShortVersionString<\/key>\s*<string>([^<]+)<\/string>/)?.[1] ?? null;
}

test("release package tasks compile every architecture with production tags", () => {
  requireWails3();
  for (const [target, expectedBuilds] of [
    ["android:package", 1],
    ["android:package:fat", 2],
    ["android:bundle", 1],
    ["android:bundle:fat", 2],
    ["ios:package", 1],
  ]) {
    const result = runTask(ROOT, [target, "--dry"], "1.2.3");
    assert.equal(result.status, 0, `${target}: ${result.stderr}`);
    const builds = `${result.stdout}\n${result.stderr}`.split(/\r?\n/).filter((line) => line.includes("go build -buildmode="));
    assert.equal(builds.length, expectedBuilds, `${target}: expected ${expectedBuilds} go build lines`);
    for (const line of builds) {
      assert.ok(line.includes("-tags production,"), `${target}: ${line}`);
      assert.ok(!line.includes(",debug"), `${target}: ${line}`);
    }
  }
});

test("android overlay generation refreshes the stamp across cached runs", (t) => {
  const root = isolatedRepo(t);
  const run = (version) => {
    const result = runTask(root, ["android:generate:android:overlay"], version);
    assert.equal(result.status, 0, result.stderr);
  };
  run("3.0.0");
  run("3.0.0");
  run("3.0.0"); // stabilized; further same-version runs may be up to date
  assert.equal(stampVersion(root), "3.0.0");
  run("4.0.0"); // was stuck at 3.0.0 when the stamp refresh was a task command
  assert.equal(stampVersion(root), "4.0.0");
  run(""); // unset removes the stale stamp and regenerates from the tracked config
  assert.equal(stampVersion(root), null);
  assert.equal(fs.readFileSync(path.join(root, MOBILE_CONFIG_PATH), "utf8").includes('version: "0.0.0"'), true);
  assert.equal(fs.existsSync(path.join(root, "build/android/overlay.json")), true);
  assert.equal(git(root, "status", "--porcelain").trim(), "");
});

test("ios xcode generation refreshes the stamp across cached runs", (t) => {
  const root = isolatedRepo(t);
  const plist = path.join(root, "build/ios/xcode/main/Info.plist");
  const run = (version) => {
    const result = runTask(root, ["harness:ios:xcode"], version);
    assert.equal(result.status, 0, result.stderr);
  };
  run("1.2.3");
  run("1.2.3");
  run("1.2.3"); // stabilized; further same-version runs may be up to date
  assert.equal(plistShortVersion(plist), "1.2.3");
  run("5.0.0"); // was stuck at 1.2.3 when the stamp refresh was a task command
  assert.equal(stampVersion(root), "5.0.0");
  assert.equal(plistShortVersion(plist), "5.0.0");
  run(""); // unset removes the stale stamp and regenerates from the tracked config
  assert.equal(stampVersion(root), null);
  assert.equal(plistShortVersion(plist), "0.0.0");
  assert.equal(fs.readFileSync(path.join(root, MOBILE_CONFIG_PATH), "utf8").includes('version: "0.0.0"'), true);
});
