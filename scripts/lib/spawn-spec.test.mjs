/**
 * Executable tests for the deterministic spawn-spec resolver used by
 * scripts/build.mjs. Run with: node --test scripts/lib/
 *
 * The Windows branches are pure functions over injected platform/env/fs, so
 * they run on any host; two tests additionally execute the resolved specs
 * through the real spawnSync to prove the mechanism end to end.
 */

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  quoteWindowsShellArg,
  resolveSpawnSpec,
  windowsShellCommandLine,
} from "./spawn-spec.mjs";

const NODE = process.execPath;

/** Case-insensitive existsSync stub mirroring Windows file semantics. */
function fakeWindowsFs(existingPaths) {
  const existing = new Set(existingPaths.map((entry) => entry.toLowerCase()));
  return (candidate) => existing.has(String(candidate).toLowerCase());
}

test("non-Windows platforms pass every invocation through untouched", () => {
  for (const platform of ["darwin", "linux"]) {
    const spec = resolveSpawnSpec("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], {
      platform,
      env: {},
      existsSync: () => {
        throw new Error("existsSync must not be consulted on non-Windows platforms");
      },
    });
    assert.deepEqual(spec, {
      command: "pnpm",
      args: ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"],
      options: {},
    });
  }
});

test("an explicit pnpm.cmd string on POSIX is not treated as a batch file", () => {
  const spec = resolveSpawnSpec("pnpm.cmd", ["--version"], { platform: "darwin", env: {} });
  assert.deepEqual(spec, { command: "pnpm.cmd", args: ["--version"], options: {} });
});

test("win32 pnpm prefers npm_execpath when the harness runs under a pnpm script", () => {
  const entry = "C:\\Users\\dev\\setup-pnpm\\node_modules\\pnpm\\bin\\pnpm.cjs";
  const spec = resolveSpawnSpec("pnpm", ["exec", "vite", "build"], {
    platform: "win32",
    env: { PATH: "C:\\Windows\\system32", npm_execpath: entry },
    execPath: "C:\\Program Files\\nodejs\\node.exe",
    existsSync: fakeWindowsFs([entry]),
  });
  assert.deepEqual(spec, {
    command: "C:\\Program Files\\nodejs\\node.exe",
    args: [entry, "exec", "vite", "build"],
    options: {},
  });
});

test("win32 npm_execpath pointing at npm-cli.js is ignored and PATH shim resolution wins", () => {
  // `npm run build:app` sets npm_execpath to npm's own CLI; executing it as
  // "pnpm" would silently run npx semantics instead of pnpm exec.
  const binDir = "C:\\Users\\runneradmin\\setup-pnpm\\node_modules\\.bin";
  const sibling = "C:\\Users\\runneradmin\\setup-pnpm\\node_modules\\pnpm\\bin\\pnpm.cjs";
  const npmCli = "C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npm-cli.js";
  const spec = resolveSpawnSpec("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], {
    platform: "win32",
    env: { PATH: `${binDir};C:\\Windows\\system32`, npm_execpath: npmCli },
    execPath: "C:\\Program Files\\nodejs\\node.exe",
    existsSync: fakeWindowsFs([npmCli, `${binDir}\\pnpm.CMD`, sibling]),
  });
  assert.deepEqual(spec, {
    command: "C:\\Program Files\\nodejs\\node.exe",
    args: [sibling, "exec", "tsc", "-p", "tsconfig.json", "--noEmit"],
    options: {},
  });
  assert.ok(!spec.args[0].includes("npm-cli"), "npm-cli.js must never be executed as pnpm");
});

test("win32 npm_execpath pointing at yarn.js is ignored and PATH shim resolution wins", () => {
  const prefix = "C:\\Users\\dev\\AppData\\Roaming\\npm";
  const sibling = `${prefix}\\node_modules\\pnpm\\bin\\pnpm.cjs`;
  const yarnCli = `${prefix}\\node_modules\\yarn\\bin\\yarn.js`;
  const spec = resolveSpawnSpec("pnpm", ["--version"], {
    platform: "win32",
    env: { PATH: `${prefix};C:\\Windows\\system32`, npm_execpath: yarnCli },
    execPath: NODE,
    existsSync: fakeWindowsFs([yarnCli, `${prefix}\\pnpm.cmd`, sibling]),
  });
  assert.deepEqual(spec, { command: NODE, args: [sibling, "--version"], options: {} });
  assert.ok(!spec.args[0].includes("yarn"), "yarn.js must never be executed as pnpm");
});

test("win32 a foreign npm_execpath with no PATH shim passes through instead of executing it", () => {
  const npmCli = "C:\\Program Files\\nodejs\\node_modules\\npm\\bin\\npm-cli.js";
  const spec = resolveSpawnSpec("pnpm", ["--version"], {
    platform: "win32",
    env: { PATH: "C:\\Windows\\system32", npm_execpath: npmCli },
    existsSync: fakeWindowsFs([npmCli]),
  });
  assert.deepEqual(spec, { command: "pnpm", args: ["--version"], options: {} });
});

test("win32 npm_execpath guard rejects lookalike entries outside pnpm-owned directories", () => {
  const binDir = "C:\\tools";
  for (const lookalike of [
    "C:\\tools\\pnpm.js", // right basename, wrong location
    "C:\\tools\\pnpm\\bin\\pnpx.cjs", // pnpx is not the pnpm CLI
    "C:\\tools\\notpnpm\\bin\\pnpm.cjs", // wrong package directory name
    "C:\\tools\\pnpm\\scripts\\pnpm.cjs", // not the bin directory
  ]) {
    const spec = resolveSpawnSpec("pnpm", ["--version"], {
      platform: "win32",
      env: { PATH: binDir, npm_execpath: lookalike },
      existsSync: fakeWindowsFs([lookalike]),
    });
    assert.deepEqual(spec, { command: "pnpm", args: ["--version"], options: {} }, `lookalike ${lookalike} must not be taken`);
  }
});

test("win32 npm_execpath accepts corepack's dist/pnpm.js entry", () => {
  const entry = "C:\\Program Files\\nodejs\\node_modules\\corepack\\dist\\pnpm.js";
  const spec = resolveSpawnSpec("pnpm", ["install", "--frozen-lockfile"], {
    platform: "win32",
    env: { PATH: "C:\\Windows\\system32", npm_execpath: entry },
    execPath: NODE,
    existsSync: fakeWindowsFs([entry]),
  });
  assert.deepEqual(spec, { command: NODE, args: [entry, "install", "--frozen-lockfile"], options: {} });
});

test("win32 resolves the pnpm/action-setup shim to its sibling JS entry without a shell", () => {
  const binDir = "C:\\Users\\runneradmin\\setup-pnpm\\node_modules\\.bin";
  const entry = "C:\\Users\\runneradmin\\setup-pnpm\\node_modules\\pnpm\\bin\\pnpm.cjs";
  const spec = resolveSpawnSpec("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], {
    platform: "win32",
    env: { PATH: `${binDir};C:\\Windows\\system32` },
    execPath: "C:\\Program Files\\nodejs\\node.exe",
    // Shim exists on disk as pnpm.CMD; Windows file lookup is case-insensitive.
    existsSync: fakeWindowsFs([`${binDir}\\pnpm.CMD`, entry]),
  });
  assert.deepEqual(spec, {
    command: "C:\\Program Files\\nodejs\\node.exe",
    args: [entry, "exec", "tsc", "-p", "tsconfig.json", "--noEmit"],
    options: {},
  });
});

test("win32 resolves an npm-global shim through its node_modules sibling", () => {
  const prefix = "C:\\Users\\dev\\AppData\\Roaming\\npm";
  const entry = `${prefix}\\node_modules\\pnpm\\bin\\pnpm.cjs`;
  const spec = resolveSpawnSpec("pnpm", ["--version"], {
    platform: "win32",
    env: { PATH: `${prefix};C:\\Windows\\system32` },
    execPath: NODE,
    existsSync: fakeWindowsFs([`${prefix}\\pnpm.cmd`, entry]),
  });
  assert.deepEqual(spec, { command: NODE, args: [entry, "--version"], options: {} });
});

test("win32 resolves a corepack shim through the corepack dist entry", () => {
  const prefix = "C:\\Program Files\\nodejs";
  const entry = `${prefix}\\node_modules\\corepack\\dist\\pnpm.js`;
  const spec = resolveSpawnSpec("pnpm", ["install", "--frozen-lockfile"], {
    platform: "win32",
    env: { PATH: `${prefix};C:\\Windows\\system32` },
    execPath: NODE,
    existsSync: fakeWindowsFs([`${prefix}\\pnpm.cmd`, entry]),
  });
  assert.deepEqual(spec, { command: NODE, args: [entry, "install", "--frozen-lockfile"], options: {} });
});

test("win32 standalone pnpm.exe passes through without any shell", () => {
  const spec = resolveSpawnSpec("pnpm", ["exec", "vite", "build"], {
    platform: "win32",
    env: { PATH: "D:\\tools;C:\\Windows\\system32" },
    existsSync: fakeWindowsFs(["D:\\tools\\pnpm.exe"]),
  });
  assert.deepEqual(spec, { command: "pnpm", args: ["exec", "vite", "build"], options: {} });
});

test("win32 bare names resolving to a real exe pass through untouched", () => {
  const spec = resolveSpawnSpec("go", ["build", "./cmd/nexterm-desktop"], {
    platform: "win32",
    env: { PATH: "C:\\Program Files\\Go\\bin;C:\\Windows\\system32" },
    existsSync: fakeWindowsFs(["C:\\Program Files\\Go\\bin\\go.exe"]),
  });
  assert.deepEqual(spec, { command: "go", args: ["build", "./cmd/nexterm-desktop"], options: {} });
});

test("win32 exe precedence: a real exe in an earlier PATH entry beats a later shim", () => {
  const spec = resolveSpawnSpec("pnpm", ["--version"], {
    platform: "win32",
    env: { PATH: "D:\\standalone;C:\\Users\\dev\\AppData\\Roaming\\npm" },
    existsSync: fakeWindowsFs([
      "D:\\standalone\\pnpm.exe",
      "C:\\Users\\dev\\AppData\\Roaming\\npm\\pnpm.cmd",
      "C:\\Users\\dev\\AppData\\Roaming\\npm\\node_modules\\pnpm\\bin\\pnpm.cjs",
    ]),
  });
  assert.deepEqual(spec, { command: "pnpm", args: ["--version"], options: {} });
});

test("win32 unresolvable names pass through so spawn reports ENOENT itself", () => {
  const spec = resolveSpawnSpec("pnpm", ["--version"], {
    platform: "win32",
    env: { PATH: "C:\\Windows\\system32" },
    existsSync: fakeWindowsFs([]),
  });
  assert.deepEqual(spec, { command: "pnpm", args: ["--version"], options: {} });
});

test("win32 explicit batch path resolves through the shim branch without a PATH walk", () => {
  const shim = "C:\\Users\\dev\\AppData\\Roaming\\npm\\pnpm.cmd";
  const entry = "C:\\Users\\dev\\AppData\\Roaming\\npm\\node_modules\\pnpm\\bin\\pnpm.cjs";
  const spec = resolveSpawnSpec(shim, ["exec", "tsc"], {
    platform: "win32",
    env: { PATH: "" },
    execPath: NODE,
    existsSync: fakeWindowsFs([entry]),
  });
  assert.deepEqual(spec, { command: NODE, args: [entry, "exec", "tsc"], options: {} });
});

test("win32 shim without a sibling JS entry falls back to a verbatim quoted cmd.exe line", () => {
  const binDir = "C:\\Program Files\\nodejs";
  const spec = resolveSpawnSpec("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit"], {
    platform: "win32",
    env: { PATH: binDir, ComSpec: "C:\\Windows\\system32\\cmd.exe" },
    existsSync: fakeWindowsFs([`${binDir}\\pnpm.cmd`]),
  });
  assert.equal(spec.command, "C:\\Windows\\system32\\cmd.exe");
  assert.deepEqual(spec.args.slice(0, 3), ["/d", "/s", "/c"]);
  // cmd.exe /s strips exactly the outer quote pair, leaving the deterministically
  // quoted command line: "<shim>" exec tsc -p tsconfig.json --noEmit
  assert.equal(
    spec.args[3],
    '""C:\\Program Files\\nodejs\\pnpm.cmd" exec tsc -p tsconfig.json --noEmit"',
  );
  assert.deepEqual(spec.options, { windowsVerbatimArguments: true });
});

test("win32 cmd.exe fallback quotes arguments containing spaces", () => {
  const binDir = "C:\\tools";
  const spec = resolveSpawnSpec("pnpm", ["exec", "vite", "build", "--base=/NexTerm Demo/"], {
    platform: "win32",
    env: { PATH: binDir },
    existsSync: fakeWindowsFs([`${binDir}\\pnpm.cmd`]),
  });
  assert.equal(spec.args[3], '"C:\\tools\\pnpm.cmd exec vite build "--base=/NexTerm Demo/""');
});

test("win32 cmd.exe fallback refuses arguments it cannot encode deterministically", () => {
  const binDir = "C:\\tools";
  const existsSync = fakeWindowsFs([`${binDir}\\pnpm.cmd`]);
  for (const dangerous of ["%PATH%", "a%b", "line\nbreak", "line\rbreak", "nul\0byte"]) {
    assert.throws(
      () => resolveSpawnSpec("pnpm", ["exec", "echo", dangerous], { platform: "win32", env: { PATH: binDir }, existsSync }),
      /refusing cmd\.exe argument/,
      `expected rejection for ${JSON.stringify(dangerous)}`,
    );
  }
});

test("quoteWindowsShellArg quotes conservatively and doubles embedded quotes", () => {
  assert.equal(quoteWindowsShellArg("exec"), "exec");
  assert.equal(quoteWindowsShellArg("tsconfig.json"), "tsconfig.json");
  assert.equal(quoteWindowsShellArg("C:\\Program Files\\nodejs\\pnpm.cmd"), '"C:\\Program Files\\nodejs\\pnpm.cmd"');
  assert.equal(quoteWindowsShellArg("--base=/NexTerm/demo/"), "--base=/NexTerm/demo/");
  assert.equal(quoteWindowsShellArg("a b"), '"a b"');
  assert.equal(quoteWindowsShellArg('say "hi"'), '"say ""hi"""');
  assert.equal(quoteWindowsShellArg("a&b"), '"a&b"');
  assert.throws(() => quoteWindowsShellArg(""), /empty or non-string/);
  assert.throws(() => quoteWindowsShellArg("%x%"), /refusing cmd\.exe argument/);
});

test("windowsShellCommandLine joins the command and args with single spaces", () => {
  assert.equal(
    windowsShellCommandLine("C:\\tools\\pnpm.cmd", ["exec", "tsc", "a b"]),
    'C:\\tools\\pnpm.cmd exec tsc "a b"',
  );
});

test("the resolved node+JS-entry spec executes for real and forwards arguments verbatim", (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-spawn-spec-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const binDir = path.join(root, "node_modules", ".bin");
  const entry = path.join(root, "node_modules", "pnpm", "bin", "pnpm.cjs");
  fs.mkdirSync(binDir, { recursive: true });
  fs.mkdirSync(path.dirname(entry), { recursive: true });
  fs.writeFileSync(path.join(binDir, "pnpm.cmd"), "@echo off\r\n");
  fs.writeFileSync(entry, "console.log(JSON.stringify(process.argv.slice(2)));\n");

  const spec = resolveSpawnSpec("pnpm", ["exec", "tsc", "-p", "tsconfig.json", "--noEmit", "--base=/NexTerm Demo/"], {
    platform: "win32",
    env: { PATH: binDir },
    execPath: NODE,
    existsSync: fs.existsSync,
    // POSIX join keeps the fixture paths spawnable on this host; the resolver
    // uses path.win32 by default on a real Windows machine.
    pathImpl: path.posix,
  });
  assert.equal(spec.command, NODE);
  assert.equal(spec.args[0], entry);
  assert.deepEqual(spec.options, {});

  const result = spawnSync(spec.command, spec.args, { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(JSON.parse(result.stdout), ["exec", "tsc", "-p", "tsconfig.json", "--noEmit", "--base=/NexTerm Demo/"]);
});

test("the passthrough spec executes for real on the host platform", () => {
  const spec = resolveSpawnSpec(NODE, ["-e", "process.stdout.write('spawn-spec-ok')"], {
    platform: process.platform,
  });
  const result = spawnSync(spec.command, spec.args, { encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "spawn-spec-ok");
});
