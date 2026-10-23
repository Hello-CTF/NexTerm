import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { releaseArtifacts } from "../.github/scripts/release-artifacts.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SCRIPT = path.join(ROOT, "scripts", "release-matrix.mjs");
const native = JSON.parse(fs.readFileSync(path.join(ROOT, ".github", "native-matrix.json"), "utf8"));

test("release desktop matrix is generated from the native matrix", () => {
  const result = spawnSync(process.execPath, [SCRIPT], { cwd: ROOT, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  const expected = native.filter((entry) => entry.kind === "desktop").map(({ runner, os, arch }) => ({ runner, os, arch }));
  assert.deepEqual(JSON.parse(result.stdout), { include: expected });
});

test("desktop release artifacts follow every native matrix row without changing targets", () => {
  const version = "1.2.3";
  const artifacts = releaseArtifacts(version);
  assert.equal(artifacts.size, 14);
  for (const name of [
    `nexterm-desktop_windows_amd64_${version}.exe`,
    `nexterm-desktop_windows_arm64_${version}.exe`,
    `nexterm-desktop_macos_arm64_${version}.dmg`,
    `nexterm-desktop_macos_amd64_${version}.dmg`,
    `nexterm-desktop_linux_amd64_${version}.deb`,
    `nexterm-desktop_linux_arm64_${version}.deb`,
  ]) {
    assert.ok(artifacts.has(name), name);
  }
  for (const { os, arch } of native.filter((entry) => entry.kind === "desktop")) {
    const matches = [...artifacts.values()].filter((id) => id.startsWith("desktop-") && id.endsWith(`-${os}-${arch}`));
    assert.equal(matches.length, 1, `${os}/${arch}`);
  }
});
