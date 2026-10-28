import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import { createHash } from "node:crypto";
import { buildRuntime, resolveVersion, runtimePlan } from "./ai-privacy-runtime.mjs";
import { RUNTIME_LICENSES, verifyRuntime, peImports, isCRTImport } from "./lib/privacy-runtime.mjs";

const sha = (data) => createHash("sha256").update(data).digest("hex");
const asset = (name, content = name) => ({ name, digest: `sha256:${sha(content)}`, browser_download_url: `https://example.test/${name}` });

function fakeFetch({ latest = "v1.40.2", prerelease = false, assets, pods = ["1.30.0", "1.40.2"] } = {}) {
  const calls = [];
  const impl = async (url) => {
    calls.push(String(url));
    const json = (body) => ({ ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) });
    const text = (body) => body === null ? { ok: false, status: 404 } : { ok: true, status: 200, text: async () => body };
    if (url.endsWith("/releases/latest")) return json({ tag_name: latest, prerelease });
    if (url.includes("/releases/tags/")) return json({ assets: assets ?? [] });
    if (url.includes("onnxruntime-android") && url.endsWith(".sha256")) return text(null);
    if (url.includes("onnxruntime-android") && url.endsWith(".sha1")) return text("abc123  file\n");
    if (url.includes("trunk.cocoapods.org")) return json({ versions: pods.map((name) => ({ name })) });
    if (url.includes("cdn.cocoapods.org")) return json({ source: { http: `https://download.onnxruntime.ai/pod-archive-onnxruntime-c-${url.split("/").at(-2)}.zip` }, platforms: { ios: "15.1" } });
    throw new Error(`unexpected fetch ${url}`);
  };
  return { impl, calls };
}

test("the latest stable release is resolved and verified against the published digest", async () => {
  const name = "onnxruntime-linux-aarch64-1.40.2.tgz";
  const { impl } = fakeFetch({ assets: [asset(name)] });
  const plan = await runtimePlan({ os: "linux", arch: "arm64" }, { env: {}, fetchImpl: impl });
  assert.equal(plan.version, "1.40.2");
  assert.equal(plan.digest.value, sha(name));
  assert.equal(new URL(plan.url).protocol, "https:");
});

test("version policy: prereleases, majors, old minors and missing digests are rejected; the override is honoured", async () => {
  const name = "onnxruntime-linux-x64-1.24.0.tgz";
  await assert.rejects(resolveVersion({ env: {}, fetchImpl: fakeFetch({ prerelease: true }).impl }), /prerelease/);
  await assert.rejects(resolveVersion({ env: {}, fetchImpl: fakeFetch({ latest: "v2.0.0" }).impl }), /1\.x\.y/);
  await assert.rejects(resolveVersion({ env: { NEXTERM_ORT_VERSION: "1.22.0" } }), /1\.23/);
  assert.equal(await resolveVersion({ env: { NEXTERM_ORT_VERSION: "1.24.0" } }), "1.24.0");
  const noDigest = fakeFetch({ assets: [{ name, browser_download_url: "https://example.test/x" }] });
  await assert.rejects(runtimePlan({ os: "linux", arch: "amd64" }, { env: { NEXTERM_ORT_VERSION: "1.24.0" }, fetchImpl: noDigest.impl }), /no digest/);
});

test("a target the release no longer publishes builds without the privacy runtime", async (t) => {
  const missing = fakeFetch({ assets: [] });
  const context = { env: { NEXTERM_ORT_VERSION: "1.24.0" }, fetchImpl: missing.impl };
  const plan = await runtimePlan({ os: "darwin", arch: "amd64" }, context);
  assert.match(plan.unavailable, /publishes no .* for darwin\/amd64/);
  const root = await mkdtemp(path.join(tmpdir(), "nexterm-ort-unavailable-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const output = path.join(root, "out");
  const manifest = await buildRuntime({ os: "darwin", arch: "amd64", output, ...context });
  assert.equal(manifest.unavailable, plan.unavailable);
  const verified = verifyRuntime(output, "darwin", "amd64");
  assert.equal(verified.unavailable, plan.unavailable);
  await writeFile(path.join(output, "libonnxruntime.dylib"), "x");
  assert.throws(() => verifyRuntime(output, "darwin", "amd64"), /unexpected files/);
});

test("Android and iOS resolve from Maven and CocoaPods with one version", async () => {
  const { impl } = fakeFetch();
  const android = await runtimePlan({ os: "android", arch: "arm64" }, { env: {}, fetchImpl: impl });
  assert.deepEqual([android.version, android.digest.algorithm, android.digest.value], ["1.40.2", "sha1", "abc123"]);
  const ios = await runtimePlan({ os: "ios", arch: "arm64" }, { env: {}, fetchImpl: impl });
  assert.equal(ios.version, "1.40.2");
  assert.equal(ios.minimumOS, "15.1");
  assert.equal(ios.digest, null);
  const trailing = await runtimePlan({ os: "ios", arch: "arm64" }, { env: {}, fetchImpl: fakeFetch({ pods: ["1.30.0"] }).impl });
  assert.equal(trailing.version, "1.30.0");
  await assert.rejects(runtimePlan({ os: "ios", arch: "arm64" }, { env: { NEXTERM_ORT_VERSION: "1.40.2" }, fetchImpl: fakeFetch({ pods: ["1.30.0"] }).impl }), /no 1\.40\.2/);
});

test("unsupported platforms and architectures are rejected", async () => {
  const context = { env: { NEXTERM_ORT_VERSION: "1.40.2" } };
  await assert.rejects(runtimePlan({ os: "browser", arch: "arm64" }, context));
  await assert.rejects(runtimePlan({ os: "linux", arch: "arm" }, context));
  await assert.rejects(runtimePlan({ os: "ios", arch: "amd64", iosPlatform: "device" }, context));
  await assert.rejects(runtimePlan({ os: "ios", arch: "arm64", iosPlatform: "unknown" }, context));
});

function peFixture(imports) {
  // Minimal PE32+ image with one .idata section holding the import table.
  const buffer = Buffer.alloc(0x600);
  buffer.write("MZ", 0); buffer.writeUInt32LE(0x80, 0x3c);
  buffer.write("PE\0\0", 0x80); buffer.writeUInt16LE(1, 0x86); buffer.writeUInt16LE(240, 0x94);
  const optional = 0x98; buffer.writeUInt16LE(0x20b, optional);
  buffer.writeUInt32LE(0x1000, optional + 112 + 8); // import directory RVA
  const section = optional + 240;
  buffer.writeUInt32LE(0x400, section + 8); buffer.writeUInt32LE(0x1000, section + 12); buffer.writeUInt32LE(0x400, section + 16); buffer.writeUInt32LE(0x200, section + 20);
  let name = 0x200 + 20 * (imports.length + 1);
  imports.forEach((dll, i) => { buffer.writeUInt32LE(0x1000 + name - 0x200, 0x200 + i * 20 + 12); buffer.write(`${dll}\0`, name); name += dll.length + 1; });
  return buffer;
}

test("PE import tables are parsed and only Visual C++ runtime DLLs are selected", () => {
  const imports = peImports(peFixture(["KERNEL32.dll", "MSVCP140.dll", "VCRUNTIME140_1.dll", "api-ms-win-crt-heap-l1-1-0.dll"]));
  assert.deepEqual(imports.filter(isCRTImport), ["msvcp140.dll", "vcruntime140_1.dll"]);
  assert.throws(() => peImports(Buffer.from("nope")), /PE/);
});

test("staging an extracted archive uses the release licenses and the DLL's own CRT imports", async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), "nexterm-ort-stage-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const extracted = path.join(root, "source/onnxruntime-win-x64-1.40.2");
  await mkdir(path.join(extracted, "lib"), { recursive: true });
  const dll = peFixture(["KERNEL32.dll", "MSVCP140.dll", "VCRUNTIME140.dll"]);
  await writeFile(path.join(extracted, "lib/onnxruntime.dll"), dll);
  await writeFile(path.join(extracted, "LICENSE"), "release license");
  await writeFile(path.join(extracted, "ThirdPartyNotices.txt"), "release notices");
  const licenseDir = path.join(root, "third_party");
  await mkdir(licenseDir);
  for (const name of ["ASTRLINK_LICENSE", "ASTRLINK_NOTICE.md"]) await writeFile(path.join(licenseDir, name), name);
  const redist = path.join(root, "redist/14.44.35208");
  const crtDir = path.join(redist, "x64/Microsoft.VC143.CRT");
  await mkdir(crtDir, { recursive: true });
  for (const name of ["msvcp140.dll", "vcruntime140.dll", "concrt140.dll"]) await writeFile(path.join(crtDir, name), `crt:${name}`);
  const output = path.join(root, "out");
  const plan = { os: "windows", arch: "amd64", library: "onnxruntime.dll", version: "1.40.2", archive: "onnxruntime-win-x64-1.40.2.zip" };
  const manifest = await buildRuntime({ os: "windows", arch: "amd64", output, plan, ortDir: path.join(root, "source"), licenseDir, crtDir: redist });
  assert.deepEqual(Object.keys(manifest.files).sort(), ["onnxruntime.dll", "msvcp140.dll", "vcruntime140.dll", ...RUNTIME_LICENSES, "licenses/MSVC-RUNTIME-NOTICE.txt"].sort());
  assert.equal(manifest.onnxRuntime, "1.40.2");
  assert.equal(manifest.crt.version, "14.44.35208");
  assert.equal(manifest.crt.files["msvcp140.dll"], sha("crt:msvcp140.dll"));
  assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"), "utf8"), "release notices");
  assert.equal(verifyRuntime(output, "windows", "amd64").sizeBytes, dll.length);
  await rm(path.join(output, "vcruntime140.dll"));
  assert.throws(() => verifyRuntime(output, "windows", "amd64"), /file is missing: vcruntime140\.dll/);
});
