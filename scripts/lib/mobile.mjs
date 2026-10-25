import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { parseReleaseVersion } from "./semver.mjs";

// Shared mobile build source state. The Android and iOS Taskfiles and
// scripts/build.mjs must agree on the exact commit label embedded in release
// artifacts: the frontend dist manifest records the commit of the producing
// build, and `--consume-dist` rejects a mismatch. A tracked version stamp
// would dirty the tree and turn the label into `<commit>+dirty`, so the mobile
// stamp is written to an ignored generated config instead (see
// scripts/mobile-source.mjs) and never touches tracked files.
export const MOBILE_CONFIG_PATH = "build/config.yml";
export const MOBILE_STAMP_CONFIG_PATH = "build/config.stamp.yml";

const VERSION_PACKAGE = "github.com/Hello-CTF/NexTerm/internal/version";

function gitOutput(args, cwd) {
  const result = spawnSync("git", args, { cwd, encoding: "utf8" });
  return result.status === 0 ? result.stdout || "" : null;
}

// `git status --porcelain` lists tracked modifications and untracked files but
// not ignored ones, so the ignored stamp config keeps a stamped release tree
// clean while any other tracked or untracked change still reports dirty.
export function sourceState({ cwd = process.cwd() } = {}) {
  const commit = gitOutput(["rev-parse", "HEAD"], cwd)?.trim() || "unknown";
  const dirty = commit !== "unknown" && (gitOutput(["status", "--porcelain"], cwd)?.trim() || "") !== "";
  return { commit, dirty };
}

// Lists what made the tree dirty so a "+dirty" mismatch can be traced to the files that caused it.
export function dirtyPaths({ cwd = process.cwd() } = {}) {
  return (gitOutput(["status", "--porcelain"], cwd) || "").split("\n").filter(Boolean);
}

export function commitLabel({ commit, dirty }) {
  return dirty ? `${commit}+dirty` : commit;
}

// Release builds always run with NEXTERM_RELEASE_VERSION set (CI exports it at
// job level); local builds fall back to the internal/version default.
export function mobileVersion(env = process.env) {
  const raw = env.NEXTERM_RELEASE_VERSION || "";
  if (!raw) return "dev";
  const parsed = parseReleaseVersion(raw);
  if (!parsed) throw new Error(`NEXTERM_RELEASE_VERSION must be a non-placeholder semver: ${raw}`);
  return parsed.version;
}

// One flag generator for both mobile platforms so debug and release builds
// cannot drift: both stamp version and commit; release additionally strips and
// trims paths, debug keeps inlining disabled for the debugger.
export function mobileLdflags({ platform, production, version, commit }) {
  if (platform !== "android" && platform !== "ios") throw new Error(`unsupported mobile platform: ${platform || "missing"}`);
  const stamp = `-X ${VERSION_PACKAGE}.Version=${version} -X ${VERSION_PACKAGE}.Commit=${commit}`;
  if (production) return `-tags production,${platform} -trimpath -buildvcs=false -ldflags="-s -w ${stamp}"`;
  return `-tags ${platform},debug -buildvcs=false -gcflags=all="-l" -ldflags="${stamp}"`;
}

export function renderStampedMobileConfig(text, version) {
  if (!parseReleaseVersion(version || "")) {
    throw new Error(`release version must be a non-placeholder semver: ${version || "missing"}`);
  }
  let count = 0;
  const updated = text.replace(/(version:\s*")0\.0\.0(")/g, (_match, prefix, suffix) => {
    count += 1;
    return `${prefix}${version}${suffix}`;
  });
  if (count === 0) throw new Error("mobile stamp config replaced nothing; the tracked config must keep 0.0.0 placeholders");
  return { updated, count };
}

export function stampMobileConfig({ configPath, outPath, version }) {
  const { updated, count } = renderStampedMobileConfig(fs.readFileSync(configPath, "utf8"), version);
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  // The overlay and Xcode generation tasks run in parallel and both refresh
  // the stamp; write-then-rename keeps readers from observing a partial file.
  const temporary = `${outPath}.tmp-${process.pid}`;
  fs.writeFileSync(temporary, updated);
  fs.renameSync(temporary, outPath);
  return count;
}

export function resolveMobileConfigPath({ root = process.cwd(), configPath = MOBILE_CONFIG_PATH, stampPath = MOBILE_STAMP_CONFIG_PATH } = {}) {
  return fs.existsSync(path.join(root, stampPath)) ? stampPath : configPath;
}
