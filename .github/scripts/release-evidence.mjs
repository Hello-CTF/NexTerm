#!/usr/bin/env node
import { releaseArtifacts } from "./release-artifacts.mjs";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const directory = path.resolve(ROOT, process.argv[2] || "candidate");
const version = process.env.NEXTERM_RELEASE_VERSION || JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info.version;
const reportIDs = releaseArtifacts(version);
const expected = [...reportIDs.keys()];
function gatesPassed(report) {
  return report.status === "passed"
    && Array.isArray(report.assertions)
    && report.assertions.length > 0
    && report.assertions.every((item) => item.status === "passed");
}

const artifacts = [];
let failed = false;
for (const name of expected) {
  const file = path.join(directory, name);
  if (!fs.existsSync(file)) {
    artifacts.push({ name, status: "not-produced" });
    failed = true;
    continue;
  }
  const reportPath = `${file}.artifact.json`;
  if (!fs.existsSync(reportPath)) {
    artifacts.push({ name, status: "missing-artifact-report" });
    failed = true;
    continue;
  }
  const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
  const bytes = fs.statSync(file).size;
  const sha256 = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
  const intact = report.artifact.size_bytes === bytes && report.artifact.sha256 === sha256;
  const passed = gatesPassed(report) && intact && report.version === version && report.id === reportIDs.get(name);
  if (!passed) failed = true;
  artifacts.push({ name, size_bytes: bytes, sha256, report_status: report.status, intact, status: passed ? "passed" : "failed" });
}
const finalReportNames = new Set(expected.map((name) => `${name}.artifact.json`));
for (const name of fs.readdirSync(directory)) {
  if (!name.endsWith(".artifact.json") || finalReportNames.has(name)) continue;
  const report = JSON.parse(fs.readFileSync(path.join(directory, name), "utf8"));
  if (!gatesPassed(report) || report.version !== version) failed = true;
}
const unexpected = fs.readdirSync(directory).filter((name) => /\.(?:exe|dmg|deb|tar\.gz|apk|ipa|zip)$/.test(name) && !expected.includes(name));
if (unexpected.length) failed = true;
fs.writeFileSync(path.join(directory, "SHA256SUMS"), `${artifacts.filter((item) => item.sha256).map((item) => `${item.sha256}  ${item.name}`).join("\n")}\n`);
console.warn(`release evidence: ${failed ? "failed" : "passed"}; ${artifacts.filter((item) => item.status === "passed").length}/${expected.length} final artifacts passed`);
if (failed) process.exit(1);
