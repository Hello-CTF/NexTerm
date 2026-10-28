#!/usr/bin/env node

// Resolves, fetches, verifies and stages the latest stable ONNX Runtime 1.x
// library that the Go privacy inference engine loads at run time. Nothing is
// compiled here. The version comes from the release channels at build time
// (or NEXTERM_ORT_VERSION) and every download is verified against the digest
// its publisher states; no digests are pinned in this repository.
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { cp, mkdir, readFile, readdir, rm, stat, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { runtimeLibrary, peImports, isCRTImport, RUNTIME_ABI, RUNTIME_LICENSES } from "./lib/privacy-runtime.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const thirdParty = path.join(repository, "internal/ai/privacy/third_party");
const MIN_MINOR = 23;
const GITHUB_API = "https://api.github.com/repos/microsoft/onnxruntime";
const MAVEN = "https://repo1.maven.org/maven2/com/microsoft/onnxruntime/onnxruntime-android";
const hostArchives = {
  "darwin-arm64": (v) => `onnxruntime-osx-arm64-${v}.tgz`,
  "darwin-amd64": (v) => `onnxruntime-osx-x86_64-${v}.tgz`,
  "linux-arm64": (v) => `onnxruntime-linux-aarch64-${v}.tgz`,
  "linux-amd64": (v) => `onnxruntime-linux-x64-${v}.tgz`,
  "windows-arm64": (v) => `onnxruntime-win-arm64-${v}.zip`,
  "windows-amd64": (v) => `onnxruntime-win-x64-${v}.zip`,
};

export function checkVersion(version) {
  const match = /^1\.(\d+)\.\d+$/.exec(version ?? "");
  if (!match) throw new Error(`ONNX Runtime version must be a stable 1.x.y release, got "${version}"`);
  if (Number(match[1]) < MIN_MINOR) throw new Error(`ONNX Runtime ${version} is older than the required 1.${MIN_MINOR}`);
  return version;
}

const versionKey = (v) => v.split(".").map(Number);
const compareVersions = (a, b) => { const x = versionKey(a), y = versionKey(b); for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] - y[i]; return 0; };

async function fetchChecked(url, { fetchImpl = fetch, env = process.env, accept } = {}) {
  const headers = { "User-Agent": "nexterm-build" };
  if (accept) headers.Accept = accept;
  if (env.GITHUB_TOKEN && new URL(url).hostname === "api.github.com") headers.Authorization = `Bearer ${env.GITHUB_TOKEN}`;
  const response = await fetchImpl(url, { headers, signal: AbortSignal.timeout(600_000) });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`${url} returned HTTP ${response.status}`);
  return response;
}

const fetchJSON = async (url, context) => (await fetchChecked(url, { ...context, accept: "application/vnd.github+json" }))?.json() ?? null;
const fetchText = async (url, context) => (await fetchChecked(url, context))?.text() ?? null;

// The version is resolved once per workflow run and handed to every job through
// NEXTERM_ORT_VERSION; unset, the latest non-prerelease GitHub release is used.
export async function resolveVersion(context = {}) {
  const env = context.env ?? process.env;
  if (env.NEXTERM_ORT_VERSION) return checkVersion(env.NEXTERM_ORT_VERSION.replace(/^v/, ""));
  const release = await fetchJSON(`${GITHUB_API}/releases/latest`, context);
  if (!release) throw new Error("microsoft/onnxruntime has no latest release");
  if (release.prerelease || release.draft) throw new Error(`Latest ONNX Runtime release ${release.tag_name} is a prerelease; set NEXTERM_ORT_VERSION to pin a stable one`);
  return checkVersion(String(release.tag_name).replace(/^v/, ""));
}

function parseDigest(value) {
  const match = /^(sha256|sha1):([0-9a-f]+)$/i.exec(value ?? "");
  return match ? { algorithm: match[1].toLowerCase(), value: match[2].toLowerCase() } : null;
}

async function podVersion(version, context) {
  const exact = !!(context.env ?? process.env).NEXTERM_ORT_VERSION;
  const pod = await fetchJSON("https://trunk.cocoapods.org/api/v1/pods/onnxruntime-c", context);
  const available = (pod?.versions ?? []).map((entry) => entry.name).filter((name) => /^1\.\d+\.\d+$/.test(name) && Number(name.split(".")[1]) >= MIN_MINOR);
  if (available.includes(version)) return version;
  if (exact) throw new Error(`The onnxruntime-c pod has no ${version} release`);
  // The pod trails the GitHub release; use the newest published pod not newer than it.
  const fallback = available.filter((name) => compareVersions(name, version) <= 0).sort(compareVersions).pop();
  if (!fallback) throw new Error(`The onnxruntime-c pod has no stable release >= 1.${MIN_MINOR} up to ${version}`);
  console.warn(`warning: onnxruntime-c pod ${version} is not published; iOS uses ${fallback}`);
  return fallback;
}

export async function runtimePlan({ os, arch, iosPlatform = "device" }, context = {}) {
  if (!["amd64", "arm64"].includes(arch)) throw new Error(`Unsupported privacy runtime architecture: ${arch}`);
  const common = { os, arch, iosPlatform, library: runtimeLibrary(os) };
  if (os === "ios" && (!["device", "simulator"].includes(iosPlatform) || (iosPlatform === "device" && arch !== "arm64"))) throw new Error("Invalid iOS privacy runtime target");
  if (os !== "ios" && os !== "android" && !hostArchives[`${os}-${arch}`]) throw new Error(`Unsupported privacy runtime platform: ${os}/${arch}`);
  const resolved = await resolveVersion(context);
  if (os === "ios") {
    const version = await podVersion(resolved, context);
    const shard = createHash("md5").update("onnxruntime-c").digest("hex").slice(0, 3).split("").join("/");
    const spec = JSON.parse(await fetchText(`https://cdn.cocoapods.org/Specs/${shard}/onnxruntime-c/${version}/onnxruntime-c.podspec.json`, context) ?? "null");
    const url = spec?.source?.http;
    if (!url) throw new Error(`onnxruntime-c ${version} podspec has no source URL`);
    // CocoaPods publishes a checksum only when the spec declares one.
    const digest = spec.source.sha256 ? { algorithm: "sha256", value: spec.source.sha256 } : spec.source.sha1 ? { algorithm: "sha1", value: spec.source.sha1 } : null;
    return { ...common, version, minimumOS: spec.platforms?.ios ?? "15.1", archive: path.posix.basename(new URL(url).pathname), url, digest };
  }
  if (os === "android") {
    const archive = `onnxruntime-android-${resolved}.aar`;
    const url = `${MAVEN}/${resolved}/${archive}`;
    let digest = null;
    for (const algorithm of ["sha256", "sha1"]) {
      const text = await fetchText(`${url}.${algorithm}`, context);
      if (text) { digest = { algorithm, value: text.trim().split(/\s+/)[0].toLowerCase() }; break; }
    }
    if (!digest) throw new Error(`Maven Central publishes no checksum for ${archive}`);
    return { ...common, version: resolved, minimumOS: "24", archive, url, digest };
  }
  const release = await fetchJSON(`${GITHUB_API}/releases/tags/v${resolved}`, context);
  if (!release) throw new Error(`microsoft/onnxruntime has no release v${resolved}`);
  const archive = hostArchives[`${os}-${arch}`](resolved);
  const asset = release.assets?.find((item) => item.name === archive);
  // A platform the release no longer publishes builds without the privacy runtime; the guard reports unsupported at run time.
  if (!asset) return { ...common, version: resolved, unavailable: `ONNX Runtime ${resolved} publishes no ${archive} for ${os}/${arch}` };
  const digest = parseDigest(asset.digest);
  if (!digest) throw new Error(`GitHub publishes no digest for ${archive}; refusing to use an unverified download`);
  return { ...common, version: resolved, ...(os === "darwin" ? { minimumOS: "13.4" } : {}), archive, digest, url: asset.browser_download_url };
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, { cwd: repository, stdio: "inherit", ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${executable} exited with ${result.status}`);
  return result.stdout?.toString().trim();
}

const hash = (data) => createHash("sha256").update(data).digest("hex");

async function download(plan, cache, context = {}) {
  const dir = path.join(cache, plan.version);
  await mkdir(dir, { recursive: true });
  const archive = path.join(dir, plan.archive);
  const digestOf = (data) => createHash(plan.digest?.algorithm ?? "sha256").update(data).digest("hex");
  try {
    const cached = await readFile(archive);
    if (plan.digest && digestOf(cached) === plan.digest.value) return { archive, sha256: hash(cached) };
  } catch { /* A missing cache is downloaded below. */ }
  const response = await fetchChecked(plan.url, context);
  if (!response) throw new Error(`Runtime download not found: ${plan.url}`);
  const data = Buffer.from(await response.arrayBuffer());
  if (plan.digest) {
    if (digestOf(data) !== plan.digest.value) throw new Error(`Runtime checksum mismatch: ${plan.archive}`);
  } else {
    console.warn(`warning: no published checksum for ${plan.archive}; relying on HTTPS (sha256 ${hash(data)})`);
  }
  await writeFile(archive, data);
  return { archive, sha256: hash(data) };
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

const archiveRoot = (plan, source) => path.join(source, plan.archive.replace(/\.(?:tgz|zip)$/, ""));

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
  const root = archiveRoot(plan, source);
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

// Verifies a redistributed DLL's Microsoft Authenticode signature on Windows
// hosts; other hosts cannot check it and skip.
function verifyAuthenticode(file) {
  if (process.platform !== "win32") return "skipped";
  const result = spawnSync("powershell", ["-NoProfile", "-NonInteractive", "-Command", "(Get-AuthenticodeSignature -LiteralPath $args[0]).Status", file], { encoding: "utf8" });
  const status = result.stdout?.trim();
  if (result.status !== 0 || status !== "Valid") throw new Error(`Authenticode signature of ${path.basename(file)} is not valid (${status || result.stderr?.trim() || "unknown"})`);
  return "valid";
}

// Copies the Visual C++ runtime DLLs that the resolved onnxruntime.dll imports
// (read from its import table) and records their version and hashes.
export async function stageWindowsCRT(redistDirectory, arch, output, library) {
  const imports = peImports(await readFile(library)).filter(isCRTImport);
  if (!imports.length) return { version: null, imports, signature: "skipped", files: {} };
  const architecture = arch === "amd64" ? "x64" : "arm64";
  const directory = path.join(redistDirectory, architecture);
  const candidates = (await readdir(directory)).filter(name => /^Microsoft\.VC\d+\.CRT$/i.test(name)).sort((a, b) => b.localeCompare(a, "en", { numeric: true }));
  if (!candidates.length) throw new Error(`MSVC redistributable CRT missing for ${architecture}`);
  const source = path.join(directory, candidates[0]);
  const version = path.basename(redistDirectory);
  const present = new Map((await readdir(source)).map(name => [name.toLowerCase(), name]));
  const files = {};
  let signature = "skipped";
  for (const name of imports) {
    if (!present.has(name)) throw new Error(`MSVC redistributable is missing ${name}`);
    const file = path.join(source, present.get(name));
    signature = verifyAuthenticode(file);
    await copyFile(file, path.join(output, name));
    files[name] = hash(await readFile(file));
  }
  await mkdir(path.join(output, "licenses"), { recursive: true });
  await writeFile(path.join(output, "licenses/MSVC-RUNTIME-NOTICE.txt"), `Microsoft Visual C++ Runtime ${version} (${candidates[0]}), redistributed app-local for ONNX Runtime.\nCopyright Microsoft Corporation. All rights reserved.\nhttps://learn.microsoft.com/cpp/windows/redistributing-visual-cpp-files\n`);
  return { version, imports, signature, files };
}

// ONNX-RUNTIME-LICENSE and the third-party notices come from the resolved
// release: its archive when it carries them, else the matching git tag.
async function stageLicenses(plan, source, output, { directory = thirdParty, ...context } = {}) {
  const searched = [archiveRoot(plan, source), source];
  const upstream = { "licenses/ONNX-RUNTIME-LICENSE": ["LICENSE"], "licenses/ONNX-RUNTIME-THIRD-PARTY-NOTICES": ["ThirdPartyNotices.txt", "ThirdPartyNotices.md"] };
  const local = { "licenses/ASTRLINK-LICENSE": "ASTRLINK_LICENSE", "licenses/ASTRLINK-NOTICE.md": "ASTRLINK_NOTICE.md" };
  for (const name of RUNTIME_LICENSES) {
    const destination = path.join(output, name);
    if (local[name]) { await copyFile(path.join(directory, local[name]), destination); continue; }
    let found;
    for (const candidate of upstream[name]) for (const root of searched) {
      const file = path.join(root, candidate);
      if (!found && await stat(file).then(entry => entry.isFile(), () => false)) found = file;
    }
    if (found) { await copyFile(found, destination); continue; }
    let text;
    for (const candidate of upstream[name]) text ??= await fetchText(`https://raw.githubusercontent.com/microsoft/onnxruntime/v${plan.version}/${candidate}`, context);
    if (!text) throw new Error(`Cannot obtain ${name} for ONNX Runtime ${plan.version}`);
    await mkdir(path.dirname(destination), { recursive: true });
    await writeFile(destination, text);
  }
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
  if (!options.output) throw new Error("--output is required");
  const context = { env: options.env, fetchImpl: options.fetchImpl };
  const plan = options.plan ?? await runtimePlan(options, context);
  const output = path.resolve(options.output);
  if (plan.unavailable) {
    console.warn(`warning: ${plan.unavailable}; building without the privacy runtime`);
    await mkdir(output, { recursive: true });
    const manifest = { abi: RUNTIME_ABI, platform: plan.os, arch: plan.arch, onnxRuntime: plan.version, unavailable: plan.unavailable, files: {} };
    await writeFile(path.join(output, "runtime.json"), `${JSON.stringify(manifest, null, 2)}\n`);
    return manifest;
  }
  const cache = path.join(repository, ".tmp/ai-privacy-runtime");
  await mkdir(cache, { recursive: true });
  await mkdir(output, { recursive: true });
  let source = options.ortDir && path.resolve(options.ortDir);
  let archiveSha256 = null;
  if (!source) {
    const downloaded = await download(plan, cache, context);
    archiveSha256 = downloaded.sha256;
    source = path.join(cache, plan.version, plan.archive.replaceAll(".", "-") + "-extracted");
    await rm(source, { recursive: true, force: true });
    await unpack(downloaded.archive, source);
  }
  await stageLibrary(plan, source, output);
  await stageLicenses(plan, source, output, { directory: options.licenseDir, ...context });
  const crt = plan.os === "windows" ? await stageWindowsCRT(options.crtDir ?? await windowsCRTDirectory(), plan.arch, output, path.join(output, plan.library)) : undefined;
  const files = {};
  await collect(output, files);
  const library = await stat(path.join(output, plan.library));
  const manifest = { abi: RUNTIME_ABI, platform: plan.os, arch: plan.arch, onnxRuntime: plan.version, archive: plan.archive, archiveSha256, minimumOS: plan.minimumOS, library: plan.library, librarySizeBytes: library.size, ...(crt ? { crt } : {}), files };
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
