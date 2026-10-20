#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test, { after, before } from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github", "scripts", "mobile-evidence.mjs");
const CHECK_SEMVER = path.join(ROOT, "scripts", "check-semver.mjs");
const SETUP_SIGNING = path.join(ROOT, "build", "android", "scripts", "setup-signing.sh");
const info = fs.readFileSync(path.join(ROOT, "build", "ios", "Info.plist"), "utf8");
let profileSignerRoot;
let profileSignerKey;
let profileSignerCert;
let validProfile;
let pythonBin;

before(() => {
  profileSignerRoot = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-profile-signer-"));
  profileSignerKey = path.join(profileSignerRoot, "fixture-key.pem");
  profileSignerCert = path.join(profileSignerRoot, "fixture-cert.pem");
  execFileSync("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", profileSignerKey, "-out", profileSignerCert, "-days", "1", "-subj", "/CN=NexTerm mobile evidence fixture"], { stdio: "ignore" });
  validProfile = makeProvisioningProfile();
  pythonBin = path.join(profileSignerRoot, "bin");
  fs.mkdirSync(pythonBin);
  const python = execFileSync("python3", ["-c", "import sys; sys.stdout.write(sys.executable)"], { encoding: "utf8" });
  fs.symlinkSync(python, path.join(pythonBin, "python3"));
});

after(() => {
  if (profileSignerRoot) fs.rmSync(profileSignerRoot, { recursive: true, force: true });
});

function setPlistString(source, key, value) {
  return source.replace(new RegExp(`(<key>${key}</key>\\s*<string>)[^<]*`), (_match, prefix) => `${prefix}${value}`);
}

function plistWithVersions(shortVersion, bundleVersion) {
  let plist = setPlistString(info, "CFBundleShortVersionString", shortVersion);
  plist = setPlistString(plist, "CFBundleVersion", bundleVersion);
  return setPlistString(plist, "CFBundleGetInfoString", "1.1.0");
}

function thinMachO(platform) {
  const bytes = Buffer.alloc(56);
  bytes.writeUInt32LE(0xfeedfacf, 0);
  bytes.writeUInt32LE(0x0100000c, 4);
  bytes.writeUInt32LE(0, 8);
  bytes.writeUInt32LE(6, 12);
  bytes.writeUInt32LE(1, 16);
  bytes.writeUInt32LE(24, 20);
  bytes.writeUInt32LE(0x32, 32);
  bytes.writeUInt32LE(24, 36);
  bytes.writeUInt32LE(platform, 40);
  bytes.writeUInt32LE(0x000f0000, 44);
  bytes.writeUInt32LE(0x000f0000, 48);
  return bytes;
}

function makeMachO(platforms = [2]) {
  const slices = platforms.map(thinMachO);
  if (slices.length === 1) return slices[0];
  let offset = 8 + slices.length * 20;
  const offsets = slices.map((slice) => {
    offset = (offset + 3) & ~3;
    const current = offset;
    offset += slice.length;
    return current;
  });
  const bytes = Buffer.alloc(offset);
  bytes.writeUInt32BE(0xcafebabe, 0);
  bytes.writeUInt32BE(slices.length, 4);
  slices.forEach((slice, index) => {
    const architecture = 8 + index * 20;
    bytes.writeUInt32BE(0x0100000c, architecture);
    bytes.writeUInt32BE(0, architecture + 4);
    bytes.writeUInt32BE(offsets[index], architecture + 8);
    bytes.writeUInt32BE(slice.length, architecture + 12);
    bytes.writeUInt32BE(2, architecture + 16);
    slice.copy(bytes, offsets[index]);
  });
  return bytes;
}

function makeProvisioningProfile({ applicationIdentifier = "TEAMID.work.nexterm.app", getTaskAllow = false, provisionedDevices = false, provisionsAllDevices = false } = {}) {
  const deviceMarkers = [
    provisionedDevices ? "<key>ProvisionedDevices</key><array><string>0123456789abcdef</string></array>" : "",
    provisionsAllDevices ? "<key>ProvisionsAllDevices</key><true/>" : "",
  ].join("");
  const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>AppIDName</key><string>NexTerm evidence fixture</string>
<key>Name</key><string>NexTerm App Store evidence fixture</string>
<key>UUID</key><string>01234567-89ab-cdef-0123-456789abcdef</string>
<key>TeamIdentifier</key><array><string>TEAMID</string></array>
<key>ExpirationDate</key><date>2099-01-01T00:00:00Z</date>
<key>Entitlements</key><dict>
<key>application-identifier</key><string>${applicationIdentifier}</string>
<key>get-task-allow</key><${getTaskAllow}/>
</dict>${deviceMarkers}
</dict></plist>`;
  return execFileSync("openssl", ["cms", "-sign", "-binary", "-nodetach", "-nosmimecap", "-outform", "DER", "-signer", profileSignerCert, "-inkey", profileSignerKey], {
    input: plist,
    maxBuffer: 16 * 1024 * 1024,
  });
}

function makePlistIPA(t, plist, executableMode = 0o755, options = {}) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-ios-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const app = path.join(directory, "Payload", "nexterm.app");
  fs.mkdirSync(app, { recursive: true });
  fs.writeFileSync(path.join(app, "Info.plist"), plist);
  const executable = path.join(app, options.executableName ?? "nexterm");
  fs.writeFileSync(executable, options.executable ?? makeMachO([2]));
  fs.chmodSync(executable, executableMode);
  if (options.profile !== null) fs.writeFileSync(path.join(app, "embedded.mobileprovision"), options.profile ?? validProfile);
  const ipa = path.join(directory, "test.ipa");
  execFileSync("zip", ["-qry", ipa, "Payload"], { cwd: directory });
  return ipa;
}

function makeIPA(t, shortVersion, bundleVersion, options = {}) {
  return makePlistIPA(t, plistWithVersions(shortVersion, bundleVersion), 0o755, options);
}

function makeBinaryIPA(t, shortVersion, bundleVersion) {
  const plist = execFileSync("python3", ["-c", "import plistlib,sys; sys.stdout.buffer.write(plistlib.dumps(plistlib.loads(sys.stdin.buffer.read()), fmt=plistlib.FMT_BINARY))"], {
    input: plistWithVersions(shortVersion, bundleVersion),
  });
  return makePlistIPA(t, plist);
}

function appendZipEntry(packagePath, name, data, mode = 0o100755) {
  const reader = `import sys, zipfile
info = zipfile.ZipInfo(sys.argv[2], date_time=(1980, 1, 1, 0, 0, 0))
info.external_attr = int(sys.argv[3]) << 16
with zipfile.ZipFile(sys.argv[1], "a") as package:
    package.writestr(info, sys.stdin.buffer.read())`;
  execFileSync("python3", ["-c", reader, packagePath, name, String(mode)], { input: data });
}

function zipFilenameRows(packagePath) {
  const reader = `import json, sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as package:
    json.dump([{"filename": info.filename, "orig_filename": info.orig_filename} for info in package.infolist()], sys.stdout)`;
  return JSON.parse(execFileSync("python3", ["-c", reader, packagePath], { encoding: "utf8" }));
}

function patchZipEntryName(packagePath, originalName, replacementName) {
  const original = Buffer.from(originalName);
  const replacement = Buffer.from(replacementName);
  assert.equal(original.length, replacement.length);
  const bytes = fs.readFileSync(packagePath);
  let count = 0;
  let cursor = 0;
  while ((cursor = bytes.indexOf(original, cursor)) >= 0) {
    replacement.copy(bytes, cursor);
    cursor += original.length;
    count += 1;
  }
  assert.equal(count, 2, "patch local and central filenames");
  fs.writeFileSync(packagePath, bytes);
  const row = zipFilenameRows(packagePath).find((item) => item.orig_filename === replacementName);
  assert.ok(row, "patched entry must retain its complete original filename");
  assert.equal(row.filename, replacementName.split("\0")[0]);
  return row;
}

function patchLocalZipEntryName(packagePath, originalName, replacementName) {
  const original = Buffer.from(originalName);
  const replacement = Buffer.from(replacementName);
  assert.equal(original.length, replacement.length);
  const bytes = fs.readFileSync(packagePath);
  const local = bytes.indexOf(original);
  assert.ok(local >= 30, "local filename must be present");
  assert.equal(bytes.readUInt32LE(local - 30), 0x04034b50);
  replacement.copy(bytes, local);
  fs.writeFileSync(packagePath, bytes);
  const central = zipFilenameRows(packagePath).find((item) => item.orig_filename === originalName);
  assert.ok(central, "central filename must remain unchanged");
  assert.equal(central.filename, originalName);
}

function zipEntryLocations(packagePath, entryName) {
  const bytes = fs.readFileSync(packagePath);
  const name = Buffer.from(entryName);
  const localName = bytes.indexOf(name);
  assert.ok(localName >= 30, "local filename must be present");
  const centralName = bytes.indexOf(name, localName + name.length);
  assert.ok(centralName >= 46, "central filename must be present");
  assert.equal(bytes.readUInt32LE(localName - 30), 0x04034b50);
  assert.equal(bytes.readUInt32LE(centralName - 46), 0x02014b50);
  return { bytes, localHeader: localName - 30, centralHeader: centralName - 46 };
}

function localDataStart(bytes, localHeader) {
  return localHeader + 30 + bytes.readUInt16LE(localHeader + 26) + bytes.readUInt16LE(localHeader + 28);
}

function zipCentralStart(packagePath) {
  return Number(execFileSync("python3", ["-c", "import sys,zipfile; print(zipfile.ZipFile(sys.argv[1]).start_dir)", packagePath], { encoding: "utf8" }).trim());
}

function moveDataStartToCentral(packagePath, entryName) {
  const { bytes, localHeader } = zipEntryLocations(packagePath, entryName);
  const extraLength = zipCentralStart(packagePath) - localDataStart(bytes, localHeader);
  assert.ok(extraLength > 0 && extraLength <= 0xffff);
  bytes.writeUInt16LE(extraLength, localHeader + 28);
  fs.writeFileSync(packagePath, bytes);
}

function pythonEntryRead(packagePath, entryName) {
  return spawnSync("python3", ["-c", "import sys,zipfile; z=zipfile.ZipFile(sys.argv[1]); i=z.getinfo(sys.argv[2]); z.read(i)", packagePath, entryName], {
    encoding: "utf8",
    timeout: 5000,
  });
}

function assertPythonLocalMismatch(packagePath, entryName) {
  const result = pythonEntryRead(packagePath, entryName);
  assert.notEqual(result.status, 0, "independent Python must reject the local/header mismatch");
  assert.match(result.stderr, /BadZipFile/);
}

function makeSetupSigningFixture(t, { existingMode = 0o600, failure = "" } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-signing-r2-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const runner = path.join(root, "runner");
  fs.mkdirSync(runner);
  const envFile = path.join(root, "github.env");
  const target = path.join(runner, "nexterm-android-release.keystore");
  if (existingMode !== null) {
    fs.writeFileSync(target, "previous keystore");
    fs.chmodSync(target, existingMode);
  }
  let nodeOptions = "";
  if (failure) {
    const preload = path.join(root, "failure.cjs");
    const source = failure === "write"
      ? `const fs = require("node:fs"); const original = fs.writeFileSync; fs.writeFileSync = function(file, ...args) { if (typeof file === "number") throw new Error("injected write failure"); return original.call(fs, file, ...args); };`
      : `const fs = require("node:fs"); fs.closeSync = function() { throw new Error("injected close failure"); };`;
    fs.writeFileSync(preload, source);
    nodeOptions = `--require=${preload}`;
  }
  const result = spawnSync("bash", [SETUP_SIGNING], {
    env: {
      ...process.env,
      RUNNER_TEMP: runner,
      GITHUB_ENV: envFile,
      NEXTERM_ANDROID_KEYSTORE_BASE64: Buffer.from("replacement keystore").toString("base64"),
      ANDROID_KEYSTORE_PASSWORD: "dummy-password",
      ANDROID_KEY_ALIAS: "dummy-alias",
      ANDROID_KEY_PASSWORD: "dummy-key-password",
      NODE_OPTIONS: nodeOptions,
    },
    encoding: "utf8",
    timeout: 5000,
  });
  return { result, runner, envFile, target };
}

const ANDROID_CERT_SHA256 = "a".repeat(64);

function runPackageEvidence(packagePath, id, environment = {}) {
  return spawnSync(process.execPath, [SCRIPT, packagePath, id], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: "1.1.0", ...environment },
    encoding: "utf8",
  });
}

function runMobileEvidence(ipa) {
  return runPackageEvidence(ipa, "mobile-ios");
}

function makeAndroidAPK(t, manifestName = "AndroidManifest.xml", data = Buffer.from("fixture")) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-android-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const apk = path.join(directory, "test.apk");
  const writer = `import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as package:
    info = zipfile.ZipInfo(sys.argv[2], date_time=(1980, 1, 1, 0, 0, 0))
    package.writestr(info, sys.stdin.buffer.read())`;
  execFileSync("python3", ["-c", writer, apk, manifestName], { input: data });
  return apk;
}

function makeFakeAndroidTools(t, { certificate = ANDROID_CERT_SHA256, versionName = "1.1.0", versionCode = "1001000", signerLabel = "Signer #1", hang = false } = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-android-tools-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const bin = path.join(root, "build-tools", "35.0.0");
  fs.mkdirSync(bin, { recursive: true });
  const signer = hang ? `#!${process.execPath}\nsetTimeout(() => {}, 10000);\n` : `#!/bin/sh\nprintf '%s\\n' '${signerLabel} certificate SHA-256 digest: ${certificate}'\n`;
  const aapt = hang ? `#!${process.execPath}\nsetTimeout(() => {}, 10000);\n` : `#!/bin/sh\nprintf '%s\\n' "package: name='work.nexterm.app' versionCode='${versionCode}' versionName='${versionName}'"\n`;
  fs.writeFileSync(path.join(bin, "apksigner"), signer, { mode: 0o755 });
  fs.writeFileSync(path.join(bin, "aapt2"), aapt, { mode: 0o755 });
  return root;
}

function makeHangingTools(t, names) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-hanging-tools-"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const name of names) {
    fs.writeFileSync(path.join(root, name), `#!${process.execPath}\nsetTimeout(() => {}, 10000);\n`, { mode: 0o755 });
  }
  return root;
}

function androidEnvironment(sdkRoot, certificate = ANDROID_CERT_SHA256) {
  return {
    PATH: pythonBin,
    ANDROID_HOME: sdkRoot,
    ANDROID_SDK_ROOT: "",
    NEXTERM_ANDROID_CERT_SHA256: certificate,
  };
}

function runAndroidEvidence(apk, environment) {
  return runPackageEvidence(apk, "mobile-android", environment);
}

test("rejects a package over the configured whole-file limit before creating a report", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runPackageEvidence(ipa, "mobile-ios", { NEXTERM_MOBILE_EVIDENCE_MAX_PACKAGE_BYTES: "16" });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /mobile package exceeds the 16 byte limit/);
  assert.equal(fs.existsSync(`${ipa}.artifact.json`), false);
});

test("rejects excessive total declared uncompressed content in the central directory", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runPackageEvidence(ipa, "mobile-ios", { NEXTERM_MOBILE_EVIDENCE_MAX_TOTAL_UNCOMPRESSED_BYTES: "16" });
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("total uncompressed size exceeds the 16 byte limit")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("rejects an over-limit ZIP entry before decompressing content", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runPackageEvidence(ipa, "mobile-ios", { NEXTERM_MOBILE_EVIDENCE_MAX_ENTRY_BYTES: "16" });
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("ZIP entry exceeds the 16 byte limit")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("rejects an Info.plist over the independent 16 MiB content limit", (t) => {
  const oversized = Buffer.concat([Buffer.from(plistWithVersions("1.1.0", "1.1.0")), Buffer.alloc(16 * 1024 * 1024)]);
  const ipa = makePlistIPA(t, oversized);
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "failed" && item.detail.includes("16777216 byte limit")));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "passed"));
});

test("times out a hanging central-directory reader before invoking content tools", (t) => {
  const tools = makeHangingTools(t, ["python3"]);
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runPackageEvidence(ipa, "mobile-ios", {
    PATH: `${tools}${path.delimiter}${process.env.PATH || ""}`,
    NEXTERM_MOBILE_EVIDENCE_PYTHON3_TIMEOUT_MS: "100",
  });
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("ETIMEDOUT")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("times out a hanging OpenSSL process and fails the profile assertion", (t) => {
  const tools = makeHangingTools(t, ["openssl"]);
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  // Only OpenSSL gets the small budget; the real python3 stages keep the
  // global default, so the timeout cannot race their startup on loaded hosts.
  const result = runPackageEvidence(ipa, "mobile-ios", {
    PATH: `${tools}${path.delimiter}${process.env.PATH || ""}`,
    NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS: "250",
  });
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed" && item.detail.includes("ETIMEDOUT")));
});

test("times out hanging apksigner and aapt2 processes", (t) => {
  const apk = makeAndroidAPK(t);
  const tools = makeFakeAndroidTools(t, { hang: true });
  const result = runAndroidEvidence(apk, {
    ...androidEnvironment(tools),
    NEXTERM_MOBILE_EVIDENCE_APKSIGNER_TIMEOUT_MS: "250",
    NEXTERM_MOBILE_EVIDENCE_AAPT2_TIMEOUT_MS: "250",
  });
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  for (const id of ["android-signature", "android-version"]) {
    assert.ok(report.assertions.some((item) => item.id === id && item.status === "failed" && item.detail.includes("ETIMEDOUT")));
  }
});

test("per-tool timeout overrides must be integers within the global ceiling", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  for (const environment of [
    { NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS: "0" },
    { NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS: "not-a-number" },
    { NEXTERM_MOBILE_EVIDENCE_TOOL_TIMEOUT_MS: "1000", NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS: "2000" },
  ]) {
    const result = runPackageEvidence(ipa, "mobile-ios", environment);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /NEXTERM_MOBILE_EVIDENCE_OPENSSL_TIMEOUT_MS must be an integer in 1\.\.\d+/);
    assert.equal(fs.existsSync(`${ipa}.artifact.json`), false);
  }
});

for (const existingMode of [0o600, 0o644]) {
  test(`setup-signing leaves a pre-existing ${existingMode.toString(8)} keystore at 0600 without exporting secrets`, (t) => {
    const { result, runner, envFile, target } = makeSetupSigningFixture(t, { existingMode });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(fs.statSync(target).mode & 0o777, 0o600);
    assert.equal(fs.readFileSync(target, "utf8"), "replacement keystore");
    assert.deepEqual(fs.readdirSync(runner), ["nexterm-android-release.keystore"]);
    const environment = fs.readFileSync(envFile, "utf8");
    assert.equal(environment, `ANDROID_KEYSTORE_FILE=${target}\n`);
    assert.ok(!environment.includes("PASSWORD") && !environment.includes("ALIAS") && !environment.includes("dummy-"));
  });
}

for (const failure of ["write", "close"]) {
  test(`setup-signing cleans temporary and pre-existing material after an injected ${failure} failure`, (t) => {
    const { result, runner, envFile, target } = makeSetupSigningFixture(t, { existingMode: 0o644, failure });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, new RegExp(`injected ${failure} failure`));
    assert.equal(fs.existsSync(target), false);
    assert.deepEqual(fs.readdirSync(runner), []);
    assert.equal(fs.existsSync(envFile), false);
  });
}

test("rejects duplicate device and simulator executables before reading Mach-O content", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { executable: makeMachO([2]) });
  appendZipEntry(ipa, "Payload/nexterm.app/nexterm", makeMachO([7]));
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("duplicate ZIP entry")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

for (const [entry, data] of [
  ["Payload/nexterm.app/Info.plist", plistWithVersions("1.1.0", "1.1.0")],
  ["Payload/nexterm.app/embedded.mobileprovision", validProfile],
]) {
  test(`rejects duplicate ${entry} before reading any iOS content`, (t) => {
    const ipa = makeIPA(t, "1.1.0", "1.1.0");
    appendZipEntry(ipa, entry, data, 0o100644);
    const result = runMobileEvidence(ipa);
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("duplicate ZIP entry")));
    assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
  });
}

test("rejects duplicate APK entries before invoking Android package tools", (t) => {
  const apk = makeAndroidAPK(t);
  appendZipEntry(apk, "AndroidManifest.xml", Buffer.from("duplicate"), 0o100644);
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("duplicate ZIP entry")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

test("rejects ZIP traversal before reading content", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  appendZipEntry(ipa, "Payload/../escape", Buffer.from("unsafe"), 0o100644);
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("unsafe ZIP entry path")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("rejects non-regular ZIP entries before reading content", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  appendZipEntry(ipa, "Payload/nexterm.app/linked", Buffer.from("nexterm"), 0o120777);
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("unsupported ZIP entry type")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("accepts ordinary entries whose original and decoded filenames are identical", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  assert.ok(zipFilenameRows(ipa).every((item) => item.orig_filename === item.filename));
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "passed"));
});

test("accepts matching local and central names for iOS and Android", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  assert.equal(pythonEntryRead(ipa, "Payload/nexterm.app/nexterm").status, 0);
  assert.equal(runMobileEvidence(ipa).status, 0);
  const apk = makeAndroidAPK(t);
  assert.equal(pythonEntryRead(apk, "AndroidManifest.xml").status, 0);
  const android = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(android.status, 0, android.stderr);
});

test("accepts an empty entry whose data ends exactly at the central directory", (t) => {
  const apk = makeAndroidAPK(t, "empty.bin", Buffer.alloc(0));
  const { bytes, localHeader } = zipEntryLocations(apk, "empty.bin");
  assert.equal(localDataStart(bytes, localHeader), zipCentralStart(apk));
  assert.equal(pythonEntryRead(apk, "empty.bin").status, 0);
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 0, result.stderr);
});

for (const [id, entryName] of [
  ["mobile-android", "AndroidManifest.xml"],
  ["mobile-ios", "Payload/nexterm.app/nexterm"],
]) {
  test(`rejects ${id} compressed data shifted beyond central-start by extra`, (t) => {
    const packagePath = id === "mobile-android" ? makeAndroidAPK(t) : makeIPA(t, "1.1.0", "1.1.0");
    moveDataStartToCentral(packagePath, entryName);
    assertPythonLocalMismatch(packagePath, entryName);
    const result = id === "mobile-android" ? runAndroidEvidence(packagePath, androidEnvironment(makeFakeAndroidTools(t))) : runMobileEvidence(packagePath);
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${packagePath}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("compressed data overlaps")));
    assert.ok(!report.assertions.some((item) => item.id.startsWith(id === "mobile-android" ? "android-" : "ios-")));
  });
}

test("rejects compressed data overlapping the next local header", (t) => {
  const apk = makeAndroidAPK(t);
  appendZipEntry(apk, "second.bin", Buffer.from("second"), 0o100644);
  const first = zipEntryLocations(apk, "AndroidManifest.xml");
  const second = zipEntryLocations(apk, "second.bin");
  const compressedSize = second.localHeader - localDataStart(first.bytes, first.localHeader) + 1;
  assert.ok(compressedSize > 0);
  first.bytes.writeUInt32LE(compressedSize, first.localHeader + 18);
  first.bytes.writeUInt32LE(compressedSize, first.centralHeader + 20);
  fs.writeFileSync(apk, first.bytes);
  assertPythonLocalMismatch(apk, "AndroidManifest.xml");
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("compressed data overlaps")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

test("rejects data-descriptor declarations whose trailer cannot be bounded", (t) => {
  const apk = makeAndroidAPK(t);
  const { bytes, localHeader, centralHeader } = zipEntryLocations(apk, "AndroidManifest.xml");
  bytes.writeUInt16LE(bytes.readUInt16LE(localHeader + 6) | 0x8, localHeader + 6);
  bytes.writeUInt16LE(bytes.readUInt16LE(centralHeader + 8) | 0x8, centralHeader + 8);
  fs.writeFileSync(apk, bytes);
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("data descriptor")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

test("rejects local ZIP64 size fields that cannot be bounded by this preflight", (t) => {
  const apk = makeAndroidAPK(t);
  const { bytes, localHeader } = zipEntryLocations(apk, "AndroidManifest.xml");
  bytes.writeUInt32LE(0xffffffff, localHeader + 18);
  fs.writeFileSync(apk, bytes);
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("ZIP64 local size fields")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

for (const nulCount of [1, 4]) {
  test(`rejects an Android local-only filename with ${nulCount} NUL bytes`, (t) => {
    const central = "AndroidManifest.xml";
    const local = `${central.slice(0, -nulCount)}${"\0".repeat(nulCount)}`;
    const apk = makeAndroidAPK(t);
    patchLocalZipEntryName(apk, central, local);
    assertPythonLocalMismatch(apk, central);
    const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("local filename")));
    assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
  });

  test(`rejects an iOS local-only filename with ${nulCount} NUL bytes`, (t) => {
    const central = "Payload/nexterm.app/nexterm";
    const local = `${central.slice(0, -nulCount)}${"\0".repeat(nulCount)}`;
    const ipa = makeIPA(t, "1.1.0", "1.1.0");
    patchLocalZipEntryName(ipa, central, local);
    assertPythonLocalMismatch(ipa, central);
    const result = runMobileEvidence(ipa);
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("local filename")));
    assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
  });
}

test("rejects an ordinary Android local/central rename mismatch", (t) => {
  const apk = makeAndroidAPK(t);
  patchLocalZipEntryName(apk, "AndroidManifest.xml", "AndroidManifest.xmX");
  assertPythonLocalMismatch(apk, "AndroidManifest.xml");
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("local filename differs")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

test("rejects an ordinary iOS local/central rename mismatch", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  patchLocalZipEntryName(ipa, "Payload/nexterm.app/nexterm", "Payload/nexterm.app/nexterX");
  assertPythonLocalMismatch(ipa, "Payload/nexterm.app/nexterm");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("local filename differs")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

test("rejects a local/central mismatch on a non-required entry", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  appendZipEntry(ipa, "Payload/nexterm.app/NOTICE", Buffer.from("notice"), 0o100644);
  patchLocalZipEntryName(ipa, "Payload/nexterm.app/NOTICE", "Payload/nexterm.app/NOTICF");
  assertPythonLocalMismatch(ipa, "Payload/nexterm.app/NOTICE");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("local filename differs")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
});

for (const nulCount of [1, 4]) {
  test(`rejects an executable original filename containing ${nulCount} NUL bytes`, (t) => {
    const executableName = `nexterm${"X".repeat(nulCount)}`;
    const ipa = makeIPA(t, "1.1.0", "1.1.0", { executableName });
    patchZipEntryName(
      ipa,
      `Payload/nexterm.app/${executableName}`,
      `Payload/nexterm.app/nexterm${"\0".repeat(nulCount)}`,
    );
    const result = runMobileEvidence(ipa);
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("ambiguous ZIP entry filename")));
    assert.ok(!report.assertions.some((item) => item.id.startsWith("ios-")));
  });
}

test("rejects an APK original filename with decoding ambiguity before Android tools", (t) => {
  const apk = makeAndroidAPK(t, "AndroidManifest.xmlX");
  patchZipEntryName(apk, "AndroidManifest.xmlX", "AndroidManifest.xml\0");
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t)));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "zip-structure" && item.status === "failed" && item.detail.includes("ambiguous ZIP entry filename")));
  assert.ok(!report.assertions.some((item) => item.id.startsWith("android-")));
});

test("rejects simulator Mach-O even when the plist claims a device build", (t) => {
  const plist = setPlistString(plistWithVersions("1.1.0", "1.1.0"), "DTPlatformName", "iphoneos");
  const ipa = makePlistIPA(t, plist, 0o755, { executable: makeMachO([7]) });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /ios-platform/);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "failed" && item.detail.includes("DTPlatformName=iphoneos") && item.detail.includes("platforms=7")));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "passed"));
});

test("accepts a fat Mach-O when every slice targets iOS devices", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { executable: makeMachO([2, 2]) });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "passed" && item.detail.includes("platforms=2,2")));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "passed"));
});

test("rejects a fat Mach-O containing a simulator slice", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { executable: makeMachO([2, 7]) });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "failed" && item.detail.includes("platforms=2,7")));
});

test("rejects a malformed executable instead of trusting plist metadata", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { executable: Buffer.from("not a Mach-O executable") });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "failed"));
});

test("requires an embedded provisioning profile", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { profile: null });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed"));
});

test("rejects development provisioning even with a device executable", (t) => {
  const profile = makeProvisioningProfile({ getTaskAllow: true, provisionedDevices: true });
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { profile });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed"));
});

test("rejects a provisioning profile for another application", (t) => {
  const profile = makeProvisioningProfile({ applicationIdentifier: "TEAMID.work.other.app" });
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { profile });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-platform" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed"));
});

test("rejects enterprise provisioning with ProvisionsAllDevices", (t) => {
  const profile = makeProvisioningProfile({ provisionsAllDevices: true });
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { profile });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed"));
});

test("rejects malformed CMS provisioning data", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0", { profile: Buffer.from("not a CMS profile") });
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-provisioning-profile" && item.status === "failed"));
});

test("accepts an IPA when both plist versions match the release", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.equal(report.status, "passed");
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
});

for (const [release, shortVersion] of [
  ["1.2.3", "1.2.3"],
  ["1.2.3-rc.1", "1.2.3"],
  ["1.2.3+build.5", "1.2.3"],
  ["1.2.3-rc.1+build.5", "1.2.3"],
]) {
  test(`accepts the numeric iOS short version for ${release}`, (t) => {
    const ipa = makeIPA(t, shortVersion, "42.1");
    const result = runPackageEvidence(ipa, "mobile-ios", { NEXTERM_RELEASE_VERSION: release, IOS_BUILD_NUMBER: "42.1" });
    assert.equal(result.status, 0, result.stderr);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.equal(report.status, "passed");
    assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
  });
}

for (const [field, shortVersion, bundleVersion] of [
  ["CFBundleShortVersionString", "1.2.3-rc.1", "42.1"],
  ["CFBundleVersion", "1.2.3", "42.2"],
]) {
  test(`rejects the wrong ${field} for a combined prerelease and build release`, (t) => {
    const ipa = makeIPA(t, shortVersion, bundleVersion);
    const result = runPackageEvidence(ipa, "mobile-ios", {
      NEXTERM_RELEASE_VERSION: "1.2.3-rc.1+build.5",
      IOS_BUILD_NUMBER: "42.1",
    });
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "failed"));
  });
}

for (const release of ["0.0.0", "1.2.3-rc..1", "1.2.3+build..5", "1.2.3-01", "1.2.3-rc_1"]) {
  test(`rejects invalid iOS release version ${release}`, (t) => {
    const ipa = makeIPA(t, "1.2.3", "42.1");
    const result = runPackageEvidence(ipa, "mobile-ios", { NEXTERM_RELEASE_VERSION: release, IOS_BUILD_NUMBER: "42.1" });
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "failed"));
  });
}

test("shared Android version CLI emits tag-derived values and rejects invalid prerelease numbers", () => {
  const valid = spawnSync(process.execPath, [CHECK_SEMVER, "--android-json", "1.2.3-rc.1+build.5"], { encoding: "utf8" });
  assert.equal(valid.status, 0, valid.stderr);
  assert.deepEqual(JSON.parse(valid.stdout), { versionName: "1.2.3-rc.1+build.5", versionCode: 1002003 });
  const invalid = spawnSync(process.execPath, [CHECK_SEMVER, "--android-json", "1.2.3-01"], { encoding: "utf8" });
  assert.equal(invalid.status, 1);
});

test("accepts combined prerelease and build metadata in Android version evidence", (t) => {
  const version = "1.2.3-rc.1+build.5";
  const apk = makeAndroidAPK(t);
  const tools = makeFakeAndroidTools(t, { versionName: version, versionCode: "1002003" });
  const result = runAndroidEvidence(apk, { ...androidEnvironment(tools), NEXTERM_RELEASE_VERSION: version });
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "android-version" && item.status === "passed"));
});

for (const [field, shortVersion, bundleVersion] of [
  ["CFBundleShortVersionString", "0.1.0", "1.1.0"],
  ["CFBundleVersion", "1.1.0", "0.1.0"],
]) {
  test(`rejects an IPA with the wrong ${field}`, (t) => {
    const ipa = makeIPA(t, shortVersion, bundleVersion);
    const result = runMobileEvidence(ipa);
    assert.equal(result.status, 1);
    assert.match(result.stderr, /ios-version/);
    const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
    assert.equal(report.status, "failed");
    assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "failed"));
  });
}

function assertRejected(ipa) {
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /ios-version/);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.equal(report.status, "failed");
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "failed"));
  return result;
}

test("rejects malformed XML even when both version elements are present", (t) => {
  const plist = plistWithVersions("1.1.0", "1.1.0").replace("</dict>\n</plist>", "");
  assert.ok(!plist.includes("</plist>"));
  const result = assertRejected(makePlistIPA(t, plist));
  assert.match(result.stderr, /cannot parse/);
});

test("accepts a binary plist when both versions match", (t) => {
  const ipa = makeBinaryIPA(t, "1.1.0", "1.1.0");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.equal(report.status, "passed");
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
});

test("rejects a binary plist with the wrong version", (t) => {
  assertRejected(makeBinaryIPA(t, "1.1.0", "0.1.0"));
});

test("rejects malformed binary plist data", (t) => {
  const result = assertRejected(makePlistIPA(t, Buffer.from("bplist00 truncated")));
  assert.match(result.stderr, /cannot parse/);
});

test("accepts an IPA whose bundle executable mode is 0755", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-executable-mode" && item.status === "passed" && item.detail.includes("mode=0755")));
});

test("rejects an IPA whose bundle executable is not executable", (t) => {
  const ipa = makePlistIPA(t, plistWithVersions("1.1.0", "1.1.0"), 0o644);
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /ios-executable-mode/);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
  assert.ok(report.assertions.some((item) => item.id === "ios-executable-mode" && item.status === "failed" && item.detail.includes("mode=0644")));
});

for (const signerLabel of ["Signer #1", "V2 Signer"]) {
  test(`accepts an APK with the pinned ${signerLabel} certificate and tag-derived version`, (t) => {
    const apk = makeAndroidAPK(t);
    const tools = makeFakeAndroidTools(t, { signerLabel });
    const result = runAndroidEvidence(apk, androidEnvironment(tools));
    assert.equal(result.status, 0, result.stderr);
    const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
    assert.equal(report.status, "passed");
    for (const id of ["android-signature", "android-version"]) {
      assert.ok(report.assertions.some((item) => item.id === id && item.status === "passed"));
    }
  });
}

test("rejects an APK whose signer does not match the pinned certificate", (t) => {
  const apk = makeAndroidAPK(t);
  const tools = makeFakeAndroidTools(t, { certificate: "b".repeat(64) });
  const result = runAndroidEvidence(apk, androidEnvironment(tools));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "android-signature" && item.status === "failed"));
  assert.ok(report.assertions.some((item) => item.id === "android-version" && item.status === "passed"));
});

for (const [field, options] of [
  ["versionName", { versionName: "1.1.1" }],
  ["versionCode", { versionCode: "1001001" }],
]) {
  test(`rejects an APK with the wrong ${field}`, (t) => {
    const apk = makeAndroidAPK(t);
    const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t, options)));
    assert.equal(result.status, 1);
    const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
    assert.ok(report.assertions.some((item) => item.id === "android-signature" && item.status === "passed"));
    assert.ok(report.assertions.some((item) => item.id === "android-version" && item.status === "failed"));
  });
}

test("requires a pinned certificate when apksigner is available", (t) => {
  const apk = makeAndroidAPK(t);
  const result = runAndroidEvidence(apk, androidEnvironment(makeFakeAndroidTools(t), ""));
  assert.equal(result.status, 1);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.ok(report.assertions.some((item) => item.id === "android-signature" && item.status === "failed"));
});

test("records Android tool checks as skipped when SDK tools are unavailable", (t) => {
  const apk = makeAndroidAPK(t);
  const emptySdk = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-empty-android-sdk-"));
  t.after(() => fs.rmSync(emptySdk, { recursive: true, force: true }));
  const result = runAndroidEvidence(apk, androidEnvironment(emptySdk));
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${apk}.artifact.json`, "utf8"));
  assert.equal(report.status, "passed");
  for (const id of ["android-signature", "android-version"]) {
    assert.ok(report.assertions.some((item) => item.id === id && item.status === "skipped" && item.detail.includes("not verified")));
  }
});
