/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";
import type { Terminal } from "@xterm/xterm";
import {
  measureTerminalGeometry,
  resizeTerminalToGrid,
} from "../../features/terminal/terminalGeometry";

function terminalFixture(width = 360, height = 400) {
  const element = document.createElement("div");
  element.style.padding = "2px 4px 2px 6px";
  const host = document.createElement("div");
  Object.defineProperties(host, {
    clientWidth: { value: width },
    clientHeight: { value: height },
  });
  const clear = vi.fn();
  const resize = vi.fn();
  const term = {
    element,
    options: { scrollback: 1000, overviewRuler: { width: 12 } },
    cols: 80,
    rows: 24,
    resize,
    _core: {
      _renderService: {
        clear,
        dimensions: { css: { cell: { width: 8.5, height: 17 } } },
      },
    },
  } as unknown as Terminal;
  return { term, host, clear, resize };
}

describe("measured terminal geometry", () => {
  it("uses renderer cells and subtracts padding and scrollbar from the viewport", () => {
    const { term, host } = terminalFixture();
    const geometry = measureTerminalGeometry(term, host);
    expect(geometry?.viewport).toEqual({ widthPx: 338, heightPx: 396 });
    expect(geometry?.metrics).toEqual({ widthPx: 8.5, heightPx: 17 });
    expect(geometry?.grid).toEqual({ cols: 39, rows: 23 });
  });

  it("does not invent dimensions when the renderer has not measured cells", () => {
    const { term, host } = terminalFixture();
    (term as unknown as { _core: { _renderService: { dimensions: unknown } } })._core
      ._renderService.dimensions = {};
    expect(measureTerminalGeometry(term, host)).toBeNull();
  });

  it("clears the renderer before a real grid change and skips identical replay", () => {
    const { term, clear, resize } = terminalFixture();
    resizeTerminalToGrid(term, { cols: 80, rows: 24 });
    expect(clear).not.toHaveBeenCalled();
    expect(resize).not.toHaveBeenCalled();

    resizeTerminalToGrid(term, { cols: 39, rows: 23 });
    expect(clear).toHaveBeenCalledOnce();
    expect(resize).toHaveBeenCalledWith(39, 23);
  });
});
