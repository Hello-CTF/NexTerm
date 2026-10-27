/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./features/reactTestUtils";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{
    cols: number;
    rows: number;
    options: Record<string, unknown>;
    resize: ReturnType<typeof vi.fn>;
  }>,
  frames: new Map<number, () => void>(),
  attach: vi.fn(),
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
    modes = { bracketedPasteMode: false, mouseTrackingMode: "none" };
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
vi.mock("@xterm/addon-search", () => ({ SearchAddon: class { findNext() {} findPrevious() {} } }));
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
// A 360x400 pane; a cell is half the font size wide and the font size high.
vi.mock("../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: (term: { options: Record<string, unknown> }) => {
    const fontSize = Number(term.options.fontSize) || 13;
    return {
      viewport: { widthPx: 360, heightPx: 400 },
      metrics: { widthPx: fontSize / 2, heightPx: fontSize },
      grid: { cols: Math.floor(360 / (fontSize / 2)), rows: Math.floor(400 / fontSize) },
    };
  },
  resizeTerminalToGrid: (term: { resize: (cols: number, rows: number) => void }, grid: { cols: number; rows: number }) =>
    term.resize(grid.cols, grid.rows),
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
    attachTab: vi.fn(),
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

import { XtermView, type XtermViewProps } from "../features/terminal/XtermView";
import { resetAppearancePrefs, setTerminalFontSize } from "../app/preferences";
import {
  FOLLOWER_MIN_FONT_PX,
  followerFontSize,
  followerGridFits,
  followerReadable,
  pinchDirection,
  type FollowerScale,
} from "../features/terminal/followerScale";
import { attachFollowerGestures } from "../features/terminal/followerGestures";
import { FollowerSizeBadge } from "../features/terminal/FollowerSizeBadge";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let frameId = 0;

async function flushWork(): Promise<void> {
  await act(async () => {
    for (let round = 0; round < 3; round++) {
      const callbacks = [...harness.frames.values()];
      harness.frames.clear();
      for (const callback of callbacks) callback();
      await flushMacrotask();
    }
  });
}

async function render(props: Partial<XtermViewProps>): Promise<void> {
  if (!root) {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  await act(async () => {
    root?.render(createElement(XtermView, { sessionId: "session", tabId: "pending", ...props }));
  });
  await flushWork();
}

function host(): HTMLElement {
  const found = container?.querySelector<HTMLElement>(".nx-xterm-host");
  if (!found) throw new Error("terminal host not found");
  return found;
}

function touchEvent(type: string, points: Array<[number, number]>): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "touches", { value: points.map(([clientX, clientY], identifier) => ({ clientX, clientY, identifier })) });
  return event;
}

beforeEach(() => {
  localStorage.clear();
  resetAppearancePrefs();
  harness.terminals.length = 0;
  harness.frames.clear();
  frameId = 0;
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-1",
    sessionId: "session",
    cols: 72,
    rows: 30,
    sizeOwner: "me",
    subscribers: 2,
    viewers: 2,
    exited: false,
    shell: "unknown",
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
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  vi.unstubAllGlobals();
});

describe("follower scale", () => {
  const cell = { widthPx: 6.5, heightPx: 13 };

  it("shrinks the font in half-pixel steps until the grid fits, never past the user's size", () => {
    // 90 columns of 6.5 px need 585 px; the pane has 360.
    const size = followerFontSize(13, { cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, cell, "fit");
    expect(size).toBe(8);
    expect(followerGridFits({ cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, { widthPx: 4, heightPx: 8 }, "fit").fits).toBe(true);
    // A grid smaller than the pane keeps the user's font: the view never enlarges text.
    expect(followerFontSize(13, { cols: 40, rows: 10 }, { widthPx: 360, heightPx: 400 }, cell, "fit")).toBe(13);
    // A huge grid stops at the smallest size.
    expect(followerFontSize(13, { cols: 1000, rows: 300 }, { widthPx: 360, heightPx: 400 }, cell, "fit")).toBe(FOLLOWER_MIN_FONT_PX);
  });

  it("fits only the rows when zoomed, and the columns then pan", () => {
    const size = followerFontSize(13, { cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, cell, "height");
    expect(size).toBe(13);
    const fit = followerGridFits({ cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, cell, "height");
    expect(fit).toEqual({ fits: true, widthFits: false });
  });

  it("flags text below the readable share and classifies pinches", () => {
    expect(followerReadable({ ratio: 0.62, zoom: "fit", panX: false })).toBe(true);
    expect(followerReadable({ ratio: 0.4, zoom: "fit", panX: false })).toBe(false);
    expect(pinchDirection(100, 150)).toBe("out");
    expect(pinchDirection(150, 100)).toBe("in");
    expect(pinchDirection(100, 105)).toBeNull();
    expect(pinchDirection(0, 100)).toBeNull();
  });
});

describe("follower gestures", () => {
  it("reports pinches and pans wide columns only while following", () => {
    const element = document.createElement("div");
    let following = true;
    let panX = false;
    const onPinch = vi.fn();
    const detach = attachFollowerGestures(element, { following: () => following, panX: () => panX, onPinch });
    try {
      element.dispatchEvent(touchEvent("touchstart", [[100, 100], [200, 100]]));
      element.dispatchEvent(touchEvent("touchmove", [[50, 100], [250, 100]]));
      element.dispatchEvent(touchEvent("touchend", []));
      expect(onPinch).toHaveBeenCalledWith("out");

      panX = true;
      element.scrollLeft = 0;
      element.dispatchEvent(touchEvent("touchstart", [[200, 100]]));
      const move = touchEvent("touchmove", [[150, 102]]);
      element.dispatchEvent(move);
      expect(move.defaultPrevented).toBe(true);
      element.dispatchEvent(touchEvent("touchend", []));

      // A vertical swipe stays with the terminal's scrolling.
      element.dispatchEvent(touchEvent("touchstart", [[200, 100]]));
      const vertical = touchEvent("touchmove", [[202, 160]]);
      element.dispatchEvent(vertical);
      expect(vertical.defaultPrevented).toBe(false);
      element.dispatchEvent(touchEvent("touchend", []));

      following = false;
      onPinch.mockClear();
      element.dispatchEvent(touchEvent("touchstart", [[100, 100], [200, 100]]));
      element.dispatchEvent(touchEvent("touchmove", [[50, 100], [250, 100]]));
      element.dispatchEvent(touchEvent("touchend", []));
      expect(onPinch).not.toHaveBeenCalled();
    } finally {
      detach();
    }
  });
});

describe("terminal view following another device's size", () => {
  it("renders the PTY grid with a smaller font, sends no resize, and takes the size back at the user's font", async () => {
    const onFollowerScale = vi.fn<(scale: FollowerScale | null) => void>();
    await render({ canResize: true, onFollowerScale });
    const term = harness.terminals[0];
    expect(term.options.fontSize).toBe(13);
    expect(onFollowerScale).not.toHaveBeenCalled();

    // Another device typed: it owns the size, and the PTY is 90x30.
    harness.resize.mockClear();
    await render({ canResize: false, remoteGrid: { cols: 90, rows: 30, revision: 1 }, onFollowerScale });
    expect([term.cols, term.rows]).toEqual([90, 30]);
    expect(term.options.fontSize).toBe(8);
    expect(onFollowerScale).toHaveBeenLastCalledWith({ ratio: 0.62, zoom: "fit", panX: false });

    // A pane resize while following keeps the PTY grid and sends nothing.
    act(() => {
      window.dispatchEvent(new Event("nexterm:resize-end"));
    });
    await flushWork();
    expect(harness.resize).not.toHaveBeenCalled();

    // Zoomed: the rows fit at the user's font and the columns pan.
    await render({ canResize: false, remoteGrid: { cols: 90, rows: 30, revision: 1 }, followerZoom: "height", onFollowerScale });
    expect(term.options.fontSize).toBe(13);
    expect(onFollowerScale).toHaveBeenLastCalledWith({ ratio: 1, zoom: "height", panX: true });
    expect(host().style.overflowX).toBe("hidden");
    expect(harness.resize).not.toHaveBeenCalled();

    // This device typed and owns the size again: its grid is measured at the user's font (55x30, not 90x50).
    await render({ canResize: true, remoteGrid: { cols: 90, rows: 30, revision: 1 }, followerZoom: "fit", onFollowerScale });
    expect(term.options.fontSize).toBe(13);
    expect(onFollowerScale).toHaveBeenLastCalledWith(null);
    expect(host().style.overflowX).toBe("");
    expect(harness.resize).toHaveBeenCalledWith("tab-1", { cols: 55, rows: 30 });
    expect(harness.resize).not.toHaveBeenCalledWith("tab-1", { cols: 90, rows: 50 });
  });

  it("follows a font size change while following without leaving the shrunken size", async () => {
    await render({ canResize: true });
    const term = harness.terminals[0];
    await render({ canResize: false, remoteGrid: { cols: 90, rows: 30, revision: 1 } });
    expect(term.options.fontSize).toBe(8);
    act(() => setTerminalFontSize(16));
    await flushWork();
    // 16 px cells of 8 px need 720 px for 90 columns; half the user's size fits the 360 px pane.
    expect(term.options.fontSize).toBe(8);
    await render({ canResize: true, remoteGrid: { cols: 90, rows: 30, revision: 1 } });
    expect(term.options.fontSize).toBe(16);
  });

  it("leaves the owner's font and resizing alone", async () => {
    const onFollowerScale = vi.fn();
    await render({ canResize: true, onFollowerScale });
    const term = harness.terminals[0];
    act(() => {
      window.dispatchEvent(new Event("nexterm:resize-end"));
    });
    await flushWork();
    expect(term.options.fontSize).toBe(13);
    expect([term.cols, term.rows]).toEqual([55, 30]);
    expect(onFollowerScale).not.toHaveBeenCalled();
    expect(host().style.overflowX).toBe("");
  });
});

describe("follower size badge", () => {
  it("says the size follows another device, how to take it, and toggles the zoom", () => {
    const element = document.createElement("div");
    document.body.append(element);
    const badgeRoot = createRoot(element);
    const onZoom = vi.fn();
    try {
      act(() => badgeRoot.render(createElement(FollowerSizeBadge, {
        scale: { ratio: 0.4, zoom: "fit", panX: false },
        ownedElsewhere: true,
        zoom: "fit",
        onZoom,
      })));
      expect(element.textContent).toContain("尺寸跟随另一台设备 · 40%");
      expect(element.textContent).toContain("在此输入即改用本设备尺寸");
      expect(element.querySelector("[data-follower-small]")).not.toBeNull();
      const button = element.querySelector("button");
      expect(button?.getAttribute("aria-pressed")).toBe("false");
      act(() => {
        button?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(onZoom).toHaveBeenCalledWith("height");

      act(() => badgeRoot.render(createElement(FollowerSizeBadge, {
        scale: { ratio: 1, zoom: "fit", panX: false },
        ownedElsewhere: true,
        zoom: "fit",
        onZoom,
      })));
      // A grid that fits at the user's size has nothing to zoom.
      expect(element.querySelector("button")).toBeNull();
    } finally {
      act(() => badgeRoot.unmount());
      element.remove();
    }
  });
});
