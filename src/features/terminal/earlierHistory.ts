import type { TerminalHistoryChunk } from "../../ipc/types";
import { decodeBase64 } from "./transcriptText";

// Earlier history of a durable tab: the recorded output before the first byte a
// terminal view rendered (a fresh open replays only the tail, and a view's own
// scrollback keeps a bounded number of lines). It is read backwards in pages from
// the helper and shown read-only in a separate replay terminal, never spliced
// into the live terminal's scrollback.

// One page is the backend's largest history read.
export const EARLIER_HISTORY_PAGE_BYTES = 192 * 1024;
// The replay terminal holds at most this much output; older pages are not loaded.
export const EARLIER_HISTORY_MAX_BYTES = 4 * 1024 * 1024;
// Lines the replay terminal keeps. Loading stops once it is full, so no loaded
// page is pushed out of it.
export const EARLIER_HISTORY_SCROLLBACK = 50_000;

// What the tab's control events report about its recorded history.
export interface TerminalHistoryInfo {
  // Oldest offset the helper kept at the last attach.
  base: number;
  // The helper's history limit dropped output at some point.
  truncated: boolean;
}

export const NO_TERMINAL_HISTORY: TerminalHistoryInfo = { base: 0, truncated: false };

export function terminalHistoryInfo(event: { historyBase?: number; historyTruncated?: boolean }): TerminalHistoryInfo {
  const base = Number.isFinite(event.historyBase) ? Math.max(0, event.historyBase ?? 0) : 0;
  return { base, truncated: event.historyTruncated === true };
}

// What one terminal view knows about its own start.
export interface EarlierHistoryView {
  // Helper offset of the first helper byte this view rendered (from its attach
  // anchor); null when the backend did not report it (regular tabs).
  from: number | null;
  // The view's scrollback is full, so lines after `from` were trimmed.
  trimmed: boolean;
}

// Where the earlier history ends: a helper offset, or the newest output.
export type EarlierHistoryEnd = number | "latest";

export type EarlierHistoryOffer =
  | { kind: "none" }
  | { kind: "truncated" }
  // overlap: the history ends at the newest output and repeats what the view still shows.
  | { kind: "available"; end: EarlierHistoryEnd; overlap: boolean };

export function earlierHistoryOffer(info: TerminalHistoryInfo, view: EarlierHistoryView): EarlierHistoryOffer {
  if (view.from === null) return { kind: "none" };
  // The view no longer holds everything after its start: reading up to the
  // newest output overlaps it instead of leaving a gap.
  if (view.trimmed) return { kind: "available", end: "latest", overlap: true };
  if (view.from > info.base) return { kind: "available", end: view.from, overlap: false };
  if (info.base > 0) return { kind: "truncated" };
  return { kind: "none" };
}

export interface EarlierHistoryState {
  // The loaded output covers [start, end) in helper offsets; null before the
  // first page of a read that ends at the newest output.
  start: number | null;
  end: number | null;
  // Loaded pages, oldest first.
  pages: Uint8Array[];
  bytes: number;
  // No earlier output is recorded.
  exhausted: boolean;
  // The output before start was dropped by the history limit.
  truncated: boolean;
  // Loading stopped although more is recorded: the byte cap or the replay
  // terminal's line limit was reached.
  capped: "bytes" | "lines" | null;
}

export function initialEarlierHistory(end: EarlierHistoryEnd): EarlierHistoryState {
  const at = end === "latest" ? null : end;
  return { start: at, end: at, pages: [], bytes: 0, exhausted: false, truncated: false, capped: null };
}

// The before argument of the next read.
export function nextEarlierHistoryRead(state: EarlierHistoryState): EarlierHistoryEnd {
  return state.start ?? "latest";
}

export class EarlierHistoryMismatchError extends Error {
  constructor() {
    super("历史读取结果与请求的位置不一致");
    this.name = "EarlierHistoryMismatchError";
  }
}

// Prepends one page read with before = nextEarlierHistoryRead(state). A reply
// that does not end at the requested offset is refused: the pages must stay
// contiguous.
export function applyEarlierHistoryPage(
  state: EarlierHistoryState,
  chunk: TerminalHistoryChunk,
  maxBytes: number = EARLIER_HISTORY_MAX_BYTES,
): EarlierHistoryState {
  if ((state.start !== null && chunk.end !== state.start) || chunk.start > chunk.end) {
    throw new EarlierHistoryMismatchError();
  }
  const data = decodeBase64(chunk.dataBase64);
  if (data.length !== chunk.end - chunk.start) throw new EarlierHistoryMismatchError();
  const pages = data.length > 0 ? [data, ...state.pages] : state.pages;
  const bytes = state.bytes + data.length;
  // The read reached the oldest kept output: an empty page, a page starting at
  // the base, or one the helper marks truncated (it starts at the base after
  // skipping a partial escape sequence).
  const exhausted = data.length === 0 || chunk.start <= chunk.historyBase || chunk.truncated;
  return {
    start: chunk.start,
    end: state.end ?? chunk.end,
    pages,
    bytes,
    exhausted,
    truncated: exhausted && (chunk.truncated || chunk.historyBase > 0),
    capped: exhausted ? null : bytes >= maxBytes ? "bytes" : state.capped,
  };
}

// The replay terminal filled its scrollback: stop loading earlier pages. This
// also applies when the read reached the base, because the terminal dropped
// the top of the loaded output.
export function capEarlierHistoryLines(state: EarlierHistoryState): EarlierHistoryState {
  if (state.capped !== null) return state;
  return { ...state, capped: "lines" };
}

export function canLoadEarlierHistory(state: EarlierHistoryState): boolean {
  return !state.exhausted && state.capped === null;
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
