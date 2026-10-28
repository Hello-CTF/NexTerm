import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

export function runtimeLibraries(os) {
  const names = {
    darwin: ["libnexterm_privacy.dylib", "libonnxruntime.dylib"],
    linux: ["libnexterm_privacy.so", "libonnxruntime.so"],
    windows: ["nexterm_privacy.dll", "onnxruntime.dll"],
  };
  if (!names[os]) throw new Error(`Unsupported privacy runtime OS: ${os}`);
  return names[os];
}

export const fileDigest = (file) => crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");

export function verifyRuntime(directory, os, arch, expectedDigest, libraryDirectory = directory) {
  const manifestFile = path.join(directory, "runtime.json");
  const digest = fileDigest(manifestFile);
  if (expectedDigest && digest !== expectedDigest) throw new Error("Privacy runtime differs from the producing binary manifest");
  const manifest = JSON.parse(fs.readFileSync(manifestFile, "utf8"));
  if (manifest.abi !== 1 || manifest.platform !== os || manifest.arch !== arch) throw new Error(`Privacy runtime target mismatch: expected ${os}/${arch}`);
  if (!manifest.files || typeof manifest.files !== "object" || Array.isArray(manifest.files)) throw new Error("Privacy runtime file manifest is missing");
  const crt = os === "windows" ? ["msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll", ...(arch === "amd64" ? ["vcruntime140_1.dll"] : [])] : [];
  for (const name of [...runtimeLibraries(os), ...crt, "licenses/ASTRLINK-LICENSE", "licenses/ONNX-RUNTIME-LICENSE", "licenses/native-dependencies.json"]) {
    if (!manifest.files[name]) throw new Error(`Privacy runtime manifest is missing ${name}`);
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
  return { manifest, digest };
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
