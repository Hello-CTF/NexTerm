import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

// The runtime directory carries only the ONNX Runtime library (plus the Visual
// C++ runtime it imports on Windows), licenses and the manifest that binds them
// to the producing binary.
export const RUNTIME_ABI = 2;
export const RUNTIME_LICENSES = ["licenses/ASTRLINK-LICENSE", "licenses/ASTRLINK-NOTICE.md", "licenses/ONNX-RUNTIME-LICENSE", "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"];

export function runtimeLibrary(os) {
  const names = {
    darwin: "libonnxruntime.dylib",
    linux: "libonnxruntime.so",
    windows: "onnxruntime.dll",
    android: "libonnxruntime.so",
    ios: "onnxruntime.framework/onnxruntime",
  };
  if (!names[os]) throw new Error(`Unsupported privacy runtime OS: ${os}`);
  return names[os];
}

// The official onnxruntime.dll imports the Visual C++ runtime (MSVCP140,
// MSVCP140_1, VCRUNTIME140 and, on x64, VCRUNTIME140_1); it ships app-local
// next to the library. The UCRT is part of Windows 10 and later.
export function windowsCRT(arch) {
  return ["msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll", ...(arch === "amd64" ? ["vcruntime140_1.dll"] : [])];
}

export function runtimeFiles(os, arch) {
  return [runtimeLibrary(os), ...(os === "windows" ? windowsCRT(arch) : [])];
}

export const fileDigest = (file) => crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");

export function verifyRuntime(directory, os, arch, expectedDigest, libraryDirectory = directory) {
  const manifestFile = path.join(directory, "runtime.json");
  const digest = fileDigest(manifestFile);
  if (expectedDigest && digest !== expectedDigest) throw new Error("Privacy runtime differs from the producing binary manifest");
  const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
  if (manifest.abi !== RUNTIME_ABI || manifest.platform !== os || manifest.arch !== arch) throw new Error(`Privacy runtime target mismatch: expected ${os}/${arch} (abi ${RUNTIME_ABI})`);
  if (!manifest.files || typeof manifest.files !== "object" || Array.isArray(manifest.files)) throw new Error("Privacy runtime file manifest is missing");
  const library = runtimeLibrary(os);
  const binaries = runtimeFiles(os, arch);
  for (const name of [...binaries, ...RUNTIME_LICENSES]) {
    if (!manifest.files[name]) throw new Error(`Privacy runtime manifest is missing ${name}`);
  }
  for (const name of Object.keys(manifest.files)) {
    if (!binaries.includes(name) && !name.startsWith("licenses/")) throw new Error(`Privacy runtime contains an unexpected file: ${name}`);
  }
  const seen = new Set();
  const visit = (directory, prefix = "") => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      const name = prefix + entry.name;
      if (name === "runtime.json") continue;
      const absolute = path.join(directory, entry.name);
      if (entry.isDirectory()) visit(absolute, `${name}/`);
      else {
        if (seen.has(name)) throw new Error(`Privacy runtime contains a duplicate file: ${name}`);
        if (!entry.isFile()) throw new Error(`Privacy runtime contains a non-regular file: ${name}`);
        if (manifest.files[name] !== fileDigest(absolute)) throw new Error(`Privacy runtime checksum mismatch: ${name}`);
        seen.add(name);
      }
    }
  };
  visit(directory);
  if (libraryDirectory !== directory) visit(libraryDirectory);
  for (const name of Object.keys(manifest.files)) if (!seen.has(name)) throw new Error(`Privacy runtime file is missing: ${name}`);
  const sizeBytes = fs.statSync(path.join(libraryDirectory, library)).size;
  return { manifest, digest, library, sizeBytes };
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  const [directory, os, arch, binaryManifest] = process.argv.slice(2);
  if (!directory || !os || !arch) throw new Error("usage: privacy-runtime.mjs <directory> <os> <arch> [binary.manifest.json]");
  let expectedDigest;
  if (binaryManifest) {
    expectedDigest = JSON.parse(fs.readFileSync(binaryManifest, "utf8")).privacy_runtime_sha256;
    if (!expectedDigest) throw new Error("Producing binary manifest has no privacy runtime evidence");
  }
  verifyRuntime(directory, os, arch, expectedDigest);
}
