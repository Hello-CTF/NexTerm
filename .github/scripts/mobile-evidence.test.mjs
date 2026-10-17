#!/usr/bin/env node
import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github", "scripts", "mobile-evidence.mjs");
const RELEASE_SCRIPT = path.join(ROOT, ".github", "scripts", "release-evidence.mjs");
const info = fs.readFileSync(path.join(ROOT, "build", "ios", "Info.plist"), "utf8");

function setPlistString(source, key, value) {
  return source.replace(new RegExp(`(<key>${key}</key>\\s*<string>)[^<]*`), (_match, prefix) => `${prefix}${value}`);
}

function plistWithVersions(shortVersion, bundleVersion) {
  let plist = setPlistString(info, "CFBundleShortVersionString", shortVersion);
  plist = setPlistString(plist, "CFBundleVersion", bundleVersion);
  return setPlistString(plist, "CFBundleGetInfoString", "1.1.0");
}

function makePlistIPA(t, plist) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-ios-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  fs.mkdirSync(path.join(directory, "Payload", "nexterm.app"), { recursive: true });
  fs.writeFileSync(path.join(directory, "Payload", "nexterm.app", "Info.plist"), plist);
  const ipa = path.join(directory, "test.ipa");
  execFileSync("zip", ["-qry", ipa, "Payload"], { cwd: directory });
  return ipa;
}

function makeIPA(t, shortVersion, bundleVersion) {
  return makePlistIPA(t, plistWithVersions(shortVersion, bundleVersion));
}

function makeBinaryIPA(t, shortVersion, bundleVersion) {
  const plist = execFileSync("python3", ["-c", "import plistlib,sys; sys.stdout.buffer.write(plistlib.dumps(plistlib.loads(sys.stdin.buffer.read()), fmt=plistlib.FMT_BINARY))"], {
    input: plistWithVersions(shortVersion, bundleVersion),
  });
  return makePlistIPA(t, plist);
}

function runMobileEvidence(ipa) {
  return spawnSync(process.execPath, [SCRIPT, ipa, "mobile-ios"], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: "1.1.0" },
    encoding: "utf8",
  });
}

const finalArtifacts = [
  ["NexTerm_1.1.0_x64-setup.exe", "desktop-nsis-windows-amd64"],
  ["NexTerm_1.1.0_arm64-setup.exe", "desktop-nsis-windows-arm64"],
  ["NexTerm_1.1.0_aarch64.dmg", "desktop-dmg-darwin-arm64"],
  ["NexTerm_1.1.0_x86_64.dmg", "desktop-dmg-darwin-amd64"],
  ["NexTerm_1.1.0_android.apk", "mobile-android"],
  ["NexTerm_1.1.0_ios.ipa", "mobile-ios"],
  ["NexTerm-agent_1.1.0_windows_amd64.zip", "agent-zip-windows-amd64"],
  ...["amd64", "arm64"].flatMap((arch) => [
    [`NexTerm-desktop_1.1.0_linux_${arch}.deb`, `desktop-linux-deb-linux-${arch}`],
    [`NexTerm-server_1.1.0_linux_${arch}.tar.gz`, `server-archive-linux-${arch}`],
    [`NexTerm-agent_1.1.0_linux_${arch}.tar.gz`, `agent-archive-linux-${arch}`],
    [`NexTerm-agent_1.1.0_darwin_${arch}.tar.gz`, `agent-archive-darwin-${arch}`],
  ]),
];

function makeCandidate(t, ipa) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-release-evidence-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const [name, id] of finalArtifacts) {
    const file = path.join(directory, name);
    if (id === "mobile-ios") {
      fs.copyFileSync(ipa, file);
      fs.copyFileSync(`${ipa}.artifact.json`, `${file}.artifact.json`);
      continue;
    }
    const bytes = Buffer.from(`stub ${name}\n`);
    fs.writeFileSync(file, bytes);
    const report = {
      id,
      version: "1.1.0",
      status: "passed",
      artifact: { size_bytes: bytes.length, sha256: crypto.createHash("sha256").update(bytes).digest("hex") },
      assertions: [{ id: "package", status: "passed" }],
    };
    fs.writeFileSync(`${file}.artifact.json`, JSON.stringify(report));
  }
  return directory;
}

function runReleaseEvidence(directory) {
  return spawnSync(process.execPath, [RELEASE_SCRIPT, directory], {
    cwd: ROOT,
    env: { ...process.env, NEXTERM_RELEASE_VERSION: "1.1.0" },
    encoding: "utf8",
  });
}

test("final release evidence accepts matching and rejects wrong IPA plist versions", (t) => {
  const matching = makeIPA(t, "1.1.0", "1.1.0");
  assert.equal(runMobileEvidence(matching).status, 0);
  const accepted = runReleaseEvidence(makeCandidate(t, matching));
  assert.equal(accepted.status, 0, accepted.stderr);

  const wrong = makeIPA(t, "0.1.0", "0.1.0");
  assert.equal(runMobileEvidence(wrong).status, 1);
  const rejected = runReleaseEvidence(makeCandidate(t, wrong));
  assert.equal(rejected.status, 1);
  assert.match(rejected.stderr, /release evidence: failed/);
});

test("accepts an IPA when both plist versions match the release", (t) => {
  const ipa = makeIPA(t, "1.1.0", "1.1.0");
  const result = runMobileEvidence(ipa);
  assert.equal(result.status, 0, result.stderr);
  const report = JSON.parse(fs.readFileSync(`${ipa}.artifact.json`, "utf8"));
  assert.equal(report.status, "passed");
  assert.ok(report.assertions.some((item) => item.id === "ios-version" && item.status === "passed"));
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
