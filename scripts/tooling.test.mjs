import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { acceptanceWatchIgnored } from "./lib/vite-watch-ignores.mjs";
import { flattenDownloadedReleaseAssets, flattenReleaseAssets } from "./flatten-release-assets.mjs";
import { stampVersion } from "./stamp-version.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const BUILD = path.join(ROOT, "scripts", "build.mjs");
const MINISIGN_PUBLIC_KEY = "RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3";

test("vite watch ignores anchor every artifact directory to the resolved root", () => {
  const root = path.join(path.sep, "work", "nexterm");
  const patterns = acceptanceWatchIgnored(root);
  for (const dir of [".tower", "target", "target-alt", ".tmp"]) {
    assert.ok(patterns.includes(`${root}/${dir}`), `missing anchored ignore for ${dir}`);
    assert.ok(patterns.includes(`${root}/${dir}/**`), `missing anchored recursive ignore for ${dir}`);
  }
  assert.equal(patterns.some((pattern) => !pattern.startsWith(`${root}/`)), false);
});

function fixture() {
  return fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-tooling-test-"));
}

test("flatten rejects different hashes before moving any file", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const first = path.join(root, "release-assets/nested/duplicate.txt");
  const second = path.join(root, "go-build/other/duplicate.txt");
  fs.mkdirSync(path.dirname(first), { recursive: true });
  fs.mkdirSync(path.dirname(second), { recursive: true });
  fs.writeFileSync(first, "first");
  fs.writeFileSync(second, "second");
  assert.throws(() => flattenReleaseAssets(root), /different hashes/);
  assert.equal(fs.readFileSync(first, "utf8"), "first");
  assert.equal(fs.readFileSync(second, "utf8"), "second");
  assert.equal(fs.existsSync(path.join(root, "duplicate.txt")), false);
});

test("flatten accepts byte-identical duplicates and removes source directories", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const first = path.join(root, "release-assets/a/duplicate.txt");
  const second = path.join(root, "go-build/b/duplicate.txt");
  const unique = path.join(root, "go-build/b/unique.txt");
  for (const file of [first, second, unique]) fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(first, "same");
  fs.writeFileSync(second, "same");
  fs.writeFileSync(unique, "unique");
  assert.equal(flattenReleaseAssets(root), 2);
  assert.equal(fs.readFileSync(path.join(root, "duplicate.txt"), "utf8"), "same");
  assert.equal(fs.readFileSync(path.join(root, "unique.txt"), "utf8"), "unique");
  assert.equal(fs.existsSync(path.join(root, "release-assets")), false);
  assert.equal(fs.existsSync(path.join(root, "go-build")), false);
});

test("download flatten rejects every cross-artifact hash conflict before creating output", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const input = path.join(root, "candidate-input");
  const output = path.join(root, "candidate");
  const first = path.join(input, "release-desktop-a/release-assets/duplicate.txt");
  const second = path.join(input, "release-server-b/go-build/duplicate.txt");
  for (const file of [first, second]) fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(first, "first");
  fs.writeFileSync(second, "second");
  assert.throws(() => flattenDownloadedReleaseAssets(input, output), /different hashes/);
  assert.equal(fs.existsSync(output), false);
  assert.equal(fs.readFileSync(first, "utf8"), "first");
  assert.equal(fs.readFileSync(second, "utf8"), "second");
});

test("download flatten deduplicates identical files and preserves unique inputs", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const input = path.join(root, "candidate-input");
  const output = path.join(root, "candidate");
  const files = [
    ["release-desktop-a/release-assets/duplicate.txt", "same"],
    ["release-server-b/go-build/duplicate.txt", "same"],
    ["release-server-b/go-build/raw.artifact.json", "report"],
  ];
  for (const [name, content] of files) {
    const file = path.join(input, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content);
  }
  assert.equal(flattenDownloadedReleaseAssets(input, output), 2);
  assert.equal(fs.readFileSync(path.join(output, "duplicate.txt"), "utf8"), "same");
  assert.equal(fs.readFileSync(path.join(output, "raw.artifact.json"), "utf8"), "report");
  assert.equal(fs.existsSync(path.join(input, "release-desktop-a/release-assets/duplicate.txt")), true);
});

function desktopLdflags(publicKey) {
  return spawnSync(process.execPath, [BUILD, "desktop-ldflags", "--os=linux"], {
    cwd: ROOT,
    env: { ...process.env, SOURCE_DATE_EPOCH: "1", NEXTERM_RELEASE_VERSION: "1.2.3", NEXTERM_MINISIGN_PUBLIC_KEY: publicKey },
    encoding: "utf8",
  });
}

test("desktop ldflags inject one normalized updater public key and preserve legacy local builds", () => {
  const injected = desktopLdflags(`untrusted comment: official test key\n${MINISIGN_PUBLIC_KEY}\n`);
  assert.equal(injected.status, 0, injected.stderr);
  assert.ok(injected.stdout.includes(`-X github.com/Hello-CTF/NexTerm/internal/update.defaultMinisignPublicKey=${MINISIGN_PUBLIC_KEY}`));
  assert.equal(injected.stdout.trim().split("\n").length, 1);
  const legacy = desktopLdflags("");
  assert.equal(legacy.status, 0, legacy.stderr);
  assert.ok(!legacy.stdout.includes("defaultMinisignPublicKey"));
  const invalid = desktopLdflags("not-a-minisign-key");
  assert.equal(invalid.status, 1);
  assert.match(invalid.stderr, /NEXTERM_MINISIGN_PUBLIC_KEY/);
});

test("single-line and two-line updater keys produce the same standard public key file", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const [name, configured] of [
    ["single", MINISIGN_PUBLIC_KEY],
    ["two-line", `untrusted comment: official test key\n${MINISIGN_PUBLIC_KEY}\n`],
  ]) {
    const output = path.join(root, `${name}.pub`);
    const result = spawnSync(process.execPath, [BUILD, "write-minisign-public-key", `--out=${output}`], {
      cwd: ROOT,
      env: { ...process.env, SOURCE_DATE_EPOCH: "1", NEXTERM_RELEASE_VERSION: "1.2.3", NEXTERM_MINISIGN_PUBLIC_KEY: configured },
      encoding: "utf8",
    });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(fs.readFileSync(output, "utf8"), `untrusted comment: NexTerm release minisign public key\n${MINISIGN_PUBLIC_KEY}\n`);
  }
});

test("version stamp replaces every placeholder and fails when nothing matches", (t) => {
  const root = fixture();
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const config = path.join(root, "config.yml");
  fs.writeFileSync(config, 'info:\n  version: "0.0.0"\nios:\n  version: "0.0.0"\n');
  stampVersion([config], "1.2.3-rc.1");
  assert.equal(fs.readFileSync(config, "utf8"), 'info:\n  version: "1.2.3-rc.1"\nios:\n  version: "1.2.3-rc.1"\n');
  assert.throws(() => stampVersion([config], "1.2.3"), /replaced nothing/);
  assert.throws(() => stampVersion([config], "0.0.0"), /non-placeholder semver/);
});
