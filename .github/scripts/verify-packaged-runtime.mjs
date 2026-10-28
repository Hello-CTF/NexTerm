#!/usr/bin/env node
// Proves a packaged desktop artifact can load its bundled ONNX Runtime, on the
// host that built it. Unlike the build-time assertions (file presence, hashes),
// this extracts the shipped installer/deb/dmg and actually dlopens the runtime
// with a tiny Go loader (scripts/ort-load-check).
//
// usage: verify-packaged-runtime.mjs <artifact> <os> <arch> <ort-load-check binary>
//
// macOS: also checks the signature of the .app: when the hardened runtime is on
// it must carry com.apple.security.cs.disable-library-validation, and the
// loader is re-signed with the app's own flags and entitlements so a missing
// entitlement fails the load (the 1.3.0 regression: AMFI rejected the bundled
// dylib with "different Team IDs").
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const [artifact, goos, goarch, loader] = process.argv.slice(2);
if (!artifact || !goos || !goarch || !loader) {
  console.error("usage: verify-packaged-runtime.mjs <artifact> <os> <arch> <loader>");
  process.exit(2);
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: "utf8", ...options });
  if (result.error) throw result.error;
  return result;
}
function must(command, args, options) {
  const result = run(command, args, options);
  if (result.status !== 0) throw new Error(`${command} ${args.join(" ")} failed (${result.status}): ${result.stderr || result.stdout}`);
  return result;
}
function fail(message) {
  console.error(`::error::packaged runtime smoke (${goos}/${goarch}): ${message}`);
  process.exitCode = 1;
}

function loadCheck(runtimeDir, binary = loader, manifestDir = runtimeDir) {
  try {
    const manifest = JSON.parse(fs.readFileSync(path.join(manifestDir, "runtime.json"), "utf8"));
    if (manifest.unavailable) {
      console.log(`runtime declared unavailable for ${goos}/${goarch} (${manifest.unavailable}); load check skipped`);
      return;
    }
  } catch {
    // No readable manifest: the load itself decides.
  }
  const result = run(binary, [runtimeDir]);
  const text = `${result.stdout}${result.stderr}`.trim();
  if (result.status !== 0 || !/^OK \S+/m.test(result.stdout)) throw new Error(`runtime did not load from ${runtimeDir}: ${text}`);
  console.log(`runtime loaded from ${runtimeDir}: ${result.stdout.trim()}`);
}

function sevenZip() {
  for (const candidate of ["C:\\Program Files\\7-Zip\\7z.exe", "7z", "7zz"]) {
    if (run(candidate, ["i"]).status === 0) return candidate;
  }
  throw new Error("7-Zip is required to extract the NSIS installer");
}

function windows(work) {
  must(sevenZip(), ["x", "-y", `-o${work}`, path.resolve(artifact)]);
  const dir = path.join(work, "ai-runtime");
  const dll = path.join(dir, "onnxruntime.dll");
  if (!fs.existsSync(dll) && fs.existsSync(path.join(dir, "runtime.json"))) return loadCheck(dir);
  if (!fs.existsSync(dll)) throw new Error("onnxruntime.dll is missing from the installer payload");
  // The runtime is loaded from a directory that only has the shipped files, so
  // a missing app-local CRT DLL is not masked by a copy elsewhere in the tree.
  const body = fs.readFileSync(dll).toString("latin1").toLowerCase();
  const present = new Set(fs.readdirSync(dir).map((name) => name.toLowerCase()));
  for (const crt of ["vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll", "msvcp140_1.dll", "concrt140.dll"]) {
    if (body.includes(crt) && !present.has(crt)) throw new Error(`onnxruntime.dll imports ${crt} but the payload does not ship it`);
  }
  loadCheck(dir);
}

function linux(work) {
  const members = must("ar", ["t", path.resolve(artifact)]).stdout.split(/\r?\n/);
  const data = members.find((name) => name.startsWith("data.tar"));
  if (!data) throw new Error("deb has no data.tar member");
  must("ar", ["x", path.resolve(artifact), data], { cwd: work });
  must("tar", ["-xf", path.join(work, data), "-C", work]);
  loadCheck(path.join(work, "usr/bin/ai-runtime"));
}

function darwin(work) {
  const mountpoint = path.join(work, "mounted");
  fs.mkdirSync(mountpoint);
  must("hdiutil", ["attach", "-readonly", "-nobrowse", "-quiet", "-mountpoint", mountpoint, path.resolve(artifact)]);
  try {
    must("ditto", [path.join(mountpoint, "NexTerm.app"), path.join(work, "NexTerm.app")]);
  } finally {
    run("hdiutil", ["detach", "-quiet", mountpoint]);
  }
  const app = path.join(work, "NexTerm.app");
  must("codesign", ["--verify", "--deep", "--strict", app]);
  const details = run("codesign", ["-dvv", app]);
  const info = `${details.stdout}${details.stderr}`;
  const hardened = /flags=0x[0-9a-f]+\([^)]*runtime[^)]*\)/.test(info);
  const entitlementsFile = path.join(work, "app-entitlements.plist");
  fs.writeFileSync(entitlementsFile, run("codesign", ["-d", "--entitlements", ":-", app]).stdout);
  const entitlements = fs.readFileSync(entitlementsFile, "utf8");
  if (hardened && !entitlements.includes("com.apple.security.cs.disable-library-validation")) {
    throw new Error("hardened runtime is on but com.apple.security.cs.disable-library-validation is missing from the app signature");
  }
  console.log(`app signature: hardened runtime ${hardened ? "on" : "off"}, library-validation entitlement ${entitlements.includes("disable-library-validation") ? "present" : "absent"}`);
  // Re-sign the loader like the app (same identity, flags and entitlements) so
  // dyld/AMFI apply the same library validation to the bundled dylib.
  const identity = process.env.NEXTERM_MACOS_SIGN_IDENTITY || "-";
  const probe = path.join(work, "ort-load-check");
  fs.copyFileSync(loader, probe);
  fs.chmodSync(probe, 0o755);
  const args = ["--force", "--sign", identity];
  if (hardened) args.push("--options", "runtime");
  if (entitlements.includes("<key>")) args.push("--entitlements", entitlementsFile);
  must("codesign", [...args, probe]);
  loadCheck(path.join(app, "Contents/Frameworks/ai-runtime"), probe, path.join(app, "Contents/Resources/ai-runtime"));
}

const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-runtime-smoke-"));
try {
  if (goos === "windows") windows(work);
  else if (goos === "linux") linux(work);
  else if (goos === "darwin") darwin(work);
  else throw new Error(`unsupported os ${goos}`);
} catch (error) {
  fail(error.message);
} finally {
  fs.rmSync(work, { recursive: true, force: true });
}
