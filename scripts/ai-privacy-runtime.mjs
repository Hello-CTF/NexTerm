#!/usr/bin/env node

// Fetches, verifies and stages the checksum-pinned ONNX Runtime library that
// the Go privacy inference engine loads at run time. Nothing is compiled here.
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { cp, mkdir, readFile, readdir, stat, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runtimeLibrary, windowsCRT, RUNTIME_ABI, RUNTIME_LICENSES } from "./lib/privacy-runtime.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const thirdParty = path.join(repository, "internal/ai/privacy/third_party");
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
  const common = { os, arch, iosPlatform, library: runtimeLibrary(os) };
  if (os === "ios") {
    if (!["device", "simulator"].includes(iosPlatform) || (iosPlatform === "device" && arch !== "arm64")) throw new Error("Invalid iOS privacy runtime target");
    return { ...common, version: "1.23.0", minimumOS: "15.1", archive: "pod-archive-onnxruntime-c-1.23.0.zip", url: "https://download.onnxruntime.ai/pod-archive-onnxruntime-c-1.23.0.zip", sha256: "756a78e0168f29840bc614b43aeb03e63673f44022e0221d21698a2c8ed747ef" };
  }
  if (os === "android") return { ...common, version: desktopVersion, minimumOS: "24", archive: "onnxruntime-android-1.23.2.aar", url: "https://repo1.maven.org/maven2/com/microsoft/onnxruntime/onnxruntime-android/1.23.2/onnxruntime-android-1.23.2.aar", sha256: "82048d1f462218adae4ba76477089ab0ba76093d84f733540066db1a8ba6b827" };
  const artifact = archives[`${os}-${arch}`];
  if (!artifact) throw new Error(`Unsupported privacy runtime platform: ${os}/${arch}`);
  return { ...common, version: desktopVersion, ...(os === "darwin" ? { minimumOS: "13.4" } : {}), archive: artifact[0], sha256: artifact[1], url: `https://github.com/microsoft/onnxruntime/releases/download/v${desktopVersion}/${artifact[0]}` };
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

async function stageLibrary(plan, source, output) {
  if (plan.os === "ios") {
    if (process.platform !== "darwin") throw new Error("iOS privacy runtime packaging requires macOS and Xcode");
    // The pod archive ships a static xcframework; link it into a dynamic framework
    // so the Go engine can dlopen it like every other platform.
    const simulator = plan.iosPlatform === "simulator";
    const slice = simulator ? "ios-arm64_x86_64-simulator" : "ios-arm64";
    const sdk = simulator ? "iphonesimulator" : "iphoneos";
    const target = `${plan.arch === "arm64" ? "arm64" : "x86_64"}-apple-ios${plan.minimumOS}${simulator ? "-simulator" : ""}`;
    const framework = path.join(output, "onnxruntime.framework");
    await mkdir(framework, { recursive: true });
    const archive = path.join(source, "onnxruntime.xcframework", slice, "onnxruntime.framework/onnxruntime");
    const sdkPath = command("xcrun", ["--sdk", sdk, "--show-sdk-path"], { stdio: "pipe" });
    command("xcrun", ["--sdk", sdk, "clang++", "-target", target, "-isysroot", sdkPath, "-dynamiclib", `-Wl,-force_load,${archive}`, "-framework", "Foundation", "-framework", "CoreML", "-framework", "Accelerate", "-install_name", "@rpath/onnxruntime.framework/onnxruntime", "-o", path.join(output, plan.library)]);
    await writeFile(path.join(framework, "Info.plist"), frameworkPlist("onnxruntime", plan.version, plan.minimumOS, simulator ? "iPhoneSimulator" : "iPhoneOS"));
    return;
  }
  if (plan.os === "android") {
    const abi = plan.arch === "arm64" ? "arm64-v8a" : "x86_64";
    await copyFile(path.join(source, "jni", abi, "libonnxruntime.so"), path.join(output, plan.library));
    return;
  }
  const root = path.join(source, plan.archive.replace(/\.(?:tgz|zip)$/, ""));
  const name = plan.os === "windows" ? "onnxruntime.dll" : plan.os === "darwin" ? "libonnxruntime.dylib" : "libonnxruntime.so";
  await copyFile(path.join(root, "lib", name), path.join(output, plan.library));
}

async function windowsCRTDirectory(env = process.env) {
  if (env.VCToolsRedistDir) return env.VCToolsRedistDir;
  const vswhere = path.join(env["ProgramFiles(x86)"] || "C:\\Program Files (x86)", "Microsoft Visual Studio/Installer/vswhere.exe");
  const installation = command(vswhere, ["-latest", "-products", "*", "-requires", "Microsoft.VisualStudio.Component.VC.Tools.x86.x64", "-property", "installationPath"], { stdio: "pipe" });
  if (!installation) throw new Error("The Visual C++ redistributable is required to package onnxruntime.dll; set VCToolsRedistDir or install the Visual Studio C++ tools");
  const root = path.join(installation, "VC/Redist/MSVC");
  const versions = (await readdir(root)).filter(name => /^\d+\.\d+\.\d+$/.test(name)).sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
  if (!versions.length) throw new Error("Visual Studio C++ redistributable directory is empty");
  return path.join(root, versions[0]);
}

// Copies the Visual C++ runtime that onnxruntime.dll imports. Hashes are pinned
// per redist version in scripts/msvc-crt.json; release builds refuse unpinned
// or mismatching files, other builds print the hashes to pin.
export async function stageWindowsCRT(redistDirectory, arch, output, { pins = JSON.parse(readFileSync(path.join(repository, "scripts/msvc-crt.json"), "utf8")).versions, release = Boolean(process.env.NEXTERM_RELEASE_VERSION && process.env.NEXTERM_RELEASE_VERSION !== "0.0.0") } = {}) {
  const architecture = arch === "amd64" ? "x64" : "arm64";
  const directory = path.join(redistDirectory, architecture);
  const candidates = (await readdir(directory)).filter(name => /^Microsoft\.VC\d+\.CRT$/i.test(name)).sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
  if (!candidates.length) throw new Error(`MSVC redistributable CRT missing for ${architecture}`);
  const source = path.join(directory, candidates[0]);
  const version = path.basename(redistDirectory);
  const present = new Map((await readdir(source)).map(name => [name.toLowerCase(), name]));
  const digests = {};
  for (const name of windowsCRT(arch)) {
    if (!present.has(name)) throw new Error(`MSVC redistributable is missing ${name}`);
    const data = await readFile(path.join(source, present.get(name)));
    digests[name] = hash(data);
  }
  const pinned = pins[version]?.[arch];
  const mismatched = Object.keys(digests).filter(name => pinned?.[name] !== digests[name]);
  if (mismatched.length) {
    const detail = `MSVC CRT ${version} ${arch} is not pinned in scripts/msvc-crt.json (${mismatched.join(", ")}); computed ${JSON.stringify(digests)}`;
    if (release) throw new Error(detail);
    console.warn(`warning: ${detail}`);
  }
  for (const name of Object.keys(digests)) await copyFile(path.join(source, present.get(name)), path.join(output, name));
  await writeFile(path.join(output, "licenses/MSVC-RUNTIME-NOTICE.txt"), `Microsoft Visual C++ Runtime ${version} (${candidates[0]}), redistributed app-local for ONNX Runtime.\nCopyright Microsoft Corporation. All rights reserved.\nhttps://learn.microsoft.com/cpp/windows/redistributing-visual-cpp-files\n`);
  return { version, digests };
}

async function stageLicenses(plan, output, directory = thirdParty) {
  const sources = {
    "licenses/ASTRLINK-LICENSE": "ASTRLINK_LICENSE",
    "licenses/ASTRLINK-NOTICE.md": "ASTRLINK_NOTICE.md",
    "licenses/ONNX-RUNTIME-LICENSE": "ONNX_RUNTIME_LICENSE",
    "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES": `ONNX_RUNTIME_${plan.version}_NOTICES`,
  };
  for (const name of RUNTIME_LICENSES) await copyFile(path.join(directory, sources[name]), path.join(output, name));
}

async function collect(directory, files, prefix = "") {
  for (const name of (await readdir(directory)).sort()) {
    const relative = `${prefix}${name}`;
    if (relative === "runtime.json") continue;
    const entry = path.join(directory, name);
    if ((await stat(entry)).isDirectory()) await collect(entry, files, `${relative}/`);
    else files[relative] = hash(await readFile(entry));
  }
}

export async function buildRuntime(options) {
  const plan = runtimePlan(options);
  if (!options.output) throw new Error("--output is required");
  const output = path.resolve(options.output);
  const cache = path.join(repository, ".tmp/ai-privacy-runtime");
  await mkdir(cache, { recursive: true });
  await mkdir(output, { recursive: true });
  let source = options.ortDir && path.resolve(options.ortDir);
  if (!source) {
    const archive = await download(plan, cache);
    source = path.join(cache, plan.archive.replaceAll(".", "-") + "-extracted");
    await unpack(archive, source);
  }
  await stageLibrary(plan, source, output);
  await stageLicenses(plan, output, options.licenseDir);
  if (plan.os === "windows") await stageWindowsCRT(options.crtDir ?? await windowsCRTDirectory(), plan.arch, output);
  const files = {};
  await collect(output, files);
  const library = await stat(path.join(output, plan.library));
  const manifest = { abi: RUNTIME_ABI, platform: plan.os, arch: plan.arch, onnxRuntime: plan.version, archive: plan.archive, archiveSha256: plan.sha256, minimumOS: plan.minimumOS, library: plan.library, librarySizeBytes: library.size, files };
  await writeFile(path.join(output, "runtime.json"), `${JSON.stringify(manifest, null, 2)}\n`);
  return manifest;
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  const options = {};
  const keys = { os: "os", arch: "arch", output: "output", "ios-platform": "iosPlatform", "ort-dir": "ortDir" };
  for (const argument of process.argv.slice(2)) {
    const match = /^--([^=]+)=(.+)$/.exec(argument);
    if (!match || !keys[match[1]]) throw new Error(`Unknown argument: ${argument}`);
    options[keys[match[1]]] = match[2];
  }
  await buildRuntime(options);
}
