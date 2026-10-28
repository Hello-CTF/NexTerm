#!/usr/bin/env node

// Installs the pinned Zig toolchain whose `zig cc` links the CGO Linux server
// against the glibc floor in scripts/lib/elf-glibc.mjs.
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

export const ZIG_VERSION = "0.16.0";
export const ZIG_ARCHIVES = {
  "linux-x64": ["zig-x86_64-linux-0.16.0.tar.xz", "70e49664a74374b48b51e6f3fdfbf437f6395d42509050588bd49abe52ba3d00"],
  "linux-arm64": ["zig-aarch64-linux-0.16.0.tar.xz", "ea4b09bfb22ec6f6c6ceac57ab63efb6b46e17ab08d21f69f3a48b38e1534f17"],
};

export function zigArchive(platform = process.platform, arch = process.arch) {
  const entry = ZIG_ARCHIVES[`${platform}-${arch}`];
  if (!entry) throw new Error(`No pinned Zig ${ZIG_VERSION} archive for ${platform}/${arch}; the CGO Linux server is built on Linux hosts`);
  const [name, sha256] = entry;
  return { name, sha256, url: `https://ziglang.org/download/${ZIG_VERSION}/${name}`, directory: name.replace(/\.tar\.xz$/, "") };
}

export async function installZig(destination) {
  const archive = zigArchive();
  const root = path.resolve(destination);
  const binary = path.join(root, archive.directory, "zig");
  if (fs.existsSync(binary)) {
    const version = spawnSync(binary, ["version"], { encoding: "utf8" }).stdout?.trim();
    if (version === ZIG_VERSION) return binary;
  }
  const response = await fetch(archive.url, { signal: AbortSignal.timeout(600_000) });
  if (!response.ok) throw new Error(`Zig download returned HTTP ${response.status}`);
  const data = Buffer.from(await response.arrayBuffer());
  const digest = createHash("sha256").update(data).digest("hex");
  if (digest !== archive.sha256) throw new Error(`Zig checksum mismatch: ${archive.name} ${digest}`);
  fs.mkdirSync(root, { recursive: true });
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-zig-"));
  try {
    const file = path.join(temporary, archive.name);
    fs.writeFileSync(file, data);
    fs.rmSync(path.join(root, archive.directory), { recursive: true, force: true });
    const result = spawnSync("tar", ["-xJf", file, "-C", root], { stdio: "inherit" });
    if (result.status !== 0) throw new Error("Zig archive extraction failed");
  } finally {
    fs.rmSync(temporary, { recursive: true, force: true });
  }
  return binary;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const destination = process.argv[2];
  if (!destination) throw new Error("usage: install-zig.mjs <directory>  (prints the zig executable path)");
  process.stdout.write(`${await installZig(destination)}\n`);
}
