import { describe, expect, it, vi } from "vitest";
import type { Terminal } from "@xterm/xterm";
import { COMMAND_BLOCK_MARK_COLORS, CommandBlockManager, MAX_ACTIVE_COMMAND_BLOCKS } from "../../features/terminal/commandBlocks";

interface Decoration {
  line: number;
  color: string;
  disposed: boolean;
}

// A scriptable terminal: tests write lines, move the cursor and scroll the viewport.
function scriptedTerminal() {
  const lines: string[] = [""];
  const decorations: Decoration[] = [];
  const scrollListeners: Array<() => void> = [];
  const selectLines = vi.fn();
  const buffer = {
    type: "normal",
    baseY: 0,
    cursorY: 0,
    cursorX: 0,
    viewportY: 0,
    getLine: (i: number) => (i >= 0 && i < lines.length ? { translateToString: () => lines[i] } : undefined),
  };
  const cursorLine = () => buffer.baseY + buffer.cursorY;
  const term = {
    buffer: { active: buffer },
    registerMarker: (offset = 0) => ({ line: cursorLine() + offset, isDisposed: false, dispose: () => undefined }),
    registerDecoration: ({ marker, overviewRulerOptions }: { marker: { line: number }; overviewRulerOptions: { color: string } }) => {
      const decoration: Decoration = { line: marker.line, color: overviewRulerOptions.color, disposed: false };
      decorations.push(decoration);
      return { dispose: () => (decoration.disposed = true) };
    },
    onScroll: (listener: () => void) => {
      scrollListeners.push(listener);
      return { dispose: () => scrollListeners.splice(scrollListeners.indexOf(listener), 1) };
    },
    scrollToLine: (line: number) => {
      buffer.viewportY = line;
      scrollListeners.forEach((listener) => listener());
    },
    selectLines,
  };
  return {
    term: term as unknown as Terminal,
    decorations,
    selectLines,
    // print appends a full line of output and moves the cursor to the start of the next line.
    print(text: string) {
      lines[cursorLine()] = text;
      lines.push("");
      buffer.cursorY += 1;
      buffer.cursorX = 0;
    },
    scrollTo(line: number) {
      term.scrollToLine(line);
    },
    activeColors: (line: number) => decorations.filter((d) => d.line === line && !d.disposed).map((d) => d.color),
    listeners: () => scrollListeners.length,
  };
}

function runCommand(manager: CommandBlockManager, t: ReturnType<typeof scriptedTerminal>, command: string, output: string[], exitCode: number) {
  manager.feedOSC133("A", null);
  manager.feedInput(`${command}\r`);
  t.print(`$ ${command}`);
  manager.feedOSC133("C", null);
  output.forEach((line) => t.print(line));
  manager.feedOSC133("D", exitCode);
}

describe("command block marks", () => {
  it("colors the overview ruler mark by state: running, then success or failure", () => {
    const t = scriptedTerminal();
    const manager = new CommandBlockManager(t.term, () => undefined);
    manager.feedOSC133("A", null);
    manager.feedInput("make\r");
    t.print("$ make");
    manager.feedOSC133("C", null);
    expect(t.activeColors(0)).toEqual([COMMAND_BLOCK_MARK_COLORS.running]);
    t.print("error: missing target");
    manager.feedOSC133("D", 2);
    expect(t.activeColors(0)).toEqual([COMMAND_BLOCK_MARK_COLORS.failure]);

    runCommand(manager, t, "ls", ["a.txt"], 0);
    expect(t.activeColors(2)).toEqual([COMMAND_BLOCK_MARK_COLORS.success]);
    expect(t.activeColors(0)).toEqual([COMMAND_BLOCK_MARK_COLORS.failure]);
  });

  it("marks a heuristic block without an exit code as finished once the next command starts", () => {
    const t = scriptedTerminal();
    const manager = new CommandBlockManager(t.term, () => undefined);
    manager.feedInput("ls\r");
    expect(t.activeColors(0)).toEqual([COMMAND_BLOCK_MARK_COLORS.running]);
    t.print("$ ls");
    t.print("a.txt");
    manager.feedInput("pwd\r");
    expect(t.activeColors(0)).toEqual([COMMAND_BLOCK_MARK_COLORS.done]);
  });
});

// A manual frame scheduler: scroll updates run when the test flushes the frame.
function manualFrames() {
  const pending: Array<() => void> = [];
  return {
    scheduleFrame: (callback: () => void) => {
      pending.push(callback);
      return () => {
        const at = pending.indexOf(callback);
        if (at >= 0) pending.splice(at, 1);
      };
    },
    pending: () => pending.length,
    flush: () => pending.splice(0).forEach((callback) => callback()),
  };
}

describe("sticky command header", () => {
  it("reports the block whose command line scrolled above the viewport while its output fills the top", () => {
    const t = scriptedTerminal();
    const frames = manualFrames();
    const sticky: Array<number | null> = [];
    const manager = new CommandBlockManager(t.term, () => undefined, {
      onSticky: (index) => sticky.push(index),
      scheduleFrame: frames.scheduleFrame,
    });
    runCommand(manager, t, "short", ["one"], 0);
    runCommand(manager, t, "journalctl", Array.from({ length: 30 }, (_, i) => `log ${i}`), 0);
    t.scrollTo(10);
    frames.flush();
    expect(sticky[sticky.length - 1]).toBe(2);
    // The command line of block 2 is line 2; with it in view nothing is pinned.
    t.scrollTo(2);
    frames.flush();
    expect(sticky[sticky.length - 1]).toBeNull();
    // Inside block 1's output its own command line (line 0) is above the top.
    t.scrollTo(1);
    frames.flush();
    expect(sticky[sticky.length - 1]).toBe(1);
  });

  it("stops listening to scrolls and drops a pending update on dispose", () => {
    const t = scriptedTerminal();
    const frames = manualFrames();
    const sticky: Array<number | null> = [];
    const manager = new CommandBlockManager(t.term, () => undefined, {
      onSticky: (index) => sticky.push(index),
      scheduleFrame: frames.scheduleFrame,
    });
    runCommand(manager, t, "journalctl", Array.from({ length: 30 }, (_, i) => `log ${i}`), 0);
    t.scrollTo(10);
    expect(t.listeners()).toBe(1);
    manager.dispose();
    expect(t.listeners()).toBe(0);
    expect(frames.pending()).toBe(0);
  });
});

describe("sticky command header cost", () => {
  // Counts marker line reads, which is what scanning blocks costs.
  function countingTerminal(blockCount: number) {
    const buffer = { type: "normal", baseY: 0, cursorY: 0, cursorX: 0, viewportY: 0, getLine: () => ({ translateToString: () => "x" }) };
    const scrollListeners: Array<() => void> = [];
    const stats = { lineReads: 0 };
    let nextLine = 0;
    const term = {
      buffer: { active: buffer },
      registerMarker: () => {
        const line = nextLine;
        return {
          isDisposed: false,
          get line() {
            stats.lineReads += 1;
            return line;
          },
          dispose: () => undefined,
        };
      },
      registerDecoration: () => ({ dispose: () => undefined }),
      onScroll: (listener: () => void) => {
        scrollListeners.push(listener);
        return { dispose: () => undefined };
      },
      scrollToLine: () => undefined,
    } as unknown as Terminal;
    const frames = manualFrames();
    const manager = new CommandBlockManager(term, () => undefined, { onSticky: () => undefined, scheduleFrame: frames.scheduleFrame });
    for (let i = 0; i < blockCount; i += 1) {
      nextLine = i * 3;
      buffer.cursorY = i * 3;
      manager.feedInput(`cmd ${i}\r`);
    }
    // The last command streams a long output: the cursor and the viewport follow it.
    buffer.cursorY = blockCount * 3 + 500;
    buffer.baseY = 0;
    return { manager, frames, stats, buffer, scroll: () => scrollListeners.forEach((listener) => listener()) };
  }

  it("coalesces a burst of scroll events into one update per frame", () => {
    const { frames, scroll, buffer } = countingTerminal(MAX_ACTIVE_COMMAND_BLOCKS);
    for (let line = 0; line < 1000; line += 1) {
      buffer.viewportY = MAX_ACTIVE_COMMAND_BLOCKS * 3 + line % 400;
      scroll();
    }
    expect(frames.pending()).toBe(1);
  });

  it("reads only the last blocks when the viewport follows output at the bottom", () => {
    const { frames, scroll, stats, buffer } = countingTerminal(MAX_ACTIVE_COMMAND_BLOCKS);
    buffer.viewportY = MAX_ACTIVE_COMMAND_BLOCKS * 3 + 400;
    stats.lineReads = 0;
    scroll();
    frames.flush();
    expect(stats.lineReads).toBeLessThanOrEqual(4);
  });
});

describe("select output of last command", () => {
  it("selects and returns only the output of the latest finished command", () => {
    const t = scriptedTerminal();
    const manager = new CommandBlockManager(t.term, () => undefined);
    runCommand(manager, t, "echo first", ["first"], 0);
    runCommand(manager, t, "cat notes", ["line a", "line b"], 1);
    // The next prompt is drawn after D.
    t.print("$ ");
    expect(manager.selectLastOutput()).toEqual({ index: 2, text: "line a\nline b" });
    expect(t.selectLines).toHaveBeenLastCalledWith(3, 4);
  });

  it("skips a command that is still running and returns null when nothing finished", () => {
    const t = scriptedTerminal();
    const manager = new CommandBlockManager(t.term, () => undefined);
    expect(manager.selectLastOutput()).toBeNull();
    runCommand(manager, t, "uname", ["Linux"], 0);
    manager.feedOSC133("A", null);
    manager.feedInput("sleep 100\r");
    t.print("$ sleep 100");
    manager.feedOSC133("C", null);
    expect(manager.selectLastOutput()).toEqual({ index: 1, text: "Linux" });
  });
});
