// "gap" stands for unchanged lines folded away between two hunks.
export type DiffLine = { kind: "add" | "del" | "same" | "gap"; text: string };

export function toLines(s: string): string[] {
  if (s === "") return [];
  const lines = s.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

const CONTEXT = 2;
// Bounds on the line-level edit search; past them the diff falls back to one replaced block.
const MAX_MIDDLE_LINES = 20000;
const MAX_EDIT_DISTANCE = 1000;

type Op = { kind: "add" | "del" | "same"; text: string };

// myers returns the shortest edit script between a and b, or null when it would exceed the bounds.
export function myers(a: string[], b: string[]): Op[] | null {
  const n = a.length;
  const m = b.length;
  if (n + m > MAX_MIDDLE_LINES) return null;
  const limit = Math.min(n + m, MAX_EDIT_DISTANCE);
  const offset = limit + 1;
  const v = new Int32Array(2 * offset + 1);
  const trace: Int32Array[] = [];
  let found = -1;
  for (let d = 0; d <= limit && found < 0; d++) {
    for (let k = -d; k <= d; k += 2) {
      let x = k === -d || (k !== d && v[offset + k - 1] < v[offset + k + 1]) ? v[offset + k + 1] : v[offset + k - 1] + 1;
      let y = x - k;
      while (x < n && y < m && a[x] === b[y]) {
        x++;
        y++;
      }
      v[offset + k] = x;
      if (x >= n && y >= m) {
        found = d;
        break;
      }
    }
    trace.push(v.slice(offset - d - 1, offset + d + 2));
  }
  if (found < 0) return null;
  const ops: Op[] = [];
  let x = n;
  let y = m;
  for (let d = found; d > 0; d--) {
    const prev = trace[d - 1];
    const at = (k: number) => prev[k + d];
    const k = x - y;
    const prevK = k === -d || (k !== d && at(k - 1) < at(k + 1)) ? k + 1 : k - 1;
    const prevX = at(prevK);
    const prevY = prevX - prevK;
    while (x > prevX && y > prevY) {
      ops.push({ kind: "same", text: a[--x] });
      y--;
    }
    if (x === prevX) ops.push({ kind: "add", text: b[--y] });
    else ops.push({ kind: "del", text: a[--x] });
  }
  while (x > 0 && y > 0) {
    ops.push({ kind: "same", text: a[--x] });
    y--;
  }
  return ops.reverse();
}

// simpleDiff compares two texts line by line and keeps CONTEXT unchanged lines around each change; longer
// unchanged stretches between changes fold into one "gap" line.
export function simpleDiff(before: string, after: string): DiffLine[] {
  const a = toLines(before);
  const b = toLines(after);

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) {
    tail++;
  }
  const midA = a.slice(head, a.length - tail);
  const midB = b.slice(head, b.length - tail);
  const middle = myers(midA, midB) ?? [
    ...midA.map((text): Op => ({ kind: "del", text })),
    ...midB.map((text): Op => ({ kind: "add", text })),
  ];
  const ops: Op[] = [
    ...a.slice(0, head).map((text): Op => ({ kind: "same", text })),
    ...middle,
    ...a.slice(a.length - tail).map((text): Op => ({ kind: "same", text })),
  ];

  const keep = new Uint8Array(ops.length);
  ops.forEach((op, i) => {
    if (op.kind === "same") return;
    for (let j = Math.max(0, i - CONTEXT); j <= Math.min(ops.length - 1, i + CONTEXT); j++) keep[j] = 1;
  });
  const out: DiffLine[] = [];
  let firstKept = true;
  let folded = false;
  ops.forEach((op, i) => {
    if (!keep[i]) {
      folded = true;
      return;
    }
    if (folded && !firstKept) out.push({ kind: "gap", text: "…" });
    folded = false;
    firstKept = false;
    out.push(op);
  });
  return out;
}

export function diffLineText(line: DiffLine): string {
  if (line.kind === "gap") return "  " + line.text;
  return (line.kind === "add" ? "+ " : line.kind === "del" ? "- " : "  ") + line.text;
}

export function hasVisibleChange(lines: DiffLine[]): boolean {
  return lines.some((l) => l.kind === "add" || l.kind === "del");
}
