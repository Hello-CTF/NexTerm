#!/usr/bin/env node
import { execFileSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";

function die(message) {
  console.error(`verify-server-archive: ${message}`);
  process.exit(1);
}

function sha256(bytes) {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

// Typed tar-header inspection: the main binary member must be a regular file,
// its ancestor chain must not be redirected through symlink/hardlink members,
// and its bytes are read from the archive itself. Nothing is extracted to
// disk, so filesystem symlinks can never redirect the evidence read.
const TAR_MEMBER_READER = `import hashlib, json, sys, tarfile

archive, basename = sys.argv[1], sys.argv[2]

def fail(message):
    print(message, file=sys.stderr)
    sys.exit(1)

def canonical(name):
    if name.startswith("/"):
        fail(f"server archive member has an unsafe path: {name}")
    parts = name.split("/")
    if parts and parts[-1] == "":
        parts = parts[:-1]
    if not parts:
        fail(f"server archive member has an empty path: {name}")
    for part in parts:
        if part == "":
            fail(f"server archive member has an empty path segment: {name}")
        if part == ".":
            fail(f"server archive member has a '.' path segment: {name}")
        if part == "..":
            fail(f"server archive member has an unsafe path: {name}")
    return "/".join(parts)

try:
    tar = tarfile.open(archive, "r:gz")
except tarfile.TarError as error:
    fail(f"cannot open the server archive: {error}")

with tar:
    members = tar.getmembers()
    if not members:
        fail("server archive is empty")
    # Every member must already be in canonical POSIX form: a "." or empty
    # segment would address a different header string than the path a real
    # extractor writes to, letting a second member overwrite the evidenced
    # bytes. Duplicates are judged by that canonical extraction path.
    canonical_names = [canonical(member.name) for member in members]
    if len(set(canonical_names)) != len(canonical_names):
        fail("server archive contains duplicate member names")
    roots = {name.split("/")[0] for name in canonical_names}
    if len(roots) != 1:
        fail("server archive must contain exactly one top-level directory; found: " + ", ".join(sorted(roots)))
    root = roots.pop()
    member_name = root + "/" + basename
    matches = [member for member, canonical_name in zip(members, canonical_names) if canonical_name == member_name]
    if not matches:
        fail(f"server archive is missing the main binary member: {member_name}")
    member = matches[0]
    if not member.isfile():
        fail(f"server archive main binary member is not a regular file: {member_name}")
    links = {canonical_name for member, canonical_name in zip(members, canonical_names) if member.issym() or member.islnk()}
    parts = member_name.split("/")[:-1]
    for index in range(1, len(parts) + 1):
        ancestor = "/".join(parts[:index])
        if ancestor in links:
            fail(f"server archive redirects the main binary parent path through a link: {ancestor}")
    extracted = tar.extractfile(member)
    if extracted is None:
        fail(f"cannot read the main binary member: {member_name}")
    digest = hashlib.sha256()
    size = 0
    while True:
        chunk = extracted.read(1024 * 1024)
        if not chunk:
            break
        size += len(chunk)
        digest.update(chunk)
    if size != member.size:
        fail(f"main binary member size differs from its tar header: {size} != {member.size}")
json.dump({"root": root, "member": member_name, "size": size, "sha256": digest.hexdigest()}, sys.stdout)`;

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

let evidence;
try {
  evidence = JSON.parse(execFileSync("python3", ["-c", TAR_MEMBER_READER, archive, "nexterm-server"], { encoding: "utf8" }));
} catch (error) {
  die(String(error.stderr || "").trim() || error.message);
}
if (evidence.sha256 !== binaryDigest) {
  die(`server archive main binary does not match the staged CI binary: archive=${evidence.sha256}, staged=${binaryDigest}`);
}
if (evidence.size !== binaryBytes.length || evidence.size !== manifest.artifact.size_bytes) {
  die(`server archive main binary size evidence mismatch: archive=${evidence.size}, staged=${binaryBytes.length}, manifest=${manifest.artifact.size_bytes}`);
}
console.warn(`verify-server-archive: ${path.basename(archive)} carries the staged main binary sha256:${binaryDigest}`);
