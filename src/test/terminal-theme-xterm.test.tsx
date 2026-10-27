/** @vitest-environment jsdom */

import { act } from "react";
import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./features/reactTestUtils";

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
    buffer = { active: { type: "normal", baseY: 0, viewportY: 0 }, onBufferChange: () => ({ dispose: () => {} }) };
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
    parser = { registerOscHandler: () => ({ dispose: () => undefined }) };
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
vi.mock("../ipc/env", () => ({ clientId: () => "me", WEB: false }));

import {
  XtermView,
  XTERM_DARK_THEME,
  XTERM_LIGHT_THEME,
  xtermThemeFor,
} from "../features/terminal/XtermView";
import {
  TERMINAL_FONT_STACKS,
  XTERM_DARK_HIGH_CONTRAST_THEME,
  XTERM_DARK_MAXIMUM_CONTRAST_THEME,
  XTERM_LIGHT_HIGH_CONTRAST_THEME,
  XTERM_LIGHT_MAXIMUM_CONTRAST_THEME,
} from "../features/terminal/xtermTheme";
import {
  getAppearancePrefs,
  resetAppearancePrefs,
  setTerminalContrast,
  setTerminalFontFamily,
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
    await flushMacrotask();
    await flushMacrotask();
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
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-new",
    sessionId: "session",
    cols: 80,
    rows: 24,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
  });
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

const chromaticColors = [
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
const textColors = ["foreground", "white", "brightWhite"] as const;
const measuredAnsiColors = ["brightBlack", ...chromaticColors] as const;
const paletteCases = [
  { name: "standard dark", theme: XTERM_DARK_THEME, text: 4.5, ansi: 3 },
  { name: "standard light", theme: XTERM_LIGHT_THEME, text: 4.5, ansi: 3 },
  { name: "high dark", theme: XTERM_DARK_HIGH_CONTRAST_THEME, text: 7, ansi: 7 },
  { name: "high light", theme: XTERM_LIGHT_HIGH_CONTRAST_THEME, text: 7, ansi: 7 },
  { name: "maximum dark", theme: XTERM_DARK_MAXIMUM_CONTRAST_THEME, text: 12, ansi: 12 },
  { name: "maximum light", theme: XTERM_LIGHT_MAXIMUM_CONTRAST_THEME, text: 12, ansi: 12 },
];

function hue(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((v) => v / 255);
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const delta = max - min;
  if (delta === 0) return 0;
  let value: number;
  if (max === r) value = 60 * (((g - b) / delta) % 6);
  else if (max === g) value = 60 * ((b - r) / delta + 2);
  else value = 60 * ((r - g) / delta + 4);
  return (value + 360) % 360;
}

function hueDistance(a: string, b: string): number {
  const difference = Math.abs(hue(a) - hue(b));
  return Math.min(difference, 360 - difference);
}

describe("xterm palettes", () => {
  it("measures foreground and key ANSI contrast for every dark/light preset", () => {
    for (const { name, theme, text, ansi } of paletteCases) {
      for (const key of textColors) {
        expect(contrast(theme[key], theme.background), `${name} ${key}`).toBeGreaterThanOrEqual(text);
      }
      for (const key of measuredAnsiColors) {
        expect(contrast(theme[key], theme.background), `${name} ${key}`).toBeGreaterThanOrEqual(ansi);
      }
    }
  });

  it("does not reduce measured contrast when moving to a stronger preset", () => {
    for (const [standard, high, maximum] of [
      [XTERM_DARK_THEME, XTERM_DARK_HIGH_CONTRAST_THEME, XTERM_DARK_MAXIMUM_CONTRAST_THEME],
      [XTERM_LIGHT_THEME, XTERM_LIGHT_HIGH_CONTRAST_THEME, XTERM_LIGHT_MAXIMUM_CONTRAST_THEME],
    ]) {
      for (const key of [...textColors, ...measuredAnsiColors]) {
        const standardRatio = contrast(standard[key], standard.background);
        const highRatio = contrast(high[key], high.background);
        const maximumRatio = contrast(maximum[key], maximum.background);
        expect(highRatio, `high ${key}`).toBeGreaterThanOrEqual(standardRatio);
        expect(maximumRatio, `maximum ${key}`).toBeGreaterThanOrEqual(highRatio);
      }
    }
  });

  it("preserves ANSI hues and selection/cursor behavior across presets", () => {
    for (const [standard, high, maximum] of [
      [XTERM_DARK_THEME, XTERM_DARK_HIGH_CONTRAST_THEME, XTERM_DARK_MAXIMUM_CONTRAST_THEME],
      [XTERM_LIGHT_THEME, XTERM_LIGHT_HIGH_CONTRAST_THEME, XTERM_LIGHT_MAXIMUM_CONTRAST_THEME],
    ]) {
      for (const theme of [high, maximum]) {
        for (const key of chromaticColors) {
          expect(hueDistance(standard[key], theme[key]), key).toBeLessThanOrEqual(2);
        }
        expect(theme.selectionBackground).toBe(standard.selectionBackground);
        expect(theme.cursorAccent).toBe(theme.background);
        expect(theme).not.toHaveProperty("cursor");
      }
    }
  });

  it("resolves actual palettes by theme and contrast while retaining the standard default", () => {
    expect(xtermThemeFor("dark")).toBe(XTERM_DARK_THEME);
    expect(xtermThemeFor("light")).toBe(XTERM_LIGHT_THEME);
    expect(xtermThemeFor("dark", "standard")).toBe(XTERM_DARK_THEME);
    expect(xtermThemeFor("dark", "high")).toBe(XTERM_DARK_HIGH_CONTRAST_THEME);
    expect(xtermThemeFor("dark", "maximum")).toBe(XTERM_DARK_MAXIMUM_CONTRAST_THEME);
    expect(xtermThemeFor("light", "high")).toBe(XTERM_LIGHT_HIGH_CONTRAST_THEME);
    expect(xtermThemeFor("light", "maximum")).toBe(XTERM_LIGHT_MAXIMUM_CONTRAST_THEME);
  });
});

describe("XtermView appearance hot updates", () => {
  it("mounts with the persisted terminal font size and dark theme by default", async () => {
    await show();
    const term = harness.terminals[0];
    expect(term.options.fontSize).toBe(13);
    expect(term.options.scrollbar).toEqual({ width: 10 });
    expect((term.options.theme as Record<string, string>).background).toBe(XTERM_DARK_THEME.background);
    expect(term.resize).toHaveBeenCalledWith(55, 30);
  });

  it("applies the persisted font size at mount", async () => {
    setTerminalFontSize(16);
    await show();
    expect(harness.terminals[0].options.fontSize).toBe(16);
  });

  it("mounts with the current Cascadia default and persisted font/contrast preferences", async () => {
    setTerminalFontFamily("jetbrains-mono");
    setTerminalContrast("high");
    await show();
    const term = harness.terminals[0];
    expect(term.options.fontFamily).toBe(TERMINAL_FONT_STACKS["jetbrains-mono"]);
    expect((term.options.theme as Record<string, string>).background).toBe(
      XTERM_DARK_HIGH_CONTRAST_THEME.background,
    );

    act(() => resetAppearancePrefs());
    expect(term.options.fontFamily).toBe(TERMINAL_FONT_STACKS.cascadia);
    expect((term.options.theme as Record<string, string>).background).toBe(XTERM_DARK_THEME.background);
  });

  it("hot-updates font and contrast on the same terminal without reattaching", async () => {
    await show();
    const term = harness.terminals[0];
    const options = term.options;
    const initialTheme = term.options.theme as Record<string, string>;
    expect(initialTheme.cursor).toBe("#516cd6");
    expect(initialTheme.selectionBackground).toBe(XTERM_DARK_THEME.selectionBackground);
    harness.attach.mockClear();

    act(() => setTerminalFontFamily("fira-code"));
    expect(term.options).toBe(options);
    expect(term.options.fontFamily).toBe(TERMINAL_FONT_STACKS["fira-code"]);

    act(() => setTerminalContrast("maximum"));
    const maximumTheme = term.options.theme as Record<string, string>;
    expect(maximumTheme.background).toBe(XTERM_DARK_MAXIMUM_CONTRAST_THEME.background);
    expect(maximumTheme.cursor).toBe(initialTheme.cursor);
    expect(maximumTheme.selectionBackground).toBe(initialTheme.selectionBackground);
    expect(maximumTheme.cursorAccent).toBe(maximumTheme.background);
    expect(harness.terminals).toHaveLength(1);
    expect(harness.terminals[0]).toBe(term);
    expect(harness.attach).not.toHaveBeenCalled();
    expect(harness.attachTab).not.toHaveBeenCalled();
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
