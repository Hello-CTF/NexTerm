import { describe, expect, it, vi } from "vitest";
import type { Terminal } from "@xterm/xterm";
import { COMMAND_BLOCK_MARK_COLORS, CommandBlockManager } from "../../features/terminal/commandBlocks";

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

describe("sticky command header", () => {
  it("reports the block whose command line scrolled above the viewport while its output fills the top", () => {
    const t = scriptedTerminal();
    const sticky: Array<number | null> = [];
    const manager = new CommandBlockManager(t.term, () => undefined, { onSticky: (index) => sticky.push(index) });
    runCommand(manager, t, "short", ["one"], 0);
    runCommand(manager, t, "journalctl", Array.from({ length: 30 }, (_, i) => `log ${i}`), 0);
    t.scrollTo(10);
    expect(sticky[sticky.length - 1]).toBe(2);
    // The command line of block 2 is line 2; with it in view nothing is pinned.
    t.scrollTo(2);
    expect(sticky[sticky.length - 1]).toBeNull();
    // Inside block 1's output its own command line (line 0) is above the top.
    t.scrollTo(1);
    expect(sticky[sticky.length - 1]).toBe(1);
  });

  it("stops listening to scrolls on dispose", () => {
    const t = scriptedTerminal();
    const manager = new CommandBlockManager(t.term, () => undefined, { onSticky: () => undefined });
    expect(t.listeners()).toBe(1);
    manager.dispose();
    expect(t.listeners()).toBe(0);
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
