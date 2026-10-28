import fs from "node:fs";
import { pathToFileURL } from "node:url";

// Linux server artifacts must load on the oldest glibc that install.sh supports
// (RHEL 8). Required symbol versions come from the SHT_GNU_verneed section.
export const LINUX_SERVER_GLIBC_FLOOR = "2.28";

const SHT_GNU_VERNEED = 0x6ffffffe;

export function compareVersions(left, right) {
  const a = left.split(".").map(Number);
  const b = right.split(".").map(Number);
  for (let index = 0; index < Math.max(a.length, b.length); index += 1) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0);
    if (difference !== 0) return difference;
  }
  return 0;
}

export function elfVersionRequirements(data) {
  if (data.length < 0x40 || data[0] !== 0x7f || data.toString("ascii", 1, 4) !== "ELF") throw new Error("not an ELF file");
  const is64 = data[4] === 2;
  const little = data[5] === 1;
  const u16 = (offset) => (little ? data.readUInt16LE(offset) : data.readUInt16BE(offset));
  const u32 = (offset) => (little ? data.readUInt32LE(offset) : data.readUInt32BE(offset));
  const word = (offset) => (is64 ? Number(little ? data.readBigUInt64LE(offset) : data.readBigUInt64BE(offset)) : u32(offset));
  const shoff = word(is64 ? 0x28 : 0x20);
  const shentsize = u16(is64 ? 0x3a : 0x2e);
  const shnum = u16(is64 ? 0x3c : 0x30);
  const section = (index) => {
    const base = shoff + index * shentsize;
    return {
      type: u32(base + 4),
      offset: word(base + (is64 ? 0x18 : 0x10)),
      link: u32(base + (is64 ? 0x28 : 0x18)),
      info: u32(base + (is64 ? 0x2c : 0x1c)),
    };
  };
  const requirements = [];
  for (let index = 0; index < shnum; index += 1) {
    const verneed = section(index);
    if (verneed.type !== SHT_GNU_VERNEED) continue;
    const strings = section(verneed.link).offset;
    const name = (offset) => {
      const start = strings + offset;
      return data.toString("latin1", start, data.indexOf(0, start));
    };
    let entry = verneed.offset;
    for (let count = 0; count < verneed.info; count += 1) {
      const file = name(u32(entry + 4));
      let aux = entry + u32(entry + 8);
      for (let auxCount = u16(entry + 2); auxCount > 0; auxCount -= 1) {
        requirements.push({ file, version: name(u32(aux + 8)) });
        const next = u32(aux + 12);
        if (next === 0) break;
        aux += next;
      }
      const next = u32(entry + 12);
      if (next === 0) break;
      entry += next;
    }
  }
  return requirements;
}

export function maxVersion(file, prefix) {
  const pattern = new RegExp(`^${prefix}_(\\d+(?:\\.\\d+)+)$`);
  let maximum = null;
  for (const { version } of elfVersionRequirements(fs.readFileSync(file))) {
    const match = pattern.exec(version);
    if (match && (maximum === null || compareVersions(match[1], maximum) > 0)) maximum = match[1];
  }
  return maximum;
}

export function assertGlibcFloor(file, floor = LINUX_SERVER_GLIBC_FLOOR) {
  const maximum = maxVersion(file, "GLIBC");
  if (maximum !== null && compareVersions(maximum, floor) > 0) throw new Error(`${file} requires GLIBC_${maximum}, above the supported floor GLIBC_${floor}`);
  return maximum;
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  const [floor, ...files] = process.argv.slice(2);
  if (!floor || files.length === 0) throw new Error("usage: elf-glibc.mjs <floor> <elf>...");
  for (const file of files) console.log(`${file}: max GLIBC_${assertGlibcFloor(file, floor) ?? "none"} <= GLIBC_${floor}`);
}
