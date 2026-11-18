import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { test } from "node:test";
import { crc32 } from "node:zlib";
import { buildRuntime, resolveVersion, runtimePlan } from "./ai-privacy-runtime.mjs";

const SHA512 = Buffer.alloc(64, 7).toString("base64");
const catalogURL = "https://api.nuget.org/v3/catalog0/data/x/microsoft.ml.onnxruntime.1.32.0.json";

function json(body, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

// fakeFetch answers by URL prefix; a function value can fail or count calls.
function fakeFetch(routes) {
  const calls = [];
  const fetchImpl = async (url) => {
    calls.push(url);
    for (const [prefix, answer] of Object.entries(routes)) {
      if (url.startsWith(prefix)) return typeof answer === "function" ? answer(url) : answer.clone();
    }
    return new Response("not found", { status: 404 });
  };
  return { fetchImpl, calls };
}

const nugetRoutes = {
  "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/index.json": json({ versions: ["1.22.0", "1.31.1", "1.32.0", "1.33.0-rc.1"] }),
  "https://api.nuget.org/v3/registration5-gz-semver2/microsoft.ml.onnxruntime/1.32.0.json": json({
    catalogEntry: catalogURL,
    packageContent: "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/1.32.0/microsoft.ml.onnxruntime.1.32.0.nupkg",
  }),
  [catalogURL]: json({
    packageHash: SHA512,
    packageHashAlgorithm: "SHA512",
    packageEntries: [
      { fullName: "runtimes/linux-x64/native/libonnxruntime.so" },
      { fullName: "runtimes/osx-arm64/native/libonnxruntime.dylib" },
      { fullName: "LICENSE" },
      { fullName: "ThirdPartyNotices.txt" },
    ],
  }),
};

const context = (fetchImpl) => ({ fetchImpl, env: {}, retryDelayMs: 0 });

test("the version comes from nuget.org when GitHub has no ONNX Runtime repository", async () => {
  const { fetchImpl } = fakeFetch(nugetRoutes);
  assert.equal(await resolveVersion(context(fetchImpl)), "1.32.0");
});

test("a passing GitHub outage is retried instead of switching channels", async () => {
  let failures = 2;
  const { fetchImpl, calls } = fakeFetch({
    "https://api.github.com/repos/microsoft/onnxruntime/releases/latest": () =>
      failures-- > 0 ? new Response("busy", { status: 503 }) : json({ tag_name: "v1.31.1" }),
  });
  assert.equal(await resolveVersion(context(fetchImpl)), "1.31.1");
  assert.equal(calls.filter((url) => url.includes("nuget")).length, 0);
});

test("an unreachable GitHub sends desktop builds to the verified NuGet package", async () => {
  const { fetchImpl } = fakeFetch({
    "https://api.github.com/": () => { throw new TypeError("fetch failed"); },
    ...nugetRoutes,
  });
  const plan = await runtimePlan({ os: "linux", arch: "amd64" }, { ...context(fetchImpl), env: { NEXTERM_ORT_VERSION: "1.32.0" } });
  assert.equal(plan.source, "nuget");
  assert.equal(plan.version, "1.32.0");
  assert.equal(plan.libraryPath, "runtimes/linux-x64/native/libonnxruntime.so");
  assert.deepEqual(plan.digest, { algorithm: "sha512", value: Buffer.from(SHA512, "base64").toString("hex") });
  assert.deepEqual(plan.members, ["runtimes/linux-x64/native/libonnxruntime.so", "LICENSE", "ThirdPartyNotices.txt"]);
  assert.match(plan.url, /microsoft\.ml\.onnxruntime\.1\.32\.0\.nupkg$/);
});

test("a platform the NuGet package does not carry builds without the runtime", async () => {
  const { fetchImpl } = fakeFetch(nugetRoutes);
  const plan = await runtimePlan({ os: "darwin", arch: "amd64" }, { ...context(fetchImpl), env: { NEXTERM_ORT_VERSION: "1.32.0" } });
  assert.equal(plan.source, "nuget");
  assert.match(plan.unavailable, /osx-x64/);
});

test("a NuGet package without a SHA-512 is refused", async () => {
  const { fetchImpl } = fakeFetch({ ...nugetRoutes, [catalogURL]: json({ packageEntries: [] }) });
  await assert.rejects(
    runtimePlan({ os: "linux", arch: "amd64" }, { ...context(fetchImpl), env: { NEXTERM_ORT_VERSION: "1.32.0" } }),
    /no SHA-512/,
  );
});

test("a reachable GitHub release stays the source", async () => {
  const digest = "a".repeat(64);
  const { fetchImpl, calls } = fakeFetch({
    "https://api.github.com/repos/microsoft/onnxruntime/releases/tags/v1.32.0": json({
      assets: [{ name: "onnxruntime-linux-x64-1.32.0.tgz", digest: `sha256:${digest}`, browser_download_url: "https://example.invalid/ort.tgz" }],
    }),
  });
  const plan = await runtimePlan({ os: "linux", arch: "amd64" }, { ...context(fetchImpl), env: { NEXTERM_ORT_VERSION: "1.32.0" } });
  assert.equal(plan.source, "github");
  assert.deepEqual(plan.digest, { algorithm: "sha256", value: digest });
  assert.equal(calls.filter((url) => url.includes("nuget")).length, 0);
});

test("the NuGet layout stages the library and the upstream licenses", async () => {
  const directory = await mkdtemp(path.join(process.env.TMPDIR || os.tmpdir(), "ort-nuget-"));
  try {
    const source = path.join(directory, "package");
    await mkdir(path.join(source, "runtimes/linux-x64/native"), { recursive: true });
    await writeFile(path.join(source, "runtimes/linux-x64/native/libonnxruntime.so"), "library");
    await writeFile(path.join(source, "LICENSE"), "MIT License");
    await writeFile(path.join(source, "ThirdPartyNotices.txt"), "notices");
    const plan = {
      os: "linux", arch: "amd64", library: "libonnxruntime.so", version: "1.32.0", source: "nuget",
      archive: "microsoft.ml.onnxruntime.1.32.0.nupkg", libraryPath: "runtimes/linux-x64/native/libonnxruntime.so",
    };
    const output = path.join(directory, "out");
    const manifest = await buildRuntime({ plan, ortDir: source, output, env: {} });
    assert.equal(manifest.source, "nuget");
    assert.equal(await readFile(path.join(output, "libonnxruntime.so"), "utf8"), "library");
    assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-LICENSE"), "utf8"), "MIT License");
    assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"), "utf8"), "notices");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

// storedZip builds an uncompressed zip, enough for a fake .nupkg.
function storedZip(files) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const [name, text] of Object.entries(files)) {
    const fileName = Buffer.from(name);
    const data = Buffer.from(text);
    const crc = crc32(data);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(fileName.length, 26);
    const central = Buffer.alloc(46);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt32LE(crc, 16);
    central.writeUInt32LE(data.length, 20);
    central.writeUInt32LE(data.length, 24);
    central.writeUInt16LE(fileName.length, 28);
    central.writeUInt32LE(offset, 42);
    locals.push(local, fileName, data);
    centrals.push(central, fileName);
    offset += local.length + fileName.length + data.length;
  }
  const directory = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(files).length, 8);
  end.writeUInt16LE(Object.keys(files).length, 10);
  end.writeUInt32LE(directory.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, directory, end]);
}

const hasUnzip = process.platform === "win32" || spawnSync("unzip", ["-v"]).status === 0;

test("mobile builds take the license files from nuget.org when GitHub cannot serve them", { skip: !hasUnzip && "unzip is not installed" }, async () => {
  const directory = await mkdtemp(path.join(process.env.TMPDIR || os.tmpdir(), "ort-license-"));
  const version = `1.32.${Date.now() % 100000}`;
  try {
    const source = path.join(directory, "aar");
    await mkdir(path.join(source, "jni/arm64-v8a"), { recursive: true });
    await writeFile(path.join(source, "jni/arm64-v8a/libonnxruntime.so"), "library");
    const nupkg = storedZip({ "LICENSE.txt": "MIT License", "ThirdPartyNotices.txt": "notices", "README.md": "readme" });
    const catalog = "https://api.nuget.org/v3/catalog0/data/x/managed.json";
    const { fetchImpl } = fakeFetch({
      [`https://api.nuget.org/v3/registration5-gz-semver2/microsoft.ml.onnxruntime.managed/${version}.json`]: json({
        catalogEntry: catalog,
        packageContent: "https://api.nuget.org/v3-flatcontainer/managed.nupkg",
      }),
      [catalog]: json({
        packageHash: createHash("sha512").update(nupkg).digest("base64"),
        packageHashAlgorithm: "SHA512",
        packageEntries: [{ fullName: "LICENSE.txt" }, { fullName: "ThirdPartyNotices.txt" }, { fullName: "README.md" }],
      }),
      "https://api.nuget.org/v3-flatcontainer/managed.nupkg": new Response(nupkg),
      "https://raw.githubusercontent.com/": new Response("gone", { status: 404 }),
    });
    const plan = { os: "android", arch: "arm64", library: "libonnxruntime.so", version, archive: `onnxruntime-android-${version}.aar` };
    const output = path.join(directory, "out");
    await buildRuntime({ plan, ortDir: source, output, env: {}, fetchImpl });
    assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-LICENSE"), "utf8"), "MIT License");
    assert.equal(await readFile(path.join(output, "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"), "utf8"), "notices");
  } finally {
    await rm(directory, { recursive: true, force: true });
    await rm(path.join(path.dirname(new URL(import.meta.url).pathname), "..", ".tmp/ai-privacy-runtime", version), { recursive: true, force: true });
  }
});
