import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, mkdir, writeFile, readFile, rm } from "node:fs/promises";
import path from "node:path";
import { tmpdir } from "node:os";
import { runtimePlan, copyWindowsCRT, nativeEnvironment } from "./ai-privacy-runtime.mjs";

test("runtime plans cover the shipped native OS and CPU combinations", () => {
  for (const os of ["darwin", "linux", "windows", "android"]) {
    for (const arch of ["amd64", "arm64"]) {
      const plan = runtimePlan({ os, arch });
      assert.equal(plan.os, os);
      assert.equal(plan.arch, arch);
      assert.match(plan.target, arch === "arm64" ? /^aarch64-/ : /^x86_64-/);
      assert.match(plan.sha256, /^[a-f0-9]{64}$/);
      assert.equal(new URL(plan.url).protocol, "https:");
      assert.ok(plan.library.endsWith(os === "windows" ? ".dll" : os === "darwin" ? ".dylib" : ".so"));
    }
  }
});

test("iOS device and simulator select distinct Rust targets", () => {
  const device = runtimePlan({ os: "ios", arch: "arm64" });
  const simulator = runtimePlan({ os: "ios", arch: "arm64", iosPlatform: "simulator" });
  assert.equal(device.target, "aarch64-apple-ios");
  assert.equal(simulator.target, "aarch64-apple-ios-sim");
  assert.equal(device.minimumOS, "15.1");
  assert.throws(() => runtimePlan({ os: "ios", arch: "amd64", iosPlatform: "device" }));
});

test("unsupported platforms and architectures are rejected", () => {
  assert.throws(() => runtimePlan({ os: "browser", arch: "arm64" }));
  assert.throws(() => runtimePlan({ os: "linux", arch: "arm" }));
  assert.throws(() => runtimePlan({ os: "ios", arch: "arm64", iosPlatform: "unknown" }));
});


test("Windows CRT packaging includes dependencies and rejects incomplete installations", async () => {
  const root = await mkdtemp(path.join(tmpdir(), "nexterm-crt-"));
  try {
    const source = path.join(root, "redist/x64/Microsoft.VC143.CRT");
    const output = path.join(root, "output");
    await mkdir(source, { recursive: true });
    const required = ["msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll", "vcruntime140_1.dll"];
    for (const name of required) await writeFile(path.join(source, name), `fixture:${name}`);
    await copyWindowsCRT(path.join(root, "redist"), "amd64", output);
    for (const name of required) assert.equal(await readFile(path.join(output, name), "utf8"), `fixture:${name}`);
    assert.match(await readFile(path.join(output, "licenses/MSVC-RUNTIME-NOTICE.txt"), "utf8"), /Microsoft/);
    await rm(path.join(source, "vcruntime140_1.dll"));
    await assert.rejects(copyWindowsCRT(path.join(root, "redist"), "amd64", output), /vcruntime140_1/);
  } finally { await rm(root, { recursive: true, force: true }); }
});


test("MSVC native builds isolate Go MinGW compiler configuration", async () => {
  const inherited = {
    CC: "x86_64-w64-mingw32-clang", CXX: "x86_64-w64-mingw32-clang++", AR: "llvm-ar",
    HOST_CC: "mingw-host", TARGET_CC: "mingw-target", CFLAGS: "--target=x86_64-w64-mingw32",
    TARGET_CXXFLAGS: "-stdlib=libstdc++", ARFLAGS: "rcs",
    PATH: "unchanged-path", VCToolsInstallDir: "visual-studio-tools",
    CC_aarch64_pc_windows_msvc: "explicit-arm64-msvc",
  };
  for (const arch of ["amd64", "arm64"]) {
    const env = await nativeEnvironment(runtimePlan({ os: "windows", arch }), undefined, inherited);
    for (const name of ["CC", "CXX", "AR", "HOST_CC", "TARGET_CC", "CFLAGS", "TARGET_CXXFLAGS", "ARFLAGS"]) assert.equal(env[name], undefined);
    assert.equal(env.PATH, inherited.PATH);
    assert.equal(env.VCToolsInstallDir, inherited.VCToolsInstallDir);
    assert.equal(env.CC_aarch64_pc_windows_msvc, inherited.CC_aarch64_pc_windows_msvc);
    assert.match(env.CARGO_ENCODED_RUSTFLAGS, /link-arg=\/Brepro/);
  }
  assert.equal(inherited.CC, "x86_64-w64-mingw32-clang");
  const linux = await nativeEnvironment(runtimePlan({ os: "linux", arch: "amd64" }), undefined, inherited);
  assert.equal(linux.CC, inherited.CC);
});
