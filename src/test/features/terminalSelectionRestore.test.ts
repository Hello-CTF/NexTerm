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
});
