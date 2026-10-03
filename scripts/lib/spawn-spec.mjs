/**
 * Deterministic child-process invocation specs for the shared build harness.
 *
 * Since the Node.js CVE-2024-27980 fix (Node 18.20.2 / 20.12.2 / 22.x and
 * later), spawnSync(file, args) without a shell refuses to launch Windows
 * .cmd/.bat shims with EINVAL. The release Windows desktop jobs hit exactly
 * that for `pnpm.cmd exec tsc` (CI run 37139368157). Node 24 keeps the same
 * behavior, so the harness must never hand a bare batch shim to spawnSync.
 *
 * Resolution order for a command on win32:
 *   1. `npm_execpath` when launching pnpm from inside a pnpm script — it is
 *      already the real CLI JS entry.
 *   2. A real `<name>.exe` found while walking PATH — batch shims are only
 *      used when no executable exists in an earlier-or-equal directory, so an
 *      exe hit means the default CreateProcess resolution is safe and the
 *      invocation passes through untouched.
 *   3. The batch shim's sibling JS entry, launched with the current node
 *      executable (no shell at all, no quoting surface):
 *        - npm global / pnpm/action-setup: `<shimdir>/../<name>/bin/<name>.cjs`
 *        - shim colocated with a node_modules root: `<shimdir>/node_modules/<name>/bin/<name>.cjs`
 *        - corepack: `<shimdir>/node_modules/corepack/dist/<name>.js`
 *   4. A `cmd.exe /d /s /c` fallback whose command line is quoted by
 *      quoteWindowsShellArg — deterministic, spaces-safe, and closed to
 *      injection: `%`, NUL, CR and LF are rejected outright, quotes are
 *      doubled, and every other metacharacter is neutralized by quoting.
 *
 * Non-Windows platforms, explicit non-batch paths, and unresolvable names all
 * pass through unchanged so existing error behavior (ENOENT etc.) is kept.
 *
 * Every input (platform, env, fs, path implementation, node executable) is
 * injectable so the Windows branches are executable as unit tests on POSIX
 * hosts; scripts/lib/spawn-spec.test.mjs also runs the resolved specs through
 * the real spawnSync.
 */

import fs from "node:fs";
import path from "node:path";

const BATCH_FILE = /\.(?:cmd|bat)$/i;
const JAVASCRIPT_ENTRY = /\.[cm]?js$/i;
// cmd.exe expands %VAR% before quote parsing, so `%` cannot be neutralized by
// quoting; NUL/CR/LF would corrupt the command line outright. Everything else
// (& | < > ^ ") is inert inside a double-quoted argument.
const UNSAFE_SHELL_ARGUMENT = /[%\0\r\n]/;
// Conservative "plain token" set that needs no quoting in a cmd.exe line.
const PLAIN_SHELL_ARGUMENT = /^[-\w./:=@+\\]+$/;

function windowsPathValue(env) {
  return env.PATH ?? env.Path ?? env.path ?? "";
}

function windowsPathDirectories(env) {
  return windowsPathValue(env)
    .split(";")
    .map((directory) => directory.trim())
    .filter(Boolean);
}

function isBatchFile(command) {
  return BATCH_FILE.test(command);
}

function hasPathSeparator(command) {
  return command.includes("/") || command.includes("\\");
}

/**
 * Quote one argument for a cmd.exe command line. Throws on arguments that
 * cannot be encoded deterministically instead of risking shell expansion.
 */
export function quoteWindowsShellArg(argument) {
  if (typeof argument !== "string" || argument.length === 0) {
    throw new Error(`spawn-spec: refusing an empty or non-string cmd.exe argument: ${JSON.stringify(argument)}`);
  }
  if (UNSAFE_SHELL_ARGUMENT.test(argument)) {
    throw new Error(`spawn-spec: refusing cmd.exe argument containing %, NUL, CR or LF: ${JSON.stringify(argument)}`);
  }
  if (PLAIN_SHELL_ARGUMENT.test(argument)) return argument;
  return `"${argument.replaceAll('"', '""')}"`;
}

/**
 * Build the deterministic cmd.exe command line for a batch shim invocation.
 */
export function windowsShellCommandLine(command, args) {
  return [command, ...args].map(quoteWindowsShellArg).join(" ");
}

function resolveBatchShim(shimPath, args, { env, execPath, existsSync, pathImpl }) {
  const directory = pathImpl.dirname(shimPath);
  const name = pathImpl.basename(shimPath).replace(BATCH_FILE, "");
  const siblingEntries = [
    pathImpl.join(directory, "..", name, "bin", `${name}.cjs`),
    pathImpl.join(directory, "node_modules", name, "bin", `${name}.cjs`),
    pathImpl.join(directory, "node_modules", "corepack", "dist", `${name}.js`),
  ];
  const entry = siblingEntries.find((candidate) => existsSync(candidate));
  if (entry) {
    return { command: execPath, args: [entry, ...args], options: {} };
  }
  const comspec = env.ComSpec || env.COMSPEC || "cmd.exe";
  const line = windowsShellCommandLine(shimPath, args);
  // Mirrors Node's own shell:true construction, except the command line is
  // quoted deterministically above instead of joined verbatim.
  return {
    command: comspec,
    args: ["/d", "/s", "/c", `"${line}"`],
    options: { windowsVerbatimArguments: true },
  };
}

function resolveWindowsSpec(command, args, { env, execPath, existsSync, pathImpl }) {
  const passthrough = { command, args: [...args], options: {} };
  if (pathImpl.basename(command).replace(BATCH_FILE, "").toLowerCase() === "pnpm"
      && typeof env.npm_execpath === "string"
      && JAVASCRIPT_ENTRY.test(env.npm_execpath)
      && existsSync(env.npm_execpath)) {
    return { command: execPath, args: [env.npm_execpath, ...args], options: {} };
  }
  if (isBatchFile(command)) return resolveBatchShim(command, args, { env, execPath, existsSync, pathImpl });
  if (hasPathSeparator(command) || pathImpl.extname(command)) return passthrough;
  // Bare name: walk PATH in order, honoring PATHEXT precedence (exe before
  // bat/cmd) per directory, to learn what CreateProcess would resolve to.
  for (const directory of windowsPathDirectories(env)) {
    if (existsSync(pathImpl.join(directory, `${command}.exe`))) return passthrough;
    for (const extension of ["bat", "cmd"]) {
      const shim = pathImpl.join(directory, `${command}.${extension}`);
      if (existsSync(shim)) return resolveBatchShim(shim, args, { env, execPath, existsSync, pathImpl });
    }
  }
  return passthrough;
}

/**
 * Resolve the deterministic spawn spec for a command.
 *
 * Returns { command, args, options } where options may carry
 * windowsVerbatimArguments for the cmd.exe fallback. All other platforms and
 * commands get a byte-identical passthrough of the request.
 */
export function resolveSpawnSpec(command, args, options = {}) {
  const {
    platform = process.platform,
    env = process.env,
    execPath = process.execPath,
    existsSync = fs.existsSync,
  } = options;
  const pathImpl = options.pathImpl ?? (platform === "win32" ? path.win32 : path);
  if (platform !== "win32") return { command, args: [...args], options: {} };
  return resolveWindowsSpec(command, args, { env, execPath, existsSync, pathImpl });
}
