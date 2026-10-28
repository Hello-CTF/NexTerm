import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileDigest, runtimeFiles, runtimeLibrary, RUNTIME_ABI, RUNTIME_LICENSES, verifyRuntime } from "./lib/privacy-runtime.mjs";

function fixture(t, platform = "linux", arch = "amd64") {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-runtime-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const files = {};
  const crt = platform === "windows" ? ["msvcp140.dll", "vcruntime140_1.dll"] : [];
  for (const name of [...runtimeFiles(platform, crt), ...RUNTIME_LICENSES]) {
    const file = path.join(directory, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, name);
    files[name] = fileDigest(file);
  }
  fs.writeFileSync(path.join(directory, "runtime.json"), JSON.stringify({ abi: RUNTIME_ABI, platform, arch, ...(crt.length ? { crt: { files: Object.fromEntries(crt.map((name) => [name, files[name]])) } } : {}), files }));
  return directory;
}

test("verifies the producing runtime digest and every distributed file", (t) => {
  for (const platform of ["linux", "darwin", "windows"]) {
    for (const arch of ["amd64", "arm64"]) {
      const directory = fixture(t, platform, arch);
      const expected = fileDigest(path.join(directory, "runtime.json"));
      const verified = verifyRuntime(directory, platform, arch, expected);
      assert.equal(verified.digest, expected);
      assert.equal(verified.sizeBytes, runtimeLibrary(platform).length);
      assert.throws(() => verifyRuntime(directory, platform, arch, "0".repeat(64)), /producing binary/);
    }
  }
});

test("rejects substituted architectures and corrupt or absent libraries", (t) => {
  const directory = fixture(t);
  assert.throws(() => verifyRuntime(directory, "linux", "arm64"), /target mismatch/);
  const library = path.join(directory, "libonnxruntime.so");
  fs.writeFileSync(library, "corrupt");
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /checksum mismatch/);
  fs.rmSync(library);
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /file is missing/);
});

test("rejects previous-layout runtimes that ship additional native libraries", (t) => {
  const directory = fixture(t);
  const file = path.join(directory, "runtime.json");
  const manifest = JSON.parse(fs.readFileSync(file));
  fs.writeFileSync(path.join(directory, "libnexterm_privacy.so"), "bridge");
  manifest.files["libnexterm_privacy.so"] = fileDigest(path.join(directory, "libnexterm_privacy.so"));
  fs.writeFileSync(file, JSON.stringify(manifest));
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /unexpected file: libnexterm_privacy\.so/);
  fs.writeFileSync(file, JSON.stringify({ ...manifest, abi: 1 }));
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /target mismatch/);
});

test("rejects untracked files and missing license evidence", (t) => {
  const directory = fixture(t);
  fs.writeFileSync(path.join(directory, "unexpected.so"), "unexpected");
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /checksum mismatch/);
  fs.rmSync(path.join(directory, "unexpected.so"));
  const file = path.join(directory, "runtime.json");
  const manifest = JSON.parse(fs.readFileSync(file));
  delete manifest.files["licenses/ASTRLINK-LICENSE"];
  fs.writeFileSync(file, JSON.stringify(manifest));
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /missing licenses/);
});

test("does not follow symlinked files", { skip: process.platform === "win32" }, (t) => {
  const directory = fixture(t);
  const file = path.join(directory, "libonnxruntime.so");
  fs.renameSync(file, path.join(directory, "real-library"));
  fs.symlinkSync("real-library", file);
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /non-regular file/);
});

test("verifies macOS libraries separately from signed bundle resources", (t) => {
  const resources = fixture(t, "darwin", "arm64");
  const libraries = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-runtime-frameworks-"));
  t.after(() => fs.rmSync(libraries, { recursive: true, force: true }));
  fs.renameSync(path.join(resources, runtimeLibrary("darwin")), path.join(libraries, runtimeLibrary("darwin")));
  assert.doesNotThrow(() => verifyRuntime(resources, "darwin", "arm64", undefined, libraries));
  fs.writeFileSync(path.join(libraries, "libonnxruntime.dylib"), "corrupt");
  assert.throws(() => verifyRuntime(resources, "darwin", "arm64", undefined, libraries), /checksum mismatch/);
});

test("requires the Visual C++ runtime that onnxruntime.dll imports on Windows", (t) => {
  const directory = fixture(t, "windows", "amd64");
  fs.rmSync(path.join(directory, "vcruntime140_1.dll"));
  assert.throws(() => verifyRuntime(directory, "windows", "amd64"), /file is missing: vcruntime140_1\.dll/);
  assert.doesNotThrow(() => verifyRuntime(fixture(t, "windows", "arm64"), "windows", "arm64"));
});
