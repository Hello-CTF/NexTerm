#!/usr/bin/env node
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
const bytes = fs.statSync(file).size;
const sha256 = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
const format = id === "mobile-ios" ? "ipa" : "apk";
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
  assertions: [{ id: "package", status: "passed", detail: `${format} package is present and readable` }],
  status: "passed",
  feature_parity_claim: false,
  real_target_acceptance_claim: false,
};
fs.writeFileSync(`${file}.artifact.json`, `${JSON.stringify(report, null, 2)}\n`);
console.warn(`${id}: ${bytes} bytes; report=${file}.artifact.json`);
