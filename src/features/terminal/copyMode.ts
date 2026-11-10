import type { IBuffer, IBufferCell, IDisposable, IMarker, Terminal } from "@xterm/xterm";
import { matchKeybinding, matchTerminalWithheldKeybinding } from "../../app/keybindings";
import { isImeKeyEvent } from "../../ui/imeKeyEvent";
import { t } from "../../i18n";

// Keyboard copy mode (WezTerm CopyMode, tmux copy-mode): a cursor over the active buffer moves with vi keys, selects
// text and copies it, while every key and paste stays away from the program in the terminal.

export interface CopyPoint {
  x: number;
  y: number;
}

export type CopySelectionKind = "none" | "char" | "line" | "block";

export interface CopyModeState {
  active: boolean;
  cursor: CopyPoint;
  kind: CopySelectionKind;
  anchor: CopyPoint | null;
  /** Open while the search prompt takes input; direction is the one "/" (forward) or "?" (backward) chose. */
  prompt: { direction: "forward" | "backward" } | null;
  message: string | null;
}

export interface CopyModeController {
  state: () => CopyModeState;
  subscribe: (listener: () => void) => () => void;
  isActive: () => boolean;
  enter: () => void;
  exit: () => void;
  /** Runs a search from the prompt and closes it; an empty query closes the prompt only. */
  submitSearch: (query: string) => void;
  cancelSearch: () => void;
  /** The text the current selection copies, or "" when nothing is selected. */
  selectedText: () => string;
  /** Rows (absolute) and column spans of the block selection, which xterm cannot draw itself. */
  blockRects: () => { y: number; x0: number; x1: number }[];
  dispose: () => void;
}

export interface CopyModeOptions {
  /** Writes the copied text to the clipboard; the result reports success or failure to the user. */
  copy: (text: string) => Promise<void>;
  /** Whether the copy mode chord may enter copy mode (false on mobile shells). */
  available?: () => boolean;
  /** Restores keyboard focus to the terminal after the search prompt closes. */
  focus?: () => void;
}

const FOCUS_REPORTS = new Set(["\x1b[I", "\x1b[O"]);

/** Whether terminal input may reach the program while copy mode is active: only focus reports pass. */
export function copyModePassesInput(data: string): boolean {
  return FOCUS_REPORTS.has(data);
}

// Character classes for word motions: blank, punctuation, word, and wide (CJK) text, which vim also keeps apart.
const BLANK = 0;
const PUNCT = 1;
const WORD = 2;
const WIDE = 3;

function cellAt(buffer: IBuffer, x: number, y: number, scratch: IBufferCell): IBufferCell | undefined {
  return buffer.getLine(y)?.getCell(x, scratch);
}

/** The start column of the cell covering x: the trailing half of a wide character maps to its first column. */
export function snapColumn(buffer: IBuffer, x: number, y: number): number {
  const scratch = buffer.getNullCell();
  let col = x;
  while (col > 0 && cellAt(buffer, col, y, scratch)?.getWidth() === 0) col -= 1;
  return col;
}

function cellWidth(buffer: IBuffer, x: number, y: number, scratch: IBufferCell): number {
  return Math.max(1, cellAt(buffer, x, y, scratch)?.getWidth() ?? 1);
}

function classAt(buffer: IBuffer, p: CopyPoint, scratch: IBufferCell): number {
  const cell = cellAt(buffer, p.x, p.y, scratch);
  const ch = cell?.getChars() ?? "";
  if (ch === "" || /^\s+$/.test(ch)) return BLANK;
  if (/^[A-Za-z0-9_]$/.test(ch)) return WORD;
  if (ch.length === 1 && ch.charCodeAt(0) < 0x80) return PUNCT;
  return (cell?.getWidth() ?? 1) > 1 ? WIDE : WORD;
}

function isWrapped(buffer: IBuffer, y: number): boolean {
  return buffer.getLine(y)?.isWrapped === true;
}

/** The first row of the logical (unwrapped) line holding row y. */
export function logicalLineStart(buffer: IBuffer, y: number): number {
  let row = y;
  while (row > 0 && isWrapped(buffer, row)) row -= 1;
  return row;
}

/** The last row of the logical (unwrapped) line holding row y. */
export function logicalLineEnd(buffer: IBuffer, y: number): number {
  let row = y;
  while (row + 1 < buffer.length && isWrapped(buffer, row + 1)) row += 1;
  return row;
}

interface Step {
  p: CopyPoint;
  /** The step crossed a hard line break, which word motions treat as blank. */
  broke: boolean;
}

function stepForward(buffer: IBuffer, cols: number, p: CopyPoint, scratch: IBufferCell): Step | null {
  const nx = p.x + cellWidth(buffer, p.x, p.y, scratch);
  if (nx < cols) return { p: { x: nx, y: p.y }, broke: false };
  if (p.y + 1 >= buffer.length) return null;
  return { p: { x: 0, y: p.y + 1 }, broke: !isWrapped(buffer, p.y + 1) };
}

function stepBackward(buffer: IBuffer, cols: number, p: CopyPoint): Step | null {
  if (p.x > 0) return { p: { x: snapColumn(buffer, p.x - 1, p.y), y: p.y }, broke: false };
  if (p.y === 0) return null;
  return { p: { x: snapColumn(buffer, cols - 1, p.y - 1), y: p.y - 1 }, broke: !isWrapped(buffer, p.y) };
}

/** vim "w": the start of the next word, across wrapped and hard line breaks. */
export function nextWordStart(buffer: IBuffer, cols: number, from: CopyPoint): CopyPoint {
  const scratch = buffer.getNullCell();
  let cur = from;
  const cls = classAt(buffer, cur, scratch);
  if (cls !== BLANK) {
    for (;;) {
      const s = stepForward(buffer, cols, cur, scratch);
      if (!s) return cur;
      cur = s.p;
      if (s.broke || classAt(buffer, cur, scratch) !== cls) break;
    }
  }
  while (classAt(buffer, cur, scratch) === BLANK) {
    const s = stepForward(buffer, cols, cur, scratch);
    if (!s) return cur;
    cur = s.p;
  }
  return cur;
}

/** vim "e": the end of the current or next word. */
export function wordEnd(buffer: IBuffer, cols: number, from: CopyPoint): CopyPoint {
  const scratch = buffer.getNullCell();
  const first = stepForward(buffer, cols, from, scratch);
  if (!first) return from;
  let cur = first.p;
  while (classAt(buffer, cur, scratch) === BLANK) {
    const s = stepForward(buffer, cols, cur, scratch);
    if (!s) return cur;
    cur = s.p;
  }
  const cls = classAt(buffer, cur, scratch);
  for (;;) {
    const s = stepForward(buffer, cols, cur, scratch);
    if (!s || s.broke || classAt(buffer, s.p, scratch) !== cls) return cur;
    cur = s.p;
  }
}

/** vim "b": the start of the current or previous word. */
export function previousWordStart(buffer: IBuffer, cols: number, from: CopyPoint): CopyPoint {
  const scratch = buffer.getNullCell();
  const first = stepBackward(buffer, cols, from);
  if (!first) return from;
  let cur = first.p;
  while (classAt(buffer, cur, scratch) === BLANK) {
    const s = stepBackward(buffer, cols, cur);
    if (!s) return cur;
    cur = s.p;
  }
  const cls = classAt(buffer, cur, scratch);
  for (;;) {
    // A step back across a hard break leaves this line; the break comes from the row being left.
    const s = stepBackward(buffer, cols, cur);
    if (!s || s.broke || classAt(buffer, s.p, scratch) !== cls) return cur;
    cur = s.p;
  }
}

/** "^": the first non-blank cell of the logical line, or its start when the line is blank. */
export function firstNonBlank(buffer: IBuffer, cols: number, y: number): CopyPoint {
  const scratch = buffer.getNullCell();
  const start = logicalLineStart(buffer, y);
  const end = logicalLineEnd(buffer, y);
  let cur: CopyPoint = { x: 0, y: start };
  for (;;) {
    if (classAt(buffer, cur, scratch) !== BLANK) return cur;
    const s = stepForward(buffer, cols, cur, scratch);
    if (!s || s.p.y > end) return { x: 0, y: start };
    cur = s.p;
  }
}

/** "$": the last non-blank cell of the logical line, or its start when the line is blank. */
export function lastNonBlank(buffer: IBuffer, cols: number, y: number): CopyPoint {
  const scratch = buffer.getNullCell();
  const start = logicalLineStart(buffer, y);
  for (let row = logicalLineEnd(buffer, y); row >= start; row -= 1) {
    for (let x = cols - 1; x >= 0; x -= 1) {
      const p = { x: snapColumn(buffer, x, row), y: row };
      if (classAt(buffer, p, scratch) !== BLANK) return p;
      x = p.x;
    }
  }
  return { x: 0, y: start };
}

interface LineText {
  text: string;
  /** The buffer cell each UTF-16 unit of text came from. */
  points: CopyPoint[];
}

function logicalLineText(buffer: IBuffer, cols: number, start: number): LineText {
  const scratch = buffer.getNullCell();
  const end = logicalLineEnd(buffer, start);
  let text = "";
  const points: CopyPoint[] = [];
  for (let y = start; y <= end; y += 1) {
    const line = buffer.getLine(y);
    if (!line) break;
    for (let x = 0; x < cols; x += 1) {
      const cell = line.getCell(x, scratch);
      if (!cell || cell.getWidth() === 0) continue;
      const chars = cell.getChars() || " ";
      for (let i = 0; i < chars.length; i += 1) points.push({ x, y });
      text += chars;
    }
  }
  return { text, points };
}

function before(a: CopyPoint, b: CopyPoint): boolean {
  return a.y < b.y || (a.y === b.y && a.x < b.x);
}

/**
 * Finds the next match of query after (forward) or before (backward) the point, wrapping around the buffer. Smartcase:
 * a query without capitals matches any case.
 */
export function searchBuffer(
  buffer: IBuffer,
  cols: number,
  query: string,
  from: CopyPoint,
  direction: "forward" | "backward",
): CopyPoint | null {
  if (!query) return null;
  const fold = query === query.toLowerCase();
  const needle = fold ? query.toLowerCase() : query;
  const lines: LineText[] = [];
  for (let y = 0; y < buffer.length; y = logicalLineEnd(buffer, y) + 1) {
    const line = logicalLineText(buffer, cols, y);
    if (fold) line.text = line.text.toLowerCase();
    lines.push(line);
  }
  const matches: CopyPoint[] = [];
  for (const line of lines) {
    for (let i = line.text.indexOf(needle); i >= 0; i = line.text.indexOf(needle, i + 1)) {
      matches.push(line.points[i]);
    }
  }
  if (matches.length === 0) return null;
  if (direction === "forward") return matches.find((m) => before(from, m)) ?? matches[0];
  for (let i = matches.length - 1; i >= 0; i -= 1) if (before(matches[i], from)) return matches[i];
  return matches[matches.length - 1];
}

/** The text of a block (rectangular) selection: each row's columns x0..x1, trailing blanks trimmed. */
export function blockText(buffer: IBuffer, from: CopyPoint, to: CopyPoint): string {
  const scratch = buffer.getNullCell();
  const y0 = Math.min(from.y, to.y);
  const y1 = Math.max(from.y, to.y);
  const left = Math.min(from.x, to.x);
  const right = Math.max(from.x, to.x);
  const rows: string[] = [];
  for (let y = y0; y <= y1; y += 1) {
    const x0 = snapColumn(buffer, left, y);
    const x1 = snapColumn(buffer, right, y) + cellWidth(buffer, snapColumn(buffer, right, y), y, scratch);
    rows.push(buffer.getLine(y)?.translateToString(true, x0, x1) ?? "");
  }
  return rows.join("\n");
}

export function createCopyMode(term: Terminal, host: HTMLElement, options: CopyModeOptions): CopyModeController {
  const listeners = new Set<() => void>();
  // Positions are kept relative to a marker so lines trimmed off the top of a full scrollback do not shift them.
  let marker: IMarker | undefined;
  let rel = { cursor: { x: 0, y: 0 }, anchor: null as CopyPoint | null };
  let desiredX = 0;
  let lastSearch: { query: string; direction: "forward" | "backward" } | null = null;
  let current: CopyModeState = { active: false, cursor: { x: 0, y: 0 }, kind: "none", anchor: null, prompt: null, message: null };
  const disposables: IDisposable[] = [];

  const buffer = () => term.buffer.active;
  const base = () => (marker && !marker.isDisposed ? marker.line : 0);
  const clampPoint = (p: CopyPoint): CopyPoint => {
    const y = Math.min(Math.max(0, p.y), Math.max(0, buffer().length - 1));
    const x = Math.min(Math.max(0, p.x), term.cols - 1);
    return { x: snapColumn(buffer(), x, y), y };
  };
  const absolute = (p: CopyPoint): CopyPoint => clampPoint({ x: p.x, y: p.y + base() });
  const relative = (p: CopyPoint): CopyPoint => ({ x: p.x, y: p.y - base() });

  const emit = () => {
    for (const listener of listeners) listener();
  };
  const publish = (patch: Partial<CopyModeState> = {}) => {
    current = {
      ...current,
      cursor: absolute(rel.cursor),
      anchor: rel.anchor ? absolute(rel.anchor) : null,
      ...patch,
    };
    emit();
  };

  const applySelection = () => {
    const { kind } = current;
    const anchor = rel.anchor ? absolute(rel.anchor) : null;
    const cursor = absolute(rel.cursor);
    if (kind === "none" || kind === "block" || !anchor) {
      term.clearSelection();
      return;
    }
    const buf = buffer();
    if (kind === "line") {
      term.selectLines(logicalLineStart(buf, Math.min(anchor.y, cursor.y)), logicalLineEnd(buf, Math.max(anchor.y, cursor.y)));
      return;
    }
    const [start, end] = before(cursor, anchor) ? [cursor, anchor] : [anchor, cursor];
    const scratch = buf.getNullCell();
    const length = (end.y - start.y) * term.cols + end.x + cellWidth(buf, end.x, end.y, scratch) - start.x;
    term.select(start.x, start.y, length);
  };

  const follow = (p: CopyPoint) => {
    const top = buffer().viewportY;
    if (p.y < top) term.scrollToLine(p.y);
    else if (p.y >= top + term.rows) term.scrollToLine(p.y - term.rows + 1);
  };

  const moveTo = (p: CopyPoint, keepColumn = false) => {
    const target = clampPoint(p);
    if (!keepColumn) desiredX = target.x;
    rel.cursor = relative(target);
    follow(target);
    applySelection();
    publish({ message: null });
  };

  const setKind = (kind: CopySelectionKind) => {
    const next = current.kind === kind ? "none" : kind;
    if (next === "none") rel.anchor = null;
    else if (current.kind === "none" || !rel.anchor) rel.anchor = { ...rel.cursor };
    current = { ...current, kind: next };
    applySelection();
    publish({ message: null });
  };

  const selectedText = (): string => {
    if (!current.active || current.kind === "none" || !rel.anchor) return "";
    if (current.kind === "block") return blockText(buffer(), absolute(rel.anchor), absolute(rel.cursor));
    return term.getSelection();
  };

  const exit = () => {
    if (!current.active) return;
    term.clearSelection();
    for (const d of disposables.splice(0)) d.dispose();
    marker?.dispose();
    marker = undefined;
    rel = { cursor: { x: 0, y: 0 }, anchor: null };
    current = { active: false, cursor: { x: 0, y: 0 }, kind: "none", anchor: null, prompt: null, message: null };
    term.scrollToBottom();
    emit();
  };

  const yank = () => {
    const text = selectedText();
    if (!text) {
      publish({ message: t("先按 v 开始选择") });
      return;
    }
    // The clipboard result is reported by the caller; copy mode ends either way, like tmux and WezTerm.
    void options.copy(text);
    exit();
  };

  const enter = () => {
    if (current.active) return;
    const buf = buffer();
    term.clearSelection();
    marker = term.registerMarker(0);
    // Trimming the anchor off a full scrollback loses the reference for every position: end copy mode.
    if (marker) disposables.push(marker.onDispose(() => exit()));
    // A full reset (RIS) clears the screen under the cursor; the sequence still reaches the default handler.
    if (term.parser?.registerEscHandler) {
      disposables.push(
        term.parser.registerEscHandler({ final: "c" }, () => {
          exit();
          return false;
        }),
      );
    }
    // Watched only while active: switching to or from the alternate screen ends copy mode, a resize re-clamps.
    disposables.push(term.buffer.onBufferChange(() => exit()));
    disposables.push(
      term.onResize(() => {
        rel.cursor = relative(absolute(rel.cursor));
        if (rel.anchor) rel.anchor = relative(absolute(rel.anchor));
        applySelection();
        publish();
      }),
    );
    const cursor = clampPoint({ x: buf.cursorX, y: buf.baseY + buf.cursorY });
    desiredX = cursor.x;
    rel = { cursor: relative(cursor), anchor: null };
    current = { ...current, active: true, kind: "none", prompt: null, message: null };
    follow(cursor);
    publish();
  };

  const runSearch = (query: string, direction: "forward" | "backward") => {
    lastSearch = { query, direction };
    const found = searchBuffer(buffer(), term.cols, query, absolute(rel.cursor), direction);
    if (!found) {
      publish({ message: t("未找到「{query}」", { query }) });
      return;
    }
    moveTo(found);
  };

  const halfPage = () => Math.max(1, Math.floor(term.rows / 2));
  const scrollAndMove = (delta: number) => {
    const row = clampPoint({ x: 0, y: absolute(rel.cursor).y + delta }).y;
    term.scrollLines(delta);
    moveTo({ x: snapColumn(buffer(), Math.min(desiredX, term.cols - 1), row), y: row }, true);
  };

  const handleCopyKey = (event: KeyboardEvent): void => {
    const buf = buffer();
    const cursor = absolute(rel.cursor);
    const cols = term.cols;
    const ctrl = event.ctrlKey && !event.metaKey && !event.altKey;
    // AltGr reports ctrl+alt on Windows: a printable character from it is a plain key.
    const altGr = event.ctrlKey && event.altKey && !event.metaKey && event.key.length === 1 && event.getModifierState?.("AltGraph") === true;
    const plain = altGr || (!event.ctrlKey && !event.metaKey && !event.altKey);
    const vertical = (dy: number) => {
      const y = cursor.y + dy;
      const row = clampPoint({ x: 0, y }).y;
      moveTo({ x: snapColumn(buf, Math.min(desiredX, cols - 1), row), y: row }, true);
    };
    if (ctrl) {
      const key = event.key.toLowerCase();
      if (key === "u") scrollAndMove(-halfPage());
      else if (key === "d") scrollAndMove(halfPage());
      else if (key === "b") scrollAndMove(-term.rows);
      else if (key === "f") scrollAndMove(term.rows);
      else if (key === "v") setKind("block");
      else if (key === "c" || key === "[") exit();
      return;
    }
    switch (event.key) {
      case "ArrowLeft":
        if (cursor.x > 0) moveTo({ x: cursor.x - 1, y: cursor.y });
        return;
      case "ArrowRight":
        moveTo({ x: Math.min(cols - 1, cursor.x + cellWidth(buf, cursor.x, cursor.y, buf.getNullCell())), y: cursor.y });
        return;
      case "ArrowUp":
        vertical(-1);
        return;
      case "ArrowDown":
        vertical(1);
        return;
      case "PageUp":
        scrollAndMove(-term.rows);
        return;
      case "PageDown":
        scrollAndMove(term.rows);
        return;
      case "Home":
        moveTo({ x: 0, y: logicalLineStart(buf, cursor.y) });
        return;
      case "End":
        moveTo(lastNonBlank(buf, cols, cursor.y));
        return;
      case "Escape":
        exit();
        return;
      case "Enter":
        yank();
        return;
    }
    if (!plain) return;
    switch (event.key) {
      case "h":
        if (cursor.x > 0) moveTo({ x: cursor.x - 1, y: cursor.y });
        return;
      case "l":
        moveTo({ x: Math.min(cols - 1, cursor.x + cellWidth(buf, cursor.x, cursor.y, buf.getNullCell())), y: cursor.y });
        return;
      case "j":
        vertical(1);
        return;
      case "k":
        vertical(-1);
        return;
      case "w":
        moveTo(nextWordStart(buf, cols, cursor));
        return;
      case "b":
        moveTo(previousWordStart(buf, cols, cursor));
        return;
      case "e":
        moveTo(wordEnd(buf, cols, cursor));
        return;
      case "0":
        moveTo({ x: 0, y: logicalLineStart(buf, cursor.y) });
        return;
      case "^":
        moveTo(firstNonBlank(buf, cols, cursor.y));
        return;
      case "$":
        moveTo(lastNonBlank(buf, cols, cursor.y));
        return;
      case "g":
        moveTo({ x: 0, y: 0 });
        return;
      case "G":
        moveTo({ x: 0, y: buf.length - 1 });
        return;
      case "v":
        setKind("char");
        return;
      case "V":
        setKind("line");
        return;
      case "y":
        yank();
        return;
      case "q":
        exit();
        return;
      case "/":
        publish({ prompt: { direction: "forward" }, message: null });
        return;
      case "?":
        publish({ prompt: { direction: "backward" }, message: null });
        return;
      case "n":
      case "N":
        if (!lastSearch) return;
        {
          // n repeats the search in its own direction and N reverses it; both keep the direction "/" or "?" set.
          const { query, direction } = lastSearch;
          runSearch(query, (event.key === "n") === (direction === "forward") ? "forward" : "backward");
          lastSearch = { query, direction };
        }
        return;
    }
  };

  const onKey = (event: KeyboardEvent) => {
    if (event.type === "keydown" && !event.repeat && matchKeybinding(event, "terminalCopyMode") && !isImeKeyEvent(event)) {
      if (current.active) {
        event.preventDefault();
        event.stopPropagation();
        exit();
        return;
      }
      if (options.available?.() === false) return;
      event.preventDefault();
      event.stopPropagation();
      enter();
      return;
    }
    if (!current.active) return;
    // Other app shortcuts keep working: xterm hands them on unhandled and they bubble to the window.
    if (matchTerminalWithheldKeybinding(event)) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.type !== "keydown" || isImeKeyEvent(event)) return;
    handleCopyKey(event);
  };
  const block = (event: Event) => {
    if (!current.active) return;
    event.preventDefault();
    event.stopImmediatePropagation();
  };

  host.addEventListener("keydown", onKey, true);
  host.addEventListener("keypress", onKey, true);
  host.addEventListener("keyup", onKey, true);
  host.addEventListener("paste", block, true);
  host.addEventListener("drop", block, true);

  return {
    state: () => current,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    isActive: () => current.active,
    enter,
    exit,
    submitSearch: (query) => {
      const direction = current.prompt?.direction ?? "forward";
      publish({ prompt: null });
      options.focus?.();
      if (query) runSearch(query, direction);
    },
    cancelSearch: () => {
      publish({ prompt: null });
      options.focus?.();
    },
    selectedText,
    blockRects: () => {
      if (!current.active || current.kind !== "block" || !rel.anchor) return [];
      const a = absolute(rel.anchor);
      const c = absolute(rel.cursor);
      const scratch = buffer().getNullCell();
      const rects: { y: number; x0: number; x1: number }[] = [];
      for (let y = Math.min(a.y, c.y); y <= Math.max(a.y, c.y); y += 1) {
        const x0 = snapColumn(buffer(), Math.min(a.x, c.x), y);
        const right = snapColumn(buffer(), Math.max(a.x, c.x), y);
        rects.push({ y, x0, x1: right + cellWidth(buffer(), right, y, scratch) });
      }
      return rects;
    },
    dispose: () => {
      exit();
      host.removeEventListener("keydown", onKey, true);
      host.removeEventListener("keypress", onKey, true);
      host.removeEventListener("keyup", onKey, true);
      host.removeEventListener("paste", block, true);
      host.removeEventListener("drop", block, true);
      listeners.clear();
    },
  };
}
