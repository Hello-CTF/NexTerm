// Go-consistent string comparison for the R-01 typed environment-name
// confirmation. This mirrors Go's strings.TrimSpace + strings.EqualFold
// exactly: goTrimSpace trims the Unicode White_Space set used by Go's
// unicode.IsSpace (U+0085 included, U+FEFF excluded), and goEqualFold
// mirrors Go's strings.EqualFold with the ASCII fast path, the caseOrbit
// special fold cycles (Go 1.26.9 unicode/tables.go), and the default
// two-element class of a rune and its simple lowercase. A local accept is
// therefore exactly a backend accept; the backend remains the final
// authorization boundary.

import { goCaseOrbit } from "./goCaseOrbit";
import { goCaseRanges, type GoCaseRange } from "./goCaseRanges";

const goSpaceChars = new Set([
  0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x20, 0x85, 0xa0, 0x1680, 0x2000, 0x2001, 0x2002, 0x2003, 0x2004, 0x2005, 0x2006,
  0x2007, 0x2008, 0x2009, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000,
]);

export function goTrimSpace(value: string): string {
  let start = 0;
  let end = value.length;
  while (start < end && goSpaceChars.has(value.codePointAt(start) ?? 0)) {
    start += 1;
  }
  while (end > start && goSpaceChars.has(value.codePointAt(end - 1) ?? 0)) {
    end -= 1;
  }
  return value.slice(start, end);
}

// asciiFold mirrors Go's asciiFold table: A-Z fold to a-z, a-z fold back to
// A-Z except 'k' (to the Kelvin sign) and 's' (to the long s), whose full
// equivalence classes include the special cycles baked into the table.
function asciiFold(r: number): number {
  if (r >= 0x41 && r <= 0x5a) {
    return r + 0x20;
  }
  if (r >= 0x61 && r <= 0x7a) {
    if (r === 0x6b) {
      return 0x212a;
    }
    if (r === 0x73) {
      return 0x17f;
    }
    return r - 0x20;
  }
  return r;
}

const upperLower = 0x110000;

function lookupCaseRange(r: number): GoCaseRange | undefined {
  let lo = 0;
  let hi = goCaseRanges.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (goCaseRanges[mid].hi < r) {
      lo = mid + 1;
    } else {
      hi = mid;
    }
  }
  if (lo < goCaseRanges.length && goCaseRanges[lo].lo <= r && r <= goCaseRanges[lo].hi) {
    return goCaseRanges[lo];
  }
  return undefined;
}

function convertCase(which: "upper" | "lower" | "title", r: number, cr: GoCaseRange): number {
  const delta = cr[which];
  if (delta >= upperLower) {
    return cr.lo + (((r - cr.lo) & ~1) | (which === "lower" ? 1 : 0));
  }
  return r + delta;
}

function goToLower(r: number): number {
  const cr = lookupCaseRange(r);
  if (cr === undefined) {
    return r;
  }
  return convertCase("lower", r, cr);
}

function goToUpper(r: number): number {
  const cr = lookupCaseRange(r);
  if (cr === undefined) {
    return r;
  }
  return convertCase("upper", r, cr);
}

export function goSimpleFold(r: number): number {
  if (r < 0x80) {
    return asciiFold(r);
  }
  const to = goCaseOrbit.get(r);
  if (to !== undefined) {
    return to;
  }
  const lower = goToLower(r);
  if (lower !== r) {
    return lower;
  }
  return goToUpper(r);
}

export function goEqualFold(a: string, b: string): boolean {
  let index = 0;
  const shared = Math.min(a.length, b.length);
  for (; index < shared; index += 1) {
    const sr = a.charCodeAt(index);
    const tr = b.charCodeAt(index);
    if (sr >= 0x80 || tr >= 0x80) {
      break;
    }
    if (sr === tr) {
      continue;
    }
    const lo = Math.min(sr, tr);
    const hi = Math.max(sr, tr);
    if (lo >= 0x41 && lo <= 0x5a && hi === lo + 0x20) {
      continue;
    }
    return false;
  }
  if (index === a.length && index === b.length) {
    return true;
  }
  const sRunes = Array.from(a.slice(index), (char) => char.codePointAt(0) ?? 0);
  const tRunes = Array.from(b.slice(index), (char) => char.codePointAt(0) ?? 0);
  if (sRunes.length !== tRunes.length) {
    return false;
  }
  for (let position = 0; position < sRunes.length; position += 1) {
    const sr = sRunes[position];
    const tr = tRunes[position];
    if (sr === tr) {
      continue;
    }
    const lo = Math.min(sr, tr);
    const hi = Math.max(sr, tr);
    if (hi < 0x80) {
      if (lo >= 0x41 && lo <= 0x5a && hi === lo + 0x20) {
        continue;
      }
      return false;
    }
    let folded = goSimpleFold(lo);
    while (folded !== lo && folded < hi) {
      folded = goSimpleFold(folded);
    }
    if (folded === hi) {
      continue;
    }
    return false;
  }
  return true;
}
