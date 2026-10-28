import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { assertGlibcFloor, maxVersion } from "./lib/elf-glibc.mjs";

// Builds a little-endian ELF64 file whose only content is a dynamic string
// table and a SHT_GNU_verneed section listing the given symbol versions.
function syntheticELF(t, versions) {
  const names = ["libc.so.6", "libstdc++.so.6", ...versions.map(([, version]) => version)];
  const strings = Buffer.from(`\0${names.join("\0")}\0`, "latin1");
  const offsetOf = (name) => strings.indexOf(Buffer.from(`\0${name}\0`, "latin1")) + 1;
  const files = [...new Set(versions.map(([file]) => file))];
  const verneed = Buffer.alloc(files.length * 16 + versions.length * 16);
  let cursor = 0;
  files.forEach((file, fileIndex) => {
    const entries = versions.filter(([owner]) => owner === file);
    verneed.writeUInt16LE(1, cursor);
    verneed.writeUInt16LE(entries.length, cursor + 2);
    verneed.writeUInt32LE(offsetOf(file), cursor + 4);
    verneed.writeUInt32LE(16, cursor + 8);
    const next = cursor + 16 + entries.length * 16;
    verneed.writeUInt32LE(fileIndex === files.length - 1 ? 0 : next - cursor, cursor + 12);
    entries.forEach(([, version], index) => {
      const aux = cursor + 16 + index * 16;
      verneed.writeUInt32LE(offsetOf(version), aux + 8);
      verneed.writeUInt32LE(index === entries.length - 1 ? 0 : 16, aux + 12);
    });
    cursor = next;
  });
  const stringsOffset = 64;
  const verneedOffset = stringsOffset + strings.length;
  const sectionsOffset = verneedOffset + verneed.length;
  const header = Buffer.alloc(64);
  header.write("\x7fELF", 0, "latin1");
  header[4] = 2;
  header[5] = 1;
  header[6] = 1;
  header.writeBigUInt64LE(BigInt(sectionsOffset), 0x28);
  header.writeUInt16LE(64, 0x3a);
  header.writeUInt16LE(3, 0x3c);
  const sections = Buffer.alloc(3 * 64);
  sections.writeUInt32LE(3, 64 + 4);
  sections.writeBigUInt64LE(BigInt(stringsOffset), 64 + 0x18);
  sections.writeUInt32LE(0x6ffffffe, 128 + 4);
  sections.writeBigUInt64LE(BigInt(verneedOffset), 128 + 0x18);
  sections.writeUInt32LE(1, 128 + 0x28);
  sections.writeUInt32LE(files.length, 128 + 0x2c);
  const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "nexterm-elf-")), "binary");
  t.after(() => fs.rmSync(path.dirname(file), { recursive: true, force: true }));
  fs.writeFileSync(file, Buffer.concat([header, strings, verneed, sections]));
  return file;
}

test("reports the highest required glibc version and accepts the 2.28 floor", (t) => {
  const file = syntheticELF(t, [["libc.so.6", "GLIBC_2.17"], ["libc.so.6", "GLIBC_2.3.4"], ["libc.so.6", "GLIBC_2.28"], ["libstdc++.so.6", "GLIBCXX_3.4.22"]]);
  assert.equal(maxVersion(file, "GLIBC"), "2.28");
  assert.equal(maxVersion(file, "GLIBCXX"), "3.4.22");
  assert.equal(assertGlibcFloor(file), "2.28");
});

test("rejects binaries linked against a newer glibc than the floor", (t) => {
  const file = syntheticELF(t, [["libc.so.6", "GLIBC_2.17"], ["libc.so.6", "GLIBC_2.34"]]);
  assert.throws(() => assertGlibcFloor(file), /GLIBC_2\.34, above the supported floor GLIBC_2\.28/);
});
