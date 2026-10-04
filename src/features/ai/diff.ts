
export type DiffLine = { kind: "add" | "del" | "same"; text: string };

export function toLines(s: string): string[] {
  if (s === "") return [];
  const lines = s.split("\n");
  if (lines.length > 1 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

export function simpleDiff(before: string, after: string): DiffLine[] {
  const a = toLines(before);
  const b = toLines(after);

  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;

  let tail = 0;
  while (
    tail < a.length - head &&
    tail < b.length - head &&
    a[a.length - 1 - tail] === b[b.length - 1 - tail]
  ) {
    tail++;
  }

  const out: DiffLine[] = [];
  const ctx = 2;
  for (let i = Math.max(0, head - ctx); i < head; i++) out.push({ kind: "same", text: a[i] });
  for (let i = head; i < a.length - tail; i++) out.push({ kind: "del", text: a[i] });
  for (let i = head; i < b.length - tail; i++) out.push({ kind: "add", text: b[i] });
  for (let i = 0; i < Math.min(ctx, tail); i++) {
    out.push({ kind: "same", text: a[a.length - tail + i] });
  }
  return out;
}

export function diffLineText(line: DiffLine): string {
  return (line.kind === "add" ? "+ " : line.kind === "del" ? "- " : "  ") + line.text;
}

export function hasVisibleChange(lines: DiffLine[]): boolean {
  return lines.some((l) => l.kind !== "same");
}
