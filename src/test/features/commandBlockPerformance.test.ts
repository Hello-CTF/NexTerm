import { describe, expect, it } from "vitest";
import type { Terminal } from "@xterm/xterm";
import {
  CommandBlockManager,
  MAX_ACTIVE_COMMAND_BLOCKS,
  type CommandBlock,
} from "../../features/terminal/commandBlocks";

function trackedTerminal() {
  const stats = {
    activeReads: 0,
    markerLineReads: 0,
    disposedMarkers: 0,
    disposedDecorations: 0,
  };
  const markers = new Set<object>();
  const decorations = new Set<object>();
  const buffer = {
    baseY: 0,
    cursorY: 0,
    viewportY: 0,
    getLine: () => ({ translateToString: () => "" }),
  };
  const terminal = {
    buffer: {
      get active() {
        stats.activeReads += 1;
        return buffer;
      },
    },
    registerMarker: () => {
      const marker = {
        get line() {
          stats.markerLineReads += 1;
          return 0;
        },
        dispose: () => {
          if (markers.delete(marker)) stats.disposedMarkers += 1;
        },
      };
      markers.add(marker);
      return marker;
    },
    registerDecoration: () => {
      const decoration = {
        dispose: () => {
          if (decorations.delete(decoration)) stats.disposedDecorations += 1;
        },
      };
      decorations.add(decoration);
      return decoration;
    },
    scrollToLine: () => undefined,
  } as unknown as Terminal;
  return { terminal, stats, markers, decorations };
}

describe("CommandBlockManager bounded work", () => {
  it("bounds live markers and decorations and disposes evicted blocks", () => {
    const { terminal, stats, markers, decorations } = trackedTerminal();
    const updates: CommandBlock[][] = [];
    const manager = new CommandBlockManager(terminal, (blocks) => updates.push(blocks));
    const total = MAX_ACTIVE_COMMAND_BLOCKS + 25;
    for (let i = 1; i <= total; i += 1) manager.feedInput(`command ${i}\r`);

    expect(manager.count).toBe(MAX_ACTIVE_COMMAND_BLOCKS);
    expect(markers.size).toBe(MAX_ACTIVE_COMMAND_BLOCKS);
    expect(decorations.size).toBe(MAX_ACTIVE_COMMAND_BLOCKS);
    expect(stats.disposedMarkers).toBe(25);
    expect(stats.disposedDecorations).toBe(25);
    const retained = updates.at(-1) ?? [];
    expect(retained[0]?.index).toBe(26);
    expect(retained.at(-1)?.index).toBe(total);
    expect(retained[0]?.command).toBe("command 26");

    manager.clear();
    expect(markers.size).toBe(0);
    expect(decorations.size).toBe(0);
    expect(stats.disposedMarkers).toBe(total);
    expect(stats.disposedDecorations).toBe(total);
  });

  it("reads the buffer once and each marker once for an Enter sync", () => {
    const { terminal, stats } = trackedTerminal();
    const manager = new CommandBlockManager(terminal, () => undefined);
    manager.feedInput("first\r");
    manager.feedInput("second\r");
    stats.activeReads = 0;
    stats.markerLineReads = 0;

    manager.feedInput("\r");

    expect(stats.activeReads).toBe(1);
    expect(stats.markerLineReads).toBe(2);
    manager.dispose();
  });
});
