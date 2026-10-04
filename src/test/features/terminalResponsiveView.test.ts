/** @vitest-environment jsdom */

import { act } from "react";
import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RESIZE_END_EVENT } from "../../ui/ResizeHandle";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{
    cols: number;
    rows: number;
    modes: { bracketedPasteMode: boolean };
    buffer: { active: { type: string; baseY: number; viewportY: number } };
    scrollToBottom: ReturnType<typeof vi.fn>;
    element: HTMLElement | null;
    resize: ReturnType<typeof vi.fn>;
  }>,
  observers: [] as Array<() => void>,
  frames: new Map<number, () => void>(),
  channels: [] as Array<{ onBytes: (bytes: Uint8Array) => void }>,
  reopen: null as (() => void) | null,
  geometry: { widthPx: 360, heightPx: 400 },
  attach: vi.fn(),
  attachTab: vi.fn(),
  execAttach: vi.fn(),
  openLineTab: vi.fn(),
  resize: vi.fn(),
  flush: vi.fn(),
  write: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options = { scrollback: 1000 };
    element: HTMLElement | null = null;
    modes = {
      bracketedPasteMode: false,
    };
    buffer = {
      active: { type: "normal", baseY: 0, viewportY: 0 },
    };
    scrollToBottom = vi.fn(() => {
      this.buffer.active.viewportY = this.buffer.active.baseY;
    });
    resize = vi.fn((cols: number, rows: number) => {
      this.cols = cols;
      this.rows = rows;
    });

    constructor() {
      harness.terminals.push(this);
    }
    open(element: HTMLElement) {
      this.element = element;
      const viewport = document.createElement("div");
      viewport.className = "xterm-viewport";
      element.append(viewport);
    }
    loadAddon() {}
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
  },
}));
vi.mock("@xterm/addon-webgl", () => ({ WebglAddon: class {} }));
vi.mock("@xterm/addon-canvas", () => ({ CanvasAddon: class {} }));
vi.mock("@xterm/addon-search", () => ({
  SearchAddon: class {
    findNext() {}
    findPrevious() {}
  },
}));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));
vi.mock("../../features/terminal/commandBlocks", () => ({
  CommandBlockManager: class {
    copyBlock() { return ""; }
    getBlockText() { return ""; }
    scrollTo() {}
    navigate() { return null; }
    clear() {}
    dispose() {}
    feedInput() {}
  },
}));
vi.mock("../../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: () => {
    const { widthPx, heightPx } = harness.geometry;
    if (widthPx <= 0 || heightPx <= 0) return null;
    return {
      viewport: { widthPx, heightPx },
      metrics: { widthPx: 10, heightPx: 20 },
      grid: {
        cols: Math.max(1, Math.floor(widthPx / 10)),
        rows: Math.max(1, Math.floor(heightPx / 20)),
      },
    };
  },
  resizeTerminalToGrid: (
    term: { resize: (cols: number, rows: number) => void },
    grid: { cols: number; rows: number },
  ) => term.resize(grid.cols, grid.rows),
}));
vi.mock("../../features/terminal/gridRuntimeAdapter", () => ({
  productionGridRuntime: { resize: harness.resize, flush: harness.flush },
}));
vi.mock("../../ipc/commands", () => ({
  terminalApi: {
    attach: harness.attach,
    attachTab: harness.attachTab,
    detach: harness.detach,
    setVisible: harness.setVisible,
    write: harness.write,
  },
  dockerApi: { execAttach: harness.execAttach },
  sessionApi: { openLineTab: harness.openLineTab },
}));
vi.mock("../../ipc/events", () => ({
  channelIdOf: () => "channel-1",
  createBinaryChannel: (onBytes: (bytes: Uint8Array) => void) => {
    const channel = { onBytes };
    harness.channels.push(channel);
    return channel;
  },
  disposeChannel: vi.fn(),
  onChannelReopen: (_channel: unknown, callback: () => void) => {
    harness.reopen = callback;
    return vi.fn();
  },
}));
vi.mock("../../ipc/env", () => ({ clientId: () => "me" }));

import { XtermView, type TerminalHandle, type XtermViewProps } from "../../features/terminal/XtermView";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let frameId = 0;

async function flushWork(): Promise<void> {
  await act(async () => {
    const callbacks = [...harness.frames.values()];
    harness.frames.clear();
    for (const callback of callbacks) callback();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function show(props: XtermViewProps): Promise<void> {
  if (!root) {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  await act(async () => {
    root?.render(createElement(XtermView, props));
  });
  await flushWork();
}

function props(overrides: Partial<XtermViewProps> = {}): XtermViewProps {
  return { sessionId: "session", tabId: "pending", ...overrides };
}

beforeEach(() => {
  harness.terminals.length = 0;
  harness.observers.length = 0;
  harness.channels.length = 0;
  harness.frames.clear();
  harness.reopen = null;
  harness.geometry = { widthPx: 360, heightPx: 400 };
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
  harness.execAttach.mockReset().mockResolvedValue("tab-exec");
  harness.openLineTab.mockReset().mockResolvedValue("tab-line");
  harness.resize.mockReset().mockResolvedValue(undefined);
  harness.flush.mockReset().mockResolvedValue(undefined);
  harness.write.mockReset().mockResolvedValue(undefined);
  harness.detach.mockReset().mockResolvedValue(undefined);
  harness.setVisible.mockReset().mockResolvedValue(undefined);
  frameId = 0;

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
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
});

describe("XtermView production grid lifecycle", () => {
  it("opens a new PTY with measured geometry instead of the terminal default", async () => {
    await show(props());
    expect(harness.attach).toHaveBeenCalledWith(
      "session",
      36,
      20,
      expect.anything(),
    );
    expect(harness.attach).not.toHaveBeenCalledWith(
      "session",
      80,
      24,
      expect.anything(),
    );
  });

  it("waits for visibility before a new attach and measures again on resume", async () => {
    await show(props({ visible: false }));
    expect(harness.attach).not.toHaveBeenCalled();

    await show(props({ visible: true }));
    expect(harness.attach).toHaveBeenCalledWith(
      "session",
      36,
      20,
      expect.anything(),
    );
  });

  it("flushes the final measured grid at the workspace resize boundary", async () => {
    await show(props());
    harness.resize.mockClear();
    harness.flush.mockClear();
    harness.geometry = { widthPx: 560, heightPx: 400 };

    window.dispatchEvent(new Event(RESIZE_END_EVENT));
    await flushWork();

    expect(harness.resize).toHaveBeenCalledWith("tab-new", { cols: 56, rows: 20 });
    expect(harness.flush).toHaveBeenCalledWith("tab-new");
  });

  it("applies authoritative observer grids without submitting local resize", async () => {
    await show(props({ resumeTabId: "tab-existing", canResize: false }));
    const term = harness.terminals[0];
    term.resize.mockClear();
    harness.resize.mockClear();

    await show(
      props({
        resumeTabId: "tab-existing",
        canResize: false,
        remoteGrid: { cols: 100, rows: 32, revision: 2 },
      }),
    );

    expect(term.resize).toHaveBeenCalledWith(100, 32);
    expect(harness.resize).not.toHaveBeenCalled();
  });

  it("reuses the existing tab on channel reopen and synchronizes the latest grid", async () => {
    await show(props());
    harness.resize.mockClear();
    harness.attachTab.mockResolvedValue({
      tabId: "tab-new",
      cols: 90,
      rows: 30,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
    });

    harness.reopen?.();
    await flushWork();

    expect(harness.attachTab).toHaveBeenCalledWith("tab-new", expect.anything());
    expect(harness.resize).toHaveBeenCalledWith("tab-new", { cols: 36, rows: 20 });
    expect(harness.attach).toHaveBeenCalledTimes(1);
  });

  it("never touches the disposed terminal when unmounted during a resume attach", async () => {
    let resolveAttach: ((v: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveAttach = resolve;
        }),
    );
    await show(props({ resumeTabId: "tab-existing" }));
    const term = harness.terminals[0];
    term.resize.mockClear();

    await act(async () => root?.unmount());
    root = null;

    resolveAttach?.({
      tabId: "tab-existing",
      cols: 90,
      rows: 30,
      controller: "other",
      subscribers: 1,
      viewers: 1,
      exited: false,
    });
    await flushWork();

    expect(term.resize).not.toHaveBeenCalled();
    expect(harness.detach).toHaveBeenCalledWith("tab-existing", "channel-1");
  });
});

describe("XtermView terminal interaction", () => {
  async function showWithHandle(overrides: Partial<XtermViewProps> = {}) {
    const handles: TerminalHandle[] = [];
    await show(
      props({
        ...overrides,
        onHandle: (h) => {
          handles.push(h);
        },
      }),
    );
    const handle = handles[0];
    if (!handle) throw new Error("terminal handle not registered");
    return { handle, term: harness.terminals[0] };
  }

  function writtenText(): string[] {
    return harness.write.mock.calls.map((call) =>
      new TextDecoder().decode(call[1] as Uint8Array),
    );
  }

  it("pastes raw bytes by default and bracketed markers only in bracketed-paste mode", async () => {
    const { handle, term } = await showWithHandle();

    handle.paste("echo one\necho two\n");
    expect(writtenText()).toEqual(["echo one\necho two\n"]);

    term.modes.bracketedPasteMode = true;
    handle.paste("echo one\necho two\n");
    expect(writtenText()).toEqual([
      "echo one\necho two\n",
      "\x1b[200~echo one\necho two\n\x1b[201~",
    ]);
  });

  it("offers return-to-bottom with the lines below and hides it at the bottom", async () => {
    const { term } = await showWithHandle();
    expect(container?.querySelector("button")).toBeNull();
    const viewportEl = term.element?.querySelector(".xterm-viewport");
    if (!viewportEl) throw new Error("viewport element missing");

    await act(async () => {
      term.buffer.active.baseY = 50;
      term.buffer.active.viewportY = 20;
      viewportEl.dispatchEvent(new Event("scroll"));
    });
    const button = container?.querySelector("button");
    expect(button?.textContent).toContain("回到底部");
    expect(button?.textContent).toContain("30");

    await act(async () => {
      (button as HTMLButtonElement).click();
    });
    expect(term.scrollToBottom).toHaveBeenCalledOnce();

    await act(async () => {
      term.buffer.active.viewportY = term.buffer.active.baseY;
      viewportEl.dispatchEvent(new Event("scroll"));
    });
    expect(container?.querySelector("button")).toBeNull();
  });

  it("grows the lines-below count when output arrives while scrolled up", async () => {
    const { term } = await showWithHandle();
    const viewportEl = term.element?.querySelector(".xterm-viewport");
    if (!viewportEl) throw new Error("viewport element missing");

    await act(async () => {
      term.buffer.active.baseY = 50;
      term.buffer.active.viewportY = 44;
      viewportEl.dispatchEvent(new Event("scroll"));
    });
    expect(container?.querySelector("button")?.textContent).toContain("6");

    await act(async () => {
      term.buffer.active.baseY = 58;
      harness.channels[0]?.onBytes(new Uint8Array([104, 105]));
    });
    expect(container?.querySelector("button")?.textContent).toContain("14");
  });

  it("keeps the zero-resize contract for visual-viewport-only changes (IME)", async () => {
    const viewport = new EventTarget();
    if (!window.visualViewport) {
      Object.defineProperty(window, "visualViewport", {
        value: viewport,
        configurable: true,
      });
    }
    const target = window.visualViewport ?? viewport;
    await show(props());
    harness.resize.mockClear();
    harness.flush.mockClear();

    await act(async () => {
      target.dispatchEvent(new Event("resize"));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(harness.resize).not.toHaveBeenCalled();
    expect(harness.flush).not.toHaveBeenCalled();
  });
});
