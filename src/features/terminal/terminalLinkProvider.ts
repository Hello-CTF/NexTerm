import type { IBufferLine, IBufferRange, ILink, ILinkProvider, Terminal } from "@xterm/xterm";
import { cleanTerminalLink, planTerminalLink } from "./terminalLinks";

// Link detection across hard line breaks. xterm's own soft wrapping is joined
// unconditionally; TUIs (Ink, gh, npm) that break a URL at their own width, or
// draw it inside a box or behind a quote/line-number prefix, are joined by the
// conservative rules in continuationFragment().

export const MIN_CONTINUATION_ROWS = 4;
export const MAX_LINK_LENGTH = 2048;
const CONTEXT_PHYSICAL_SLACK = 16;
const CONTEXT_LIMIT = 120;

/** Rows a URL may continue over: enough to carry MAX_LINK_LENGTH at this width. */
export function maxContinuationRows(cols: number): number {
  return Math.max(MIN_CONTINUATION_ROWS, Math.ceil(MAX_LINK_LENGTH / Math.max(1, cols)));
}

export interface TextRow {
  text: string;
  isWrapped: boolean;
}
export interface LinkRowSegment {
  row: number;
  start: number;
  end: number;
  /** Logical line the segment belongs to: segments sharing it are one soft-wrapped run. */
  group?: number;
}
export interface StitchedLink {
  url: string;
  segments: LinkRowSegment[];
}
export interface DetectedTarget {
  kind: "ip" | "hostport";
  text: string;
  host: string;
  port?: number;
  segment: LinkRowSegment;
}

export interface LogicalLine {
  text: string;
  rows: number[];
  offsets: number[];
}

// First-row URLs end at full-width punctuation; continuation fragments are
// printable ASCII only.
const URL_START = /https?:\/\/[^\s"'<>`│┃║|\\^。，；：！？\u3000-\u303f\uff00-\uff60]+/gi;
const URL_CHARS = /^(?:(?!["'<>`|\\^])[\x21-\x7e])+/;
const SCHEME_AT_START = /^[a-z][a-z0-9+.-]*:\/\//i;
const HTTP_SCHEME = /https?:\/\//i;
const ONLY_TRAILING_DECOR = /^[\s│┃║|]*$/;
const BORDER_GAP = /^(\s*)[│┃║|]/;

const LEFT_BORDER_PAD = /[│┃║|]( *)$/;

/**
 * Index (in `line.text`) of the right border that follows `end`, or -1 when
 * the text does not reach a border. The blank cells allowed before it mirror
 * the padding after the left border of the text starting at `start`, and are
 * at least one.
 */
function borderIndexAfter(line: LogicalLine, start: number, end: number): number {
  const m = BORDER_GAP.exec(line.text.slice(end));
  if (m === null) return -1;
  const pad = LEFT_BORDER_PAD.exec(line.text.slice(0, start));
  return m[1].length <= Math.max(1, pad ? pad[1].length : 0) ? end + m[1].length : -1;
}
const LEFT_PREFIX = /^(?:\s+|[│┃║|>]|\d+\s*[│|:])+/;
const LEFT_PREFIX_ONLY = /^(?:\s+|[│┃║|>]|\d+\s*[│|:])*$/;

function charWidth(code: number): number {
  if (code < 0x300) return 1;
  if ((code >= 0x300 && code <= 0x36f) || (code >= 0x200b && code <= 0x200f) || (code >= 0xfe00 && code <= 0xfe0f)) return 0;
  if (
    (code >= 0x1100 && code <= 0x115f) ||
    (code >= 0x2e80 && code <= 0xa4cf && code !== 0x303f) ||
    (code >= 0xac00 && code <= 0xd7a3) ||
    (code >= 0xf900 && code <= 0xfaff) ||
    (code >= 0xfe30 && code <= 0xfe6f) ||
    (code >= 0xff00 && code <= 0xff60) ||
    (code >= 0xffe0 && code <= 0xffe6) ||
    (code >= 0x1f300 && code <= 0x1f64f) ||
    (code >= 0x1f900 && code <= 0x1f9ff) ||
    (code >= 0x20000 && code <= 0x3fffd)
  ) {
    return 2;
  }
  return 1;
}

function cellWidth(text: string, from = 0, to = text.length): number {
  let w = 0;
  for (let i = from; i < to; i++) {
    const code = text.codePointAt(i)!;
    if (code > 0xffff) i++;
    w += charWidth(code);
  }
  return w;
}

export function buildLogicalLines(rows: TextRow[]): LogicalLine[] {
  const lines: LogicalLine[] = [];
  let cur: LogicalLine | null = null;
  rows.forEach((row, index) => {
    if (!cur || !row.isWrapped) {
      cur = { text: "", rows: [], offsets: [] };
      lines.push(cur);
    }
    cur.rows.push(index);
    cur.offsets.push(cur.text.length);
    cur.text += row.text;
  });
  return lines;
}

function rowSlot(line: LogicalLine, index: number): number {
  let slot = 0;
  for (let i = 1; i < line.offsets.length; i++) {
    if (index >= line.offsets[i]) slot = i;
    else break;
  }
  return slot;
}

/** Start column (0-based cells) of the character at logical index `index`. */
function columnOf(rows: TextRow[], line: LogicalLine, index: number): number {
  const slot = rowSlot(line, index);
  const text = rows[line.rows[slot]].text;
  return cellWidth(text, 0, index - line.offsets[slot]);
}

export function segmentsOf(line: LogicalLine, start: number, end: number, group: number): LinkRowSegment[] {
  const out: LinkRowSegment[] = [];
  for (let slot = 0; slot < line.rows.length; slot++) {
    const rowStart = line.offsets[slot];
    const rowEnd = slot + 1 < line.offsets.length ? line.offsets[slot + 1] : line.text.length;
    const s = Math.max(start, rowStart);
    const e = Math.min(end, rowEnd);
    if (e > s) out.push({ row: line.rows[slot], start: s - rowStart, end: e - rowStart, group });
  }
  return out;
}

interface Piece {
  line: number;
  start: number;
  end: number;
}

// Where a candidate continuation sits in its URL, for the lone-token rules.
interface ChainState {
  prevBorderCol: number;
  /** The URL's first row holds nothing but the URL (and prefix decoration). */
  bare: boolean;
  /** A full-width continuation row has already been accepted. */
  chained: boolean;
  /** Content end column of the previous row of the chain. */
  prevEndCol: number;
  /** The URL stitched so far contains a query. */
  hasQuery: boolean;
  /** Last character stitched so far. */
  lastChar: string;
  /** Text of the logical line after the candidate; undefined at the end of the rows. */
  following: string | undefined;
}

const CLEAN_CONTEXT = /^(?:\s*|\s*[$>#](?:\s.*)?|[\s╰└╯┘─━═]*[╰└─━═][\s╰└╯┘─━═]*)$/;

// Rows of a URL hard-wrapped at an application's own width (which can differ
// from the terminal's after a resize) end in the same column. Narrower rows
// are too likely to match by chance.
const MIN_EQUAL_WIDTH = 30;

function sameWidth(a: number, b: number): boolean {
  return a >= MIN_EQUAL_WIDTH && b >= MIN_EQUAL_WIDTH && Math.abs(a - b) <= 1;
}

const QUERY_EVIDENCE = /[=&%?#/]/;

// The candidate looks like a tail but is too weak to join. When the previous
// row was full, stopping here would leave a valid-looking prefix of a cut URL.
const WEAK_TAIL = Symbol("weak tail");

// Decides whether logical line `next` continues a URL that ended at `prev`
// (see the rules in the module comment). Returns the accepted fragment piece
// and whether stitching may go on after it.
function continuationFragment(
  rows: TextRow[],
  line: LogicalLine,
  lineIndex: number,
  cols: number,
  state: ChainState,
): { piece: Piece; more: boolean; endCol: number; borderCol: number } | typeof WEAK_TAIL | null {
  const prefix = LEFT_PREFIX.exec(line.text);
  const from = prefix ? prefix[0].length : 0;
  const rest = line.text.slice(from);
  const match = URL_CHARS.exec(rest);
  if (!match) return null;
  let fragment = match[0];
  if (SCHEME_AT_START.test(fragment)) return null;
  const inner = HTTP_SCHEME.exec(fragment);
  if (inner) {
    // A second URL glued on: keep what precedes it and stop.
    fragment = fragment.slice(0, inner.index);
    if (!fragment) return null;
    return { piece: { line: lineIndex, start: from, end: from + fragment.length }, more: false, endCol: 0, borderCol: -1 };
  }
  const end = from + fragment.length;
  if (!ONLY_TRAILING_DECOR.test(line.text.slice(end))) return null;
  const endCol = columnOf(rows, line, end - 1);
  // Inside a box a row is full only when the text reaches the border of the
  // same box as the previous row; a short row followed by a border is just
  // padding.
  if (fragment.startsWith("[")) return null;
  const borderIdx = borderIndexAfter(line, from, end);
  const borderCol = borderIdx >= 0 ? columnOf(rows, line, borderIdx) : -1;
  const full =
    endCol >= cols - 2 ||
    sameWidth(endCol, state.prevEndCol) ||
    (borderCol >= 0 && state.prevBorderCol >= 0 && Math.abs(borderCol - state.prevBorderCol) <= 1);
  if (full) return { piece: { line: lineIndex, start: from, end }, more: true, endCol, borderCol };
  if (!/[^A-Za-z]/.test(fragment)) {
    // Letters only is usually prose; a query URL followed by a blank line, a
    // prompt or a box edge is the exception.
    const clean = state.following === undefined || CLEAN_CONTEXT.test(state.following);
    if (!(state.bare && state.hasQuery && clean)) return null;
    return { piece: { line: lineIndex, start: from, end }, more: false, endCol, borderCol };
  }
  // A short tail is the weakest evidence: a lone number is not a URL tail, and
  // after a row that also holds prose it must look like a query string.
  if (/^\d{1,3}$/.test(fragment) && !/[\d/=]/.test(state.lastChar)) return WEAK_TAIL;
  if (!/^\d{1,3}$/.test(fragment) && !state.bare && !state.chained && !QUERY_EVIDENCE.test(fragment)) return WEAK_TAIL;
  return { piece: { line: lineIndex, start: from, end }, more: false, endCol, borderCol };
}

function trimLength(url: string): number {
  let n = url.length;
  for (;;) {
    const ch = url[n - 1];
    const body = url.slice(0, n);
    if (ch && ".,;:!?'\"".includes(ch)) {
      n--;
    } else if (ch === ")" && count(body, ")") > count(body, "(")) {
      n--;
    } else if (ch === "]" && count(body, "]") > count(body, "[")) {
      n--;
    } else if (ch === "}" && count(body, "}") > count(body, "{")) {
      n--;
    } else {
      return n;
    }
  }
}

function count(text: string, ch: string): number {
  let n = 0;
  for (let i = text.indexOf(ch); i >= 0; i = text.indexOf(ch, i + 1)) n++;
  return n;
}

export function findLinksInRows(rows: TextRow[], cols: number): StitchedLink[] {
  const lines = buildLogicalLines(rows);
  const links: StitchedLink[] = [];
  for (let li = 0; li < lines.length; li++) {
    const line = lines[li];
    if (!/https?:\/\//i.test(line.text)) continue;
    URL_START.lastIndex = 0;
    for (let m = URL_START.exec(line.text); m; m = URL_START.exec(line.text)) {
      const pieces: Piece[] = [{ line: li, start: m.index, end: m.index + m[0].length }];
      let total = m[0].length;
      let cur = li;
      const urlEnd = m.index + m[0].length;
      const endCol = columnOf(rows, line, urlEnd - 1);
      const tail = line.text.slice(urlEnd);
      const borderIdx = borderIndexAfter(line, m.index, urlEnd);
      const state: ChainState = {
        prevBorderCol: borderIdx >= 0 ? columnOf(rows, line, borderIdx) : -1,
        bare: LEFT_PREFIX_ONLY.test(line.text.slice(0, m.index)),
        chained: false,
        prevEndCol: endCol,
        hasQuery: m[0].includes("?"),
        lastChar: m[0].slice(-1),
        following: undefined,
      };
      // Whether the previous row of the chain is known to be a full row, and
      // the end column of the row before it.
      let prevFull = borderIdx >= 0 || endCol >= cols - 2;
      let prevPrevEnd = -1;
      const maxRows = maxContinuationRows(cols);
      // A URL that would continue past a limit, or into a tail too weak to join, is dropped: a truncated address
      // is worse than an unrecognised one.
      let overflow = false;
      for (let n = 0; ONLY_TRAILING_DECOR.test(tail) && cur + 1 < lines.length; n++) {
        state.following = lines[cur + 2]?.text;
        const next = continuationFragment(rows, lines[cur + 1], cur + 1, cols, state);
        if (next === WEAK_TAIL) {
          if (prevFull) overflow = true;
          break;
        }
        if (!next) break;
        // A row that is not full by width is still full when it is as wide as
        // its neighbours (the program wrapped at a different width).
        if (!prevFull && !sameWidth(state.prevEndCol, next.endCol) && !sameWidth(state.prevEndCol, prevPrevEnd)) break;
        const len = next.piece.end - next.piece.start;
        if (n >= maxRows || total + len > MAX_LINK_LENGTH) {
          overflow = true;
          break;
        }
        pieces.push(next.piece);
        total += len;
        cur++;
        if (!next.more) break;
        prevFull = true;
        prevPrevEnd = state.prevEndCol;
        state.prevEndCol = next.endCol;
        state.prevBorderCol = next.borderCol;
        state.chained = true;
        state.lastChar = lines[next.piece.line].text[next.piece.end - 1];
        state.hasQuery ||= lines[next.piece.line].text.slice(next.piece.start, next.piece.end).includes("?");
      }
      if (overflow) continue;
      const built = finishLink(lines, pieces);
      if (built) links.push(built);
    }
  }
  return links;
}

function finishLink(lines: LogicalLine[], pieces: Piece[]): StitchedLink | null {
  let url = pieces.map((p) => lines[p.line].text.slice(p.start, p.end)).join("");
  const keep = trimLength(url);
  let drop = url.length - keep;
  url = url.slice(0, keep);
  while (drop > 0 && pieces.length > 0) {
    const last = pieces[pieces.length - 1];
    const size = last.end - last.start;
    if (size <= drop) {
      drop -= size;
      pieces.pop();
    } else {
      last.end -= drop;
      drop = 0;
    }
  }
  if (pieces.length === 0) return null;
  try {
    new URL(url);
  } catch {
    return null;
  }
  if (planTerminalLink(url).kind === "blocked") return null;
  const segments = pieces.flatMap((p) => segmentsOf(lines[p.line], p.start, p.end, p.line));
  return { url, segments };
}

const FILE_EXTENSIONS = new Set(
  "go ts tsx js jsx mjs cjs py rs c h cc cpp hpp cs java kt swift rb php vue css scss html xml json yaml yml toml ini conf md txt log sh sql lock".split(" "),
);
const IPV4 = /(?<![\w.])(\d{1,3}(?:\.\d{1,3}){3})(?::(\d{1,5}))?(?!\w|\.\d)/g;
const IPV6_PORT = /\[([0-9a-fA-F:.]*:[0-9a-fA-F:.]*)\]:(\d{1,5})(?!\w)/g;
const HOST_PORT = /(?<![\w.@/:-])((?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}|localhost):(\d{1,5})(?!\w)/gi;

function validPort(text: string | undefined): number | undefined {
  if (!text) return undefined;
  const port = Number(text);
  return port >= 1 && port <= 65535 ? port : undefined;
}

function overlaps(a: LinkRowSegment, links: StitchedLink[]): boolean {
  return links.some((l) => l.segments.some((s) => s.row === a.row && a.start < s.end && s.start < a.end));
}

export function findTargetsInRows(rows: TextRow[], cols: number, links?: StitchedLink[]): DetectedTarget[] {
  const urlLinks = links ?? findLinksInRows(rows, cols);
  const found: DetectedTarget[] = [];
  const add = (line: LogicalLine, index: number, text: string, kind: "ip" | "hostport", host: string, port?: number) => {
    const slot = rowSlot(line, index);
    const rowStart = line.offsets[slot];
    const rowEnd = slot + 1 < line.offsets.length ? line.offsets[slot + 1] : line.text.length;
    if (index + text.length > rowEnd) return;
    const segment = { row: line.rows[slot], start: index - rowStart, end: index - rowStart + text.length };
    if (overlaps(segment, urlLinks)) return;
    if (found.some((f) => f.segment.row === segment.row && segment.start < f.segment.end && f.segment.start < segment.end)) return;
    found.push({ kind, text, host, ...(port === undefined ? {} : { port }), segment });
  };
  for (const line of buildLogicalLines(rows)) {
    const t = line.text;
    if (!/[:.]/.test(t)) continue;
    for (const m of t.matchAll(IPV4)) {
      const octets = m[1].split(".");
      if (octets.some((o) => Number(o) > 255)) continue;
      const port = validPort(m[2]);
      if (m[2] && port === undefined) {
        add(line, m.index, m[1], "ip", m[1]);
      } else if (port !== undefined) {
        add(line, m.index, m[0], "hostport", m[1], port);
      } else {
        add(line, m.index, m[1], "ip", m[1]);
      }
    }
    for (const m of t.matchAll(IPV6_PORT)) {
      const port = validPort(m[2]);
      if (port !== undefined) add(line, m.index, m[0], "hostport", m[1], port);
    }
    for (const m of t.matchAll(HOST_PORT)) {
      const port = validPort(m[2]);
      if (port === undefined) continue;
      const host = m[1];
      if (host.toLowerCase() !== "localhost") {
        const ext = host.slice(host.lastIndexOf(".") + 1).toLowerCase();
        if (FILE_EXTENSIONS.has(ext)) continue;
      }
      add(line, m.index, m[0], "hostport", host, port);
    }
  }
  return found.sort((a, b) => a.segment.row - b.segment.row || a.segment.start - b.segment.start);
}

export interface TerminalLinkProviderHandlers {
  activate(event: MouseEvent, uri: string): void;
  hover(event: MouseEvent, uri: string): void;
  leave(event: MouseEvent, uri: string): void;
  onTargetHover?(target: DetectedTarget | null): void;
}

function lineAt(term: Terminal, row: number): IBufferLine | undefined {
  return term.buffer.active.getLine(row);
}

export function windowBounds(term: Terminal, y0: number): { first: number; last: number } {
  const buf = term.buffer.active;
  const WINDOW_LOGICAL_LINES = maxContinuationRows(term.cols);
  const WINDOW_MAX_PHYSICAL_ROWS = WINDOW_LOGICAL_LINES * 2 + CONTEXT_PHYSICAL_SLACK;
  const wrappedAt = (r: number) => lineAt(term, r)?.isWrapped === true;
  let first = y0;
  for (let seen = 0; ; seen++) {
    while (first > 0 && wrappedAt(first) && y0 - first < WINDOW_MAX_PHYSICAL_ROWS) first--;
    if (seen >= WINDOW_LOGICAL_LINES || first <= 0 || y0 - first >= WINDOW_MAX_PHYSICAL_ROWS) break;
    first--;
  }
  let last = y0;
  for (let seen = 0; ; seen++) {
    while (last + 1 < buf.length && wrappedAt(last + 1) && last - y0 < WINDOW_MAX_PHYSICAL_ROWS) last++;
    // One line more than a URL can span, so the line after its last row is seen.
    if (seen >= WINDOW_LOGICAL_LINES + 1 || last + 1 >= buf.length || last - y0 >= WINDOW_MAX_PHYSICAL_ROWS) break;
    last++;
  }
  return { first, last };
}

/** Maps string indices of translateToString(true) to 0-based cell columns, with the cell width of each. */
export function cellMap(line: IBufferLine): { col: number[]; width: number[] } {
  const col: number[] = [];
  const width: number[] = [];
  for (let x = 0; x < line.length; x++) {
    const cell = line.getCell(x);
    if (!cell) break;
    const w = cell.getWidth();
    if (w === 0) continue;
    const chars = cell.getChars() || " ";
    for (let i = 0; i < chars.length; i++) {
      col.push(x);
      width.push(w);
    }
  }
  return { col, width };
}

export function cellRange(map: { col: number[]; width: number[] }, seg: LinkRowSegment, y: number): IBufferRange {
  const startCell = map.col[seg.start] ?? seg.start;
  const lastIdx = seg.end - 1;
  const endCell = (map.col[lastIdx] ?? lastIdx) + (map.width[lastIdx] ?? 1);
  return { start: { x: startCell + 1, y }, end: { x: endCell, y } };
}

/** Splits segments into soft-wrapped runs; hard-wrapped and boxed pieces stay separate. */
export function softRuns(segments: LinkRowSegment[]): LinkRowSegment[][] {
  const runs: LinkRowSegment[][] = [];
  for (const seg of segments) {
    const run = runs[runs.length - 1];
    if (run && seg.group !== undefined && run[0].group === seg.group) run.push(seg);
    else runs.push([seg]);
  }
  return runs;
}

export function createTerminalLinkProvider(term: Terminal, handlers: TerminalLinkProviderHandlers): ILinkProvider {
  return {
    provideLinks(y, callback) {
      const row0 = y - 1;
      if (!lineAt(term, row0)) {
        callback(undefined);
        return;
      }
      const { first, last } = windowBounds(term, row0);
      const rows: TextRow[] = [];
      for (let r = first; r <= last; r++) {
        const line = lineAt(term, r);
        rows.push({ text: line?.translateToString(true) ?? "", isWrapped: r > first && line?.isWrapped === true });
      }
      const idx = row0 - first;
      const cols = term.cols;
      const links = findLinksInRows(rows, cols);
      const targets = handlers.onTargetHover ? findTargetsInRows(rows, cols, links) : [];
      const result: ILink[] = [];
      const maps = new Map<number, ReturnType<typeof cellMap>>();
      const mapOfRow = (row: number) => {
        let map = maps.get(row);
        if (!map) {
          map = cellMap(lineAt(term, first + row)!);
          maps.set(row, map);
        }
        return map;
      };
      for (const link of links) {
        for (const run of softRuns(link.segments)) {
          if (!run.some((seg) => seg.row === idx)) continue;
          // A soft-wrapped URL is one range, so the underline covers all of it.
          const head = run[0];
          const tail = run[run.length - 1];
          result.push({
            range: {
              start: cellRange(mapOfRow(head.row), head, first + head.row + 1).start,
              end: cellRange(mapOfRow(tail.row), tail, first + tail.row + 1).end,
            },
            text: link.url,
            decorations: { underline: true, pointerCursor: true },
            activate: (event, text) => handlers.activate(event, text),
            hover: (event, text) => handlers.hover(event, text),
            leave: (event, text) => handlers.leave(event, text),
          });
        }
      }
      for (const target of targets) {
        if (target.segment.row !== idx) continue;
        result.push({
          range: cellRange(mapOfRow(idx), target.segment, y),
          text: target.text,
          decorations: { underline: false, pointerCursor: false },
          activate: () => {},
          hover: () => handlers.onTargetHover?.(target),
          leave: () => handlers.onTargetHover?.(null),
        });
      }
      callback(result.length > 0 ? result : undefined);
    },
  };
}

export interface ExtractedLink {
  url: string;
  context: string;
  row: number;
}

export function extractTerminalLinks(term: Terminal, maxScrollbackRows = 2000): ExtractedLink[] {
  const buf = term.buffer.active;
  const start = Math.max(0, buf.length - term.rows - maxScrollbackRows);
  const rows: TextRow[] = [];
  for (let r = start; r < buf.length; r++) {
    const line = buf.getLine(r);
    rows.push({ text: line?.translateToString(true) ?? "", isWrapped: r > start && line?.isWrapped === true });
  }
  const links = findLinksInRows(rows, term.cols);
  const seen = new Set<string>();
  const out: ExtractedLink[] = [];
  for (let i = links.length - 1; i >= 0; i--) {
    const link = links[i];
    if (seen.has(link.url)) continue;
    seen.add(link.url);
    const first = link.segments[0].row;
    out.push({ url: link.url, context: cleanTerminalLink(rows[first].text).trim().slice(0, CONTEXT_LIMIT), row: start + first });
  }
  return out;
}
