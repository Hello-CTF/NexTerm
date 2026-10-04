/** @vitest-environment jsdom */

import { describe, expect, it } from "vitest";
import { Terminal } from "@xterm/xterm";
import { resizeTerminalToGridPreservingSelection } from "../../features/terminal/terminalGeometry";

function stubMatchMedia(): void {
  window.matchMedia = (() => ({
    matches: false,
    media: "",
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    onchange: null,
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

function openedTerminal(options: { scrollback: number; cols: number; rows: number }): Terminal {
  stubMatchMedia();
  const term = new Terminal(options);
  const host = document.createElement("div");
  document.body.append(host);
  term.open(host);
  return term;
}

async function writeLines(term: Terminal, count: number): Promise<void> {
  const data = `${Array.from({ length: count }, (_, i) => `line-${String(i).padStart(3, "0")}`).join("\r\n")}\r\n`;
  await new Promise<void>((resolve) => term.write(data, resolve));
}

function findLine(term: Terminal, text: string): number {
  for (let y = 0; y < term.buffer.active.length; y += 1) {
    if (term.buffer.active.getLine(y)?.translateToString(true) === text) return y;
  }
  throw new Error(`missing buffer line ${text}`);
}

async function writeAltScreen(term: Terminal, count: number): Promise<void> {
  await new Promise<void>((resolve) => term.write("\x1b[?1049h", resolve));
  expect(term.buffer.active.type).toBe("alternate");
  await writeLines(term, count);
}

function setRangeSelection(
  term: Terminal,
  start: [number, number],
  end: [number, number],
): void {
  const model = (
    term as unknown as {
      _core: {
        _selectionService: {
          _model: {
            selectionStart?: [number, number];
            selectionEnd?: [number, number];
            selectionStartLength?: number;
          };
        };
      };
    }
  )._core._selectionService._model;
  model.selectionStart = start;
  model.selectionEnd = end;
  model.selectionStartLength = 0;
}

describe("resizeTerminalToGridPreservingSelection", () => {
  it("keeps the selected text when a full buffer trims lines on a rows-only shrink", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 10 });
    await writeLines(term, 120);
    term.select(0, 5, 8);
    expect(term.getSelection()).toBe("line-016");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 9 });

    expect(term.getSelection()).toBe("line-016");
    expect(term.getSelectionPosition()?.start.y).toBe(4);
    term.dispose();
  });

  it("restores in the length-based form that survives a later right-click outside the selection", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 10 });
    await writeLines(term, 120);
    term.select(0, 5, 8);
    expect(term.getSelection()).toBe("line-016");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 9 });

    expect(term.getSelection()).toBe("line-016");
    const model = (
      term as unknown as {
        _core: { _selectionService: { _model: { selectionStartLength?: number; selectionEnd?: [number, number] } } };
      }
    )._core._selectionService._model;
    expect(model.selectionStartLength).toBe(8);
    expect(model.selectionEnd).toBeUndefined();
    term.dispose();
  });

  it("keeps a multi-line selection ending at the next line start, including the trailing newline", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 10 });
    await new Promise<void>((resolve) => term.write("alpha\r\n", resolve));
    setRangeSelection(term, [0, 0], [0, 1]);
    expect(term.getSelection()).toBe("alpha\n");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 9 });

    expect(term.getSelection()).toBe("alpha\n");
    expect(term.getSelectionPosition()?.end).toEqual({ x: 0, y: 1 });
    term.dispose();
  });

  it("leaves the selection cleared instead of selecting different text when its lines were trimmed", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 10 });
    await writeLines(term, 120);
    term.select(0, 0, 8);
    expect(term.getSelection()).toBe("line-011");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 5 });

    expect(term.hasSelection()).toBe(false);
    term.dispose();
  });

  it("does not restore the selection when a resize changes columns", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 10 });
    await writeLines(term, 120);
    term.select(0, 5, 8);
    expect(term.getSelection()).toBe("line-016");

    resizeTerminalToGridPreservingSelection(term, { cols: 70, rows: 9 });

    expect(term.hasSelection()).toBe(false);
    term.dispose();
  });

  it("adjusts a surviving alternate-buffer selection by the actual head trim on shrink", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 6 });
    await writeAltScreen(term, 6);
    const y = findLine(term, "line-003");
    term.select(0, y, 8);
    expect(term.getSelection()).toBe("line-003");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 4 });

    expect(term.getSelection()).toBe("line-003");
    expect(term.getSelectionPosition()?.start).toEqual({ x: 0, y: y - 2 });
    term.dispose();
  });

  it("keeps a normal-buffer selection at the same coordinates when blank rows below the cursor are removed first", async () => {
    const term = openedTerminal({ scrollback: 4, cols: 80, rows: 6 });
    await writeLines(term, 10);
    await new Promise<void>((resolve) => term.write("\x1b[3;1H\x1b[0J", resolve));
    const y = findLine(term, "line-003");
    term.select(0, y, 8);
    expect(term.getSelection()).toBe("line-003");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 4 });

    expect(findLine(term, "line-003")).toBe(y);
    expect(term.getSelection()).toBe("line-003");
    expect(term.getSelectionPosition()?.start).toEqual({ x: 0, y });
    term.dispose();
  });

  it("keeps a selection at the same coordinates when rows grow", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 6 });
    await writeLines(term, 10);
    const y = findLine(term, "line-005");
    term.select(0, y, 8);
    expect(term.getSelection()).toBe("line-005");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 8 });

    expect(term.getSelection()).toBe("line-005");
    expect(term.getSelectionPosition()?.start).toEqual({ x: 0, y });
    term.dispose();
  });

  it("keeps a selection through a heavy alternate-buffer shrink", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 6 });
    await writeAltScreen(term, 6);
    const y = findLine(term, "line-004");
    term.select(0, y, 8);
    expect(term.getSelection()).toBe("line-004");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 3 });

    expect(term.getSelection()).toBe("line-004");
    expect(term.buffer.active.getLine(term.getSelectionPosition()?.start.y ?? -1)?.translateToString(true)).toBe("line-004");
    term.dispose();
  });

  it("clears an alternate-buffer selection whose line was trimmed away", async () => {
    const term = openedTerminal({ scrollback: 100, cols: 80, rows: 6 });
    await writeAltScreen(term, 6);
    const y = findLine(term, "line-002");
    term.select(0, y, 8);
    expect(term.getSelection()).toBe("line-002");

    resizeTerminalToGridPreservingSelection(term, { cols: 80, rows: 3 });

    expect(term.hasSelection()).toBe(false);
    term.dispose();
  });
});
