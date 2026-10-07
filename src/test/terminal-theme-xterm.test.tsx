/** @vitest-environment jsdom */

import { act } from "react";
import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{
    options: Record<string, unknown>;
    resize: ReturnType<typeof vi.fn>;
  }>,
  frames: new Map<number, () => void>(),
  attach: vi.fn(),
  attachTab: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
  write: vi.fn(),
  resize: vi.fn(),
  flush: vi.fn(),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    modes = { bracketedPasteMode: false };
    buffer = { active: { type: "normal", baseY: 0, viewportY: 0 } };
    element: HTMLElement | null = null;
    options: Record<string, unknown>;
    resize = vi.fn((cols: number, rows: number) => {
      this.cols = cols;
      this.rows = rows;
    });
    constructor(options: Record<string, unknown>) {
      this.options = { scrollback: 1000, ...options };
      harness.terminals.push(this);
    }
    open(element: HTMLElement) {
      this.element = element;
      const viewport = document.createElement("div");
      viewport.className = "xterm-viewport";
      element.append(viewport);
    }
    loadAddon() {}
    attachCustomKeyEventHandler() {}
    write(_data: unknown, callback?: () => void) {
      callback?.();
    }
    writeln() {}
    clear() {}
    dispose() {}
    focus() {}
    getSelection() {
      return "";
    }
    onData() {
      return { dispose: vi.fn() };
    }
    onScroll() {
      return { dispose: vi.fn() };
    }
    onTitleChange() {
      return { dispose: vi.fn() };
    }
    onSelectionChange() {
      return { dispose: vi.fn() };
    }
  },
}));
vi.mock("@xterm/addon-webgl", () => ({ WebglAddon: class {} }));
vi.mock("@xterm/addon-search", () => ({
  SearchAddon: class {
    findNext() {}
    findPrevious() {}
  },
}));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));
vi.mock("../features/terminal/commandBlocks", () => ({
  CommandBlockManager: class {
    getBlockText() {
      return "";
    }
    scrollTo() {}
    navigate() {
      return null;
    }
    clear() {}
    dispose() {}
    feedInput() {}
  },
}));
vi.mock("../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: (term: { options: Record<string, unknown> }) => {
    const fontSize = Number(term.options.fontSize) || 13;
    return {
      viewport: { widthPx: 360, heightPx: 400 },
      metrics: { widthPx: fontSize / 2, heightPx: fontSize },
      grid: { cols: 1, rows: 1 },
    };
  },
  resizeTerminalToGrid: (
    term: { resize: (cols: number, rows: number) => void },
    grid: { cols: number; rows: number },
  ) => term.resize(grid.cols, grid.rows),
  resizeTerminalToGridPreservingSelection: (
    term: { resize: (cols: number, rows: number) => void },
    grid: { cols: number; rows: number },
  ) => term.resize(grid.cols, grid.rows),
}));
vi.mock("../features/terminal/gridRuntimeAdapter", () => ({
  productionGridRuntime: { resize: harness.resize, flush: harness.flush },
}));
vi.mock("../ipc/commands", () => ({
  terminalApi: {
    attach: harness.attach,
    attachTab: harness.attachTab,
    detach: harness.detach,
    setVisible: harness.setVisible,
    write: harness.write,
  },
  dockerApi: { execAttach: vi.fn() },
  sessionApi: { openLineTab: vi.fn() },
}));
vi.mock("../ipc/events", () => ({
  channelIdOf: () => "channel-1",
  createBinaryChannel: () => ({}),
  disposeChannel: vi.fn(),
  onChannelReopen: () => vi.fn(),
}));
vi.mock("../ipc/env", () => ({ clientId: () => "me" }));

import {
  XtermView,
  XTERM_DARK_THEME,
  XTERM_LIGHT_THEME,
  xtermThemeFor,
} from "../features/terminal/XtermView";
import {
  getAppearancePrefs,
  resetAppearancePrefs,
  setTerminalFontSize,
  setTerminalTheme,
} from "../app/preferences";
import { setThemeMode } from "../app/theme";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let frameId = 0;

async function flushWork(): Promise<void> {
  await act(async () => {
    const callbacks = [...harness.frames.values()];
    harness.frames.clear();
    for (const callback of callbacks) callback();
    await new Promise((resolve) => setTimeout(resolve, 0));
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function show(): Promise<void> {
  if (!root) {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  await act(async () => {
    root?.render(createElement(XtermView, { sessionId: "session", tabId: "pending" }));
  });
  await flushWork();
}

function luminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const channels = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => {
    const s = v / 255;
    return s <= 0.04045 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  });
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

function contrast(fg: string, bg: string): number {
  const hi = Math.max(luminance(fg), luminance(bg));
  const lo = Math.min(luminance(fg), luminance(bg));
  return (hi + 0.05) / (lo + 0.05);
}

beforeEach(() => {
  localStorage.clear();
  resetAppearancePrefs();
  setThemeMode("dark");
  harness.terminals.length = 0;
  harness.frames.clear();
  frameId = 0;
  harness.attach.mockReset().mockResolvedValue("tab-new");
  harness.attachTab.mockReset().mockResolvedValue({
    tabId: "tab-existing",
    cols: 90,
    rows: 30,
    controller: "other",
    subscribers: 1,
    viewers: 1,
    exited: false,
  });
  harness.resize.mockReset().mockResolvedValue(undefined);
  harness.flush.mockReset().mockResolvedValue(undefined);
  harness.write.mockReset().mockResolvedValue(undefined);
  harness.detach.mockReset().mockResolvedValue(undefined);
  harness.setVisible.mockReset().mockResolvedValue(undefined);

  vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
    const id = ++frameId;
    harness.frames.set(id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => harness.frames.delete(id));
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
});

describe("xterm palettes", () => {
  it("keeps foreground and white readable on both canvases", () => {
    for (const theme of [XTERM_DARK_THEME, XTERM_LIGHT_THEME]) {
      expect(contrast(theme.foreground, theme.background)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(theme.white, theme.background)).toBeGreaterThanOrEqual(4.5);
      expect(contrast(theme.brightWhite, theme.background)).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("keeps chromatic ansi colors at 3:1 or better on both canvases", () => {
    const chromatic = [
      "red",
      "brightRed",
      "green",
      "brightGreen",
      "yellow",
      "brightYellow",
      "blue",
      "brightBlue",
      "magenta",
      "brightMagenta",
      "cyan",
      "brightCyan",
    ] as const;
    for (const theme of [XTERM_DARK_THEME, XTERM_LIGHT_THEME]) {
      for (const key of chromatic) {
        expect(
          contrast(theme[key], theme.background),
          `${key} on ${theme.background}`,
        ).toBeGreaterThanOrEqual(3);
      }
    }
  });

  it("resolves the palette strictly by the resolved terminal theme", () => {
    expect(xtermThemeFor("dark")).toBe(XTERM_DARK_THEME);
    expect(xtermThemeFor("light")).toBe(XTERM_LIGHT_THEME);
  });
});

describe("XtermView appearance hot updates", () => {
  it("mounts with the persisted terminal font size and dark theme by default", async () => {
    await show();
    const term = harness.terminals[0];
    expect(term.options.fontSize).toBe(13);
    expect((term.options.theme as Record<string, string>).background).toBe(XTERM_DARK_THEME.background);
    expect(term.resize).toHaveBeenCalledWith(55, 30);
  });

  it("applies the persisted font size at mount", async () => {
    setTerminalFontSize(16);
    await show();
    expect(harness.terminals[0].options.fontSize).toBe(16);
  });

  it("hot-updates the font size and recomputes the grid", async () => {
    await show();
    const term = harness.terminals[0];
    term.resize.mockClear();
    harness.resize.mockClear();
    harness.flush.mockClear();

    act(() => {
      setTerminalFontSize(16);
    });
    await flushWork();

    expect(term.options.fontSize).toBe(16);
    expect(term.resize).toHaveBeenCalledWith(45, 25);
    expect(harness.resize).toHaveBeenCalledWith("tab-new", { cols: 45, rows: 25 });
    expect(harness.flush).toHaveBeenCalledWith("tab-new");
  });

  it("hot-updates the terminal theme on preference changes", async () => {
    await show();
    const term = harness.terminals[0];

    act(() => {
      setTerminalTheme("light");
    });
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_LIGHT_THEME.background,
    );

    act(() => {
      setTerminalTheme("dark");
    });
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_DARK_THEME.background,
    );
  });

  it("stays on dark when the interface theme flips and the pref is not following", async () => {
    await show();
    const term = harness.terminals[0];

    act(() => {
      setThemeMode("light");
    });
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_DARK_THEME.background,
    );
  });

  it("follows the interface theme only in interface mode", async () => {
    await show();
    const term = harness.terminals[0];

    act(() => {
      setThemeMode("light");
      setTerminalTheme("interface");
    });
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_LIGHT_THEME.background,
    );

    act(() => {
      setThemeMode("dark");
    });
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_DARK_THEME.background,
    );
  });

  it("does not recompute geometry when the font size is unchanged", async () => {
    await show();
    const term = harness.terminals[0];
    term.resize.mockClear();

    act(() => {
      setTerminalTheme("light");
    });
    await flushWork();

    expect(term.resize).not.toHaveBeenCalled();
    expect(getAppearancePrefs().terminalFontSize).toBe(13);
  });
});
