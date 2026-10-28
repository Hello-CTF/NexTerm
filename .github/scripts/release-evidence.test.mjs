#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { releaseArtifacts } from "./release-artifacts.mjs";
import { verifyReleaseEvidence } from "./release-evidence.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github", "scripts", "release-evidence.mjs");
const VERSION = "1.2.3";
const HEAD = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();

function makeCandidate(t, version = VERSION) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-release-evidence-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const [name, id] of releaseArtifacts(version)) {
    const bytes = Buffer.from(`stub ${name}\n`);
    const file = path.join(directory, name);
    fs.writeFileSync(file, bytes);
    const report = {
      id,
      version,
      commit: HEAD,
      status: "passed",
      artifact: { size_bytes: bytes.length, sha256: crypto.createHash("sha256").update(bytes).digest("hex") },
      assertions: [{ id: "package", status: "passed" }, { id: "privacy-runtime", status: "passed" }],
    };
    fs.writeFileSync(`${file}.artifact.json`, `${JSON.stringify(report, null, 2)}\n`);
  }
  return directory;
}

function editReport(directory, name, edit) {
  const reportPath = path.join(directory, `${name}.artifact.json`);
  const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
  edit(report);
  fs.writeFileSync(reportPath, JSON.stringify(report));
}

function writeIntermediateReport(directory, name, edit) {
  const report = {
    schema_version: 2,
    id: "desktop-windows-amd64",
    kind: "desktop",
    version: VERSION,
    commit: HEAD,
    status: "passed",
    artifact: { path: "target/go-build/nexterm-desktop-windows-amd64.exe", size_bytes: 123, sha256: "a".repeat(64) },
    assertions: [{ id: "binary-format", status: "passed" }, { id: "privacy-runtime", status: "passed" }],
  };
  edit?.(report);
  const file = path.join(directory, name);
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, JSON.stringify(report));
}

test("accepts valid intermediate reports without adding them to SHA256SUMS", (t) => {
  const directory = makeCandidate(t);
  writeIntermediateReport(directory, "nexterm-desktop-windows-amd64.artifact.json");
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, true, JSON.stringify(result.issues));
  assert.deepEqual(result.intermediateReports, [{ name: "nexterm-desktop-windows-amd64.artifact.json", status: "passed" }]);
  const checksums = fs.readFileSync(path.join(directory, "SHA256SUMS"), "utf8");
  assert.equal(checksums.trim().split("\n").length, releaseArtifacts(VERSION).size);
  assert.ok(!checksums.includes("nexterm-desktop-windows-amd64.artifact.json"));
});

test("rejects a corrupt intermediate report", (t) => {
  const directory = makeCandidate(t);
  fs.writeFileSync(path.join(directory, "broken.artifact.json"), "{not json\n");
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.ok(result.issues.some((issue) => issue.name === "broken.artifact.json" && issue.status === "corrupt-report"));
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

for (const [status, edit] of [
  ["wrong-version", (report) => { report.version = "1.2.4"; }],
  ["wrong-commit", (report) => { report.commit = "0".repeat(40); }],
  ["gates", (report) => { report.status = "failed"; }],
]) {
  test(`rejects an intermediate report with ${status}`, (t) => {
    const directory = makeCandidate(t);
    writeIntermediateReport(directory, "raw.artifact.json", edit);
    const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
    assert.equal(result.passed, false);
    assert.ok(result.issues.some((issue) => issue.name === "raw.artifact.json" && issue.status === status));
    assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
  });
}

test("rejects duplicate report IDs from nested intermediate inputs", (t) => {
  const directory = makeCandidate(t);
  writeIntermediateReport(directory, "raw.artifact.json");
  writeIntermediateReport(directory, "nested/raw.artifact.json");
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.ok(result.issues.some((issue) => issue.status === "duplicate" && issue.detail.includes("desktop-windows-amd64")));
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

function runCLI(directory, ...args) {
  const version = args.length === 0 ? VERSION : args[0];
  const env = { ...process.env };
  if (version === undefined) delete env.NEXTERM_RELEASE_VERSION;
  else env.NEXTERM_RELEASE_VERSION = version;
  return spawnSync(process.execPath, [SCRIPT, directory], { cwd: ROOT, env, encoding: "utf8" });
}

function withVersion(t, version, run) {
  const previous = process.env.NEXTERM_RELEASE_VERSION;
  process.env.NEXTERM_RELEASE_VERSION = version;
  t.after(() => {
    if (previous === undefined) delete process.env.NEXTERM_RELEASE_VERSION;
    else process.env.NEXTERM_RELEASE_VERSION = previous;
  });
  return run();
}

test("accepts only a complete matching artifact set and writes SHA256SUMS", (t) => {
  const directory = makeCandidate(t);
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, true, JSON.stringify(result.issues));
  assert.equal(result.artifacts.length, releaseArtifacts(VERSION).size);
  assert.ok(result.artifacts.every((item) => item.status === "passed" && item.report_commit === HEAD));
  const lines = fs.readFileSync(path.join(directory, "SHA256SUMS"), "utf8").trim().split("\n");
  assert.equal(lines.length, releaseArtifacts(VERSION).size);
  assert.equal(runCLI(directory).status, 0);
});

test("accepts combined prerelease and build metadata in a complete release candidate", (t) => {
  const version = "1.2.3-rc.1+build.5";
  const directory = makeCandidate(t, version);
  const result = withVersion(t, version, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, true, JSON.stringify(result.issues));
  assert.equal(result.artifacts.length, releaseArtifacts(version).size);
  assert.equal(runCLI(directory, version).status, 0);
});

test("missing artifacts fail without writing SHA256SUMS", (t) => {
  const directory = makeCandidate(t);
  const [name] = releaseArtifacts(VERSION).keys();
  fs.rmSync(path.join(directory, name));
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.equal(result.artifacts.find((item) => item.name === name).status, "not-produced");
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

test("wrong report versions fail without writing SHA256SUMS", (t) => {
  const directory = makeCandidate(t);
  const [name] = releaseArtifacts(VERSION).keys();
  editReport(directory, name, (report) => { report.version = "0.0.0"; });
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.ok(result.artifacts.find((item) => item.name === name).failures.includes("wrong-version"));
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

for (const commit of ["0000000000000000000000000000000000000000", "unknown"]) {
  test(`wrong report commit ${commit === "unknown" ? "unknown" : "mismatch"} is rejected`, (t) => {
    const directory = makeCandidate(t);
    const [name] = releaseArtifacts(VERSION).keys();
    editReport(directory, name, (report) => { report.commit = commit; });
    const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
    assert.equal(result.passed, false);
    const expectedFailure = commit === "unknown" ? "unknown-commit" : "wrong-commit";
    assert.ok(result.artifacts.find((item) => item.name === name).failures.includes(expectedFailure));
    assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
  });
}

test("duplicate expected basenames in nested directories are rejected", (t) => {
  const directory = makeCandidate(t);
  const [name] = releaseArtifacts(VERSION).keys();
  fs.mkdirSync(path.join(directory, "duplicate"));
  fs.copyFileSync(path.join(directory, name), path.join(directory, "duplicate", name));
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.ok(result.issues.some((issue) => issue.status === "duplicate" && issue.name === path.join("duplicate", name)));
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

test("any unexpected file, including an AAB, is rejected", (t) => {
  const directory = makeCandidate(t);
  fs.writeFileSync(path.join(directory, `NexTerm_${VERSION}_android.aab`), "unexpected\n");
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.ok(result.issues.some((issue) => issue.status === "unexpected" && issue.name.endsWith(".aab")));
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

test("corrupt reports are reported per artifact without aborting the summary", (t) => {
  const directory = makeCandidate(t);
  const names = [...releaseArtifacts(VERSION).keys()].slice(0, 2);
  for (const name of names) fs.writeFileSync(path.join(directory, `${name}.artifact.json`), "{not json\n");
  const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
  assert.equal(result.passed, false);
  assert.deepEqual(result.artifacts.filter((item) => item.status === "corrupt-report").map((item) => item.name), names);
  const cli = runCLI(directory);
  assert.equal(cli.status, 1);
  for (const name of names) assert.match(cli.stderr, new RegExp(`${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}: corrupt-report`));
  assert.match(cli.stderr, /release evidence: failed/);
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});

for (const version of ["", "0.0.0", "1.2", "v1.2.3", "1.2.3-rc..1", "1.2.3+build..5", "1.2.3-01", "1.2.3-rc_1"]) {
  test(`invalid release version ${JSON.stringify(version)} fails closed`, (t) => {
    const directory = makeCandidate(t);
    const result = withVersion(t, version, () => verifyReleaseEvidence(directory));
    assert.equal(result.passed, false);
    assert.ok(result.issues.some((issue) => issue.status === "invalid-version"));
    assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
  });
}

test("the CLI requires NEXTERM_RELEASE_VERSION instead of reading the placeholder", (t) => {
  const directory = makeCandidate(t);
  const result = runCLI(directory, undefined);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /invalid-version/);
  assert.equal(fs.existsSync(path.join(directory, "SHA256SUMS")), false);
});


for (const suffix of [".dmg", ".apk"]) {
  test(`rejects ${suffix} packages without privacy runtime verification`, (t) => {
    const directory = makeCandidate(t);
    const name = [...releaseArtifacts(VERSION).keys()].find((name) => name.endsWith(suffix));
    editReport(directory, name, (report) => { report.assertions = report.assertions.filter((item) => item.id !== "privacy-runtime"); });
    const result = withVersion(t, VERSION, () => verifyReleaseEvidence(directory));
    assert.equal(result.passed, false);
    assert.ok(result.artifacts.find((artifact) => artifact.name === name).failures.includes("privacy-runtime"));
  });
}
