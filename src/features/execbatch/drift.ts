import type { ExecBatchResult } from "../../ipc/execBatchApi";
import { diffLineText, type DiffLine } from "../ai/diff";

export interface DriftGroup {
  id: string;
  hash: string;
  content: string;
  binary: boolean;
  truncated: boolean;
  results: ExecBatchResult[];
}

export interface DriftAnalysis {
  groups: DriftGroup[];
  failures: ExecBatchResult[];
}

export function isBinaryDriftOutput(value: string): boolean {
  return value.includes("\0") || value.includes("�") || /[\x01-\x08\x0b\x0c\x0e-\x1f]/.test(value);
}

async function driftContentHash(value: string): Promise<string> {
  const digest = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((item) => item.toString(16).padStart(2, "0")).join("");
}

export async function analyzeDriftResults(results: readonly ExecBatchResult[]): Promise<DriftAnalysis> {
  const groups = new Map<string, DriftGroup>();
  const failures: ExecBatchResult[] = [];
  for (const result of results) {
    if (result.status !== "success") {
      failures.push(result);
      continue;
    }
    const hash = await driftContentHash(result.stdout);
    const id = `${result.truncated ? "partial" : "full"}:${hash}`;
    const existing = groups.get(id);
    if (existing) {
      existing.results.push(result);
      continue;
    }
    groups.set(id, {
      id,
      hash,
      content: result.stdout,
      binary: isBinaryDriftOutput(result.stdout),
      truncated: result.truncated,
      results: [result],
    });
  }
  return { groups: [...groups.values()], failures };
}

type DriftSourceLine = { text: string; hasNewline: boolean };

function driftSourceLines(value: string): DriftSourceLine[] {
  if (value === "") return [];
  const lines = value.split("\n");
  const endsWithNewline = lines[lines.length - 1] === "";
  if (endsWithNewline) lines.pop();
  return lines.map((text, index) => ({
    text,
    hasNewline: index < lines.length - 1 || endsWithNewline,
  }));
}

function sameDriftLine(left: DriftSourceLine, right: DriftSourceLine): boolean {
  return left.text === right.text && left.hasNewline === right.hasNewline;
}

function driftDisplayLine(line: DriftSourceLine): string {
  return line.hasNewline ? line.text : `${line.text} [末行无换行]`;
}

export function driftTextDiff(before: string, after: string): DiffLine[] {
  const left = driftSourceLines(before);
  const right = driftSourceLines(after);
  let head = 0;
  while (head < left.length && head < right.length && sameDriftLine(left[head], right[head])) head++;
  let tail = 0;
  while (tail < left.length - head && tail < right.length - head
    && sameDriftLine(left[left.length - 1 - tail], right[right.length - 1 - tail])) tail++;
  const result: DiffLine[] = [];
  for (let index = Math.max(0, head - 2); index < head; index++) {
    result.push({ kind: "same", text: driftDisplayLine(left[index]) });
  }
  for (let index = head; index < left.length - tail; index++) {
    result.push({ kind: "del", text: driftDisplayLine(left[index]) });
  }
  for (let index = head; index < right.length - tail; index++) {
    result.push({ kind: "add", text: driftDisplayLine(right[index]) });
  }
  for (let index = 0; index < Math.min(2, tail); index++) {
    result.push({ kind: "same", text: driftDisplayLine(left[left.length - tail + index]) });
  }
  return result;
}

export function driftDiffLineText(line: DiffLine): string {
  return diffLineText(line);
}

export function shellSingleQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function driftFileCommand(path: string): string {
  const trimmed = path.trim();
  if (!trimmed || trimmed === "~" || trimmed === "~/" || /[\r\n\0]/.test(trimmed)) {
    throw new Error("请输入有效的单行文件路径");
  }
  let expression: string;
  if (trimmed.startsWith("~/") && trimmed.length > 2) {
    expression = `"$HOME"/${shellSingleQuote(trimmed.slice(2))}`;
  } else {
    expression = shellSingleQuote(trimmed);
  }
  return `cat < ${expression}`;
}
