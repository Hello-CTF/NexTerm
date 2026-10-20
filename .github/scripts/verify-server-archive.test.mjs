import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const script = fileURLToPath(new URL("./verify-server-archive.mjs", import.meta.url));

function sha256(bytes) {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

function writeArchive(directory, root, mainBytes) {
  const stage = path.join(directory, `stage-${root}`);
  fs.mkdirSync(path.join(stage, root), { recursive: true });
  fs.writeFileSync(path.join(stage, root, "nexterm-server"), mainBytes);
  fs.writeFileSync(path.join(stage, root, "nexterm-helper-linux-amd64"), Buffer.from("helper fixture"));
  fs.writeFileSync(path.join(stage, root, "web-index-placeholder"), Buffer.from("placeholder"));
  const archive = path.join(directory, `${root}.tar.gz`);
  // COPYFILE_DISABLE keeps bsdtar from adding AppleDouble members; the real
  // producer (portable-archive.py) never emits them either.
  execFileSync("tar", ["-czf", archive, "-C", stage, root], { env: { ...process.env, COPYFILE_DISABLE: "1" } });
  return archive;
}

test("server archive byte evidence accepts the intact archive and rejects tampering", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  fs.writeFileSync(`${binary}.manifest.json`, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));
  const archive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  const run = () => spawnSync(process.execPath, [script, archive, binary], { cwd: directory, encoding: "utf8" });

  const accepted = run();
  assert.equal(accepted.status, 0, accepted.stderr);
  assert.match(accepted.stderr, /carries the staged main binary/);

  const tamperedArchive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64-tampered", Buffer.from("server main binary fixture!"));
  const tampered = spawnSync(process.execPath, [script, tamperedArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(tampered.status, 0);
  assert.match(tampered.stderr, /does not match the staged CI binary/);

  fs.appendFileSync(binary, "tampered");
  const stagedMismatch = run();
  assert.notEqual(stagedMismatch.status, 0);
  assert.match(stagedMismatch.stderr, /does not match its CI manifest/);
});

test("server archive byte evidence rejects link redirection and duplicate members", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-links-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  fs.writeFileSync(`${binary}.manifest.json`, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));

  const build = (builder, ...args) => {
    const root = `NexTerm-server_1.2.3_linux_amd64-${args[0]}`;
    const archive = path.join(directory, `${root}.tar.gz`);
    execFileSync("python3", ["-c", builder, archive, root, ...args.slice(1)], { encoding: "utf8" });
    return archive;
  };
  const run = (archive) => spawnSync(process.execPath, [script, archive, binary], { cwd: directory, encoding: "utf8" });

  // The member is a symlink to the staged binary itself: following it would
  // produce matching bytes, so only the tar-header regular-file check rejects it.
  const symlinkTarget = build(`import sys, tarfile
archive, root, target = sys.argv[1], sys.argv[2], sys.argv[3]
with tarfile.open(archive, "w:gz") as tar:
    info = tarfile.TarInfo(f"{root}/nexterm-server")
    info.type = tarfile.SYMTYPE
    info.linkname = target
    tar.addfile(info)
`, "symlink-target", binary);
  const symlink = run(symlinkTarget);
  assert.notEqual(symlink.status, 0);
  assert.match(symlink.stderr, /not a regular file/);

  // The member's parent path is a symlink member; the member itself carries
  // the exact staged bytes, so only the ancestor link check rejects it.
  const parentSymlink = build(`import io, sys, tarfile
archive, root, payload_path = sys.argv[1], sys.argv[2], sys.argv[3]
payload = open(payload_path, "rb").read()
with tarfile.open(archive, "w:gz") as tar:
    link = tarfile.TarInfo(root)
    link.type = tarfile.SYMTYPE
    link.linkname = "payload-dir"
    tar.addfile(link)
    info = tarfile.TarInfo(f"{root}/nexterm-server")
    info.size = len(payload)
    tar.addfile(info, io.BytesIO(payload))
`, "parent-symlink", binary);
  const parent = run(parentSymlink);
  assert.notEqual(parent.status, 0);
  assert.match(parent.stderr, /parent path/);

  // A hardlink member is not a regular file either, even when its target
  // carries the exact staged bytes.
  const hardlinkMember = build(`import sys, tarfile
archive, root, target = sys.argv[1], sys.argv[2], sys.argv[3]
with tarfile.open(archive, "w:gz") as tar:
    info = tarfile.TarInfo(f"{root}/nexterm-server")
    info.type = tarfile.LNKTYPE
    info.linkname = target
    tar.addfile(info)
`, "hardlink", binary);
  const hardlink = run(hardlinkMember);
  assert.notEqual(hardlink.status, 0);
  assert.match(hardlink.stderr, /not a regular file/);

  // Duplicate member names make the evidenced bytes ambiguous.
  const duplicates = build(`import io, sys, tarfile
archive, root, payload_path = sys.argv[1], sys.argv[2], sys.argv[3]
payload = open(payload_path, "rb").read()
with tarfile.open(archive, "w:gz") as tar:
    for content in (payload, payload + b"tampered"):
        info = tarfile.TarInfo(f"{root}/nexterm-server")
        info.size = len(content)
        tar.addfile(info, io.BytesIO(content))
`, "duplicates", binary);
  const duplicate = run(duplicates);
  assert.notEqual(duplicate.status, 0);
  assert.match(duplicate.stderr, /duplicate member names/);

  // The intact archive from the same fixture still passes.
  const intact = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  assert.equal(run(intact).status, 0);
});

test("server archive byte evidence rejects non-canonical members that would overwrite the evidenced bytes", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-canon-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  fs.writeFileSync(`${binary}.manifest.json`, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));

  // The canonical member carries the exact staged bytes; a second member whose
  // name only normalizes onto the same extraction path ("."/empty segment)
  // would overwrite them during a real unpack, so the header policy rejects it.
  const build = (builder, label) => {
    const root = `NexTerm-server_1.2.3_linux_amd64-${label}`;
    const archive = path.join(directory, `${root}.tar.gz`);
    execFileSync("python3", ["-c", builder, archive, root, binary], { encoding: "utf8" });
    return archive;
  };
  const overwriteBuilder = (name) => `import io, sys, tarfile
archive, root, payload_path = sys.argv[1], sys.argv[2], sys.argv[3]
payload = open(payload_path, "rb").read()
with tarfile.open(archive, "w:gz") as tar:
    info = tarfile.TarInfo(f"{root}/nexterm-server")
    info.size = len(payload)
    tar.addfile(info, io.BytesIO(payload))
    tampered = tarfile.TarInfo(f"{root}/${name}/nexterm-server")
    data = payload + b"tampered"
    tampered.size = len(data)
    tar.addfile(tampered, io.BytesIO(data))
`;
  const run = (archive) => spawnSync(process.execPath, [script, archive, binary], { cwd: directory, encoding: "utf8" });

  const dotSegment = run(build(overwriteBuilder("."), "dot-segment"));
  assert.notEqual(dotSegment.status, 0);
  assert.match(dotSegment.stderr, /'\.' path segment/);

  const emptySegment = run(build(overwriteBuilder(""), "empty-segment"));
  assert.notEqual(emptySegment.status, 0);
  assert.match(emptySegment.stderr, /empty path segment/);

  // The intact archive from the same fixture still passes.
  const intact = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  assert.equal(run(intact).status, 0);
});

test("server archive byte evidence rejects missing members, manifests and layouts", (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-server-archive-neg-"));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const mainBytes = Buffer.from("server main binary fixture");
  const binary = path.join(directory, "nexterm-server-linux-amd64");
  fs.writeFileSync(binary, mainBytes);
  const manifestPath = `${binary}.manifest.json`;
  fs.writeFileSync(manifestPath, JSON.stringify({
    artifact: { size_bytes: mainBytes.length, sha256: sha256(mainBytes) },
  }));

  const stage = path.join(directory, "stage-empty");
  fs.mkdirSync(path.join(stage, "NexTerm-server_1.2.3_linux_amd64"), { recursive: true });
  const emptyArchive = path.join(directory, "empty.tar.gz");
  execFileSync("tar", ["-czf", emptyArchive, "-C", stage, "NexTerm-server_1.2.3_linux_amd64"], { env: { ...process.env, COPYFILE_DISABLE: "1" } });
  const missing = spawnSync(process.execPath, [script, emptyArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /missing the main binary member/);

  const goodArchive = writeArchive(directory, "NexTerm-server_1.2.3_linux_amd64", mainBytes);
  fs.rmSync(manifestPath);
  const noManifest = spawnSync(process.execPath, [script, goodArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(noManifest.status, 0);
  assert.match(noManifest.stderr, /manifest is missing/);
  fs.writeFileSync(manifestPath, JSON.stringify({ artifact: { size_bytes: mainBytes.length, sha256: "0".repeat(64) } }));
  const wrongManifest = spawnSync(process.execPath, [script, goodArchive, binary], { cwd: directory, encoding: "utf8" });
  assert.notEqual(wrongManifest.status, 0);
  assert.match(wrongManifest.stderr, /does not match its CI manifest/);
});
