/**
 * Deterministic makensis discovery for the Windows desktop package step.
 *
 * Release run 37149307116: `choco install nsis --version=3.11` succeeded on
 * both Windows runner families, but the windows-latest job failed with
 * `makensis: spawnSync makensis ENOENT`. The nsis.install package runs the
 * official NSIS setup, which deploys makensis.exe to
 * `C:\Program Files (x86)\NSIS`, creates no `C:\ProgramData\chocolatey\bin`
 * shim, and writes no PATH entry of any kind (the v3.11 installer script
 * contains no Environment writes) — so a mid-job install leaves the running
 * job's PATH without any NSIS directory. The windows-11-arm job passed by
 * image luck: the runner image build adds `C:\Program Files (x86)\NSIS\` to
 * the machine PATH itself (runner-images Install-NSIS.ps1,
 * Add-MachinePathItem) and preinstalls NSIS 3.10, so the directory was
 * already on PATH before the job began.
 *
 * Resolution order (first hit wins; every root comes from the environment,
 * so nothing here is tied to one runner family's accidental layout):
 *   1. makensis.exe on PATH — ordinary installs whose PATH is fresh, and
 *      images that preinstall NSIS.
 *   2. %ProgramFiles(x86)%\NSIS\makensis.exe — the NSIS setup default on
 *      every Windows family, x64 and ARM64 alike (makensis is a 32-bit x86
 *      binary). This is where Chocolatey nsis.install deploys it.
 *   3. %ProgramFiles%\NSIS\makensis.exe — the native-program-files variant.
 *   4. %ChocolateyInstall%\bin\makensis.exe — the shim directory, should a
 *      package or an admin ever create a makensis shim there.
 *
 * Every input is injectable so the Windows branches run as unit tests on
 * POSIX hosts, mirroring scripts/lib/spawn-spec.mjs.
 */

import fs from "node:fs";
import path from "node:path";

// Chocolatey's documented default; used only when ChocolateyInstall is unset.
const DEFAULT_CHOCOLATEY_INSTALL = "C:\\ProgramData\\chocolatey";

function windowsPathValue(env) {
  return env.PATH ?? env.Path ?? env.path ?? "";
}

/**
 * Resolve the makensis executable for a Windows environment.
 * Returns { command, source }; throws an Error listing every probed
 * location when nothing is found, so a broken CI image fails with the
 * searched paths instead of a bare ENOENT.
 */
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
