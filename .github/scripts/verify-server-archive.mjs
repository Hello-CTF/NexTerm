#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

function die(message) {
  console.error(`verify-server-archive: ${message}`);
  process.exit(1);
}

function sha256(bytes) {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

const [archive, binary] = process.argv.slice(2);
if (!archive || !binary) die("usage: verify-server-archive.mjs <archive.tar.gz> <staged-binary>");
if (!fs.existsSync(archive) || !fs.statSync(archive).isFile()) die(`archive does not exist: ${archive}`);
if (!fs.existsSync(binary) || !fs.statSync(binary).isFile()) die(`staged binary does not exist: ${binary}`);

const manifestPath = `${binary}.manifest.json`;
if (!fs.existsSync(manifestPath) || !fs.statSync(manifestPath).isFile()) {
  die(`staged binary manifest is missing: ${manifestPath}`);
}
let manifest;
try {
  manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
} catch (error) {
  die(`staged binary manifest is not valid JSON: ${error.message}`);
}
if (!/^[0-9a-f]{64}$/.test(manifest.artifact?.sha256 || "")) {
  die("staged binary manifest lacks a SHA-256 artifact digest");
}
const binaryBytes = fs.readFileSync(binary);
const binaryDigest = sha256(binaryBytes);
if (binaryDigest !== manifest.artifact.sha256) {
  die(`staged binary sha256 does not match its CI manifest: staged=${binaryDigest}, manifest=${manifest.artifact.sha256}`);
}

const head = fs.readFileSync(archive);
if (head[0] !== 0x1f || head[1] !== 0x8b) die("server archive is not gzip compressed");
let listing;
try {
  listing = execFileSync("tar", ["-tzf", archive], { encoding: "utf8" });
} catch (error) {
  die(`cannot list the server archive: ${error.message}`);
}
const members = listing.split("\n").filter((name) => name !== "");
const roots = new Set(members.map((name) => name.split("/")[0]));
if (roots.size !== 1) {
  die(`server archive must contain exactly one top-level directory; found: ${[...roots].sort().join(", ")}`);
}
const member = `${[...roots][0]}/nexterm-server`;
if (!members.includes(member)) die(`server archive is missing the main binary member: ${member}`);

const work = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-"));
try {
  execFileSync("tar", ["-xzf", archive, "-C", work, member]);
  const packagedBytes = fs.readFileSync(path.join(work, member));
  const packagedDigest = sha256(packagedBytes);
  if (packagedDigest !== binaryDigest) {
    die(`server archive main binary does not match the staged CI binary: archive=${packagedDigest}, staged=${binaryDigest}`);
  }
  if (packagedBytes.length !== binaryBytes.length || packagedBytes.length !== manifest.artifact.size_bytes) {
    die(`server archive main binary size evidence mismatch: archive=${packagedBytes.length}, staged=${binaryBytes.length}, manifest=${manifest.artifact.size_bytes}`);
  }
} finally {
  fs.rmSync(work, { recursive: true, force: true });
}
console.warn(`verify-server-archive: ${path.basename(archive)} carries the staged main binary sha256:${binaryDigest}`);
