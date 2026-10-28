import fs from "node:fs";
import path from "node:path";

// On Windows, the GNU tar bundled with MSYS toolchains often precedes System32
// on PATH. It parses "C:\x.zip" as host:path and cannot read zip archives, so
// prefer the bsdtar that ships with Windows 10 1803+ / Server 2019+.
export function resolveTar(platform = process.platform, env = process.env, exists = fs.existsSync) {
  if (platform !== "win32") return "tar";
  const candidate = path.win32.join(env.SystemRoot || "C:\\Windows", "System32", "tar.exe");
  return exists(candidate) ? candidate : "tar";
}
