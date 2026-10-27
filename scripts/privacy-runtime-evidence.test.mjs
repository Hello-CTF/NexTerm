import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileDigest, runtimeLibraries, verifyRuntime } from "./lib/privacy-runtime.mjs";

function fixture(t, platform = "linux", arch = "amd64") {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-runtime-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const files = {};
  const crt = platform === "windows" ? ["msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll", ...(arch === "amd64" ? ["vcruntime140_1.dll"] : [])] : [];
  for (const name of [...runtimeLibraries(platform), ...crt, "licenses/ASTRLINK-LICENSE", "licenses/ONNX-RUNTIME-LICENSE", "licenses/native-dependencies.json"]) {
    const file = path.join(directory, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, name);
    files[name] = fileDigest(file);
  }
  fs.writeFileSync(path.join(directory, "runtime.json"), JSON.stringify({ abi: 1, platform, arch, files }));
  return directory;
}

test("verifies the producing runtime digest and every distributed file", (t) => {
  for (const platform of ["linux", "darwin", "windows"]) {
    for (const arch of ["amd64", "arm64"]) {
      const directory = fixture(t, platform, arch);
      const expected = fileDigest(path.join(directory, "runtime.json"));
      assert.equal(verifyRuntime(directory, platform, arch, expected).digest, expected);
      assert.throws(() => verifyRuntime(directory, platform, arch, "0".repeat(64)), /producing binary/);
    }
  }
});

test("rejects substituted architectures and corrupt or absent native libraries", (t) => {
  const directory = fixture(t);
  assert.throws(() => verifyRuntime(directory, "linux", "arm64"), /target mismatch/);
  const library = path.join(directory, "libonnxruntime.so");
  fs.writeFileSync(library, "corrupt");
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /checksum mismatch/);
  fs.rmSync(library);
  assert.throws(() => verifyRuntime(directory, "linux", "amd64"), /file is missing/);
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
  for (const name of runtimeLibraries("darwin")) fs.renameSync(path.join(resources, name), path.join(libraries, name));
  assert.doesNotThrow(() => verifyRuntime(resources, "darwin", "arm64", undefined, libraries));
  fs.writeFileSync(path.join(libraries, "libonnxruntime.dylib"), "corrupt");
  assert.throws(() => verifyRuntime(resources, "darwin", "arm64", undefined, libraries), /checksum mismatch/);
});


test("requires Windows C runtime dependencies in the distributed manifest", (t) => {
  const directory = fixture(t, "windows", "amd64");
  fs.rmSync(path.join(directory, "msvcp140_1.dll"));
  assert.throws(() => verifyRuntime(directory, "windows", "amd64"), /file is missing/);
});
