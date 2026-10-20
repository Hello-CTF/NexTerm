/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Profiler, act, createElement } from "react";
import { flushMacrotask, mount, type MountedView } from "./features/reactTestUtils";

interface FakeTerminal {
  cols: number;
  rows: number;
  options: Record<string, unknown>;
  modes: { bracketedPasteMode: boolean };
  buffer: { active: { baseY: number; viewportY: number } };
  writeLog: string[];
  pendingWrites: (() => void)[];
  handlers: {
    data: ((data: string) => void) | null;
    scroll: (() => void) | null;
    title: ((title: string) => void) | null;
    selection: (() => void) | null;
  };
  drainWrites: (count?: number) => void;
}

const harness = vi.hoisted(() => ({
  terminals: [] as unknown[],
  channel: null as { onBytes?: (bytes: Uint8Array) => void; onControl?: (control: unknown) => void } | null,
  reopenCb: null as (() => void) | null,
  feedInput: vi.fn(),
  feedOSC133: vi.fn(),
  blocksClear: vi.fn(),
  attach: vi.fn(),
  attachTab: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
  write: vi.fn(),
  pushToast: vi.fn(),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    modes = { bracketedPasteMode: false };
    buffer = { active: { baseY: 0, viewportY: 0 } };
    writeLog: string[] = [];
    pendingWrites: (() => void)[] = [];
    handlers: {
      data: ((data: string) => void) | null;
      scroll: (() => void) | null;
      title: ((title: string) => void) | null;
      selection: (() => void) | null;
    } = { data: null, scroll: null, title: null, selection: null };
    constructor() {
      harness.terminals.push(this);
    }
    open() {}
    loadAddon() {}
    attachCustomKeyEventHandler() {}
    write(data: string | Uint8Array, callback?: () => void) {
      this.writeLog.push(typeof data === "string" ? data : new TextDecoder().decode(data));
      if (callback) this.pendingWrites.push(callback);
    }
    drainWrites(count?: number) {
      if (count === undefined) {
        let guard = 0;
        while (this.pendingWrites.length > 0 && guard++ < 10_000) this.pendingWrites.shift()!();
        return;
      }
      for (let i = 0; i < count && this.pendingWrites.length > 0; i += 1) {
        this.pendingWrites.shift()!();
      }
    }
    clear() {}
    clearSelection() {}
    dispose() {}
    focus() {}
    scrollToBottom() {}
    selectAll() {}
    getSelection() {
      return "";
    }
    onData(callback: (data: string) => void) {
      this.handlers.data = callback;
      return { dispose: () => undefined };
    }
    onScroll(callback: () => void) {
      this.handlers.scroll = callback;
      return { dispose: () => undefined };
    }
    onTitleChange(callback: (title: string) => void) {
      this.handlers.title = callback;
      return { dispose: () => undefined };
    }
    onSelectionChange(callback: () => void) {
      this.handlers.selection = callback;
      return { dispose: () => undefined };
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
    feedInput = harness.feedInput;
    feedOSC133 = harness.feedOSC133;
    getBlockText() {
      return "";
    }
    scrollTo() {}
    navigate() {
      return null;
    }
    clear() {
      harness.blocksClear();
    }
    dispose() {}
  },
}));

vi.mock("../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: () => ({
    viewport: { widthPx: 640, heightPx: 480 },
    metrics: { widthPx: 8, heightPx: 16 },
    grid: { cols: 80, rows: 30 },
  }),
  resizeTerminalToGridPreservingSelection: () => undefined,
}));

vi.mock("../features/terminal/gridRuntimeAdapter", () => ({
  productionGridRuntime: {
    resize: vi.fn().mockResolvedValue(undefined),
    flush: vi.fn().mockResolvedValue(undefined),
  },
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
  createBinaryChannel: (
    onBytes: (bytes: Uint8Array) => void,
    onControl?: (control: unknown) => void,
  ) => {
    harness.channel = { onBytes, onControl };
    return {};
  },
  disposeChannel: vi.fn(),
  onChannelReopen: (_channel: unknown, callback: () => void) => {
    harness.reopenCb = callback;
    return () => {
      harness.reopenCb = null;
    };
  },
}));

vi.mock("../ipc/env", () => ({ WEB: false }));
vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
  openExternalURL: vi.fn(),
}));
vi.mock("../app/keybindings", () => ({ matchAppKeybinding: () => null }));
vi.mock("../app/store", () => ({
  useUi: {
    getState: () => ({ sessions: [], pushToast: harness.pushToast }),
  },
}));
vi.mock("../app/preferences", () => ({
  getAppearancePrefs: () => ({ terminalFontSize: 13 }),
  getInputPrefs: () => ({ selectionAutoCopy: false }),
  getResolvedTerminalTheme: () => "dark",
  subscribeAppearancePrefs: () => () => undefined,
}));
vi.mock("../ui/clipboard", () => ({ copyText: vi.fn().mockResolvedValue(undefined) }));

import { XtermView } from "../features/terminal/XtermView";

const enc = new TextEncoder();

let mounted: MountedView | undefined;
let commits = 0;
let frameId = 0;
let frames = new Map<number, () => void>();

function runFrames() {
  const queued = [...frames.values()];
  frames.clear();
  for (const callback of queued) callback();
}

function term(): FakeTerminal {
  const t = harness.terminals[0] as FakeTerminal | undefined;
  if (!t) throw new Error("no fake terminal");
  return t;
}

function buttonText(): string {
  return [...(mounted?.container.querySelectorAll("button") ?? [])]
    .map((b) => b.textContent ?? "")
    .join(" ");
}

async function show(props: Record<string, unknown> = {}) {
  frames = new Map();
  frameId = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: () => void) => {
    const id = ++frameId;
    frames.set(id, callback);
    return id;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  commits = 0;
  mounted = mount(
    createElement(
      Profiler,
      { id: "xterm-view", onRender: () => { commits += 1; } },
      createElement(XtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
    ),
  );
  await act(async () => {
    await flushMacrotask();
    await flushMacrotask();
  });
}

async function attachOk() {
  await act(async () => {
    await flushMacrotask();
  });
}

function feedBytes(data: string) {
  act(() => {
    harness.channel?.onBytes?.(enc.encode(data));
  });
}

function feedControl(control: Record<string, unknown>) {
  act(() => {
    harness.channel?.onControl?.(control);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  harness.terminals.length = 0;
  harness.channel = null;
  harness.reopenCb = null;
  harness.attach.mockResolvedValue({
    tabId: "kernel-1",
    cols: 80,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
    retainable: false,
  });
  harness.attachTab.mockResolvedValue({
    tabId: "kernel-1",
    cols: 80,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
  });
  harness.detach.mockResolvedValue(undefined);
  harness.setVisible.mockResolvedValue(undefined);
  harness.write.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("XtermView stream wiring", () => {
  it("attaches through the channel and routes automatic responses and input upstream", async () => {
    await show();
    await attachOk();
    expect(harness.attach).toHaveBeenCalledWith("session-1", 80, 30, expect.anything());
    const t = term();
    act(() => t.handlers.data?.("\x1b[?1;2c"));
    expect(harness.write).toHaveBeenCalledWith("kernel-1", enc.encode("\x1b[?1;2c"));
    act(() => t.handlers.data?.("ls\r"));
    expect(harness.write).toHaveBeenCalledWith("kernel-1", enc.encode("ls\r"));
    expect(harness.feedInput).toHaveBeenCalledWith("ls\r");
  });

  it("suppresses pre-expiry OSC events whose write callbacks fire after the expired control frame", async () => {
    const onNotification = vi.fn();
    await show({ onNotification });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]9;stale notification\x07");
    expect(t.writeLog).toEqual(["before"]);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true });
    expect(harness.blocksClear).toHaveBeenCalledTimes(1);
    expect(t.writeLog.some((entry) => entry.includes("输出历史已过期"))).toBe(true);
    feedBytes("after\x1b]9;fresh notification\x07");
    act(() => t.drainWrites());
    expect(onNotification).toHaveBeenCalledTimes(1);
    expect(onNotification).toHaveBeenCalledWith("fresh notification");
    expect(t.writeLog[0]).toBe("before");
    expect(t.writeLog[t.writeLog.length - 1]).toBe("after");
  });

  it("keeps pre-expiry events when the reattach control frame is not expired", async () => {
    const onNotification = vi.fn();
    await show({ onNotification });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]9;still valid\x07");
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 7, controlBytes: 0 });
    feedBytes("after");
    act(() => t.drainWrites());
    expect(onNotification).toHaveBeenCalledWith("still valid");
  });

  it("pauses upstream writes past the high watermark and resumes with visibility", async () => {
    const onStreamBacklog = vi.fn();
    await show({ onStreamBacklog });
    await attachOk();
    const t = term();
    const flood = new Uint8Array((4 << 20) + 4096).fill(0x61);
    act(() => {
      harness.channel?.onBytes?.(flood);
    });
    expect(onStreamBacklog).toHaveBeenCalledTimes(1);
    const backlog = onStreamBacklog.mock.calls[0][0];
    expect(backlog.paused).toBe(true);
    expect(backlog.pendingBytes).toBe(flood.length);
    act(() => {
      harness.channel?.onBytes?.(new Uint8Array(4096).fill(0x62));
    });
    expect(t.writeLog.length).toBe(1);
    act(() => t.drainWrites());
    expect(onStreamBacklog).toHaveBeenLastCalledWith(null);
    expect(t.writeLog.length).toBe(2);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(flood.length + 4096);
  });

  it("bounds the FIFO under a sustained flood: refuses overflow, detaches, and replays from the accepted offset", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Four 4 MiB chunks reach the 16 MiB hard cap; the writer is paused at
    // the 4 MiB high watermark after the first one.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.pushToast).not.toHaveBeenCalled();
    // The fifth chunk is refused: it never enters the FIFO or the accounting.
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledTimes(1);
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    expect(harness.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("重新同步"));
    // Refusal is sticky: even after the FIFO drains, new chunks are refused
    // until the reattach succeeds, so the accepted prefix stays contiguous.
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(4);
    act(() => harness.channel?.onBytes?.(new Uint8Array(1024).fill(0x62)));
    expect(t.writeLog.length).toBe(4);
    expect(harness.detach).toHaveBeenCalledTimes(1);
    // Transport reopen → reattach with a resume anchor at the last accepted
    // byte; the refused suffix will be replayed from there.
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledWith(
      "kernel-1",
      expect.anything(),
      -1,
      { seq: 4 * (4 << 20), epoch: "e1" },
      "session-1",
    );
    // The replay redelivers the refused chunk: accepted and rendered in order.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });
});

describe("XtermView render coalescing (T-09 component portion)", () => {
  it("coalesces scroll-driven linesBelow updates into one render per frame", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    const before = commits;
    for (const y of [10, 20, 30, 40, 50]) {
      t.buffer.active.viewportY = y;
      act(() => t.handlers.scroll?.());
    }
    expect(commits).toBe(before);
    act(() => runFrames());
    expect(commits).toBe(before + 1);
    expect(buttonText()).toContain("50 行");
  });

  it("coalesces title changes to the latest one per frame and skips duplicates", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    act(() => {
      t.handlers.title?.("t1");
      t.handlers.title?.("t2");
      t.handlers.title?.("t3");
    });
    expect(onTitle).not.toHaveBeenCalled();
    act(() => runFrames());
    expect(onTitle.mock.calls).toEqual([["t3"]]);
    act(() => {
      t.handlers.title?.("t3");
    });
    act(() => runFrames());
    expect(onTitle).toHaveBeenCalledTimes(1);
    act(() => {
      t.handlers.title?.("t4");
    });
    act(() => runFrames());
    expect(onTitle.mock.calls).toEqual([["t3"], ["t4"]]);
  });

  it("coalesces write-completion updates into one render per frame", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 42;
    t.buffer.active.viewportY = 0;
    const before = commits;
    feedBytes("one\r\n");
    act(() => t.drainWrites());
    feedBytes("two\r\n");
    act(() => t.drainWrites());
    feedBytes("three\r\n");
    act(() => t.drainWrites());
    expect(commits).toBe(before);
    act(() => runFrames());
    expect(commits).toBe(before + 1);
    expect(buttonText()).toContain("42 行");
  });
});
