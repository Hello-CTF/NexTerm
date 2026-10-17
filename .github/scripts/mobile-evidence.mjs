#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const [fileArg, id] = process.argv.slice(2);
if (!fileArg || !id) {
  console.error("usage: node .github/scripts/mobile-evidence.mjs <package> <report-id>");
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
const PLIST_READER = `import json, plistlib, sys
document = plistlib.loads(sys.stdin.buffer.read())
result = {}
for key in ("CFBundleShortVersionString", "CFBundleVersion"):
    value = document.get(key)
    if not isinstance(value, str):
        raise ValueError(f"{key} is not a string")
    result[key] = value
json.dump(result, sys.stdout)`;

function iosVersions(file) {
  const entry = "Payload/nexterm.app/Info.plist";
  const extracted = spawnSync("unzip", ["-p", file, entry], { maxBuffer: 16 * 1024 * 1024 });
  if (extracted.error) throw extracted.error;
  if (extracted.status !== 0) throw new Error(`cannot read ${entry}: ${extracted.stderr.toString("utf8").trim()}`);
  const parsed = spawnSync("python3", ["-c", PLIST_READER], {
    input: extracted.stdout,
    encoding: "utf8",
    maxBuffer: 16 * 1024 * 1024,
  });
  if (parsed.error) throw parsed.error;
  if (parsed.status !== 0) throw new Error(`cannot parse ${entry}: ${parsed.stderr.trim()}`);
  const plist = JSON.parse(parsed.stdout);
  return {
    shortVersion: plist.CFBundleShortVersionString,
    bundleVersion: plist.CFBundleVersion,
  };
}

const bytes = fs.statSync(file).size;
const sha256 = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
const format = id === "mobile-ios" ? "ipa" : "apk";
const assertions = [{ id: "package", status: "passed", detail: `${format} package is present and readable` }];
if (id === "mobile-ios") {
  try {
    const versions = iosVersions(file);
    const passed = versions.shortVersion === version && versions.bundleVersion === version;
    assertions.push({
      id: "ios-version",
      status: passed ? "passed" : "failed",
      detail: `CFBundleShortVersionString=${versions.shortVersion}, CFBundleVersion=${versions.bundleVersion}; expected ${version}`,
    });
  } catch (error) {
    assertions.push({ id: "ios-version", status: "failed", detail: String(error) });
  }
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
  status: assertions.every((item) => item.status === "passed") ? "passed" : "failed",
};
fs.writeFileSync(`${file}.artifact.json`, `${JSON.stringify(report, null, 2)}\n`);
console.warn(`${id}: ${bytes} bytes; report=${file}.artifact.json`);
for (const assertion of assertions.filter((item) => item.status !== "passed")) {
  console.error(`${id}: ${assertion.id}: ${assertion.detail}`);
}
if (report.status === "failed") process.exit(1);
