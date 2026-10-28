// Grapheme-aware clipping for user-visible titles. A cut never splits a surrogate pair or a
// grapheme cluster (ZWJ emoji sequences, flags, skin tones, keycaps, variation selectors,
// combining marks). Limits count Unicode code points so they match the backend's rune limits.

type Segmenter = { segment(input: string): Iterable<{ segment: string }> };

let cachedSegmenter: Segmenter | null | undefined;

function graphemeSegmenter(): Segmenter | null {
  if (cachedSegmenter !== undefined) return cachedSegmenter;
  const ctor = (Intl as unknown as { Segmenter?: new (locale?: string, options?: { granularity: string }) => Segmenter })
    .Segmenter;
  cachedSegmenter = typeof ctor === "function" ? new ctor(undefined, { granularity: "grapheme" }) : null;
  return cachedSegmenter;
}

// Test hook: force the fallback path (null) or restore detection (undefined).
export function setGraphemeSegmenterForTest(segmenter: Segmenter | null | undefined): void {
  cachedSegmenter = segmenter;
}

const ZWJ = 0x200d;

function isRegionalIndicator(cp: number): boolean {
  return cp >= 0x1f1e6 && cp <= 0x1f1ff;
}

const EXTENDING_MARK = /^[\p{M}]$/u;

// Code points that attach to the preceding character instead of starting a new cluster.
function extendsPrevious(cp: number, ch: string): boolean {
  return (
    cp === ZWJ ||
    (cp >= 0xfe00 && cp <= 0xfe0f) || // variation selectors
    (cp >= 0xe0100 && cp <= 0xe01ef) || // variation selectors supplement
    (cp >= 0x1f3fb && cp <= 0x1f3ff) || // emoji skin-tone modifiers
    (cp >= 0xe0020 && cp <= 0xe007f) || // emoji tag sequences
    EXTENDING_MARK.test(ch) // combining marks, including the keycap U+20E3
  );
}

// Fallback segmentation by code point: never splits a surrogate pair, keeps extenders with
// their base, keeps a ZWJ-joined code point with the sequence and pairs regional indicators.
function fallbackGraphemes(text: string): string[] {
  const out: string[] = [];
  let joinNext = false;
  let regionalOpen = false;
  for (const ch of text) {
    const cp = ch.codePointAt(0) ?? 0;
    const regional = isRegionalIndicator(cp);
    const attach: boolean =
      out.length > 0 && (joinNext || extendsPrevious(cp, ch) || (regional && regionalOpen));
    if (attach) out[out.length - 1] += ch;
    else out.push(ch);
    regionalOpen = regional && !(attach && regionalOpen);
    joinNext = cp === ZWJ;
  }
  return out;
}

export function graphemes(text: string): string[] {
  const segmenter = graphemeSegmenter();
  if (!segmenter) return fallbackGraphemes(text);
  return Array.from(segmenter.segment(text), (part) => part.segment);
}

function codePointLength(text: string): number {
  return Array.from(text).length;
}

// clipText keeps whole grapheme clusters while the total stays within maxCodePoints.
export function clipText(text: string, maxCodePoints: number): string {
  if (maxCodePoints <= 0) return "";
  // Fast path: a string within the limit in UTF-16 units is within it in code points too.
  if (text.length <= maxCodePoints) return text;
  if (codePointLength(text) <= maxCodePoints) return text;
  let used = 0;
  let out = "";
  for (const cluster of graphemes(text)) {
    const size = codePointLength(cluster);
    if (used + size > maxCodePoints) break;
    out += cluster;
    used += size;
  }
  return out;
}
