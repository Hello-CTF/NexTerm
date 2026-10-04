import fs from "node:fs";
import path from "node:path";

const DEFAULT_CHOCOLATEY_INSTALL = "C:\\ProgramData\\chocolatey";

function windowsPathValue(env) {
  return env.PATH ?? env.Path ?? env.path ?? "";
}

export function resolveMakensis(options = {}) {
  const { env = process.env, existsSync = fs.existsSync } = options;
  const pathImpl = options.pathImpl ?? path.win32;
  const probed = [];
  const consider = (candidate, source) => {
    probed.push(candidate);
    return existsSync(candidate) ? { command: candidate, source } : null;
  };
  for (const directory of windowsPathValue(env).split(";").map((entry) => entry.trim()).filter(Boolean)) {
    const hit = consider(pathImpl.join(directory, "makensis.exe"), "PATH");
    if (hit) return hit;
  }
  for (const [root, source] of [
    [env["ProgramFiles(x86)"], "NSIS default install under ProgramFiles(x86)"],
    [env.ProgramFiles, "NSIS default install under ProgramFiles"],
  ]) {
    if (!root) continue;
    const hit = consider(pathImpl.join(root, "NSIS", "makensis.exe"), source);
    if (hit) return hit;
  }
  const chocolatey = env.ChocolateyInstall || DEFAULT_CHOCOLATEY_INSTALL;
  const hit = consider(pathImpl.join(chocolatey, "bin", "makensis.exe"), "Chocolatey bin shim");
  if (hit) return hit;
  throw new Error(
    "makensis was not found; install NSIS (`choco install nsis`) or put makensis.exe on PATH. Probed locations:\n"
      + probed.map((candidate) => `  - ${candidate}`).join("\n"),
  );
}
