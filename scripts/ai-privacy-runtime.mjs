#!/usr/bin/env node

import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { cp, mkdir, mkdtemp, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { homedir } from "node:os";
import { fileURLToPath, pathToFileURL } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const nativeDirectory = path.join(repository, "internal/ai/privacy/inference/native");
const upstreamCommit = "24ddf0d823785fe9b3ed7ee23b3a0fc517313e93";
const desktopVersion = "1.23.2";
const archives = {
  "darwin-arm64": ["onnxruntime-osx-arm64-1.23.2.tgz", "b4d513ab2b26f088c66891dbbc1408166708773d7cc4163de7bdca0e9bbb7856"],
  "darwin-amd64": ["onnxruntime-osx-x86_64-1.23.2.tgz", "d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6"],
  "linux-arm64": ["onnxruntime-linux-aarch64-1.23.2.tgz", "7c63c73560ed76b1fac6cff8204ffe34fe180e70d6582b5332ec094810241e5c"],
  "linux-amd64": ["onnxruntime-linux-x64-1.23.2.tgz", "1fa4dcaef22f6f7d5cd81b28c2800414350c10116f5fdd46a2160082551c5f9b"],
  "windows-arm64": ["onnxruntime-win-arm64-1.23.2.zip", "1cfe88b6435df3b5fb0e9f6bd7d6f5df1e887b6174de7f6e2a47bab956f3f168"],
  "windows-amd64": ["onnxruntime-win-x64-1.23.2.zip", "0b38df9af21834e41e73d602d90db5cb06dbd1ca618948b8f1d66d607ac9f3cd"],
};

export function runtimePlan({ os, arch, iosPlatform = "device" }) {
  if (!["amd64", "arm64"].includes(arch)) throw new Error(`Unsupported privacy runtime architecture: ${arch}`);
  const cpu = arch === "arm64" ? "aarch64" : "x86_64";
  const common = { os, arch, iosPlatform, upstreamCommit };
  if (os === "ios") {
    if (!["device", "simulator"].includes(iosPlatform) || (iosPlatform === "device" && arch !== "arm64")) throw new Error("Invalid iOS privacy runtime target");
    const target = iosPlatform === "device" ? "aarch64-apple-ios" : arch === "arm64" ? "aarch64-apple-ios-sim" : "x86_64-apple-ios";
    return { ...common, target, version: "1.23.0", minimumOS: "15.1", library: "libnexterm_privacy.dylib", archive: "pod-archive-onnxruntime-c-1.23.0.zip", url: "https://download.onnxruntime.ai/pod-archive-onnxruntime-c-1.23.0.zip", sha256: "756a78e0168f29840bc614b43aeb03e63673f44022e0221d21698a2c8ed747ef" };
  }
  if (os === "android") return { ...common, target: `${cpu}-linux-android`, version: desktopVersion, minimumOS: "24", library: "libnexterm_privacy.so", archive: "onnxruntime-android-1.23.2.aar", url: "https://repo1.maven.org/maven2/com/microsoft/onnxruntime/onnxruntime-android/1.23.2/onnxruntime-android-1.23.2.aar", sha256: "82048d1f462218adae4ba76477089ab0ba76093d84f733540066db1a8ba6b827" };
  const artifact = archives[`${os}-${arch}`];
  if (!artifact) throw new Error(`Unsupported privacy runtime platform: ${os}/${arch}`);
  const suffix = os === "darwin" ? "apple-darwin" : os === "windows" ? "pc-windows-msvc" : "unknown-linux-gnu";
  return { ...common, target: `${cpu}-${suffix}`, version: desktopVersion, ...(os === "darwin" ? { minimumOS: "13.4" } : {}), library: os === "windows" ? "nexterm_privacy.dll" : os === "darwin" ? "libnexterm_privacy.dylib" : "libnexterm_privacy.so", archive: artifact[0], sha256: artifact[1], url: `https://github.com/microsoft/onnxruntime/releases/download/v${desktopVersion}/${artifact[0]}` };
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, { cwd: repository, stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${executable} exited with ${result.status}`);
  return result.stdout?.toString().trim();
}

const hash = (data) => createHash("sha256").update(data).digest("hex");

async function download(plan, cache) {
  const archive = path.join(cache, plan.archive);
  try { if (hash(await readFile(archive)) === plan.sha256) return archive; } catch { /* A missing cache is downloaded below. */ }
  const response = await fetch(plan.url, { signal: AbortSignal.timeout(600_000) });
  if (!response.ok) throw new Error(`Runtime download returned HTTP ${response.status}`);
  const data = Buffer.from(await response.arrayBuffer());
  if (hash(data) !== plan.sha256) throw new Error(`Runtime checksum mismatch: ${plan.archive}`);
  await writeFile(archive, data);
  return archive;
}

async function unpack(archive, directory) {
  await mkdir(directory, { recursive: true });
  if (archive.endsWith(".tgz") || process.platform === "win32") command("tar", ["-xf", archive, "-C", directory]);
  else command("unzip", ["-oq", archive, "-d", directory]);
}

async function copyFile(source, destination) {
  await mkdir(path.dirname(destination), { recursive: true });
  await cp(source, destination, { dereference: true });
}

function frameworkPlist(name, version, minimumOS, supportedPlatform) {
  return `<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>work.nexterm.runtime.${name}</string><key>CFBundleName</key><string>${name}</string><key>CFBundleExecutable</key><string>${name}</string><key>CFBundlePackageType</key><string>FMWK</string><key>CFBundleVersion</key><string>${version}</string><key>CFBundleShortVersionString</key><string>${version}</string><key>MinimumOSVersion</key><string>${minimumOS}</string><key>CFBundleSupportedPlatforms</key><array><string>${supportedPlatform}</string></array></dict></plist>\n`;
}

async function prepareORT(plan, source, output) {
  if (plan.os === "ios") {
    if (process.platform !== "darwin") throw new Error("iOS privacy runtime packaging requires macOS and Xcode");
    const simulator = plan.iosPlatform === "simulator";
    const slice = simulator ? "ios-arm64_x86_64-simulator" : "ios-arm64";
    const sdk = simulator ? "iphonesimulator" : "iphoneos";
    const target = `${plan.arch === "arm64" ? "arm64" : "x86_64"}-apple-ios${plan.minimumOS}${simulator ? "-simulator" : ""}`;
    const framework = path.join(output, "onnxruntime.framework");
    await mkdir(framework, { recursive: true });
    const archive = path.join(source, "onnxruntime.xcframework", slice, "onnxruntime.framework/onnxruntime");
    const sdkPath = command("xcrun", ["--sdk", sdk, "--show-sdk-path"], { stdio: "pipe" });
    command("xcrun", ["--sdk", sdk, "clang++", "-target", target, "-isysroot", sdkPath, "-dynamiclib", `-Wl,-force_load,${archive}`, "-framework", "Foundation", "-framework", "CoreML", "-framework", "Accelerate", "-install_name", "@rpath/onnxruntime.framework/onnxruntime", "-o", path.join(framework, "onnxruntime")]);
    await writeFile(path.join(framework, "Info.plist"), frameworkPlist("onnxruntime", plan.version, plan.minimumOS, simulator ? "iPhoneSimulator" : "iPhoneOS"));
    await copyFile(path.join(source, "LICENSE"), path.join(output, "licenses/ONNX-RUNTIME-LICENSE"));
    return;
  }
  if (plan.os === "android") {
    const abi = plan.arch === "arm64" ? "arm64-v8a" : "x86_64";
    await copyFile(path.join(source, "jni", abi, "libonnxruntime.so"), path.join(output, "libonnxruntime.so"));
    // Retain the AAR build metadata alongside the licenses copied below.
    const notice = path.join(source, "META-INF/com/android/build/gradle/aar-metadata.properties");
    if (await stat(notice).catch(() => null)) await copyFile(notice, path.join(output, "licenses/ONNX-ANDROID-METADATA"));
    return;
  }
  const root = path.join(source, plan.archive.replace(/\.(?:tgz|zip)$/, ""));
  const extension = plan.os === "windows" ? ".dll" : plan.os === "darwin" ? ".dylib" : ".so";
  await copyFile(path.join(root, "lib", `libonnxruntime${extension}`.replace(/^lib(?=onnxruntime\.dll)/, "")), path.join(output, plan.os === "windows" ? "onnxruntime.dll" : `libonnxruntime${extension}`));
  if (plan.os === "windows") {
    for (const name of await readdir(path.join(root, "lib"))) if (name.endsWith(".dll") && name !== "onnxruntime.dll") await copyFile(path.join(root, "lib", name), path.join(output, name));
  }
  await copyFile(path.join(root, "LICENSE"), path.join(output, "licenses/ONNX-RUNTIME-LICENSE"));
  for (const name of ["ThirdPartyNotices.txt", "Privacy.md"]) if (await stat(path.join(root, name)).catch(() => null)) await copyFile(path.join(root, name), path.join(output, "licenses", name));
}

export async function copyWindowsCRT(redistDirectory, arch, output) {
  const architecture = arch === "amd64" ? "x64" : "arm64";
  const directory = path.join(redistDirectory, architecture);
  const candidates = (await readdir(directory)).filter(name => /^Microsoft\.VC\d+\.CRT$/i.test(name)).sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
  if (!candidates.length) throw new Error(`MSVC redistributable CRT missing for ${architecture}`);
  const source = path.join(directory, candidates[0]);
  const names = (await readdir(source)).filter(name => name.toLowerCase().endsWith(".dll"));
  for (const required of ["msvcp140.dll", "msvcp140_1.dll", "vcruntime140.dll"]) {
    if (!names.some(name => name.toLowerCase() === required)) throw new Error(`MSVC redistributable is missing ${required}`);
  }
  if (arch === "amd64" && !names.some(name => name.toLowerCase() === "vcruntime140_1.dll")) throw new Error("MSVC x64 redistributable is missing vcruntime140_1.dll");
  for (const name of names) await copyFile(path.join(source, name), path.join(output, name));
  await mkdir(path.join(output, "licenses"), { recursive: true });
  await writeFile(path.join(output, "licenses/MSVC-RUNTIME-NOTICE.txt"), `Microsoft Visual C++ Runtime Libraries (${candidates[0]}).\nCopyright Microsoft Corporation. All rights reserved.\nRedistributed from the Visual Studio licensed redistributable directory.\nhttps://learn.microsoft.com/cpp/windows/redistributing-visual-cpp-files\n`);
}

async function windowsCRTDirectory() {
  if (process.env.VCToolsRedistDir) return process.env.VCToolsRedistDir;
  const vswhere = path.join(process.env["ProgramFiles(x86)"] || "C:\\Program Files (x86)", "Microsoft Visual Studio/Installer/vswhere.exe");
  const installation = command(vswhere, ["-latest", "-products", "*", "-requires", "Microsoft.VisualStudio.Component.VC.Tools.x86.x64", "-property", "installationPath"], { stdio: "pipe" });
  if (!installation) throw new Error("Visual Studio C++ redistributables are required to package Windows inference");
  const root = path.join(installation, "VC/Redist/MSVC");
  const versions = (await readdir(root)).filter(name => /^\d+\.\d+\.\d+$/.test(name)).sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
  if (!versions.length) throw new Error("Visual Studio C++ redistributable directory is empty");
  return path.join(root, versions[0]);
}

export async function nativeEnvironment(plan, sourceDirectory = nativeDirectory, inherited = process.env) {
  const env = { ...inherited };
  if (plan.os === "windows") {
    // Go CGO uses MinGW, while this Cargo target and cc-rs require MSVC.
    // Retain explicit target-qualified overrides, but let cc-rs discover VS
    // instead of inheriting the surrounding Go build's compiler and flags.
    for (const variable of ["CC", "CXX", "AR", "CFLAGS", "CXXFLAGS", "ARFLAGS"]) {
      for (const prefix of ["", "HOST_", "TARGET_"]) delete env[`${prefix}${variable}`];
    }
  }
  const flags=env.CARGO_ENCODED_RUSTFLAGS?.split("\x1f") ?? (env.RUSTFLAGS ?? "").split(/\s+/).filter(Boolean);
  flags.push(`--remap-path-prefix=${repository}=/nexterm`, `--remap-path-prefix=${sourceDirectory}=/nexterm/privacy-native`, `--remap-path-prefix=${env.CARGO_HOME || path.join(homedir(),".cargo")}=/cargo`);
  if (plan.os === "darwin" || plan.os === "ios") flags.push("-C", "link-arg=-Wl,-install_name,@rpath/libnexterm_privacy.dylib");
  if (plan.os === "android") flags.push("-C", "link-arg=-Wl,-z,max-page-size=16384");
  if (plan.os === "windows") flags.push("-C", "link-arg=/Brepro");
  env.CARGO_ENCODED_RUSTFLAGS=flags.join("\x1f");
  delete env.RUSTFLAGS;
  if (plan.os === "ios") env.IPHONEOS_DEPLOYMENT_TARGET = plan.minimumOS;
  if (plan.os === "android") {
    const ndk = env.ANDROID_NDK_HOME || env.ANDROID_NDK_ROOT;
    if (!ndk) throw new Error("Android privacy runtime build requires ANDROID_NDK_HOME");
    const prebuilt = path.join(ndk, "toolchains/llvm/prebuilt");
    const host = process.platform === "darwin" ? "darwin-x86_64" : process.platform === "win32" ? "windows-x86_64" : "linux-x86_64";
    const bin = path.join(prebuilt, host, "bin");
    const compiler = path.join(bin, `${plan.target}${plan.minimumOS}-clang${process.platform === "win32" ? ".cmd" : ""}`);
    const key = plan.target.replaceAll("-", "_");
    env[`CC_${key}`] = compiler;
    env[`CARGO_TARGET_${key.toUpperCase()}_LINKER`] = compiler;
    env[`AR_${key}`] = path.join(bin, process.platform === "win32" ? "llvm-ar.exe" : "llvm-ar");
    env[`CXX_${key}`] = compiler.replace(/-clang(?=\.cmd|$)/, "-clang++");
  }
  return env;
}

async function licenses(output, env, plan) {
  await copyFile(path.join(nativeDirectory, "LICENSE-ASTRLINK"), path.join(output, "licenses/ASTRLINK-LICENSE"));
  await copyFile(path.join(nativeDirectory, "UPSTREAM.md"), path.join(output, "licenses/ASTRLINK-SOURCE.md"));
  await copyFile(path.join(nativeDirectory, "licenses/ONNX-RUNTIME-LICENSE"), path.join(output, "licenses/ONNX-RUNTIME-LICENSE"));
  await copyFile(path.join(nativeDirectory, `licenses/ONNX-RUNTIME-${plan.version}-NOTICES`), path.join(output, "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES"));
  const metadata = JSON.parse(command("cargo", ["metadata", "--locked", "--format-version=1", "--manifest-path", path.join(nativeDirectory, "Cargo.toml")], { env, stdio: "pipe" }));
  const summary = [];
  for (const dependency of metadata.packages) {
    if (!dependency.source) continue;
    summary.push({ name: dependency.name, version: dependency.version, license: dependency.license, source: dependency.source });
    const directory = path.dirname(dependency.manifest_path);
    for (const name of await readdir(directory)) {
      if (!/^(?:licen[cs]e|copying|notice)(?:[._-]|$)/i.test(name)) continue;
      const source = path.join(directory, name);
      if ((await stat(source)).isFile()) await copyFile(source, path.join(output, "licenses", `${dependency.name}-${dependency.version}`, name));
    }
  }
  await writeFile(path.join(output, "licenses/native-dependencies.json"), `${JSON.stringify(summary, null, 2)}\n`);
}

export async function buildRuntime(options) {
  const plan = runtimePlan(options);
  if (!options.output) throw new Error("--output is required");
  const output = path.resolve(options.output);
  const cache = path.join(repository, ".tmp/ai-privacy-runtime");
  await mkdir(cache, { recursive: true });
  await mkdir(output, { recursive: true });
  let ortSource = options.ortDir && path.resolve(options.ortDir);
  if (!ortSource) {
    const archive = await download(plan, cache);
    ortSource = path.join(cache, plan.archive.replaceAll(".", "-") + "-extracted");
    await unpack(archive, ortSource);
  }
  await prepareORT(plan, ortSource, output);
  const env = await nativeEnvironment(plan);
  const targetDirectory = path.join(cache, "target");
  command("cargo", ["build", "--release", "--locked", "--target", plan.target, "--manifest-path", path.join(nativeDirectory, "Cargo.toml"), "--target-dir", targetDirectory], { env });
  const nativeLibrary = path.join(targetDirectory, plan.target, "release", plan.library);
  if (options.reproCheck) {
    const fresh = await mkdtemp(path.join(cache, "repro-"));
    try {
      const source = path.join(fresh, "source");
      await cp(nativeDirectory, source, { recursive: true });
      const target = path.join(fresh, "target");
      const cleanEnv = await nativeEnvironment(plan, source);
      command("cargo", ["build", "--release", "--locked", "--target", plan.target, "--manifest-path", path.join(source, "Cargo.toml"), "--target-dir", target], { env: cleanEnv });
      const rebuilt = path.join(target, plan.target, "release", plan.library);
      if (hash(await readFile(nativeLibrary)) !== hash(await readFile(rebuilt))) {
        await copyFile(rebuilt,path.join(cache,`repro-mismatch-${plan.library}`));
        throw new Error("Privacy runtime is not reproducible across source paths and cold build caches");
      }
    } finally {
      await rm(fresh, { recursive: true, force: true });
    }
  }
  if (plan.os === "ios") {
    const framework = path.join(output, "NexTermPrivacy.framework");
    await copyFile(nativeLibrary, path.join(framework, "NexTermPrivacy"));
    command("install_name_tool", ["-id", "@rpath/NexTermPrivacy.framework/NexTermPrivacy", path.join(framework, "NexTermPrivacy")]);
    await writeFile(path.join(framework, "Info.plist"), frameworkPlist("NexTermPrivacy", "0.1.0", plan.minimumOS, plan.iosPlatform === "simulator" ? "iPhoneSimulator" : "iPhoneOS"));
  } else await copyFile(nativeLibrary, path.join(output, plan.library));
  if (plan.os === "windows") await copyWindowsCRT(await windowsCRTDirectory(), plan.arch, output);
  await licenses(output, env, plan);
  const files = {};
  async function collect(directory, prefix = "") {
    for (const name of (await readdir(directory)).sort()) {
      const relative = `${prefix}${name}`;
      if (relative === "runtime.json") continue;
      const entry = path.join(directory, name);
      if ((await stat(entry)).isDirectory()) await collect(entry, `${relative}/`);
      else files[relative] = hash(await readFile(entry));
    }
  }
  await collect(output);
  const manifest = { abi: 1, platform: plan.os, arch: plan.arch, target: plan.target, onnxRuntime: plan.version, minimumOS: plan.minimumOS, astrlinkCommit: upstreamCommit, files };
  await writeFile(path.join(output, "runtime.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const options = {};
  const keys = { os: "os", arch: "arch", output: "output", "ios-platform": "iosPlatform", "ort-dir": "ortDir", "repro-check": "reproCheck" };
  for (const argument of process.argv.slice(2)) {
    const match = /^--([^=]+)=(.+)$/.exec(argument);
    if (!match || !keys[match[1]]) throw new Error(`Unknown argument: ${argument}`);
    options[keys[match[1]]] = match[2];
  }
  if (options.reproCheck !== undefined && !["true", "false"].includes(options.reproCheck)) throw new Error("--repro-check must be true or false");
  options.reproCheck = options.reproCheck === "true";
  await buildRuntime(options);
}
