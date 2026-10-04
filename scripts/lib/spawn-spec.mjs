import fs from "node:fs";
import path from "node:path";

const BATCH_FILE = /\.(?:cmd|bat)$/i;
const JAVASCRIPT_ENTRY = /\.[cm]?js$/i;
const PNPM_ENTRY_BASENAME = /^pnpm\.[cm]?js$/i;
const UNSAFE_SHELL_ARGUMENT = /[%\0\r\n]/;
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
  return {
    command: comspec,
    args: ["/d", "/s", "/c", `"${line}"`],
    options: { windowsVerbatimArguments: true },
  };
}

function isPnpmCliEntry(candidate, pathImpl) {
  if (typeof candidate !== "string" || !JAVASCRIPT_ENTRY.test(candidate)) return false;
  if (!PNPM_ENTRY_BASENAME.test(pathImpl.basename(candidate))) return false;
  const parent = pathImpl.basename(pathImpl.dirname(candidate)).toLowerCase();
  const grandparent = pathImpl.basename(pathImpl.dirname(pathImpl.dirname(candidate))).toLowerCase();
  return (parent === "bin" && grandparent === "pnpm") || (parent === "dist" && grandparent === "corepack");
}

function resolveWindowsSpec(command, args, { env, execPath, existsSync, pathImpl }) {
  const passthrough = { command, args: [...args], options: {} };
  if (pathImpl.basename(command).replace(BATCH_FILE, "").toLowerCase() === "pnpm"
      && isPnpmCliEntry(env.npm_execpath, pathImpl)
      && existsSync(env.npm_execpath)) {
    return { command: execPath, args: [env.npm_execpath, ...args], options: {} };
  }
  if (isBatchFile(command)) return resolveBatchShim(command, args, { env, execPath, existsSync, pathImpl });
  if (hasPathSeparator(command) || pathImpl.extname(command)) return passthrough;
  for (const directory of windowsPathDirectories(env)) {
    if (existsSync(pathImpl.join(directory, `${command}.exe`))) return passthrough;
    for (const extension of ["bat", "cmd"]) {
      const shim = pathImpl.join(directory, `${command}.${extension}`);
      if (existsSync(shim)) return resolveBatchShim(shim, args, { env, execPath, existsSync, pathImpl });
    }
  }
  return passthrough;
}

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
