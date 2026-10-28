#!/usr/bin/env node
import { releaseArtifacts } from "./release-artifacts.mjs";
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseReleaseVersion } from "../../scripts/lib/semver.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

function gatesPassed(report) {
  return report !== null && typeof report === "object"
    && report.status === "passed"
    && Array.isArray(report.assertions)
    && report.assertions.length > 0
    && report.assertions.every((item) => item !== null && typeof item === "object" && item.status === "passed");
}

function privacyRuntimePassed(report) {
  return !/^(?:desktop-|server-|mobile-)/.test(report?.id || "")
    || report.assertions?.some((item) => item.id === "privacy-runtime" && item.status === "passed");
}

function candidateFiles(directory) {
  const files = [];
  function visit(parent, relativeParent = "") {
    for (const entry of fs.readdirSync(parent, { withFileTypes: true })) {
      const relative = path.join(relativeParent, entry.name);
      const absolute = path.join(parent, entry.name);
      if (entry.isDirectory()) visit(absolute, relative);
      else files.push(relative);
    }
  }
  visit(directory);
  return files;
}

function recordReportID(seenReportIDs, id, name) {
  if (typeof id !== "string" || id === "") return;
  const names = seenReportIDs.get(id) || [];
  names.push(name);
  seenReportIDs.set(id, names);
}

function validateIntermediateReport(result, name, file, seenReportIDs) {
  const issue = (status, detail) => result.issues.push({ name, status, detail });
  let report;
  try {
    report = JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (error) {
    issue("corrupt-report", `cannot parse intermediate report ${name}: ${error.message}`);
    result.intermediateReports.push({ name, status: "failed" });
    return;
  }
  const initialIssueCount = result.issues.length;
  if (!privacyRuntimePassed(report)) issue("privacy-runtime", "desktop/server/mobile evidence must verify the packaged ONNX Runtime privacy runtime");
  if (!gatesPassed(report)) issue("gates", "intermediate artifact report must have a passed status and non-empty passing assertions");
  if (report?.version !== result.version) issue("wrong-version", `intermediate report version ${JSON.stringify(report?.version)} does not match ${result.version}`);
  if (report?.commit !== result.commit) {
    issue(report?.commit === "unknown" ? "unknown-commit" : "wrong-commit", `intermediate report commit ${JSON.stringify(report?.commit)} does not match ${result.commit}`);
  }
  if (typeof report?.id !== "string" || report.id === "") issue("wrong-report-id", "intermediate artifact report requires a non-empty id");
  const metadataPassed = Number.isInteger(report?.artifact?.size_bytes) && report.artifact.size_bytes >= 0
    && /^[0-9a-f]{64}$/.test(report.artifact?.sha256 || "");
  if (!metadataPassed) issue("artifact-metadata", "intermediate artifact report requires artifact size_bytes and a SHA-256 digest");
  recordReportID(seenReportIDs, report?.id, name);
  result.intermediateReports.push({ name, status: result.issues.length === initialIssueCount ? "passed" : "failed" });
}

export function verifyReleaseEvidence(directory) {
  const candidate = path.resolve(directory);
  const version = process.env.NEXTERM_RELEASE_VERSION || "";
  const result = {
    directory: candidate,
    version,
    commit: "",
    artifacts: [],
    intermediateReports: [],
    issues: [],
    passed: false,
    checksumFile: null,
  };
  if (!parseReleaseVersion(version)) {
    result.issues.push({ name: "NEXTERM_RELEASE_VERSION", status: "invalid-version", detail: `expected a non-0.0.0 semver, got ${JSON.stringify(version)}` });
    return result;
  }
  if (!fs.existsSync(candidate) || !fs.statSync(candidate).isDirectory()) {
    result.issues.push({ name: candidate, status: "missing-candidate-directory", detail: "candidate directory does not exist" });
    return result;
  }
  try {
    result.commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
  } catch (error) {
    result.issues.push({ name: "HEAD", status: "commit-unavailable", detail: error.message });
    return result;
  }
  if (!result.commit || result.commit === "unknown") {
    result.issues.push({ name: "HEAD", status: "commit-unavailable", detail: "git rev-parse HEAD returned no usable commit" });
    return result;
  }

  const reportIDs = releaseArtifacts(version);
  const expectedArtifacts = [...reportIDs.keys()];
  const expectedFiles = new Set(expectedArtifacts.flatMap((name) => [name, `${name}.artifact.json`]));
  let files;
  try {
    files = candidateFiles(candidate);
  } catch (error) {
    result.issues.push({ name: candidate, status: "unreadable-candidate", detail: error.message });
    return result;
  }
  const seenReportIDs = new Map();
  for (const name of files) {
    if (expectedFiles.has(name) || name === "SHA256SUMS") continue;
    const duplicate = expectedFiles.has(path.basename(name));
    if (duplicate) {
      result.issues.push({ name, status: "duplicate", detail: "another file with an expected artifact basename is present" });
      continue;
    }
    if (name.endsWith(".artifact.json")) {
      validateIntermediateReport(result, name, path.join(candidate, name), seenReportIDs);
      continue;
    }
    result.issues.push({ name, status: "unexpected", detail: "file is not part of the expected release set" });
  }

  for (const name of expectedArtifacts) {
    const file = path.join(candidate, name);
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
      result.artifacts.push({ name, status: "not-produced" });
      continue;
    }
    const reportPath = `${file}.artifact.json`;
    if (!fs.existsSync(reportPath) || !fs.statSync(reportPath).isFile()) {
      result.artifacts.push({ name, status: "missing-artifact-report" });
      continue;
    }
    let report;
    try {
      report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
    } catch (error) {
      result.artifacts.push({ name, status: "corrupt-report", detail: `cannot parse ${name}.artifact.json: ${error.message}` });
      continue;
    }
    const bytes = fs.statSync(file).size;
    const sha256 = crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
    const intact = report?.artifact?.size_bytes === bytes && report?.artifact?.sha256 === sha256;
    const failures = [];
    if (!gatesPassed(report)) failures.push("gates");
    if (!privacyRuntimePassed(report)) failures.push("privacy-runtime");
    if (!intact) failures.push("integrity");
    if (report?.version !== version) failures.push("wrong-version");
    if (report?.id !== reportIDs.get(name)) failures.push("wrong-report-id");
    if (report?.commit !== result.commit) failures.push(report?.commit === "unknown" ? "unknown-commit" : "wrong-commit");
    const passed = failures.length === 0;
    const item = {
      name,
      size_bytes: bytes,
      sha256,
      report_status: report?.status,
      report_commit: report?.commit,
      intact,
      status: passed ? "passed" : "failed",
    };
    if (!passed) item.failures = failures;
    result.artifacts.push(item);
    recordReportID(seenReportIDs, report?.id, name);
  }
  for (const [id, names] of seenReportIDs) {
    if (names.length > 1) result.issues.push({ name: names.join(", "), status: "duplicate", detail: `duplicate artifact report id: ${id}` });
  }

  result.passed = result.issues.length === 0 && result.artifacts.length === expectedArtifacts.length
    && result.artifacts.every((item) => item.status === "passed");
  if (result.passed) {
    const checksumFile = path.join(candidate, "SHA256SUMS");
    fs.writeFileSync(checksumFile, `${result.artifacts.map((item) => `${item.sha256}  ${item.name}`).join("\n")}\n`);
    result.checksumFile = checksumFile;
  }
  return result;
}

function main() {
  const directory = path.resolve(ROOT, process.argv[2] || "candidate");
  const result = verifyReleaseEvidence(directory);
  for (const item of result.artifacts.filter((artifact) => artifact.status !== "passed")) {
    const detail = item.detail || (item.failures ? item.failures.join(", ") : "not validated");
    console.error(`release evidence: ${item.name}: ${item.status}: ${detail}`);
  }
  for (const issue of result.issues) console.error(`release evidence: ${issue.status}: ${issue.name}: ${issue.detail}`);
  const passed = result.artifacts.filter((item) => item.status === "passed").length;
  console.warn(`release evidence: ${result.passed ? "passed" : "failed"}; ${passed}/${releaseArtifacts(result.version).size} final artifacts passed`);
  if (!result.passed) process.exitCode = 1;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
