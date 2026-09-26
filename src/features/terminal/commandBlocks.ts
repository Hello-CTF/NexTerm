// 命令块 v1（M1-T9 / §4.5）—— 块分隔 · 输出折叠 · 复制输出。
//
// 设计说明（重要，别照抄别的终端的做法）：
//
// xterm.js **没有删除/隐藏缓冲行的 API**（buffer 只读）。所以「在终端里把输出折起来」
// 物理上做不到 —— 任何声称做到的实现都是把行涂白，会留下空白仍占位。
// 因此 v1 的「折叠」落在**块面板**里（见 CommandBlockPanel）：按块折叠、可展开预览。
//
// 终端内的「块分隔」用两件事实现，都不碰缓冲内容：
//   ① 每个命令起点 registerMarker() → 供定位（scrollToLine）与输出抽取；
//   ② 同一 marker 上挂一个 decoration，只在 overview ruler 上画一个刻点
//      （ruler 不覆盖任何文字，不像 cell decoration 会遮住首字符）。
//
// 已知边界：
//   · 命令文本靠按键流还原（无 shell integration），遇 Tab 补全 / 方向键 / 粘贴多行
//     会标记为 unreliable，UI 上以「?」提示；
//   · AI 通过 terminal_write 直接写 PTY 的按键不经过 term.onData，故不产生块。
import type { IDisposable, IMarker, Terminal } from "@xterm/xterm";

export interface CommandBlock {
  /** 1 起的序号，与 UI 显示一致。 */
  index: number;
  /** 还原出的命令文本；reliable=false 时可能不完整。 */
  command: string;
  /** 命令文本是否完整可信（含 Tab/方向键/粘贴时为 false）。 */
  reliable: boolean;
  startedAt: number;
  endedAt: number | null;
  /** 区块起始的绝对缓冲行；marker 被回收后为 -1。 */
  startLine: number;
  /** 区块结束的绝对缓冲行（下一个命令起点，或当前光标行）。 */
  endLine: number;
}

const OVERVIEW_RULER_COLOR = "#4f9cf9";
/** 单块抽取输出的行数上限，防止病态长输出把内存吃光。 */
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
  /** 自上次提交以来累计的按键（用于还原命令文本）。 */
  private pending = "";
  private pendingReliable = true;
  private disposed = false;

  constructor(term: Terminal, onUpdate: (blocks: CommandBlock[]) => void) {
    this.term = term;
    this.onUpdate = onUpdate;
  }

  /** 由 XtermView 的 term.onData 转发；只读按键流，不拦截、不改写。 */
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
        // Backspace / ^H
        this.pending = this.pending.slice(0, -1);
        continue;
      }
      if (code === 0x03 || code === 0x15) {
        // Ctrl-C / Ctrl-U：放弃当前输入，不产生块
        this.pending = "";
        this.pendingReliable = true;
        continue;
      }
      if (code === 0x1b) {
        // ESC 序列（方向键 / Alt 组合）：跳过本序列并标记不可信
        this.pendingReliable = false;
        while (i + 1 < data.length && data[i + 1] !== "\r" && data[i + 1].charCodeAt(0) !== 0x1b) {
          i += 1;
        }
        continue;
      }
      if (code === 0x09) {
        // Tab 补全：补出来的字符我们看不到
        this.pendingReliable = false;
        continue;
      }
      if (code < 0x20) {
        // 其余控制键（Ctrl-A/E/W 等）会让我们的还原失真
        this.pendingReliable = false;
        continue;
      }
      this.pending += ch;
    }
  }

  /** 一次提交：关闭上一块，开启新块。 */
  private submit(): void {
    const command = this.pending.trim();
    const reliable = this.pendingReliable;
    this.pending = "";
    this.pendingReliable = true;

    const now = Date.now();
    this.closeLast(now);

    // 空回车（常用于翻页/确认）不单独成块，只把上一块的结束行往前推。
    if (!command) {
      this.syncEndLines();
      this.emit();
      return;
    }

    const marker = this.term.registerMarker(0);
    if (!marker) {
      // alt buffer（vim/htop 之类）下不产生块：那里没有「命令+输出」的语义。
      this.emit();
      return;
    }
    const slot: MarkerSlot = { marker };
    // overview ruler 刻点：不覆盖任何字符，失败也不影响主流程。
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

  /** 每块的结束行 = 下一块的起始行 - 1（或当前光标行）。 */
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

  /** 抽取某块的完整文本（命令回显 + 输出）。 */
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
    // 去掉首尾空行，保留中间的空行（有意义的排版）
    while (lines.length && lines[0] === "") lines.shift();
    while (lines.length && lines[lines.length - 1] === "") lines.pop();
    return lines.join("\n");
  }

  /** 定位到某块的起始行。 */
  scrollTo(index: number): void {
    const b = this.blocks.find((x) => x.index === index);
    if (!b || b.startLine < 0) return;
    this.term.scrollToLine(b.startLine);
  }

  /** 相对当前视口向上/向下找最近的一块。 */
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
