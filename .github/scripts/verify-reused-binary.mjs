#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { helperTarget } from "./helper-matrix.mjs";
import { binaryManifestProblems, helperMetadataManifestProblems, loadManifest } from "../../scripts/lib/build-manifest.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const [file, id, goos, goarch, cgo] = process.argv.slice(2);

function die(message) {
  console.error(`verify-reused-binary: ${message}`);
  process.exit(1);
}

if (!file || !id || !goos || !goarch || !cgo) {
  die("usage: verify-reused-binary.mjs <binary> <id> <os> <arch> <cgo>");
}
const helper = id.startsWith("helper-") ? helperTarget(goos, goarch) : null;
if (id.startsWith("helper-") && (!helper || id !== `helper-${goos}-${goarch}`)) die(`unsupported helper identity ${id} for ${goos}/${goarch}`);
if (!fs.existsSync(file) || !fs.statSync(file).isFile()) die(`binary does not exist: ${file}`);
const version = process.env.NEXTERM_RELEASE_VERSION || JSON.parse(fs.readFileSync(path.join(ROOT, "wails.json"), "utf8")).info?.version;
if (!version) die("wails.json info.version is unavailable");
const commit = execFileSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).trim();
const manifest = loadManifest(`${file}.manifest.json`);
const problems = binaryManifestProblems({
  manifest,
  file,
  expect: { id, goos, goarch, goarm: helper?.goarm || "", tags: ["production"], cgo, stripped: true, version, commit },
});
if (id.startsWith("helper-")) problems.push(...helperMetadataManifestProblems({ manifest, goos, goarch }));
if (problems.length) die(`reused binary manifest validation failed:\n${problems.map((problem) => `  - ${problem}`).join("\n")}`);
console.warn(`${id}: verified reused production binary sha256:${manifest.artifact.sha256}`);
