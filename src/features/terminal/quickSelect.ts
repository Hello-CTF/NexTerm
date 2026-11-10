import { buildLogicalLines, findLinksInRows, segmentsOf, type LinkRowSegment, type TextRow } from "./terminalLinkProvider";
import { findPathLinksInRows, type PathDetectOptions } from "./terminalPathLinks";

// Quick select: label every URL, path, hash, address and id on screen so one
// or two keystrokes copy it (or, with Shift, insert it at the prompt).

export type QuickSelectKind = "url" | "path" | "uuid" | "hash" | "container" | "ipv6" | "ipv4";

export interface QuickSelectMatch {
  kind: QuickSelectKind;
  text: string;
  segments: LinkRowSegment[];
}

export interface LabeledMatch extends QuickSelectMatch {
  label: string;
}

const UUID = /(?<![0-9A-Za-z-])[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}(?![0-9A-Za-z-])/g;
// A hash needs a digit and a letter: words such as "deadline" or "facade" and plain numbers are not hashes.
const HEX = /(?<![0-9A-Za-z_-])[0-9a-f]{7,64}(?![0-9A-Za-z_-])/g;
const IPV4 = /(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?:\/\d{1,2})?(?![\w.]*\w)/g;
const IPV6 = /(?<![\w:.])(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}(?:%[\w.]+)?(?:\/\d{1,3})?(?![\w:])/g;

function validIpv4(text: string): boolean {
  const [address, prefix] = text.split("/");
  if (prefix !== undefined && Number(prefix) > 32) return false;
  return address.split(".").every((octet) => Number(octet) <= 255 && (octet === "0" || !octet.startsWith("0")));
}

function validIpv6(text: string): boolean {
  const [address] = text.split(/[/%]/);
  if (!address.includes("::") && address.split(":").length !== 8) return false;
  if ((address.match(/::/g) ?? []).length > 1) return false;
  // Times (12:30:45) and similar colon-separated numbers need a hex letter or a "::".
  if (!address.includes("::") && !/[a-fA-F]/.test(address)) return false;
  if (address === "::") return true;
  try {
    new URL(`http://[${address}]/`);
    return true;
  } catch {
    return false;
  }
}

function hexKind(text: string): QuickSelectKind | null {
  if (!/\d/.test(text) || !/[a-f]/.test(text)) return null;
  if (text.length === 64) return "container";
  return text.length <= 40 ? "hash" : null;
}

const PRIORITY: QuickSelectKind[] = ["url", "uuid", "path", "ipv6", "ipv4", "container", "hash"];

/** Every quick-select candidate in the rows, top to bottom; a span claimed by a stronger kind is not reused. */
export function findQuickSelectMatches(rows: TextRow[], cols: number, paths: PathDetectOptions | null): QuickSelectMatch[] {
  const candidates: QuickSelectMatch[] = [];
  const urls = findLinksInRows(rows, cols);
  for (const link of urls) candidates.push({ kind: "url", text: link.url, segments: link.segments });
  if (paths) {
    for (const link of findPathLinksInRows(rows, cols, paths, urls)) candidates.push({ kind: "path", text: link.text, segments: link.segments });
  }
  buildLogicalLines(rows).forEach((line, index) => {
    const add = (kind: QuickSelectKind, start: number, text: string) => {
      const segments = segmentsOf(line, start, start + text.length, index);
      if (segments.length) candidates.push({ kind, text, segments });
    };
    for (const m of line.text.matchAll(UUID)) add("uuid", m.index, m[0]);
    for (const m of line.text.matchAll(HEX)) {
      const kind = hexKind(m[0]);
      if (kind) add(kind, m.index, m[0]);
    }
    for (const m of line.text.matchAll(IPV4)) if (validIpv4(m[0])) add("ipv4", m.index, m[0]);
    for (const m of line.text.matchAll(IPV6)) if (validIpv6(m[0])) add("ipv6", m.index, m[0]);
  });
  candidates.sort((a, b) => PRIORITY.indexOf(a.kind) - PRIORITY.indexOf(b.kind));
  const kept: QuickSelectMatch[] = [];
  const overlaps = (a: QuickSelectMatch, b: QuickSelectMatch) =>
    a.segments.some((x) => b.segments.some((y) => x.row === y.row && x.start < y.end && y.start < x.end));
  for (const candidate of candidates) {
    if (!kept.some((k) => overlaps(k, candidate))) kept.push(candidate);
  }
  const first = (m: QuickSelectMatch) => m.segments[0];
  return kept.sort((a, b) => first(a).row - first(b).row || first(a).start - first(b).start);
}

// Home row first, then the rows above and below; every label is typed with one hand position.
export const QUICK_SELECT_ALPHABET = "asdfghjklqwertyuiopzxcvbnm";

/**
 * Labels matches nearest the bottom (the latest output) first. Up to 26 matches get one letter; more get two
 * letters each, so no label is a prefix of another. Repeated text shares a label.
 */
export function labelQuickSelectMatches(matches: QuickSelectMatch[], alphabet = QUICK_SELECT_ALPHABET): LabeledMatch[] {
  const distinct: string[] = [];
  for (let i = matches.length - 1; i >= 0; i--) {
    if (!distinct.includes(matches[i].text)) distinct.push(matches[i].text);
  }
  const n = alphabet.length;
  const capacity = distinct.length <= n ? n : n * n;
  const labels = new Map<string, string>();
  distinct.slice(0, capacity).forEach((text, i) => {
    labels.set(text, distinct.length <= n ? alphabet[i] : alphabet[Math.floor(i / n)] + alphabet[i % n]);
  });
  return matches.flatMap((m) => {
    const label = labels.get(m.text);
    return label ? [{ ...m, label }] : [];
  });
}

export type QuickSelectStep =
  | { kind: "pending"; typed: string }
  | { kind: "select"; match: LabeledMatch; insert: boolean }
  | { kind: "cancel" }
  | { kind: "ignore" };

export interface QuickSelectKey {
  key: string;
  shiftKey?: boolean;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
}

/** Typing state for one quick-select session. A letter typed with Shift (on any key of the label) inserts. */
export function createQuickSelectInput(matches: LabeledMatch[]) {
  let typed = "";
  let insert = false;
  return {
    typed: () => typed,
    press(event: QuickSelectKey): QuickSelectStep {
      if (event.key === "Escape") return { kind: "cancel" };
      if (event.ctrlKey || event.metaKey || event.altKey) return { kind: "ignore" };
      if (event.key === "Backspace") {
        if (!typed) return { kind: "cancel" };
        typed = typed.slice(0, -1);
        if (!typed) insert = false;
        return { kind: "pending", typed };
      }
      if (!/^[a-zA-Z]$/.test(event.key)) return { kind: "ignore" };
      const next = typed + event.key.toLowerCase();
      const candidates = matches.filter((m) => m.label.startsWith(next));
      if (candidates.length === 0) return { kind: "ignore" };
      typed = next;
      insert ||= event.shiftKey === true || event.key !== event.key.toLowerCase();
      const exact = candidates.find((m) => m.label === typed);
      if (exact) return { kind: "select", match: exact, insert };
      return { kind: "pending", typed };
    },
  };
}

export type QuickSelectShell = "posix" | "powershell" | "cmd";

const POSIX_SPECIAL = /[\s$&#!;|<>*?'"`\\()[\]{}~^%]/;
const CMD_SPECIAL = /[\s&|<>^%()!,;=]/;

/**
 * Text for inserting a match at the prompt: left as is when the shell reads it
 * as one plain word, otherwise quoted for the session's shell.
 */
export function quoteForShell(text: string, shell: QuickSelectShell): string {
  if (shell === "cmd") {
    if (!CMD_SPECIAL.test(text) && !text.includes('"')) return text;
    return `"${text.replace(/"/g, '""')}"`;
  }
  if (shell === "powershell") {
    if (!POSIX_SPECIAL.test(text.replace(/\\/g, ""))) return text;
    return `'${text.replace(/'/g, "''")}'`;
  }
  // A leading ~ is expanded by the shell, so it stays unquoted only when the rest is plain.
  if (!POSIX_SPECIAL.test(text.startsWith("~/") ? text.slice(1) : text)) return text;
  return `'${text.replace(/'/g, `'\\''`)}'`;
}
