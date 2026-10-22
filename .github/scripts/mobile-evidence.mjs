#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseReleaseVersion } from "../../scripts/lib/semver.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const [fileArg, id] = process.argv.slice(2);
if (!fileArg || !["mobile-android", "mobile-ios"].includes(id)) {
  console.error("usage: node .github/scripts/mobile-evidence.mjs <package> <mobile-android|mobile-ios>");
  process.exit(1);
}
const file = path.resolve(ROOT, fileArg);
if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
  console.error(`mobile package does not exist: ${file}`);
  process.exit(1);
}
const version = process.env.NEXTERM_RELEASE_VERSION;
if (!version) {
  console.error("NEXTERM_RELEASE_VERSION is required");
  process.exit(1);
}
const MIB = 1024 * 1024;
const GIB = 1024 * MIB;
const MAX_TOOL_OUTPUT_BYTES = 16 * MIB;
const MAX_PLIST_OR_PROFILE_BYTES = 16 * MIB;
const MAX_EXECUTABLE_BYTES = 512 * MIB;

function environmentCeiling(name, ceiling) {
  const raw = process.env[name];
  if (raw === undefined || raw === "") return ceiling;
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 1 || value > ceiling) {
    console.error(`${name} must be an integer in 1..${ceiling}`);
    process.exit(1);
  }
  return value;
}

const MAX_PACKAGE_BYTES = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_PACKAGE_BYTES", 2 * GIB);
const MAX_TOTAL_UNCOMPRESSED_BYTES = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_TOTAL_UNCOMPRESSED_BYTES", 4 * GIB);
const MAX_ZIP_ENTRY_BYTES = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_MAX_ENTRY_BYTES", 512 * MIB);
const TOOL_TIMEOUT_MS = environmentCeiling("NEXTERM_MOBILE_EVIDENCE_TOOL_TIMEOUT_MS", 60000);

// Per-tool budget overrides: a deliberately hanging tool under test must not
// share a small budget with the real tools around it. Each tool keeps the
// global budget unless NEXTERM_MOBILE_EVIDENCE_<TOOL>_TIMEOUT_MS lowers it for
// that tool alone; values are validated eagerly so a bad override fails before
// any tool runs.
const TOOL_TIMEOUT_OVERRIDES = new Map();
for (const tool of ["openssl", "python3", "apksigner", "aapt2"]) {
  const name = `NEXTERM_MOBILE_EVIDENCE_${tool.toUpperCase()}_TIMEOUT_MS`;
  const raw = process.env[name];
  if (raw === undefined || raw === "") continue;
  const value = Number(raw);
  if (!Number.isSafeInteger(value) || value < 1 || value > TOOL_TIMEOUT_MS) {
    console.error(`${name} must be an integer in 1..${TOOL_TIMEOUT_MS}`);
    process.exit(1);
  }
  TOOL_TIMEOUT_OVERRIDES.set(tool, value);
}

function toolTimeoutMs(command) {
  const base = String(command).split(/[\\/]/).pop().replace(/\.[^.]*$/, "").toLowerCase();
  return TOOL_TIMEOUT_OVERRIDES.get(base) ?? TOOL_TIMEOUT_MS;
}

const PLIST_READER = `import json, plistlib, sys
document = plistlib.loads(sys.stdin.buffer.read())
result = {}
for key in ("CFBundleShortVersionString", "CFBundleVersion", "DTPlatformName"):
    value = document.get(key)
    if not isinstance(value, str):
        raise ValueError(f"{key} is not a string")
    result[key] = value
json.dump(result, sys.stdout)`;
const PROFILE_READER = `import json, plistlib, sys
document = plistlib.loads(sys.stdin.buffer.read())
if not isinstance(document, dict):
    raise ValueError("provisioning profile is not a dictionary")
team = document.get("TeamIdentifier")
entitlements = document.get("Entitlements")
if not isinstance(team, list) or not all(isinstance(item, str) for item in team):
    raise ValueError("TeamIdentifier is not a string array")
if not isinstance(entitlements, dict):
    raise ValueError("Entitlements is not a dictionary")
json.dump({
    "teamIdentifiers": team,
    "applicationIdentifier": entitlements.get("application-identifier"),
    "getTaskAllow": entitlements.get("get-task-allow"),
    "hasProvisionedDevices": "ProvisionedDevices" in document,
    "hasProvisionsAllDevices": "ProvisionsAllDevices" in document,
}, sys.stdout)`;
const ZIP_STRUCTURE_READER = `import json, stat, struct, sys, zipfile
def local_entry_data_start(info, raw_name, stream, central_end):
    expected = raw_name.encode("utf-8" if info.flag_bits & 0x800 else "cp437")
    offset = info.header_offset
    if offset < 0 or offset + 30 > central_end:
        raise ValueError("cannot verify local filename bounds for " + raw_name)
    stream.seek(offset)
    header = stream.read(30)
    if len(header) != 30 or header[:4] != bytes((0x50, 0x4b, 0x03, 0x04)):
        raise ValueError("cannot verify local filename header for " + raw_name)
    local_flags = struct.unpack("<H", header[6:8])[0]
    if local_flags & 0x8 or info.flag_bits & 0x8:
        raise ValueError("data descriptor entries cannot be bounded for " + raw_name)
    local_compressed = struct.unpack("<I", header[18:22])[0]
    local_uncompressed = struct.unpack("<I", header[22:26])[0]
    if local_compressed == 0xffffffff or local_uncompressed == 0xffffffff:
        raise ValueError("ZIP64 local size fields cannot be bounded for " + raw_name)
    if local_compressed != info.compress_size or local_uncompressed != info.file_size:
        raise ValueError("local and central size fields differ for " + raw_name)
    name_length = struct.unpack("<H", header[26:28])[0]
    extra_length = struct.unpack("<H", header[28:30])[0]
    if name_length != len(expected):
        raise ValueError("local filename length differs from central directory for " + raw_name)
    if offset + 30 + name_length + extra_length > central_end:
        raise ValueError("truncated local header for " + raw_name)
    local_name = stream.read(name_length)
    if len(local_name) != name_length or local_name != expected:
        raise ValueError("local filename differs from central directory for " + raw_name)
    return offset + 30 + name_length + extra_length

max_entry = int(sys.argv[2])
max_total = int(sys.argv[3])
required_names = set(sys.argv[4].split(",")) if sys.argv[4] else set()
with zipfile.ZipFile(sys.argv[1]) as package, open(sys.argv[1], "rb") as stream:
    infos = package.infolist()
    if len(infos) > 100000:
        raise ValueError("ZIP has more than 100000 entries")
    seen = set()
    required = {}
    total_size = 0
    local_entries = []
    for info in infos:
        raw_name = info.orig_filename
        if raw_name != info.filename:
            raise ValueError("ambiguous ZIP entry filename: " + repr(raw_name))
        name = raw_name
        if name in seen:
            raise ValueError("duplicate ZIP entry: " + name)
        seen.add(name)
        if not name or name.startswith("/") or "\\\\" in name or "\\x00" in name or (len(name) > 1 and name[1] == ":"):
            raise ValueError("unsafe ZIP entry path: " + repr(name))
        if any(ord(char) < 32 for char in name) or ".." in name.split("/"):
            raise ValueError("unsafe ZIP entry path: " + repr(name))
        if info.flag_bits & 1:
            raise ValueError("encrypted ZIP entry is not supported: " + name)
        mode = (info.external_attr >> 16) & 0o177777
        file_type = stat.S_IFMT(mode)
        if file_type not in (0, stat.S_IFREG, stat.S_IFDIR):
            raise ValueError("unsupported ZIP entry type: " + name)
        if info.is_dir() and file_type not in (0, stat.S_IFDIR):
            raise ValueError("ZIP directory has a non-directory type: " + name)
        if not info.is_dir() and file_type == stat.S_IFDIR:
            raise ValueError("ZIP file has a directory type: " + name)
        if info.file_size < 0 or info.file_size > max_entry:
            raise ValueError("ZIP entry exceeds the %d byte limit: %s" % (max_entry, name))
        total_size += info.file_size
        if total_size > max_total:
            raise ValueError("ZIP total uncompressed size exceeds the %d byte limit" % max_total)
        data_start = local_entry_data_start(info, raw_name, stream, package.start_dir)
        local_entries.append((info.header_offset, data_start, raw_name, info.compress_size))
        if name in required_names:
            if info.is_dir() or file_type not in (0, stat.S_IFREG):
                raise ValueError("required ZIP entry is not a regular file: " + name)
            required[name] = {"mode": mode & 0o7777, "size": info.file_size}
    local_entries.sort(key=lambda item: item[0])
    for index, (offset, data_start, raw_name, compressed_size) in enumerate(local_entries):
        next_offset = local_entries[index + 1][0] if index + 1 < len(local_entries) else package.start_dir
        if compressed_size < 0:
            raise ValueError("invalid compressed size for " + raw_name)
        if data_start > next_offset or data_start + compressed_size > next_offset:
            raise ValueError("compressed data overlaps another local header or central directory for " + raw_name)
    json.dump({"entryCount": len(infos), "totalUncompressedBytes": total_size, "required": required}, sys.stdout)`;
const ZIP_ENTRY_READER = `import sys, zipfile
limit = int(sys.argv[3])
with zipfile.ZipFile(sys.argv[1]) as package:
    infos = package.infolist()
    for info in infos:
        if info.orig_filename != info.filename:
            raise ValueError("ambiguous ZIP entry filename: " + repr(info.orig_filename))
    matches = [info for info in infos if info.orig_filename == sys.argv[2]]
    if len(matches) != 1:
        raise ValueError("required ZIP entry is not unique: " + sys.argv[2])
    info = matches[0]
    if info.file_size > limit:
        raise ValueError("ZIP entry exceeds the %d byte limit: %s" % (limit, sys.argv[2]))
    data = package.read(info)
    if len(data) > limit:
        raise ValueError("ZIP entry output exceeds the %d byte limit: %s" % (limit, sys.argv[2]))
    sys.stdout.buffer.write(data)`;
let zipManifest = null;

function runTool(command, args, options = {}) {
  return spawnSync(command, args, { maxBuffer: MAX_TOOL_OUTPUT_BYTES, timeout: toolTimeoutMs(command), ...options });
}

function inspectZip(file) {
  const required = id === "mobile-ios" ? ["Payload/nexterm.app/Info.plist", "Payload/nexterm.app/nexterm", "Payload/nexterm.app/embedded.mobileprovision"] : [];
  const inspected = runTool("python3", ["-c", ZIP_STRUCTURE_READER, file, String(MAX_ZIP_ENTRY_BYTES), String(MAX_TOTAL_UNCOMPRESSED_BYTES), required.join(",")], { encoding: "utf8" });
  if (inspected.error) throw inspected.error;
  if (inspected.status !== 0) throw new Error(`ZIP central directory validation failed: ${inspected.stderr.trim()}`);
  return JSON.parse(inspected.stdout);
}

function zipEntry(file, entry, maxBuffer = MAX_PLIST_OR_PROFILE_BYTES) {
  const metadata = zipManifest?.required?.[entry];
  if (!metadata) throw new Error(`required ZIP entry is missing: ${entry}`);
  if (metadata.size > maxBuffer) throw new Error(`ZIP entry ${entry} exceeds the ${maxBuffer} byte limit`);
  const extracted = runTool("python3", ["-c", ZIP_ENTRY_READER, file, entry, String(maxBuffer)], { maxBuffer });
  if (extracted.error) throw extracted.error;
  if (extracted.status !== 0) throw new Error(`cannot read ${entry}: ${extracted.stderr.toString("utf8").trim()}`);
  return extracted.stdout;
}

function parsePlist(bytes, reader, label) {
  const parsed = runTool("python3", ["-c", reader], {
    input: bytes,
    encoding: "utf8",
  });
  if (parsed.error) throw parsed.error;
  if (parsed.status !== 0) throw new Error(`cannot parse ${label}: ${parsed.stderr.trim()}`);
  return JSON.parse(parsed.stdout);
}

function iosMetadata(file) {
  const entry = "Payload/nexterm.app/Info.plist";
  const plist = parsePlist(zipEntry(file, entry), PLIST_READER, entry);
  return {
    shortVersion: plist.CFBundleShortVersionString,
    bundleVersion: plist.CFBundleVersion,
    platformName: plist.DTPlatformName,
  };
}

function iosExecutableMode() {
  const entry = "Payload/nexterm.app/nexterm";
  const metadata = zipManifest?.required?.[entry];
  if (!metadata) throw new Error(`required ZIP entry is missing: ${entry}`);
  return metadata.mode;
}

function iosProvisioningProfile(file) {
  const entry = "Payload/nexterm.app/embedded.mobileprovision";
  const decoded = runTool("openssl", ["cms", "-verify", "-binary", "-inform", "DER", "-noverify"], {
    input: zipEntry(file, entry),
  });
  if (decoded.error) throw decoded.error;
  if (decoded.status !== 0) throw new Error(`cannot verify ${entry} CMS signature: ${commandFailure(decoded)}`);
  return parsePlist(decoded.stdout, PROFILE_READER, `${entry} CMS payload`);
}

const THIN_MACHO_FORMATS = new Map([
  ["feedface", { littleEndian: false, is64: false }],
  ["cefaedfe", { littleEndian: true, is64: false }],
  ["feedfacf", { littleEndian: false, is64: true }],
  ["cffaedfe", { littleEndian: true, is64: true }],
]);
const FAT_MACHO_FORMATS = new Map([
  ["cafebabe", { littleEndian: false, is64: false }],
  ["bebafeca", { littleEndian: true, is64: false }],
  ["cafebabf", { littleEndian: false, is64: true }],
  ["bfbafeca", { littleEndian: true, is64: true }],
]);

function readMachOU32(bytes, offset, littleEndian) {
  if (offset < 0 || offset + 4 > bytes.length) throw new Error("truncated Mach-O structure");
  return littleEndian ? bytes.readUInt32LE(offset) : bytes.readUInt32BE(offset);
}

function readMachOU64(bytes, offset, littleEndian) {
  if (offset < 0 || offset + 8 > bytes.length) throw new Error("truncated Mach-O structure");
  const value = littleEndian ? bytes.readBigUInt64LE(offset) : bytes.readBigUInt64BE(offset);
  if (value > BigInt(Number.MAX_SAFE_INTEGER)) throw new Error("Mach-O slice offset or size is too large");
  return Number(value);
}

function thinMachOPlatform(bytes, offset, size) {
  if (offset < 0 || offset + 4 > bytes.length) throw new Error("truncated Mach-O header");
  const format = THIN_MACHO_FORMATS.get(bytes.toString("hex", offset, offset + 4));
  if (!format) throw new Error("unknown Mach-O magic");
  const headerSize = format.is64 ? 32 : 28;
  if (size < headerSize || offset + headerSize > bytes.length) throw new Error("truncated Mach-O header");
  const commandCount = readMachOU32(bytes, offset + 16, format.littleEndian);
  const commandsSize = readMachOU32(bytes, offset + 20, format.littleEndian);
  const commandsStart = offset + headerSize;
  const commandsEnd = commandsStart + commandsSize;
  if (commandCount > Math.floor(commandsSize / 8) || commandsEnd > offset + size || commandsEnd > bytes.length) {
    throw new Error("invalid Mach-O load command region");
  }
  let platform = null;
  let cursor = commandsStart;
  for (let index = 0; index < commandCount; index += 1) {
    const command = readMachOU32(bytes, cursor, format.littleEndian);
    const commandSize = readMachOU32(bytes, cursor + 4, format.littleEndian);
    if (commandSize < 8 || cursor + commandSize > commandsEnd) throw new Error("invalid Mach-O load command size");
    if (command === 0x32) {
      if (commandSize < 24) throw new Error("truncated LC_BUILD_VERSION command");
      const candidate = readMachOU32(bytes, cursor + 8, format.littleEndian);
      if (platform !== null && platform !== candidate) throw new Error("conflicting LC_BUILD_VERSION platforms");
      platform = candidate;
    }
    cursor += commandSize;
  }
  if (platform === null) throw new Error("Mach-O has no LC_BUILD_VERSION command");
  return platform;
}

function machOBuildPlatforms(bytes) {
  if (bytes.length < 4) throw new Error("truncated Mach-O magic");
  const fat = FAT_MACHO_FORMATS.get(bytes.toString("hex", 0, 4));
  if (!fat) return [thinMachOPlatform(bytes, 0, bytes.length)];
  const architectureCount = readMachOU32(bytes, 4, fat.littleEndian);
  const architectureSize = fat.is64 ? 32 : 20;
  const tableEnd = 8 + architectureCount * architectureSize;
  if (architectureCount < 1 || tableEnd > bytes.length) throw new Error("invalid fat Mach-O architecture table");
  const platforms = [];
  for (let index = 0; index < architectureCount; index += 1) {
    const architecture = 8 + index * architectureSize;
    const offset = fat.is64 ? readMachOU64(bytes, architecture + 8, fat.littleEndian) : readMachOU32(bytes, architecture + 8, fat.littleEndian);
    const size = fat.is64 ? readMachOU64(bytes, architecture + 16, fat.littleEndian) : readMachOU32(bytes, architecture + 12, fat.littleEndian);
    if (offset < tableEnd || size < 4 || offset + size > bytes.length) throw new Error("invalid fat Mach-O slice bounds");
    try {
      platforms.push(thinMachOPlatform(bytes, offset, size));
    } catch (error) {
      throw new Error(`invalid Mach-O slice ${index}: ${error.message}`);
    }
  }
  return platforms;
}

function iosExecutablePlatforms(file) {
  return machOBuildPlatforms(zipEntry(file, "Payload/nexterm.app/nexterm", MAX_EXECUTABLE_BYTES));
}

function executableFile(candidate) {
  try {
    return fs.statSync(candidate).isFile() && (fs.accessSync(candidate, fs.constants.X_OK), true);
  } catch {
    return false;
  }
}

function pathTool(name) {
  for (const directory of (process.env.PATH || "").split(path.delimiter)) {
    if (!directory) continue;
    const candidate = path.join(directory, name);
    if (executableFile(candidate)) return candidate;
  }
  return null;
}

function androidTool(name) {
  const onPath = pathTool(name);
  if (onPath) return onPath;
  const roots = [...new Set([process.env.ANDROID_HOME, process.env.ANDROID_SDK_ROOT].filter(Boolean))];
  for (const root of roots) {
    const buildTools = path.join(root, "build-tools");
    if (!fs.existsSync(buildTools) || !fs.statSync(buildTools).isDirectory()) continue;
    const versions = fs.readdirSync(buildTools).sort((left, right) => right.localeCompare(left, undefined, { numeric: true }));
    for (const installed of versions) {
      const candidate = path.join(buildTools, installed, name);
      if (executableFile(candidate)) return candidate;
    }
  }
  return null;
}

function commandFailure(result) {
  if (result.error) return String(result.error);
  const output = `${result.stderr || ""}\n${result.stdout || ""}`.trim();
  return output || `tool exited with status ${result.status ?? result.signal}`;
}

function expectedAndroidVersionCode(value) {
  const parsed = parseReleaseVersion(value);
  if (!parsed) throw new Error(`NEXTERM_RELEASE_VERSION is not a non-placeholder semantic version: ${value}`);
  const major = Number(parsed.major);
  const minor = Number(parsed.minor);
  const patch = Number(parsed.patch);
  if (minor > 999 || patch > 999) throw new Error("Android versionCode requires minor and patch versions no greater than 999");
  const code = major * 1000000 + minor * 1000 + patch;
  if (!Number.isSafeInteger(code) || code < 1 || code > 2100000000) {
    throw new Error(`Android versionCode ${code} is outside the supported range 1..2100000000`);
  }
  return code;
}

function validIOSBuildNumber(value) {
  if (!/^\d+(?:\.\d+){0,2}$/.test(value)) return false;
  const parts = value.split(".");
  return Number(parts[0]) > 0 && parts.every((part) => Number.isSafeInteger(Number(part)));
}

function verifyAndroidSignature(file, assertions) {
  const tool = androidTool("apksigner");
  if (!tool) {
    assertions.push({
      id: "android-signature",
      status: "skipped",
      detail: "apksigner was not found on PATH or in Android SDK build-tools; APK signature and pinned certificate were not verified",
    });
    return;
  }
  const pinned = (process.env.NEXTERM_ANDROID_CERT_SHA256 || "").replace(/:/g, "").trim().toLowerCase();
  if (!/^[0-9a-f]{64}$/.test(pinned)) {
    assertions.push({ id: "android-signature", status: "failed", detail: "NEXTERM_ANDROID_CERT_SHA256 must contain one SHA-256 hex digest" });
    return;
  }
  const result = runTool(tool, ["verify", "--print-certs", file], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
  if (result.error || result.status !== 0) {
    assertions.push({ id: "android-signature", status: "failed", detail: `apksigner verify failed: ${commandFailure(result)}` });
    return;
  }
  const output = `${result.stdout}\n${result.stderr}`;
  const digests = [...output.matchAll(/certificate\s+SHA-256\s+digest:\s*([0-9a-f]{64})/gi)].map((match) => match[1].toLowerCase());
  const passed = digests.includes(pinned);
  assertions.push({
    id: "android-signature",
    status: passed ? "passed" : "failed",
    detail: passed
      ? `apksigner verified the APK and signer certificate SHA-256 ${pinned}`
      : `pinned signer certificate SHA-256 ${pinned} did not match apksigner output: ${digests.join(", ") || "no certificate digest found"}`,
  });
}

function verifyAndroidVersion(file, assertions) {
  const tool = androidTool("aapt2");
  if (!tool) {
    assertions.push({
      id: "android-version",
      status: "skipped",
      detail: "aapt2 was not found on PATH or in Android SDK build-tools; APK versionName and versionCode were not verified",
    });
    return;
  }
  let expectedCode;
  try {
    expectedCode = expectedAndroidVersionCode(version);
  } catch (error) {
    assertions.push({ id: "android-version", status: "failed", detail: String(error) });
    return;
  }
  const result = runTool(tool, ["dump", "badging", file], { encoding: "utf8", maxBuffer: 16 * 1024 * 1024 });
  if (result.error || result.status !== 0) {
    assertions.push({ id: "android-version", status: "failed", detail: `aapt2 dump badging failed: ${commandFailure(result)}` });
    return;
  }
  const packageLine = result.stdout.split(/\r?\n/).find((line) => line.startsWith("package: ")) || "";
  const actualName = /\bversionName='([^']*)'/.exec(packageLine)?.[1];
  const actualCode = /\bversionCode='(\d+)'/.exec(packageLine)?.[1];
  const passed = actualName === version && actualCode === String(expectedCode);
  assertions.push({
    id: "android-version",
    status: passed ? "passed" : "failed",
    detail: `versionName=${actualName ?? "missing"}, versionCode=${actualCode ?? "missing"}; expected versionName=${version}, versionCode=${expectedCode}`,
  });
}

async function fileSha256(file) {
  const hash = crypto.createHash("sha256");
  for await (const chunk of fs.createReadStream(file, { highWaterMark: MIB })) {
    hash.update(chunk);
  }
  return hash.digest("hex");
}

const bytes = fs.statSync(file).size;
if (bytes > MAX_PACKAGE_BYTES) {
  console.error(`mobile package exceeds the ${MAX_PACKAGE_BYTES} byte limit: ${file}`);
  process.exit(1);
}
const format = id === "mobile-ios" ? "ipa" : "apk";
const assertions = [{ id: "package", status: "passed", detail: `${format} package is present, readable and within the ${MAX_PACKAGE_BYTES} byte limit` }];
try {
  zipManifest = inspectZip(file);
  assertions.push({
    id: "zip-structure",
    status: "passed",
    detail: `central directory contains ${zipManifest.entryCount} unique safe entries totaling ${zipManifest.totalUncompressedBytes} uncompressed bytes`,
  });
} catch (error) {
  assertions.push({ id: "zip-structure", status: "failed", detail: String(error) });
}
const sha256 = await fileSha256(file);
if (zipManifest && id === "mobile-ios") {
  let platformName = "unavailable";
  try {
    const metadata = iosMetadata(file);
    platformName = metadata.platformName;
    const releaseVersion = parseReleaseVersion(version);
    const expectedShortVersion = releaseVersion?.core || "invalid NEXTERM_RELEASE_VERSION";
    const expectedBuildNumber = process.env.IOS_BUILD_NUMBER || "";
    const buildNumberPassed = expectedBuildNumber
      ? validIOSBuildNumber(expectedBuildNumber) && metadata.bundleVersion === expectedBuildNumber
      : validIOSBuildNumber(metadata.bundleVersion);
    const versionPassed = Boolean(releaseVersion) && buildNumberPassed && metadata.shortVersion === expectedShortVersion;
    assertions.push({
      id: "ios-version",
      status: versionPassed ? "passed" : "failed",
      detail: `CFBundleShortVersionString=${metadata.shortVersion}, CFBundleVersion=${metadata.bundleVersion}; expected CFBundleShortVersionString=${expectedShortVersion}, CFBundleVersion=${expectedBuildNumber || "a valid numeric build number (IOS_BUILD_NUMBER not supplied)"}`,
    });
  } catch (error) {
    assertions.push({ id: "ios-version", status: "failed", detail: String(error) });
  }
  try {
    const platforms = iosExecutablePlatforms(file);
    const platformPassed = platformName === "iphoneos" && platforms.length > 0 && platforms.every((platform) => platform === 2);
    assertions.push({
      id: "ios-platform",
      status: platformPassed ? "passed" : "failed",
      detail: `DTPlatformName=${platformName}; Mach-O LC_BUILD_VERSION platforms=${platforms.join(",")}; expected DTPlatformName=iphoneos and every slice platform=2 (iOS device)`,
    });
  } catch (error) {
    assertions.push({ id: "ios-platform", status: "failed", detail: String(error) });
  }
  try {
    const profile = iosProvisioningProfile(file);
    const team = profile.teamIdentifiers[0] || "";
    const expectedIdentifier = team ? `${team}.work.nexterm.app` : "a team-qualified work.nexterm.app";
    const profilePassed = Boolean(team) && profile.applicationIdentifier === expectedIdentifier && profile.getTaskAllow === false && !profile.hasProvisionedDevices && !profile.hasProvisionsAllDevices;
    assertions.push({
      id: "ios-provisioning-profile",
      status: profilePassed ? "passed" : "failed",
      detail: `application-identifier=${profile.applicationIdentifier}; expected ${expectedIdentifier}; get-task-allow=${profile.getTaskAllow}; ProvisionedDevices present=${profile.hasProvisionedDevices}; ProvisionsAllDevices present=${profile.hasProvisionsAllDevices}; CMS signature verified with the embedded signer (certificate trust not checked)`,
    });
  } catch (error) {
    assertions.push({ id: "ios-provisioning-profile", status: "failed", detail: String(error) });
  }
  try {
    const mode = iosExecutableMode();
    assertions.push({
      id: "ios-executable-mode",
      status: mode === 0o755 ? "passed" : "failed",
      detail: `Payload/nexterm.app/nexterm mode=${mode.toString(8).padStart(4, "0")}; expected 0755`,
    });
  } catch (error) {
    assertions.push({ id: "ios-executable-mode", status: "failed", detail: String(error) });
  }
} else if (zipManifest) {
  verifyAndroidSignature(file, assertions);
  verifyAndroidVersion(file, assertions);
}
const report = {
  schema_version: 2,
  id,
  kind: "mobile",
  platform: { os: id === "mobile-ios" ? "ios" : "android", arch: "arm64", cgo: "enabled-only-for-wails-platform-layer" },
  version,
  commit: process.env.GITHUB_SHA || "unknown",
  source_date_epoch: Number(process.env.SOURCE_DATE_EPOCH || 0),
  artifact: { path: path.relative(ROOT, file), size_bytes: bytes, sha256, stripped: false },
  format: { format, architecture: "arm64", static: false },
  assertions,
  status: assertions.some((item) => item.status === "failed") ? "failed" : "passed",
};
fs.writeFileSync(`${file}.artifact.json`, `${JSON.stringify(report, null, 2)}\n`);
console.warn(`${id}: ${bytes} bytes; report=${file}.artifact.json`);
for (const assertion of assertions.filter((item) => item.status === "skipped")) {
  console.warn(`${id}: ${assertion.id}: skipped: ${assertion.detail}`);
}
for (const assertion of assertions.filter((item) => item.status === "failed")) {
  console.error(`${id}: ${assertion.id}: ${assertion.detail}`);
}
if (report.status === "failed") process.exit(1);
