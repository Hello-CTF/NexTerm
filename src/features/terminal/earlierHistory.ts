import type { TerminalHistoryChunk } from "../../ipc/types";
import { decodeBase64 } from "./transcriptText";

// Earlier history of a durable tab: the recorded output before the first byte the
// terminal rendered (a fresh open replays only the tail). It is read backwards in
// pages from the helper and shown read-only in a separate replay terminal, never
// spliced into the live terminal's scrollback.

// One page is the backend's largest history read.
export const EARLIER_HISTORY_PAGE_BYTES = 192 * 1024;
// The replay terminal holds at most this much output; older pages are not loaded.
export const EARLIER_HISTORY_MAX_BYTES = 4 * 1024 * 1024;

export interface TerminalHistoryInfo {
  // Helper offset of the tab's first rendered byte.
  from: number;
  // Oldest offset the helper kept at the last attach.
  base: number;
  // The helper's history limit dropped output at some point.
  truncated: boolean;
}

export const NO_TERMINAL_HISTORY: TerminalHistoryInfo = { from: 0, base: 0, truncated: false };

export function terminalHistoryInfo(event: {
  historyFrom?: number;
  historyBase?: number;
  historyTruncated?: boolean;
}): TerminalHistoryInfo {
  const from = Number.isFinite(event.historyFrom) ? Math.max(0, event.historyFrom ?? 0) : 0;
  const base = Number.isFinite(event.historyBase) ? Math.max(0, event.historyBase ?? 0) : 0;
  return { from, base, truncated: event.historyTruncated === true };
}

// available: output before the first rendered byte is still recorded.
// truncated: the history limit already dropped everything before it.
export type EarlierHistoryOffer = "none" | "available" | "truncated";

export function earlierHistoryOffer(info: TerminalHistoryInfo): EarlierHistoryOffer {
  if (info.from > info.base) return "available";
  if (info.base > 0 && info.from <= info.base) return "truncated";
  return "none";
}

export interface EarlierHistoryState {
  // The loaded output covers [start, end) in helper offsets; end is fixed.
  start: number;
  end: number;
  // Loaded pages, oldest first.
  pages: Uint8Array[];
  bytes: number;
  // No earlier output is recorded.
  exhausted: boolean;
  // The output before start was dropped by the history limit.
  truncated: boolean;
  // Loading stopped at EARLIER_HISTORY_MAX_BYTES although more is recorded.
  capped: boolean;
}

export function initialEarlierHistory(end: number): EarlierHistoryState {
  return { start: end, end, pages: [], bytes: 0, exhausted: false, truncated: false, capped: false };
}

export class EarlierHistoryMismatchError extends Error {
  constructor() {
    super("历史读取结果与请求的位置不一致");
    this.name = "EarlierHistoryMismatchError";
  }
}

// Prepends one page read with before = state.start. A reply that does not end at
// the requested offset is refused: the pages must stay contiguous.
export function applyEarlierHistoryPage(
  state: EarlierHistoryState,
  chunk: TerminalHistoryChunk,
  maxBytes: number = EARLIER_HISTORY_MAX_BYTES,
): EarlierHistoryState {
  if (chunk.end !== state.start || chunk.start > chunk.end) throw new EarlierHistoryMismatchError();
  const data = decodeBase64(chunk.dataBase64);
  if (data.length !== chunk.end - chunk.start) throw new EarlierHistoryMismatchError();
  const pages = data.length > 0 ? [data, ...state.pages] : state.pages;
  const bytes = state.bytes + data.length;
  // An empty page before the base means nothing earlier is readable.
  const exhausted = data.length === 0 || chunk.start <= chunk.historyBase;
  return {
    start: chunk.start,
    end: state.end,
    pages,
    bytes,
    exhausted,
    truncated: exhausted && (chunk.truncated || chunk.historyBase > 0),
    capped: !exhausted && bytes >= maxBytes,
  };
}

export function canLoadEarlierHistory(state: EarlierHistoryState): boolean {
  return !state.exhausted && !state.capped;
}

export function earlierHistoryBytes(state: EarlierHistoryState): Uint8Array {
  const out = new Uint8Array(state.bytes);
  let offset = 0;
  for (const page of state.pages) {
    out.set(page, offset);
    offset += page.length;
  }
  return out;
}

export function formatHistoryBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
