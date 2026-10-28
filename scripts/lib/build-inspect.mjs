// Executable format inspection (PE, ELF, Mach-O) and glibc portability checks.
import fs from "node:fs";

export function inspectBinary(file) {
  const data = fs.readFileSync(file);
  if (data.length >= 0x40 && data[0] === 0x4d && data[1] === 0x5a) {
    const offset = data.readUInt32LE(0x3c);
    if (data.toString("ascii", offset, offset + 4) !== "PE\0\0") throw new Error("invalid PE header");
    const machine = data.readUInt16LE(offset + 4);
    const architecture = { 0x8664: "amd64", 0xaa64: "arm64" }[machine];
    const optionalOffset = offset + 24;
    const magic = data.readUInt16LE(optionalOffset);
    const directoryTableOffset = { 0x10b: 96, 0x20b: 112 }[magic];
    if (directoryTableOffset === undefined) throw new Error("unknown PE optional header magic");
    const resourceRva = data.readUInt32LE(optionalOffset + directoryTableOffset + 2 * 8);
    return { format: "pe", architecture, static: null, resourceRva };
  }
  if (data.length >= 20 && data.readUInt32LE(0) === 0xfeedfacf) {
    const cpu = data.readUInt32LE(4);
    const architecture = { 0x01000007: "amd64", 0x0100000c: "arm64" }[cpu];
    return { format: "mach-o", architecture, static: null };
  }
  if (data.length >= 0x34 && data[0] === 0x7f && data.toString("ascii", 1, 4) === "ELF") {
    if (data[4] !== 1 && data[4] !== 2) throw new Error("unknown ELF class");
    if (data[5] !== 1 && data[5] !== 2) throw new Error("unknown ELF byte order");
    const littleEndian = data[5] === 1;
    const read16 = (offset) => (littleEndian ? data.readUInt16LE(offset) : data.readUInt16BE(offset));
    const read32 = (offset) => (littleEndian ? data.readUInt32LE(offset) : data.readUInt32BE(offset));
    const read64 = (offset) => (littleEndian ? data.readBigUInt64LE(offset) : data.readBigUInt64BE(offset));
    const machine = read16(18);
    const architecture = { 40: "arm", 62: "amd64", 183: "arm64", 243: "riscv64", 258: "loong64" }[machine];
    const phoff = data[4] === 1 ? read32(0x1c) : Number(read64(0x20));
    const phentsize = read16(data[4] === 1 ? 0x2a : 0x36);
    const phnum = read16(data[4] === 1 ? 0x2c : 0x38);
    const is64 = data[4] === 2;
    const readWord = (offset) => (is64 ? Number(read64(offset)) : read32(offset));
    const segments = [];
    let dynamic = false;
    let interpreter = null;
    let dynamicSegment = null;
    for (let index = 0; index < phnum; index += 1) {
      const base = phoff + index * phentsize;
      const type = read32(base);
      const segment = is64
        ? { offset: Number(read64(base + 8)), vaddr: Number(read64(base + 16)), filesz: Number(read64(base + 32)) }
        : { offset: read32(base + 4), vaddr: read32(base + 8), filesz: read32(base + 16) };
      if (type === 1) segments.push(segment);
      if (type === 2) { dynamic = true; dynamicSegment = segment; }
      if (type === 3) interpreter = data.toString("latin1", segment.offset, segment.offset + segment.filesz).replace(/\0.*$/s, "");
    }
    const result = { format: "elf", architecture, static: !dynamic };
    if (!dynamic) return result;
    const toOffset = (vaddr) => {
      const segment = segments.find((candidate) => vaddr >= candidate.vaddr && vaddr < candidate.vaddr + candidate.filesz);
      if (!segment) throw new Error(`ELF address 0x${vaddr.toString(16)} is outside every PT_LOAD segment`);
      return segment.offset + (vaddr - segment.vaddr);
    };
    const entries = [];
    const entrySize = is64 ? 16 : 8;
    for (let at = dynamicSegment.offset; at + entrySize <= dynamicSegment.offset + dynamicSegment.filesz; at += entrySize) {
      const tag = is64 ? Number(read64(at)) : read32(at);
      if (tag === 0) break;
      entries.push({ tag, value: readWord(at + entrySize / 2) });
    }
    const value = (tag) => entries.find((entry) => entry.tag === tag)?.value;
    const strtab = value(5);
    const cstring = (offset) => {
      const end = data.indexOf(0, offset);
      return data.toString("latin1", offset, end);
    };
    const needed = strtab === undefined ? [] : entries.filter((entry) => entry.tag === 1).map((entry) => cstring(toOffset(strtab) + entry.value));
    // DT_VERNEED (0x6ffffffe) lists the versioned symbols the binary requires from each library.
    const verneed = value(0x6ffffffe);
    const verneedNum = value(0x6fffffff) ?? 0;
    const versionRequirements = [];
    if (verneed !== undefined && strtab !== undefined) {
      let at = toOffset(verneed);
      for (let entry = 0; entry < verneedNum; entry += 1) {
        const count = read16(at + 2);
        const file = cstring(toOffset(strtab) + read32(at + 4));
        let aux = at + read32(at + 8);
        for (let index = 0; index < count; index += 1) {
          versionRequirements.push({ file, version: cstring(toOffset(strtab) + read32(aux + 8)) });
          aux += read32(aux + 12);
        }
        const next = read32(at + 12);
        if (next === 0) break;
        at += next;
      }
    }
    return { ...result, interpreter, needed, versionRequirements };
  }
  throw new Error("unknown binary format");
}

const GLIBC_NEEDED = new Set(["libc.so.6", "libdl.so.2", "libpthread.so.0"]);
const GLIBC_INTERPRETER = { amd64: "/lib64/ld-linux-x86-64.so.2", arm64: "/lib/ld-linux-aarch64.so.1" };
const GLIBC_MAX_VERSION = [2, 17];

// A purego (CGO_ENABLED=0) binary is dynamically linked against glibc only; this keeps it runnable on any glibc Linux.
export function glibcPortableProblems(inspected, goarch) {
  if (inspected.static) return [];
  const problems = [];
  for (const name of inspected.needed ?? []) if (!GLIBC_NEEDED.has(name)) problems.push(`unexpected NEEDED ${name}`);
  if (inspected.interpreter !== GLIBC_INTERPRETER[goarch]) problems.push(`interpreter ${inspected.interpreter} is not ${GLIBC_INTERPRETER[goarch]}`);
  for (const { file, version } of inspected.versionRequirements ?? []) {
    const match = /^GLIBC_(\d+)\.(\d+)/.exec(version);
    const [major, minor] = match ? [Number(match[1]), Number(match[2])] : [Infinity, Infinity];
    if (!match || major > GLIBC_MAX_VERSION[0] || (major === GLIBC_MAX_VERSION[0] && minor > GLIBC_MAX_VERSION[1])) problems.push(`symbol version ${version} required from ${file}`);
  }
  return problems;
}
