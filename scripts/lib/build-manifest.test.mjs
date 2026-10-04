import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  binaryManifestProblems,
  createBinaryManifest,
  createDistManifest,
  distManifestProblems,
  loadManifest,
  treeHash,
} from "./build-manifest.mjs";

function makeTree(files) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-manifest-test-"));
  for (const [name, content] of Object.entries(files)) {
    const file = path.join(root, name);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, content);
  }
  return root;
}

function makeBinary(content = "fake-production-binary\n") {
  const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-binary-test-")), "nexterm-desktop-darwin-arm64");
  fs.writeFileSync(file, content);
  return file;
}

const IDENTITY = { version: "9.9.9", commit: "0123456789abcdef" };

function binaryExpect(overrides = {}) {
  return {
    id: "desktop-darwin-arm64",
    kind: "desktop",
    goos: "darwin",
    goarch: "arm64",
    tags: ["production"],
    cgo: "1",
    stripped: true,
    version: IDENTITY.version,
    commit: IDENTITY.commit,
    ...overrides,
  };
}

function binaryManifestFor(file, overrides = {}) {
  return createBinaryManifest({
    file,
    id: "desktop-darwin-arm64",
    kind: "desktop",
    goos: "darwin",
    goarch: "arm64",
    tags: ["production"],
    cgo: "1",
    stripped: true,
    version: IDENTITY.version,
    commit: IDENTITY.commit,
    source_date_epoch: 1_700_000_000,
    ...overrides,
  });
}

test("treeHash is deterministic and independent of file creation order", (t) => {
  const first = makeTree({ "assets/app.js": "console.log(1);\n", "index.html": "<html></html>\n", "assets/nested/deep.txt": "deep\n" });
  const second = makeTree({ "assets/nested/deep.txt": "deep\n", "index.html": "<html></html>\n", "assets/app.js": "console.log(1);\n" });
  t.after(() => {
    fs.rmSync(first, { recursive: true, force: true });
    fs.rmSync(second, { recursive: true, force: true });
  });
  assert.equal(treeHash(first), treeHash(second));
});

test("treeHash changes when any file content changes", (t) => {
  const first = makeTree({ "assets/app.js": "console.log(1);\n" });
  const second = makeTree({ "assets/app.js": "console.log(2);\n" });
  t.after(() => {
    fs.rmSync(first, { recursive: true, force: true });
    fs.rmSync(second, { recursive: true, force: true });
  });
  assert.notEqual(treeHash(first), treeHash(second));
});

test("treeHash rejects non-regular files", (t) => {
  const root = makeTree({ "index.html": "<html></html>\n" });
  fs.symlinkSync(path.join(root, "index.html"), path.join(root, "linked.html"));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  assert.throws(() => treeHash(root), /non-regular file: linked\.html/);
});

test("createDistManifest records the tree hash and every file digest", (t) => {
  const root = makeTree({ "assets/app.js": "console.log(1);\n", "index.html": "<html></html>\n" });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const manifest = createDistManifest(root, IDENTITY);
  assert.equal(manifest.schema_version, 1);
  assert.equal(manifest.kind, "frontend-dist");
  assert.equal(manifest.version, IDENTITY.version);
  assert.equal(manifest.commit, IDENTITY.commit);
  assert.equal(manifest.tree_hash, treeHash(root));
  assert.deepEqual(manifest.files.map((file) => file.path), ["assets/app.js", "index.html"]);
  for (const file of manifest.files) {
    const content = fs.readFileSync(path.join(root, file.path));
    assert.equal(file.size_bytes, content.length);
    assert.equal(file.sha256.length, 64);
  }
});

test("distManifestProblems accepts an intact tree", (t) => {
  const root = makeTree({ "assets/app.js": "console.log(1);\n", "index.html": "<html></html>\n" });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const manifest = createDistManifest(root, IDENTITY);
  assert.deepEqual(distManifestProblems(root, manifest, IDENTITY), []);
});

test("distManifestProblems detects tampered file content", (t) => {
  const root = makeTree({ "assets/app.js": "console.log(1);\n", "index.html": "<html></html>\n" });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const manifest = createDistManifest(root, IDENTITY);
  fs.writeFileSync(path.join(root, "assets/app.js"), "console.log(2);\n");
  const problems = distManifestProblems(root, manifest, IDENTITY);
  assert.ok(problems.some((problem) => problem.includes("assets/app.js") && problem.includes("does not match")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("tree hash mismatch")), problems.join("\n"));
});

test("distManifestProblems detects added and removed files", (t) => {
  const root = makeTree({ "assets/app.js": "console.log(1);\n", "index.html": "<html></html>\n" });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const manifest = createDistManifest(root, IDENTITY);
  fs.writeFileSync(path.join(root, "assets", "extra.js"), "console.log(3);\n");
  fs.rmSync(path.join(root, "index.html"));
  const problems = distManifestProblems(root, manifest, IDENTITY);
  assert.ok(problems.some((problem) => problem.includes("assets/extra.js") && problem.includes("absent from the manifest")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("index.html") && problem.includes("missing")), problems.join("\n"));
});

test("distManifestProblems rejects a foreign version or commit", (t) => {
  const root = makeTree({ "index.html": "<html></html>\n" });
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const manifest = createDistManifest(root, IDENTITY);
  const problems = distManifestProblems(root, manifest, { version: "0.0.1", commit: "ffffffffffffffff" });
  assert.ok(problems.some((problem) => problem.includes("version") && problem.includes("0.0.1")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("commit") && problem.includes("ffffffffffffffff")), problems.join("\n"));
});

test("distManifestProblems rejects malformed manifests", () => {
  assert.deepEqual(distManifestProblems("/nonexistent", null, IDENTITY), ["dist manifest is not a JSON object"]);
  assert.deepEqual(distManifestProblems("/nonexistent", [], IDENTITY), ["dist manifest is not a JSON object"]);
  const wrongSchema = distManifestProblems("/nonexistent", { schema_version: 2, kind: "frontend-dist", files: [] }, IDENTITY);
  assert.ok(wrongSchema.some((problem) => problem.includes("schema_version")), wrongSchema.join("\n"));
  const wrongKind = distManifestProblems("/nonexistent", { schema_version: 1, kind: "go-binary", files: [] }, IDENTITY);
  assert.ok(wrongKind.some((problem) => problem.includes("frontend-dist")), wrongKind.join("\n"));
  const noFiles = distManifestProblems("/nonexistent", { schema_version: 1, kind: "frontend-dist" }, IDENTITY);
  assert.ok(noFiles.some((problem) => problem.includes("files must be an array")), noFiles.join("\n"));
});

test("loadManifest throws on missing and invalid files", (t) => {
  const root = makeTree({});
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  assert.throws(() => loadManifest(path.join(root, "absent.json")), /manifest does not exist/);
  const invalid = path.join(root, "invalid.json");
  fs.writeFileSync(invalid, "{not json");
  assert.throws(() => loadManifest(invalid), /not valid JSON/);
  const valid = path.join(root, "valid.json");
  fs.writeFileSync(valid, "{\"schema_version\":1}\n");
  assert.deepEqual(loadManifest(valid), { schema_version: 1 });
});

test("createBinaryManifest and binaryManifestProblems accept an intact production binary", (t) => {
  const file = makeBinary();
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  const manifest = binaryManifestFor(file);
  assert.equal(manifest.schema_version, 1);
  assert.equal(manifest.kind, "go-binary");
  assert.deepEqual(manifest.tags, ["production"]);
  assert.deepEqual(binaryManifestProblems({ manifest, file, expect: binaryExpect() }), []);
});

test("binaryManifestProblems rejects smoke-tagged test binaries for release packaging", (t) => {
  const file = makeBinary();
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  const manifest = binaryManifestFor(file, { tags: ["production", "smoke"] });
  const problems = binaryManifestProblems({ manifest, file, expect: binaryExpect() });
  assert.equal(problems.length, 1);
  assert.match(problems[0], /smoke-tagged test binaries must never be substituted for production release binaries/);
});

test("binaryManifestProblems rejects untagged and debug-tagged binaries", (t) => {
  const file = makeBinary();
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  for (const tags of [[], ["smoke"]]) {
    const manifest = binaryManifestFor(file, { tags });
    const problems = binaryManifestProblems({ manifest, file, expect: binaryExpect() });
    assert.equal(problems.length, 1, problems.join("\n"));
    assert.match(problems[0], /tags/);
  }
});

test("binaryManifestProblems detects sha256 and size drift", (t) => {
  const file = makeBinary();
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  const manifest = binaryManifestFor(file);
  fs.writeFileSync(file, "tampered-binary-content\n");
  const problems = binaryManifestProblems({ manifest, file, expect: binaryExpect() });
  assert.ok(problems.some((problem) => problem.includes("sha256 mismatch")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("size mismatch")), problems.join("\n"));
});

test("binaryManifestProblems rejects target, version, commit, cgo and stripped drift", (t) => {
  const file = makeBinary();
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  const manifest = binaryManifestFor(file);
  const cases = [
    [binaryExpect({ id: "desktop-darwin-amd64" }), /id/],
    [binaryExpect({ goos: "linux" }), /os/],
    [binaryExpect({ goarch: "amd64" }), /arch/],
    [binaryExpect({ cgo: "0" }), /cgo/],
    [binaryExpect({ stripped: false }), /stripped/],
    [binaryExpect({ version: "0.0.1" }), /version/],
    [binaryExpect({ commit: "ffffffffffffffff" }), /commit/],
  ];
  for (const [expect, pattern] of cases) {
    const problems = binaryManifestProblems({ manifest, file, expect });
    assert.equal(problems.length, 1, `${pattern}: ${problems.join("\n")}`);
    assert.match(problems[0], pattern);
  }
});

test("binaryManifestProblems reports a missing binary", (t) => {
  const file = makeBinary();
  const directory = path.dirname(file);
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const manifest = binaryManifestFor(file);
  fs.rmSync(file);
  const problems = binaryManifestProblems({ manifest, file, expect: binaryExpect() });
  assert.deepEqual(problems, [`binary does not exist: ${file}`]);
});

test("binaryManifestProblems rejects malformed manifests", () => {
  assert.deepEqual(binaryManifestProblems({ manifest: null, file: "/nonexistent", expect: binaryExpect() }), ["binary manifest is not a JSON object"]);
  const problems = binaryManifestProblems({ manifest: { schema_version: 2, kind: "frontend-dist" }, file: "/nonexistent", expect: binaryExpect() });
  assert.ok(problems.some((problem) => problem.includes("schema_version")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("go-binary")), problems.join("\n"));
  assert.ok(problems.some((problem) => problem.includes("tags must be an array")), problems.join("\n"));
});
