// Pinned Wails CLI invocation and IPC bindings generation/drift check.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { GO_TOOLCHAIN, ROOT, WAILS_MODULE, WAILS_VERSION, die, flag, log, run } from "./build-context.mjs";

function compareDirectories(expected, actual, prefix = "") {
  const expectedEntries = fs.readdirSync(expected, { withFileTypes: true });
  const actualEntries = fs.readdirSync(actual, { withFileTypes: true });
  const names = new Set([...expectedEntries, ...actualEntries].map((entry) => entry.name));
  const differences = [];
  for (const name of [...names].sort()) {
    const relative = path.join(prefix, name);
    const left = path.join(expected, name);
    const right = path.join(actual, name);
    if (!fs.existsSync(left) || !fs.existsSync(right)) {
      differences.push(`${relative}: only in ${fs.existsSync(left) ? "committed bindings" : "generated bindings"}`);
      continue;
    }
    const leftStat = fs.statSync(left);
    const rightStat = fs.statSync(right);
    if (leftStat.isDirectory() && rightStat.isDirectory()) differences.push(...compareDirectories(left, right, relative));
    else if (!leftStat.isFile() || !rightStat.isFile() || !fs.readFileSync(left).equals(fs.readFileSync(right))) {
      differences.push(`${relative}: content differs`);
    }
  }
  return differences;
}

let wailsVerified = false;

export function runWails(args, options = {}) {
  const env = { ...process.env, GOTOOLCHAIN: GO_TOOLCHAIN, ...options.env };
  if (!wailsVerified) {
    const result = run("go", ["run", `${WAILS_MODULE}@${WAILS_VERSION}`, "version"], { env, quiet: true });
    const outputLines = `${result.stdout || ""}${result.stderr || ""}`.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    const actual = outputLines.at(-1) || "unavailable";
    if (actual !== WAILS_VERSION) die(`Wails CLI version ${actual} does not match go.mod ${WAILS_VERSION}`);
    wailsVerified = true;
    log(`Wails CLI: ${actual} (pinned by go.mod)`);
  }
  return run("go", ["run", `${WAILS_MODULE}@${WAILS_VERSION}`, ...args], { ...options, env });
}

function replaceBindings(generated, committed) {
  const backup = `${committed}.previous-${process.pid}`;
  const hadCommitted = fs.existsSync(committed);
  if (hadCommitted) fs.renameSync(committed, backup);
  try {
    fs.cpSync(generated, committed, { recursive: true });
    if (hadCommitted) fs.rmSync(backup, { recursive: true, force: true });
  } catch (error) {
    fs.rmSync(committed, { recursive: true, force: true });
    if (hadCommitted) fs.renameSync(backup, committed);
    throw error;
  }
}

export function verifyBindings() {
  const committed = path.join(ROOT, "src/ipc/bindings");
  const write = flag("write");
  if (!write && !fs.existsSync(committed)) die("src/ipc/bindings is missing; generate and review bindings before enabling drift checks");
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-bindings-"));
  try {
    runWails(["generate", "bindings", "-ts", "-i", "-d", temporary, "./cmd/nexterm-desktop"]);
    if (write) {
      replaceBindings(temporary, committed);
      log(`Wails bindings generated with ${WAILS_VERSION}`);
      return;
    }
    const differences = compareDirectories(committed, temporary);
    if (differences.length) die(`Wails bindings drift detected:\n${differences.join("\n")}`);
    log("Wails bindings: no drift");
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
}
