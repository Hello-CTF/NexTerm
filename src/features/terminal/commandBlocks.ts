import type { IDisposable, IMarker, Terminal } from "@xterm/xterm";

export interface CommandBlock {
  index: number;
  command: string;
  reliable: boolean;
  startedAt: number;
  endedAt: number | null;
  startLine: number;
  endLine: number;
}

const OVERVIEW_RULER_COLOR = "#4f9cf9";
const MAX_BLOCK_LINES = 2000;

interface MarkerSlot {
  marker: IMarker;
  decoration?: IDisposable;
}

export class CommandBlockManager {
  private readonly term: Terminal;
  private readonly onUpdate: (blocks: CommandBlock[]) => void;
  private blocks: CommandBlock[] = [];
  private slots: MarkerSlot[] = [];
  private pending = "";
  private pendingReliable = true;
  private disposed = false;

  constructor(term: Terminal, onUpdate: (blocks: CommandBlock[]) => void) {
    this.term = term;
    this.onUpdate = onUpdate;
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
    this.closeLast(now);

    if (!command) {
      this.syncEndLines();
      this.emit();
      return;
    }

    const marker = this.term.registerMarker(0);
    if (!marker) {
      this.emit();
      return;
    }
    const slot: MarkerSlot = { marker };
    try {
      slot.decoration = this.term.registerDecoration({
        marker,
        width: 1,
        height: 1,
        overviewRulerOptions: { color: OVERVIEW_RULER_COLOR, position: "full" },
      });
    } catch {
      slot.decoration = undefined;
    }
    this.slots.push(slot);

    this.blocks.push({
      index: this.blocks.length + 1,
      command,
      reliable,
      startedAt: now,
      endedAt: null,
      startLine: marker.line,
      endLine: marker.line,
    });
    this.syncEndLines();
    this.emit();
  }

  private closeLast(now: number): void {
    const last = this.blocks[this.blocks.length - 1];
    if (last && last.endedAt === null) {
      last.endedAt = now;
    }
  }

  private syncEndLines(): void {
    const buf = this.term.buffer.active;
    const cursor = buf.baseY + buf.cursorY;
    for (let i = 0; i < this.blocks.length; i += 1) {
      const b = this.blocks[i];
      const next = this.blocks[i + 1];
      b.startLine = this.slots[i]?.marker.line ?? b.startLine;
      b.endLine = next ? Math.max(b.startLine, (this.slots[i + 1]?.marker.line ?? cursor) - 1) : cursor;
    }
  }

  private emit(): void {
    this.syncEndLines();
    this.onUpdate(this.blocks.map((b) => ({ ...b })));
  }

  getBlockText(index: number): string {
    const b = this.blocks.find((x) => x.index === index);
    if (!b) return "";
    const buf = this.term.buffer.active;
    const from = Math.max(0, b.startLine);
    const to = Math.min(Math.max(from, b.endLine), from + MAX_BLOCK_LINES);
    const lines: string[] = [];
    for (let i = from; i <= to; i += 1) {
      const line = buf.getLine(i);
      if (line) lines.push(line.translateToString(true));
    }
    while (lines.length && lines[0] === "") lines.shift();
    while (lines.length && lines[lines.length - 1] === "") lines.pop();
    return lines.join("\n");
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
    for (const s of this.slots) {
      s.marker.dispose();
      s.decoration?.dispose();
    }
    this.slots = [];
    this.blocks = [];
    this.pending = "";
    this.pendingReliable = true;
    this.emit();
  }

  dispose(): void {
    this.disposed = true;
    this.clear();
  }
}
