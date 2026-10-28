// Output classes of a terminal stream. The server classifies output ranges by
// stream sequence: "replay" is history redrawn into the view (no side
// effects at all), "answered" is output whose queries the server already
// answered (no automatic replies, other effects apply). Everything else is
// live. Spans arrive in the attach anchor and as stream-class frames ahead of
// the bytes they cover.

export type StreamMode = "live" | "replay" | "answered";

export interface StreamClassSpan {
  from: number;
  to: number;
  kind: string;
}

export interface StreamRun {
  offset: number;
  length: number;
  mode: StreamMode;
}

function modeOf(kind: string): StreamMode {
  return kind === "replay" || kind === "answered" ? kind : "live";
}

// Splits `length` bytes starting at stream sequence `seq` into runs of one
// mode each, in order.
export function splitByClass(seq: number, length: number, spans: readonly StreamClassSpan[]): StreamRun[] {
  const runs: StreamRun[] = [];
  const push = (offset: number, end: number, mode: StreamMode) => {
    if (end <= offset) return;
    const last = runs[runs.length - 1];
    if (last && last.mode === mode && last.offset + last.length === offset) last.length += end - offset;
    else runs.push({ offset, length: end - offset, mode });
  };
  const ordered = spans.filter((span) => span.to > seq && span.from < seq + length).sort((a, b) => a.from - b.from);
  let at = 0;
  for (const span of ordered) {
    const from = Math.max(span.from - seq, at);
    const to = Math.min(span.to - seq, length);
    if (to <= from) continue;
    push(at, from, "live");
    push(from, to, modeOf(span.kind));
    at = to;
  }
  push(at, length, "live");
  return runs;
}

// Drops spans that end at or before `floor`: those bytes were delivered.
export function pruneSpans(spans: StreamClassSpan[], floor: number): StreamClassSpan[] {
  return spans.some((span) => span.to <= floor) ? spans.filter((span) => span.to > floor) : spans;
}
