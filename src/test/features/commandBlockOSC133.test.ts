import { describe, expect, it } from "vitest";
import type { Terminal } from "@xterm/xterm";
import { CommandBlockManager, type CommandBlock } from "../../features/terminal/commandBlocks";

interface FakeMarker {
  line: number;
  dispose: () => void;
}

function fakeTerminal(lines: string[]): Terminal {
  const last = Math.max(0, lines.length - 1);
  const buffer = {
    baseY: 0,
    cursorY: last,
    viewportY: 0,
    getLine: (i: number) =>
      i >= 0 && i < lines.length ? { translateToString: () => lines[i] } : undefined,
  };
  return {
    buffer: { active: buffer },
    registerMarker: () => ({ line: last, dispose: () => undefined }) as FakeMarker,
    registerDecoration: () => ({ dispose: () => undefined }),
    scrollToLine: () => undefined,
  } as unknown as Terminal;
}

function managerWith(lines: string[] = [""]) {
  const terminal = fakeTerminal(lines);
  const updates: CommandBlock[][] = [];
  const manager = new CommandBlockManager(terminal, (blocks) => updates.push(blocks));
  const latest = () => updates[updates.length - 1] ?? [];
  return { manager, latest };
}

describe("CommandBlockManager OSC 133 blocks", () => {
  it("keeps the keystroke heuristic before any 133 report", () => {
    const { manager, latest } = managerWith();
    manager.feedInput("ls -la\r");
    const blocks = latest();
    expect(blocks).toHaveLength(1);
    expect(blocks[0].command).toBe("ls -la");
    expect(blocks[0].exitCode).toBeNull();
    manager.feedInput("git status\r");
    expect(latest()).toHaveLength(2);
    expect(latest()[0].endedAt).not.toBeNull();
  });

  it("opens a block on C and closes it on D with the exit code", () => {
    const { manager, latest } = managerWith();
    manager.feedOSC133("B", null);
    manager.feedInput("make test\r");
    manager.feedOSC133("C", null);
    let blocks = latest();
    expect(blocks).toHaveLength(1);
    expect(blocks[0].command).toBe("make test");
    expect(blocks[0].reliable).toBe(true);
    expect(blocks[0].endedAt).toBeNull();
    expect(blocks[0].exitCode).toBeNull();
    manager.feedOSC133("D", 2);
    blocks = latest();
    expect(blocks[0].endedAt).not.toBeNull();
    expect(blocks[0].exitCode).toBe(2);
  });

  it("stops creating heuristic blocks once 133 is seen", () => {
    const { manager, latest } = managerWith();
    manager.feedOSC133("A", null);
    manager.feedInput("echo hi\r");
    expect(latest()).toHaveLength(0);
    manager.feedOSC133("C", null);
    const blocks = latest();
    expect(blocks).toHaveLength(1);
    expect(blocks[0].command).toBe("echo hi");
    manager.feedOSC133("D", 0);
    expect(latest()[0].exitCode).toBe(0);
  });

  it("labels AI-injected commands from the echoed buffer line", () => {
    const { manager, latest } = managerWith(["user@host:~$ systemctl status nginx", ""]);
    manager.feedOSC133("C", null);
    const blocks = latest();
    expect(blocks).toHaveLength(1);
    expect(blocks[0].command).toBe("user@host:~$ systemctl status nginx");
  });

  it("falls back to a placeholder when neither keystrokes nor echo exist", () => {
    const { manager, latest } = managerWith([""]);
    manager.feedOSC133("C", null);
    expect(latest()[0].command).toBe("（命令）");
  });

  it("closes a dangling block on A without an exit code", () => {
    const { manager, latest } = managerWith();
    manager.feedOSC133("C", null);
    manager.feedOSC133("A", null);
    const blocks = latest();
    expect(blocks[0].endedAt).not.toBeNull();
    expect(blocks[0].exitCode).toBeNull();
  });

  it("ignores D and B without side effects", () => {
    const { manager, latest } = managerWith();
    manager.feedOSC133("D", 1);
    manager.feedOSC133("B", null);
    expect(latest()).toHaveLength(0);
  });

  it("keeps OSC mode after clear so no heuristic duplicates return", () => {
    const { manager, latest } = managerWith();
    manager.feedOSC133("C", null);
    manager.feedOSC133("D", 0);
    manager.clear();
    expect(latest()).toHaveLength(0);
    manager.feedInput("ls\r");
    expect(latest()).toHaveLength(0);
    manager.feedOSC133("C", null);
    expect(latest()).toHaveLength(1);
  });
});
