export const MERGED_LOG_LINE_LIMIT = 5000;
export const MERGED_LOG_MAX_CONTAINERS = 8;

export interface MergedLogLine {
  source: number;
  // Milliseconds since epoch; the fraction keeps sub-millisecond ordering.
  ts: number;
  seq: number;
  text: string;
}

const TIMESTAMP = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2}) ?/;

export function parseTimestampedLine(raw: string): { ts: number | null; text: string } {
  const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;
  const match = TIMESTAMP.exec(line);
  if (!match) return { ts: null, text: line };
  const fraction = (match[2] ?? "").padEnd(9, "0");
  const base = Date.parse(`${match[1]}.${fraction.slice(0, 3)}${match[3]}`);
  if (Number.isNaN(base)) return { ts: null, text: line };
  return { ts: base + Number(fraction.slice(3)) / 1e6, text: line.slice(match[0].length) };
}

export interface MergedLogBuffer {
  push(source: number, lines: string[]): void;
  snapshot(): MergedLogLine[];
  readonly size: number;
}

// A line without a timestamp inherits the previous line of its source; before a
// source has any timestamp such lines sort first (-Infinity, arrival order via seq).
export function createMergedLogBuffer(limit = MERGED_LOG_LINE_LIMIT): MergedLogBuffer {
  const lines: MergedLogLine[] = [];
  const lastBySource = new Map<number, number>();
  let seq = 0;
  return {
    push(source, batch) {
      for (const raw of batch) {
        const parsed = parseTimestampedLine(raw);
        const ts = parsed.ts ?? lastBySource.get(source) ?? -Infinity;
        lastBySource.set(source, ts);
        const entry: MergedLogLine = { source, ts, seq: seq++, text: parsed.text };
        // Upper bound: after every line with an equal or smaller timestamp.
        let lo = 0;
        let hi = lines.length;
        while (lo < hi) {
          const mid = (lo + hi) >>> 1;
          if (lines[mid].ts <= ts) lo = mid + 1;
          else hi = mid;
        }
        lines.splice(lo, 0, entry);
        if (lines.length > limit) lines.shift();
      }
    },
    snapshot: () => lines.slice(),
    get size() {
      return lines.length;
    },
  };
}
