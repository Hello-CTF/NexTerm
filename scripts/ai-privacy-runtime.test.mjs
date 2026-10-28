import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { buildRuntime, runtimePlan, stageWindowsCRT } from "./ai-privacy-runtime.mjs";
import { RUNTIME_LICENSES, verifyRuntime, windowsCRT } from "./lib/privacy-runtime.mjs";

async function crtFixture(root, arch, version = "14.44.35208") {
  const redist = path.join(root, "redist", version);
  const source = path.join(redist, arch === "amd64" ? "x64" : "arm64", "Microsoft.VC143.CRT");
  await mkdir(source, { recursive: true });
  const digests = {};
  for (const name of [...windowsCRT(arch), "concrt140.dll"]) {
    await writeFile(path.join(source, name), `crt:${name}`);
    if (name !== "concrt140.dll") digests[name] = createHash("sha256").update(`crt:${name}`).digest("hex");
  }
  return { redist, source, pins: { [version]: { [arch]: digests } } };
}

test("runtime plans pin a checksum-verified ONNX Runtime for every shipped target", () => {
  const expected = { darwin: "libonnxruntime.dylib", linux: "libonnxruntime.so", windows: "onnxruntime.dll", android: "libonnxruntime.so" };
  for (const os of Object.keys(expected)) {
    for (const arch of ["amd64", "arm64"]) {
      const plan = runtimePlan({ os, arch });
      assert.equal(plan.library, expected[os]);
      assert.equal(plan.version, "1.23.2");
      assert.match(plan.sha256, /^[a-f0-9]{64}$/);
      assert.equal(new URL(plan.url).protocol, "https:");
    }
  }
  const ios = runtimePlan({ os: "ios", arch: "arm64" });
  assert.equal(ios.library, "onnxruntime.framework/onnxruntime");
  assert.equal(ios.version, "1.23.0");
  assert.equal(ios.minimumOS, "15.1");
  assert.equal(runtimePlan({ os: "ios", arch: "arm64", iosPlatform: "simulator" }).iosPlatform, "simulator");
});

test("unsupported platforms and architectures are rejected", () => {
  assert.throws(() => runtimePlan({ os: "browser", arch: "arm64" }));
  assert.throws(() => runtimePlan({ os: "linux", arch: "arm" }));
  assert.throws(() => runtimePlan({ os: "ios", arch: "amd64", iosPlatform: "device" }));
  assert.throws(() => runtimePlan({ os: "ios", arch: "arm64", iosPlatform: "unknown" }));
});

test("staging an extracted archive produces only the ONNX Runtime library and licenses", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "nexterm-ort-stage-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const extracted = path.join(root, "source/onnxruntime-win-x64-1.23.2/lib");
  await mkdir(extracted, { recursive: true });
  await writeFile(path.join(extracted, "onnxruntime.dll"), "ort");
  await writeFile(path.join(extracted, "onnxruntime_providers_shared.dll"), "provider");
  const licenseDir = path.join(root, "third_party");
  await mkdir(licenseDir);
  for (const name of ["ASTRLINK_LICENSE", "ASTRLINK_NOTICE.md", "ONNX_RUNTIME_LICENSE", "ONNX_RUNTIME_1.23.2_NOTICES"]) await writeFile(path.join(licenseDir, name), name);
  const output = path.join(root, "out");
  const crt = await crtFixture(root, "amd64");
  const manifest = await buildRuntime({ os: "windows", arch: "amd64", output, ortDir: path.join(root, "source"), licenseDir, crtDir: crt.redist });
  assert.deepEqual(Object.keys(manifest.files).sort(), ["onnxruntime.dll", ...windowsCRT("amd64"), ...RUNTIME_LICENSES, "licenses/MSVC-RUNTIME-NOTICE.txt"].sort());
  assert.equal(manifest.librarySizeBytes, 3);
  assert.equal(await readFile(path.join(output, "onnxruntime.dll"), "utf8"), "ort");
  assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"), "utf8"), "ONNX_RUNTIME_1.23.2_NOTICES");
  assert.equal(verifyRuntime(output, "windows", "amd64").sizeBytes, 3);
});

test("Windows CRT staging copies the imported DLLs and enforces pinned hashes for releases", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "nexterm-crt-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  for (const arch of ["amd64", "arm64"]) {
    const crt = await crtFixture(path.join(root, arch), arch);
    const output = path.join(root, arch, "out");
    await mkdir(path.join(output, "licenses"), { recursive: true });
    await stageWindowsCRT(crt.redist, arch, output, { pins: crt.pins, release: true });
    for (const name of windowsCRT(arch)) assert.equal(await readFile(path.join(output, name), "utf8"), `crt:${name}`);
    await assert.rejects(readFile(path.join(output, "concrt140.dll")));
    await assert.rejects(stageWindowsCRT(crt.redist, arch, output, { pins: {}, release: true }), /not pinned/);
    await stageWindowsCRT(crt.redist, arch, output, { pins: {}, release: false });
    await writeFile(path.join(crt.source, "vcruntime140.dll"), "tampered");
    await assert.rejects(stageWindowsCRT(crt.redist, arch, output, { pins: crt.pins, release: true }), /vcruntime140\.dll/);
    await rm(path.join(crt.source, "msvcp140_1.dll"));
    await assert.rejects(stageWindowsCRT(crt.redist, arch, output, { pins: crt.pins, release: false }), /missing msvcp140_1\.dll/);
  }
});
