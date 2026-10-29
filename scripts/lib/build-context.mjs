// Shared build state: paths, parsed options, version/source metadata, process helpers.
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { commitLabel, dirtyPaths, sourceState } from "./mobile.mjs";
import { resolveSpawnSpec } from "./spawn-spec.mjs";
import { parseReleaseVersion, parseSemVer } from "./semver.mjs";

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "..");
const goMod = fs.readFileSync(path.join(ROOT, "go.mod"), "utf8");
export const WAILS_MODULE = "github.com/wailsapp/wails/v3/cmd/wails3";
export const WAILS_VERSION = goMod.match(/^\s*github\.com\/wailsapp\/wails\/v3\s+(v\S+)\s*$/m)?.[1];
export const GO_TOOLCHAIN = goMod.match(/^toolchain\s+(go\d+\.\d+\.\d+)\s*$/m)?.[1];
if (!WAILS_VERSION || !GO_TOOLCHAIN) die("go.mod must declare the Wails module and an exact Go toolchain");
export const RSRC_VERSION = "v0.10.2";
const argv = process.argv.slice(2);
export const command = argv[0] && !argv[0].startsWith("-") ? argv.shift() : "debug";
const KNOWN_OPTIONS = new Set([
  "arch", "assets-dir", "base", "binary", "binary-manifest", "cgo", "consume-dist",
  "dist-manifest", "file", "helpers-dir", "id", "kind", "os", "out", "output", "package",
  "package-only", "release", "repro-check", "require-evidence",
  "skip-frontend", "write",
]);
export const options = {};
for (const arg of argv) {
  if (!arg.startsWith("--")) die(`unknown positional argument: ${arg}`);
  const [name, ...rest] = arg.slice(2).split("=");
  if (!KNOWN_OPTIONS.has(name)) die(`unknown option: --${name}`);
  options[name] = rest.length ? rest.join("=") : "true";
}

export const wailsConfig = JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8"));
const WAILS_VERSION_PLACEHOLDER = wailsConfig.info?.version;
// The desktop entry is named after the GTK application id (Linux.ApplicationID
// in cmd/nexterm-desktop) so shells match windows to it and its icon.
export const LINUX_DESKTOP_ENTRY = `usr/share/applications/${wailsConfig.info.productIdentifier}.desktop`;
export const VERSION = process.env.NEXTERM_RELEASE_VERSION || WAILS_VERSION_PLACEHOLDER;
export const VERSION_INFO = process.env.NEXTERM_RELEASE_VERSION ? parseReleaseVersion(VERSION) : parseSemVer(VERSION);
if (!VERSION_INFO) {
  die(`release version is not a semver version: ${VERSION}`);
}
export const packageJSON = JSON.parse(fs.readFileSync(path.join(ROOT, "package.json"), "utf8"));
const packageVersion = packageJSON.version;
if (packageVersion !== WAILS_VERSION_PLACEHOLDER) die(`package.json version ${packageVersion} diverges from the wails.json placeholder ${WAILS_VERSION_PLACEHOLDER}`);
export let SOURCE_DATE_EPOCH = process.env.SOURCE_DATE_EPOCH || "";
export const { commit: SOURCE_COMMIT, dirty: DIRTY } = sourceState({ cwd: ROOT });
export const COMMIT = commitLabel({ commit: SOURCE_COMMIT, dirty: DIRTY });
export const DIRTY_PATHS = DIRTY ? dirtyPaths({ cwd: ROOT }) : [];
export const RELEASE_REF = (process.env.NEXTERM_RELEASE_REF || "").trim();
SOURCE_DATE_EPOCH ||= output("git", ["show", "-s", "--format=%ct", "HEAD"], { allowFailure: true })?.trim();
if (!SOURCE_DATE_EPOCH || !/^\d+$/.test(SOURCE_DATE_EPOCH)) {
  die("SOURCE_DATE_EPOCH is required when the git commit timestamp is unavailable");
}

export const HOST_OS = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
export const HOST_ARCH = {
  arm: String(process.config.variables.arm_version) === "7" ? "armv7" : `armv${process.config.variables.arm_version || "unknown"}`,
  arm64: "arm64",
  loong64: "loong64",
  riscv64: "riscv64",
  x64: "amd64",
}[process.arch];

export function log(message) {
  console.log(message);
}

export function die(message) {
  console.error(`\nbuild: ${message}`);
  process.exit(1);
}

export function run(command, args, { cwd = ROOT, env = process.env, allowFailure = false, quiet = false, timeout = 0 } = {}) {
  const spec = resolveSpawnSpec(command, args, { platform: process.platform, env, execPath: process.execPath });
  if (!quiet) log(`\n$ ${spec.command} ${spec.args.join(" ")}`);
  const result = spawnSync(spec.command, spec.args, {
    cwd,
    env: { ...env, SOURCE_DATE_EPOCH: String(SOURCE_DATE_EPOCH), TZ: "UTC" },
    stdio: quiet ? "pipe" : "inherit",
    encoding: quiet ? "utf8" : undefined,
    timeout: timeout || undefined,
    ...spec.options,
  });
  if (result.error && !allowFailure) die(`${command}: ${result.error.message}`);
  if ((result.status !== 0 || result.signal) && !allowFailure) {
    die(`${command} exited with ${result.status ?? result.signal}`);
  }
  return result;
}

export function output(command, args, options = {}) {
  const result = run(command, args, { ...options, quiet: true });
  return result.status === 0 ? (result.stdout || "") : null;
}

let archiveToolPath = null;

// archiveTool builds scripts/archive once per run for reproducible tar/zip work.
export function archiveTool() {
  if (archiveToolPath) return archiveToolPath;
  const dir = path.join(ROOT, "target/package-work/tools");
  fs.mkdirSync(dir, { recursive: true });
  archiveToolPath = path.join(dir, process.platform === "win32" ? "archive.exe" : "archive");
  run("go", ["build", "-o", archiveToolPath, "./scripts/archive"]);
  return archiveToolPath;
}

export function option(name, fallback) {
  return options[name] === undefined ? fallback : options[name];
}

export function flag(name) {
  return options[name] === "true" || options[name] === "1";
}

export function targetOS() {
  return option("os", HOST_OS);
}

export function targetArch() {
  return option("arch", HOST_ARCH);
}
