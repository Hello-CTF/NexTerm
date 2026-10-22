import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  canExecuteHelperMetadata,
  helperArchiveMembers,
  helperTargets,
  missingHelperArchiveMembers,
  selectNativeMatrix,
} from "./helper-matrix.mjs";
import { requiresHelperExecutionEvidence } from "./helper-metadata.mjs";
import {
  binaryManifestProblems,
  createBinaryManifest,
  helperMetadataManifestProblems,
} from "../../scripts/lib/build-manifest.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SCRIPT = path.join(ROOT, ".github/scripts/helper-matrix.mjs");
const EXPECTED = [
  ["linux", "amd64", "amd64", "", "nexterm-helper-linux-amd64"],
  ["linux", "arm64", "arm64", "", "nexterm-helper-linux-arm64"],
  ["linux", "armv7", "arm", "7", "nexterm-helper-linux-armv7"],
  ["linux", "riscv64", "riscv64", "", "nexterm-helper-linux-riscv64"],
  ["linux", "loong64", "loong64", "", "nexterm-helper-linux-loong64"],
  ["darwin", "amd64", "amd64", "", "nexterm-helper-darwin-amd64"],
  ["darwin", "arm64", "arm64", "", "nexterm-helper-darwin-arm64"],
  ["windows", "amd64", "amd64", "", "nexterm-helper-windows-amd64.exe"],
  ["windows", "arm64", "arm64", "", "nexterm-helper-windows-arm64.exe"],
];

test("helper matrix contains exactly nine direct targets with Go architecture metadata", () => {
  assert.deepEqual(helperTargets().map(({ os, arch, goarch, goarm, name }) => [os, arch, goarch, goarm, name]), EXPECTED);
  assert.equal(new Set(helperTargets().map((target) => target.name)).size, 9);
});

test("ordinary PRs retain only the Linux amd64 helper while full validation selects all nine", () => {
  const ordinary = selectNativeMatrix({ isPR: true, fullPR: false }).filter((entry) => entry.kind === "helper");
  assert.deepEqual(ordinary.map(({ os, arch }) => [os, arch]), [["linux", "amd64"]]);
  const full = selectNativeMatrix({ isPR: true, fullPR: true }).filter((entry) => entry.kind === "helper");
  assert.equal(full.length, 9);
  assert.deepEqual(selectNativeMatrix({ isPR: false, fullPR: false }).filter((entry) => entry.kind === "helper"), full);
});

test("metadata execution requires a matching real host OS and architecture", () => {
  for (const target of helperTargets()) {
    assert.equal(canExecuteHelperMetadata({ os: target.os, arch: target.arch }, target), true, target.name);
    assert.equal(canExecuteHelperMetadata({ os: "linux", arch: "amd64" }, target), target.os === "linux" && target.arch === "amd64");
  }
  const armv7 = helperTargets().find((target) => target.arch === "armv7");
  assert.equal(canExecuteHelperMetadata({ os: "linux", arch: "arm64" }, armv7), false);
  assert.equal(canExecuteHelperMetadata({ os: "linux", arch: "arm" }, armv7), false);
});

test("archive member contracts cover all nine helpers and fail on a missing target", () => {
  const expected = EXPECTED.map(([, , , , name]) => `root/${name}`);
  assert.deepEqual(helperArchiveMembers("root/"), expected);
  assert.deepEqual(missingHelperArchiveMembers(expected, "root/"), []);
  assert.deepEqual(missingHelperArchiveMembers(expected.filter((name) => !name.includes("armv7")), "root/"), ["root/nexterm-helper-linux-armv7"]);
});

test("shell staging output carries every target without deriving names in workflows", () => {
  const result = spawnSync(process.execPath, [SCRIPT, "--shell"], { cwd: ROOT, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.stdout.trim().split("\n"), EXPECTED.map(([os, arch, , , name]) => `${os}|${arch}|${name}`));
});

function manifestFixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-helper-manifest-test-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const file = path.join(directory, "nexterm-helper-linux-armv7");
  const bytes = Buffer.from("armv7 helper fixture\n");
  fs.writeFileSync(file, bytes);
  const sha256 = crypto.createHash("sha256").update(bytes).digest("hex");
  const manifest = createBinaryManifest({
    file,
    id: "helper-linux-armv7",
    kind: "helper",
    goos: "linux",
    goarch: "armv7",
    goarm: "7",
    tags: ["production"],
    cgo: "0",
    stripped: true,
    version: "1.2.3",
    commit: "a".repeat(40),
    source_date_epoch: 1,
  });
  Object.assign(manifest, {
    protocol: 1,
    features: ["exec"],
    sha256,
    binary_metadata_verified: true,
    metadata_host: { os: "linux", arch: "armv7" },
  });
  return { file, manifest, sha256 };
}

function expectManifest() {
  return {
    id: "helper-linux-armv7",
    kind: "helper",
    goos: "linux",
    goarch: "armv7",
    goarm: "7",
    tags: ["production"],
    cgo: "0",
    stripped: true,
    version: "1.2.3",
    commit: "a".repeat(40),
  };
}

test("helper manifest binds GOARM, runtime metadata host and runtime sha256 to the artifact", (t) => {
  const { file, manifest } = manifestFixture(t);
  assert.equal(manifest.platform.goarm, "7");
  assert.deepEqual(binaryManifestProblems({ manifest, file, expect: expectManifest() }), []);
  assert.deepEqual(helperMetadataManifestProblems({ manifest, goos: "linux", goarch: "armv7", protocol: 1, features: ["exec"] }), []);
});

test("helper manifest rejects stale, placeholder or mismatched execution evidence", (t) => {
  for (const [name, edit, pattern] of [
    ["unverified", (manifest) => { manifest.binary_metadata_verified = false; }, /not-run/],
    ["missing GOARM", (manifest) => { delete manifest.platform.goarm; }, /GOARM/],
    ["wrong metadata host", (manifest) => { manifest.metadata_host.arch = "amd64"; }, /host/],
    ["placeholder sha256", (manifest) => { manifest.sha256 = "0".repeat(64); }, /sha256/],
    ["uppercase sha256", (manifest) => { manifest.sha256 = manifest.sha256.toUpperCase(); }, /sha256/],
    ["protocol mismatch", (manifest) => { manifest.protocol = 2; }, /protocol/],
  ]) {
    t.test(name, (t) => {
      const { file, manifest } = manifestFixture(t);
      edit(manifest);
      const binaryProblems = binaryManifestProblems({ manifest, file, expect: expectManifest() });
      const metadataProblems = helperMetadataManifestProblems({ manifest, goos: "linux", goarch: "armv7", protocol: 1, features: ["exec"] });
      assert.ok([...binaryProblems, ...metadataProblems].some((problem) => pattern.test(problem)), JSON.stringify({ binaryProblems, metadataProblems }));
    });
  }
});

test("metadata representatives are exactly linux amd64, windows amd64 and darwin arm64", () => {
  assert.deepEqual(helperTargets().filter((target) => requiresHelperExecutionEvidence(target.os, target.arch)).map((target) => `${target.os}/${target.arch}`), [
    "linux/amd64",
    "darwin/arm64",
    "windows/amd64",
  ]);
});

function notRunFixture(t) {
  const { file, manifest } = manifestFixture(t);
  Object.assign(manifest, { binary_metadata_verified: false, metadata_status: "not-run", cross_compiled: true, untested: true });
  delete manifest.protocol;
  delete manifest.features;
  delete manifest.sha256;
  delete manifest.metadata_host;
  return { file, manifest };
}

test("non-representative helper accepts a well-formed untested not-run record", (t) => {
  const { manifest } = notRunFixture(t);
  assert.deepEqual(helperMetadataManifestProblems({ manifest, goos: "linux", goarch: "armv7", protocol: 1, features: ["exec"] }), []);
});

test("representative helper cannot substitute a not-run record for execution evidence", (t) => {
  const { manifest } = notRunFixture(t);
  assert.ok(helperMetadataManifestProblems({ manifest, goos: "linux", goarch: "amd64", protocol: 1, features: ["exec"] }).some((problem) => /representative/.test(problem)));
});

test("not-run records reject partial execution fields and verified records reject mixed not-run state", (t) => {
  const partial = notRunFixture(t);
  partial.manifest.protocol = 1;
  assert.ok(helperMetadataManifestProblems({ manifest: partial.manifest, goos: "linux", goarch: "armv7", protocol: 1, features: ["exec"] }).some((problem) => /execution fields/.test(problem)));
  const mixed = manifestFixture(t);
  mixed.manifest.metadata_status = "not-run";
  assert.ok(helperMetadataManifestProblems({ manifest: mixed.manifest, goos: "linux", goarch: "armv7", protocol: 1, features: ["exec"] }).some((problem) => /mixed/.test(problem)));
});
