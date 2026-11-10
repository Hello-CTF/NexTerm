import type { IDisposable, IMarker, Terminal } from "@xterm/xterm";
import { t } from "../../i18n";

export interface CommandBlock {
  index: number;
  command: string;
  reliable: boolean;
  startedAt: number;
  endedAt: number | null;
  startLine: number;
  endLine: number;
  exitCode: number | null;
}

export type CommandBlockStatus = "running" | "success" | "failure" | "done";

// Overview ruler colors by block state: running, exit 0, non-zero exit, and finished without a reported exit code.
export const COMMAND_BLOCK_MARK_COLORS: Record<CommandBlockStatus, string> = {
  running: "#4f9cf9",
  success: "#3fb950",
  failure: "#f85149",
  done: "#6e7681",
};
const MAX_BLOCK_LINES = 2000;
export const MAX_ACTIVE_COMMAND_BLOCKS = 200;

export function commandBlockStatus(block: Pick<CommandBlock, "endedAt" | "exitCode">): CommandBlockStatus {
  if (block.endedAt === null) return "running";
  if (block.exitCode === null) return "done";
  return block.exitCode === 0 ? "success" : "failure";
}

interface MarkerSlot {
  marker: IMarker;
  decoration?: IDisposable;
  status?: CommandBlockStatus;
  // Set when OSC 133 D reported the end: the line the cursor was on, and whether output reached that line.
  endMarker?: IMarker;
  endInclusive?: boolean;
}

function disposeSlot(slot: MarkerSlot): void {
  slot.decoration?.dispose();
  slot.marker.dispose();
  slot.endMarker?.dispose();
}

export interface CommandBlockManagerOptions {
  // Reports the block whose output fills the top of the viewport while its command line is scrolled out of view.
  onSticky?: (index: number | null) => void;
  // Schedules the sticky update after scrolling, so a burst of output lines costs one update per frame. Defaults to
  // requestAnimationFrame; returns a cancel function.
  scheduleFrame?: (callback: () => void) => () => void;
}

function defaultScheduleFrame(callback: () => void): () => void {
  if (typeof requestAnimationFrame === "function") {
    const id = requestAnimationFrame(callback);
    return () => cancelAnimationFrame(id);
  }
  const id = setTimeout(callback, 16);
  return () => clearTimeout(id);
}

export class CommandBlockManager {
  private readonly term: Terminal;
  private readonly onUpdate: (blocks: CommandBlock[]) => void;
  private blocks: CommandBlock[] = [];
  private slots: MarkerSlot[] = [];
  private nextBlockIndex = 1;
  private pending = "";
  private pendingReliable = true;
  private osc133 = false;
  private submitted = "";
  private disposed = false;
  private readonly onSticky?: (index: number | null) => void;
  private sticky: number | null = null;
  private readonly scrollListener?: IDisposable;
  private readonly scheduleFrame: (callback: () => void) => () => void;
  private cancelStickyFrame: (() => void) | null = null;

  constructor(term: Terminal, onUpdate: (blocks: CommandBlock[]) => void, options: CommandBlockManagerOptions = {}) {
    this.term = term;
    this.onUpdate = onUpdate;
    this.onSticky = options.onSticky;
    this.scheduleFrame = options.scheduleFrame ?? defaultScheduleFrame;
    if (this.onSticky) this.scrollListener = term.onScroll?.(() => this.scheduleSticky());
  }

  feedInput(data: string): void {
    if (this.disposed) return;
    for (let i = 0; i < data.length; i += 1) {
      const ch = data[i];
      const code = ch.charCodeAt(0);

      if (ch === "\r" || ch === "\n") {
        this.submit();
        continue;
      }
      if (code === 0x7f || code === 0x08) {
        this.pending = this.pending.slice(0, -1);
        continue;
      }
      if (code === 0x03 || code === 0x15) {
        this.pending = "";
        this.pendingReliable = true;
        continue;
      }
      if (code === 0x1b) {
        this.pendingReliable = false;
        while (i + 1 < data.length && data[i + 1] !== "\r" && data[i + 1].charCodeAt(0) !== 0x1b) {
          i += 1;
        }
        continue;
      }
      if (code === 0x09) {
        this.pendingReliable = false;
        continue;
      }
      if (code < 0x20) {
        this.pendingReliable = false;
        continue;
      }
      this.pending += ch;
    }
  }

  private submit(): void {
    const command = this.pending.trim();
    const reliable = this.pendingReliable;
    this.pending = "";
    this.pendingReliable = true;

    const now = Date.now();
    if (this.osc133) {
      if (command) {
        this.submitted = command;
      }
      return;
    }
    this.closeLast(now);

    if (!command) {
      this.emit();
      return;
    }

    const marker = this.term.registerMarker(0);
    if (!marker) {
      this.emit();
      return;
    }
    const slot: MarkerSlot = { marker };
    this.decorate(slot, "running");
    this.slots.push(slot);

    this.blocks.push({
      index: this.nextBlockIndex++,
      command,
      reliable,
      startedAt: now,
      endedAt: null,
      startLine: marker.line,
      endLine: marker.line,
      exitCode: null,
    });
    this.emit();
  }

  // feedOSC133 consumes OSC 133 command lifecycle reports. Once any report
  // arrives the tab is shell-integrated: blocks open on C (command start) and
  // close on D (command end) with the real exit code, and the keystroke
  // heuristic stops creating blocks so no duplicates appear.
  feedOSC133(phase: "A" | "B" | "C" | "D", exitCode: number | null): void {
    if (this.disposed) return;
    this.osc133 = true;
    const now = Date.now();
    switch (phase) {
      case "C": {
        this.closeLast(now);
        const command = this.submitted || this.echoLine() || t("（命令）");
        this.submitted = "";
        const marker = this.term.registerMarker(this.commandLineOffset()) ?? this.term.registerMarker(0);
        if (!marker) {
          this.emit();
          return;
        }
        const slot: MarkerSlot = { marker };
        this.decorate(slot, "running");
        this.slots.push(slot);
        this.blocks.push({
          index: this.nextBlockIndex++,
          command,
          reliable: true,
          startedAt: now,
          endedAt: null,
          startLine: marker.line,
          endLine: marker.line,
          exitCode: null,
        });
        this.emit();
        break;
      }
      case "D": {
        const last = this.blocks[this.blocks.length - 1];
        if (last && last.endedAt === null) {
          last.endedAt = now;
          last.exitCode = exitCode;
          this.markOutputEnd(this.slots[this.slots.length - 1]);
          this.emit();
        }
        break;
      }
      case "A": {
        this.submitted = "";
        const last = this.blocks[this.blocks.length - 1];
        if (last && last.endedAt === null) {
          last.endedAt = now;
          this.emit();
        }
        break;
      }
      case "B":
        break;
    }
  }

  // echoLine reads the most recent non-empty buffer line near the cursor as a
  // best-effort command label when no keystroke text was captured (e.g. keys
  // injected by the AI rather than typed locally).
  private echoLine(): string {
    const buf = this.term.buffer.active;
    const cursorLine = buf.baseY + buf.cursorY;
    for (let i = 0; i < 4; i++) {
      const line = buf.getLine(cursorLine - i);
      if (!line) continue;
      const text = line.translateToString(true).trim();
      if (text) return text;
    }
    return "";
  }

  // commandLineOffset is the marker offset from the cursor at command start:
  // the shell has echoed the command and its newline by the time C arrives,
  // so the command's line is usually one above the cursor. It falls back to
  // the cursor line when the line above is blank.
  private commandLineOffset(): number {
    const buf = this.term.buffer.active;
    const cursorLine = buf.baseY + buf.cursorY;
    if (cursorLine > 0) {
      const prev = buf.getLine(cursorLine - 1);
      if (prev && prev.translateToString(true).trim() !== "") return -1;
    }
    return 0;
  }

  private closeLast(now: number): void {
    const last = this.blocks[this.blocks.length - 1];
    if (last && last.endedAt === null) {
      last.endedAt = now;
    }
  }

  // decorate (re)registers the block's overview ruler mark in the color of its state. xterm cannot recolor a
  // decoration, so a state change replaces it.
  private decorate(slot: MarkerSlot, status: CommandBlockStatus): void {
    if (slot.status === status) return;
    slot.decoration?.dispose();
    slot.decoration = undefined;
    slot.status = status;
    if (slot.marker.isDisposed) return;
    try {
      slot.decoration = this.term.registerDecoration({
        marker: slot.marker,
        width: 1,
        height: 1,
        overviewRulerOptions: { color: COMMAND_BLOCK_MARK_COLORS[status], position: "full" },
      });
    } catch {
      slot.decoration = undefined;
    }
  }

  // markOutputEnd records where the output stopped when OSC 133 D arrives, before the next prompt is drawn.
  private markOutputEnd(slot: MarkerSlot | undefined): void {
    if (!slot || slot.endMarker) return;
    try {
      slot.endMarker = this.term.registerMarker(0);
    } catch {
      slot.endMarker = undefined;
    }
    slot.endInclusive = this.term.buffer.active.cursorX > 0;
  }

  // outputRange is the buffer lines of a block's output, after its command line; null when it has none.
  private outputRange(position: number): { from: number; to: number } | null {
    const b = this.blocks[position];
    if (!b || b.startLine < 0) return null;
    const slot = this.slots[position];
    const buf = this.term.buffer.active;
    const cursor = buf.baseY + buf.cursorY;
    const start = slot?.marker.line ?? b.startLine;
    if (start < 0) return null;
    // Read live from the markers: a block ends where the next one starts, the last one at the cursor.
    const next = position + 1 < this.blocks.length ? (this.slots[position + 1]?.marker.line ?? this.blocks[position + 1].startLine) : null;
    let to = next === null ? cursor : Math.max(start, next - 1);
    if (slot?.endMarker && !slot.endMarker.isDisposed && slot.endMarker.line >= 0) {
      to = Math.min(to, slot.endMarker.line - (slot.endInclusive ? 0 : 1));
    } else if (position === this.blocks.length - 1 && b.endedAt !== null) {
      // The cursor line of a finished last block already holds the next prompt.
      to = Math.min(to, cursor - 1);
    }
    const from = start + 1;
    if (to < from) return null;
    return { from, to: Math.min(to, from + MAX_BLOCK_LINES) };
  }

  private scheduleSticky(): void {
    if (this.cancelStickyFrame || this.disposed) return;
    this.cancelStickyFrame = this.scheduleFrame(() => {
      this.cancelStickyFrame = null;
      this.updateSticky();
    });
  }

  private updateSticky(): void {
    if (!this.onSticky || this.disposed) return;
    const next = this.computeSticky();
    if (next === this.sticky) return;
    this.sticky = next;
    this.onSticky(next);
  }

  private computeSticky(): number | null {
    const buf = this.term.buffer.active;
    if (buf.type !== undefined && buf.type !== "normal") return null;
    const top = buf.viewportY;
    for (let i = this.blocks.length - 1; i >= 0; i -= 1) {
      const start = this.slots[i]?.marker.line ?? this.blocks[i].startLine;
      if (start < 0 || start >= top) continue;
      // The latest block that starts above the viewport is sticky only while its output still reaches the top. The
      // scan stops there, so at the bottom of a long output it reads only the last block.
      const range = this.outputRange(i);
      return range && range.to >= top ? this.blocks[i].index : null;
    }
    return null;
  }

  private syncEndLines(): void {
    const buf = this.term.buffer.active;
    const cursor = buf.baseY + buf.cursorY;
    let nextStart = cursor;
    for (let i = this.blocks.length - 1; i >= 0; i -= 1) {
      const b = this.blocks[i];
      b.startLine = this.slots[i]?.marker.line ?? b.startLine;
      b.endLine = i === this.blocks.length - 1 ? cursor : Math.max(b.startLine, nextStart - 1);
      nextStart = b.startLine;
    }
  }

  private emit(): void {
    while (this.blocks.length > MAX_ACTIVE_COMMAND_BLOCKS) {
      this.blocks.shift();
      const slot = this.slots.shift();
      if (slot) disposeSlot(slot);
    }
    this.syncEndLines();
    this.blocks.forEach((b, i) => {
      const slot = this.slots[i];
      if (slot) this.decorate(slot, commandBlockStatus(b));
    });
    this.onUpdate(this.blocks.map((b) => ({ ...b })));
    this.updateSticky();
  }

  private linesText(from: number, to: number): string {
    const buf = this.term.buffer.active;
    const lines: string[] = [];
    for (let i = from; i <= to; i += 1) {
      const line = buf.getLine(i);
      if (line) lines.push(line.translateToString(true));
    }
    while (lines.length && lines[0] === "") lines.shift();
    while (lines.length && lines[lines.length - 1] === "") lines.pop();
    return lines.join("\n");
  }

  getBlockText(index: number): string {
    const b = this.blocks.find((x) => x.index === index);
    if (!b) return "";
    const from = Math.max(0, b.startLine);
    const to = Math.min(Math.max(from, b.endLine), from + MAX_BLOCK_LINES);
    return this.linesText(from, to);
  }

  // selectLastOutput selects the output of the most recent finished command (iTerm2 "select output of last
  // command") and returns it with its block index; null when that block has no output left in the buffer.
  selectLastOutput(): { index: number; text: string } | null {
    if (this.disposed) return null;
    this.syncEndLines();
    for (let i = this.blocks.length - 1; i >= 0; i -= 1) {
      if (this.blocks[i].endedAt === null) continue;
      const range = this.outputRange(i);
      if (!range) return null;
      const text = this.linesText(range.from, range.to);
      if (!text) return null;
      try {
        this.term.selectLines?.(range.from, range.to);
      } catch {
        // The selection is only a visual aid; the text is still returned.
      }
      return { index: this.blocks[i].index, text };
    }
    return null;
  }

  scrollTo(index: number): void {
    const b = this.blocks.find((x) => x.index === index);
    if (!b || b.startLine < 0) return;
    this.term.scrollToLine(b.startLine);
  }

  navigate(dir: "prev" | "next"): number | null {
    if (!this.blocks.length) return null;
    const viewportTop = this.term.buffer.active.viewportY;
    const candidates = this.blocks.filter((b) => b.startLine >= 0);
    const target =
      dir === "prev"
        ? [...candidates].reverse().find((b) => b.startLine < viewportTop)
        : candidates.find((b) => b.startLine > viewportTop + 1);
    if (!target) return null;
    this.term.scrollToLine(target.startLine);
    return target.index;
  }

  get count(): number {
    return this.blocks.length;
  }

  clear(): void {
    for (const slot of this.slots) disposeSlot(slot);
    this.slots = [];
    this.blocks = [];
    this.nextBlockIndex = 1;
    this.pending = "";
    this.pendingReliable = true;
    this.submitted = "";
    this.emit();
  }

  dispose(): void {
    this.clear();
    this.disposed = true;
    this.scrollListener?.dispose();
    this.cancelStickyFrame?.();
    this.cancelStickyFrame = null;
  }
}
