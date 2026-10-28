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

// The official onnxruntime.dll imports the Visual C++ runtime; the DLLs it
// actually imports ship app-local next to the library and are listed in the
// manifest's crt section. The UCRT is part of Windows 10 and later.
export const isCRTImport = (name) => /^(?:msvcp|vcruntime|concrt|vcomp)\d+(?:_\d+)?\.dll$/i.test(name);

// Lists the DLLs a PE image imports (regular and delay-load imports), lowercased.
export function peImports(buffer) {
  const fail = () => { throw new Error("Not a valid PE image"); };
  if (buffer.length < 0x40 || buffer.readUInt16LE(0) !== 0x5a4d) fail();
  const pe = buffer.readUInt32LE(0x3c);
  if (pe + 24 > buffer.length || buffer.readUInt32LE(pe) !== 0x4550) fail();
  const sectionCount = buffer.readUInt16LE(pe + 6);
  const optionalSize = buffer.readUInt16LE(pe + 20);
  const optional = pe + 24;
  const magic = buffer.readUInt16LE(optional);
  if (magic !== 0x10b && magic !== 0x20b) fail();
  const directories = optional + (magic === 0x20b ? 112 : 96);
  const sections = [];
  for (let i = 0; i < sectionCount; i++) {
    const at = optional + optionalSize + i * 40;
    sections.push({ size: Math.max(buffer.readUInt32LE(at + 8), buffer.readUInt32LE(at + 16)), rva: buffer.readUInt32LE(at + 12), raw: buffer.readUInt32LE(at + 20) });
  }
  const offset = (rva) => {
    const section = sections.find((item) => rva >= item.rva && rva < item.rva + item.size);
    if (!section) fail();
    return section.raw + rva - section.rva;
  };
  const cString = (rva) => { const start = offset(rva); return buffer.toString("latin1", start, buffer.indexOf(0, start)).toLowerCase(); };
  const names = new Set();
  const walk = (index, stride, nameField) => {
    const rva = buffer.readUInt32LE(directories + index * 8);
    if (!rva) return;
    for (let at = offset(rva); at + stride <= buffer.length && buffer.readUInt32LE(at + nameField) !== 0; at += stride) names.add(cString(buffer.readUInt32LE(at + nameField)));
  };
  walk(1, 20, 12);
  walk(13, 32, 4);
  return [...names].sort();
}

export function runtimeFiles(os, extra = []) {
  return [runtimeLibrary(os), ...extra];
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
  if (manifest.unavailable !== undefined) {
    // A target whose ONNX Runtime release publishes no library ships only this manifest.
    if (typeof manifest.unavailable !== "string" || !manifest.unavailable) throw new Error("Privacy runtime unavailable reason is missing");
    if (Object.keys(manifest.files).length) throw new Error("Unavailable privacy runtime must not list files");
    for (const root of new Set([directory, libraryDirectory])) {
      const extra = fs.existsSync(root) ? fs.readdirSync(root).filter((name) => name !== "runtime.json") : [];
      if (extra.length) throw new Error(`Unavailable privacy runtime contains unexpected files: ${extra.join(", ")}`);
    }
    return { manifest, digest, library: null, sizeBytes: 0, unavailable: manifest.unavailable };
  }
  if (os === "windows" && (!manifest.crt || typeof manifest.crt.files !== "object")) throw new Error("Privacy runtime manifest is missing the Visual C++ runtime section");
  const binaries = runtimeFiles(os, os === "windows" ? Object.keys(manifest.crt.files) : []);
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
