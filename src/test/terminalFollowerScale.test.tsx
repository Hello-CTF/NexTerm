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
  viewport: { widthPx: 360, heightPx: 400 },
  observers: [] as Array<() => void>,
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
// A 360x400 pane unless a test changes it; a cell is half the font size wide and the font size high.
vi.mock("../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: (term: { options: Record<string, unknown> }) => {
    const fontSize = Number(term.options.fontSize) || 13;
    const viewport = { ...harness.viewport };
    return {
      viewport,
      metrics: { widthPx: fontSize / 2, heightPx: fontSize },
      grid: { cols: Math.floor(viewport.widthPx / (fontSize / 2)), rows: Math.floor(viewport.heightPx / fontSize) },
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
  FOLLOWER_SETTLE_MS,
  followerFontSize,
  predictedCell,
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
  harness.viewport = { widthPx: 360, heightPx: 400 };
  harness.observers.length = 0;
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
    constructor(callback: () => void) {
      harness.observers.push(callback);
    }
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
  vi.useRealTimers();
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

  it("picks the size against cells rounded up to device pixels, so one font change is enough", () => {
    const wide = { widthPx: 6.6, heightPx: 13 };
    // The plain estimate is 7.5 px; at 8 px the 4.06 px cells would round up to 5 px and overflow.
    const size = followerFontSize(13, { cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, wide, "fit");
    expect(size).toBe(7.5);
    expect(followerGridFits({ cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, predictedCell(13, wide, size), "fit").fits).toBe(true);
    expect(followerGridFits({ cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, predictedCell(13, wide, size + 0.5), "fit").fits).toBe(false);
  });

  it("fits only the rows when zoomed, and the columns then pan", () => {
    const size = followerFontSize(13, { cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, cell, "height");
    expect(size).toBe(13);
    const fit = followerGridFits({ cols: 90, rows: 30 }, { widthPx: 360, heightPx: 400 }, cell, "height");
    expect(fit).toEqual({ fits: true, widthFits: false, heightFits: true });
  });

  it("flags text below the readable share and classifies pinches", () => {
    expect(followerReadable({ ratio: 0.62, zoom: "fit", panX: false, panY: false })).toBe(true);
    expect(followerReadable({ ratio: 0.4, zoom: "fit", panX: false, panY: false })).toBe(false);
    expect(followerReadable({ ratio: 0.62, zoom: "fit", panX: true, panY: true })).toBe(false);
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
    const detach = attachFollowerGestures(element, { following: () => following, panX: () => panX, panY: () => false, onPinch });
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
      // Not following: nothing pans.
      element.dispatchEvent(touchEvent("touchstart", [[200, 100]]));
      const ignored = touchEvent("touchmove", [[150, 102]]);
      element.dispatchEvent(ignored);
      expect(ignored.defaultPrevented).toBe(false);
      element.dispatchEvent(touchEvent("touchend", []));
      element.dispatchEvent(touchEvent("touchstart", [[100, 100], [200, 100]]));
      element.dispatchEvent(touchEvent("touchmove", [[50, 100], [250, 100]]));
      element.dispatchEvent(touchEvent("touchend", []));
      expect(onPinch).not.toHaveBeenCalled();
    } finally {
      detach();
    }
  });
});

describe("follower vertical pan", () => {
  it("pans rows taller than the pane and keeps the swipe from the scrollback scrolling", () => {
    const element = document.createElement("div");
    const detach = attachFollowerGestures(element, { following: () => true, panX: () => false, panY: () => true, onPinch: vi.fn() });
    // The terminal's own touch scrolling is attached after the gestures.
    const scrollback = vi.fn();
    element.addEventListener("touchmove", scrollback, { capture: true });
    try {
      element.dispatchEvent(touchEvent("touchstart", [[100, 200]]));
      const move = touchEvent("touchmove", [[102, 150]]);
      element.dispatchEvent(move);
      expect(move.defaultPrevented).toBe(true);
      expect(scrollback).not.toHaveBeenCalled();
      element.dispatchEvent(touchEvent("touchend", []));
    } finally {
      element.removeEventListener("touchmove", scrollback, { capture: true });
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
    expect(onFollowerScale).toHaveBeenLastCalledWith({ ratio: 0.62, zoom: "fit", panX: false, panY: false });

    // A pane resize while following keeps the PTY grid and sends nothing.
    act(() => {
      window.dispatchEvent(new Event("nexterm:resize-end"));
    });
    await flushWork();
    expect(harness.resize).not.toHaveBeenCalled();

    // Zoomed: the rows fit at the user's font and the columns pan.
    await render({ canResize: false, remoteGrid: { cols: 90, rows: 30, revision: 1 }, followerZoom: "height", onFollowerScale });
    expect(term.options.fontSize).toBe(13);
    expect(onFollowerScale).toHaveBeenLastCalledWith({ ratio: 1, zoom: "height", panX: true, panY: false });
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

  it("holds the font during a drag and applies it once after the drag settles", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await render({ canResize: true });
    const term = harness.terminals[0];
    await render({ canResize: false, remoteGrid: { cols: 90, rows: 30, revision: 1 } });
    expect(term.options.fontSize).toBe(8);
    const sizes: unknown[] = [];
    const options = term.options;
    term.options = new Proxy(options, {
      set(target, key, value) {
        if (key === "fontSize") sizes.push(value);
        return Reflect.set(target, key, value);
      },
    });
    // The pane narrows step by step, as during a drag.
    for (const width of [340, 320, 300, 270]) {
      harness.viewport = { widthPx: width, heightPx: 400 };
      act(() => harness.observers.forEach((callback) => callback()));
      await flushWork();
    }
    expect(sizes).toEqual([]);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(FOLLOWER_SETTLE_MS + 10);
    });
    // 90 columns in 270 px: 3 px cells, a 6 px font, set once.
    expect(sizes).toEqual([6]);
    expect(harness.resize).not.toHaveBeenCalled();
  });

  it("warns and pans vertically when even the smallest font leaves the grid taller than the pane", async () => {
    const onFollowerScale = vi.fn<(scale: FollowerScale | null) => void>();
    await render({ canResize: true, onFollowerScale });
    const term = harness.terminals[0];
    await render({ canResize: false, remoteGrid: { cols: 400, rows: 200, revision: 1 }, onFollowerScale });
    expect(term.options.fontSize).toBe(FOLLOWER_MIN_FONT_PX);
    expect(onFollowerScale).toHaveBeenLastCalledWith({ ratio: 0.31, zoom: "fit", panX: true, panY: true });
    expect(host().style.overflowY).toBe("hidden");
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
  it("is a compact chip that names the follow state, explains on demand and toggles the zoom", () => {
    const element = document.createElement("div");
    document.body.append(element);
    const badgeRoot = createRoot(element);
    const onZoom = vi.fn();
    const scale = (patch: Partial<FollowerScale>): FollowerScale => ({ ratio: 0.4, zoom: "fit", panX: false, panY: false, ...patch });
    try {
      act(() => badgeRoot.render(createElement(FollowerSizeBadge, { scale: scale({}), ownedElsewhere: true, zoom: "fit", onZoom })));
      const chip = element.querySelector<HTMLButtonElement>("[data-follower-chip]")!;
      expect(chip.textContent).toBe("跟随 · 40%");
      expect(chip.getAttribute("aria-label")).toBe("终端尺寸跟随另一台设备，缩放到 40%");
      expect(element.querySelector('[role="status"]')?.textContent).toBe("终端尺寸跟随另一台设备");
      expect(element.querySelector("[data-follower-small]")).not.toBeNull();

      // The explanation is behind the chip and collapses again.
      expect(element.querySelector("[data-follower-why]")).toBeNull();
      act(() => {
        chip.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(chip.getAttribute("aria-expanded")).toBe("true");
      expect(element.querySelector("[data-follower-why]")?.textContent).toContain("在此输入即改用本设备尺寸");
      act(() => {
        chip.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(element.querySelector("[data-follower-why]")).toBeNull();

      const zoom = element.querySelector<HTMLButtonElement>("[data-follower-zoom-toggle]")!;
      expect(zoom.textContent).toBe("放大文字");
      expect(zoom.getAttribute("aria-pressed")).toBe("false");
      act(() => {
        zoom.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      });
      expect(onZoom).toHaveBeenCalledWith("height");
      act(() => badgeRoot.render(createElement(FollowerSizeBadge, { scale: scale({ ratio: 1, zoom: "height", panX: true }), ownedElsewhere: true, zoom: "height", onZoom })));
      const pressed = element.querySelector<HTMLButtonElement>("[data-follower-zoom-toggle]")!;
      expect(pressed.textContent).toBe("放大文字");
      expect(pressed.getAttribute("aria-pressed")).toBe("true");

      act(() => badgeRoot.render(createElement(FollowerSizeBadge, { scale: scale({ ratio: 0.31, panX: true, panY: true }), ownedElsewhere: true, zoom: "fit", onZoom })));
      expect(element.querySelector("[data-follower-small]")?.textContent).toContain("上下滑动查看");

      act(() => badgeRoot.render(createElement(FollowerSizeBadge, { scale: scale({ ratio: 1 }), ownedElsewhere: true, zoom: "fit", onZoom })));
      // A grid that fits at the user's size has nothing to zoom.
      expect(element.querySelector("[data-follower-zoom-toggle]")).toBeNull();
      expect(element.querySelector("[data-follower-chip]")?.textContent).toBe("跟随");
    } finally {
      act(() => badgeRoot.unmount());
      element.remove();
    }
  });

  it("suggests a pinch only on a coarse pointer", () => {
    const element = document.createElement("div");
    document.body.append(element);
    const badgeRoot = createRoot(element);
    const original = window.matchMedia;
    const scale: FollowerScale = { ratio: 0.3, zoom: "fit", panX: false, panY: false };
    const render = () => act(() => badgeRoot.render(createElement(FollowerSizeBadge, { scale, ownedElsewhere: true, zoom: "fit", onZoom: vi.fn() })));
    try {
      render();
      expect(element.querySelector("[data-follower-small]")?.textContent).toBe("文字较小：点「放大文字」");
      act(() => badgeRoot.unmount());
      window.matchMedia = ((query: string) => ({ ...original(query), matches: true })) as typeof window.matchMedia;
      const touchRoot = createRoot(element);
      act(() => touchRoot.render(createElement(FollowerSizeBadge, { scale, ownedElsewhere: true, zoom: "fit", onZoom: vi.fn() })));
      expect(element.querySelector("[data-follower-small]")?.textContent).toContain("双指张开");
      act(() => touchRoot.unmount());
    } finally {
      window.matchMedia = original;
      element.remove();
    }
  });
});
