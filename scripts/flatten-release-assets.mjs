#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

function digest(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function filesBelow(directory) {
  const files = [];
  const visit = (current) => {
    for (const entry of fs.readdirSync(current, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const target = path.join(current, entry.name);
      if (entry.isDirectory()) visit(target);
      else if (!entry.isFile()) throw new Error(`release artifact input is not a regular file: ${target}`);
      else files.push(target);
    }
  };
  visit(directory);
  return files;
}

function removeEmptyDirectories(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    if (entry.isDirectory()) removeEmptyDirectories(path.join(directory, entry.name));
  }
  fs.rmdirSync(directory);
}

export function flattenReleaseAssets(root, directories = ["release-assets", "go-build"]) {
  const destinationRoot = path.resolve(root);
  if (!fs.existsSync(destinationRoot) || !fs.statSync(destinationRoot).isDirectory()) throw new Error(`artifact directory does not exist: ${destinationRoot}`);
  const sources = [];
  for (const directory of directories) {
    const source = path.resolve(destinationRoot, directory);
    if (source !== destinationRoot && !source.startsWith(`${destinationRoot}${path.sep}`)) throw new Error(`flatten source escapes the artifact directory: ${directory}`);
    if (fs.existsSync(source)) sources.push(source);
  }
  const byBasename = new Map();
  const add = (file, existing) => {
    const basename = path.basename(file);
    const record = { file, hash: digest(file), existing };
    if (!byBasename.has(basename)) byBasename.set(basename, []);
    byBasename.get(basename).push(record);
  };
  for (const entry of fs.readdirSync(destinationRoot, { withFileTypes: true })) {
    if (entry.isFile()) add(path.join(destinationRoot, entry.name), true);
  }
  for (const source of sources) {
    for (const file of filesBelow(source)) add(file, false);
  }
  const conflicts = [];
  for (const [basename, records] of byBasename) {
    if (new Set(records.map((record) => record.hash)).size > 1) {
      conflicts.push(`${basename}:\n${records.map((record) => `  ${record.hash}  ${record.file}`).join("\n")}`);
    }
  }
  if (conflicts.length) throw new Error(`refusing to flatten same-named artifacts with different hashes:\n${conflicts.join("\n")}`);
  for (const [basename, records] of byBasename) {
    const destination = path.join(destinationRoot, basename);
    if (fs.existsSync(destination) && !fs.statSync(destination).isFile()) throw new Error(`flatten destination is not a regular file: ${destination}`);
    for (const record of records.filter((record) => !record.existing)) {
      if (!fs.existsSync(destination)) fs.renameSync(record.file, destination);
      else fs.rmSync(record.file);
    }
  }
  for (const source of sources) removeEmptyDirectories(source);
  return [...byBasename.values()].filter((records) => records.some((record) => !record.existing)).length;
}

export function flattenDownloadedReleaseAssets(root, destination) {
  const sourceRoot = path.resolve(root);
  const destinationRoot = path.resolve(destination);
  if (!fs.existsSync(sourceRoot) || !fs.statSync(sourceRoot).isDirectory()) throw new Error(`artifact directory does not exist: ${sourceRoot}`);
  if (destinationRoot === sourceRoot || destinationRoot.startsWith(`${sourceRoot}${path.sep}`)) throw new Error("flatten destination must be outside the download directory");
  if (fs.existsSync(destinationRoot)) throw new Error(`flatten destination already exists: ${destinationRoot}`);
  const byBasename = new Map();
  for (const file of filesBelow(sourceRoot)) {
    const basename = path.basename(file);
    const record = { file, hash: digest(file) };
    if (!byBasename.has(basename)) byBasename.set(basename, []);
    byBasename.get(basename).push(record);
  }
  const conflicts = [];
  for (const [basename, records] of byBasename) {
    if (new Set(records.map((record) => record.hash)).size > 1) {
      conflicts.push(`${basename}:\n${records.map((record) => `  ${record.hash}  ${record.file}`).join("\n")}`);
    }
  }
  if (conflicts.length) throw new Error(`refusing to flatten same-named artifacts with different hashes:\n${conflicts.join("\n")}`);
  fs.mkdirSync(destinationRoot, { recursive: true });
  for (const [basename, records] of byBasename) {
    fs.copyFileSync(records[0].file, path.join(destinationRoot, basename));
  }
  return byBasename.size;
}

function main() {
  const [root, ...directories] = process.argv.slice(2);
  if (!root) throw new Error("usage: node scripts/flatten-release-assets.mjs ARTIFACT_DIR [SOURCE_DIR ...] | node scripts/flatten-release-assets.mjs DOWNLOAD_DIR --into ARTIFACT_DIR");
  if (directories.includes("--into")) {
    if (directories.length !== 2 || directories[0] !== "--into" || !directories[1]) throw new Error("--into requires exactly one destination directory");
    const count = flattenDownloadedReleaseAssets(root, directories[1]);
    console.log(`downloaded release artifacts flattened safely: ${count} basenames`);
    return;
  }
  const count = flattenReleaseAssets(root, directories.length ? directories : undefined);
  console.log(`release artifacts flattened safely: ${count} basenames`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(`flatten-release-assets: ${error.message}`);
    process.exitCode = 1;
  }
}
