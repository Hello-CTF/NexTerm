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
  call: vi.fn(),
  execAttach: vi.fn(),
  openLineTab: vi.fn(),
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
  dockerApi: { execAttach: harness.execAttach },
  sessionApi: { openLineTab: harness.openLineTab },
  call: harness.call,
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
    retainable: true,
    ephemeral: false,
  });
  harness.attachTab.mockResolvedValue({
    tabId: "kernel-1",
    cols: 80,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    retainable: true,
    ephemeral: false,
  });
  harness.detach.mockResolvedValue(undefined);
  harness.setVisible.mockResolvedValue(undefined);
  harness.write.mockResolvedValue(undefined);
  harness.call.mockReset().mockResolvedValue(undefined);
  harness.execAttach.mockReset().mockResolvedValue("kernel-1");
  harness.openLineTab.mockReset().mockResolvedValue("kernel-1");
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
    // The high watermark asked the server to pause the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: expect.any(Number),
    });
    // The fifth chunk is refused: it never enters the FIFO or the accounting.
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledTimes(1);
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    expect(harness.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("重新同步"));
    // Refusal is sticky: even after the FIFO drains, new chunks are refused
    // until the attachment boundary, so the accepted prefix stays contiguous.
    act(() => t.drainWrites());
    expect(t.writeLog.length).toBe(4);
    act(() => harness.channel?.onBytes?.(new Uint8Array(1024).fill(0x62)));
    expect(t.writeLog.length).toBe(4);
    expect(harness.detach).toHaveBeenCalledTimes(1);
    // Draining asked the server to resume the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
    });
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

  it("accepts replay that arrives before the attach response (attachment boundary, not response timing)", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledTimes(1);
    act(() => t.drainWrites());
    // The reattach RPC stays in flight while the anchor and replay arrive.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => harness.reopenCb?.());
    // The anchor frame marks the attachment stream boundary: the episode
    // ends here even though the attach response has not been processed.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * (4 << 20), controlBytes: 0 });
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
    });
    await act(async () => {
      await flushMacrotask();
    });
    // The replay was accepted before the response and rendered in order:
    // four original chunks plus the replayed chunk.
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("ephemeral tabs: a working pause keeps the full sequence lossless with no refusal", async () => {
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
      ephemeral: true,
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // The first chunk reaches the high watermark and the client pauses the
    // feed instead of letting the FIFO hit the cap.
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: expect.any(Number),
    });
    // The feed is paused (no more bytes arrive); the FIFO drains below the
    // low watermark and the client resumes the feed.
    act(() => t.drainWrites());
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
    });
    // The feed resumes and the full sequence arrives in order: nothing was
    // refused, so there is no hole.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
      act(() => t.drainWrites());
    }
    expect(harness.pushToast).not.toHaveBeenCalledWith("error", expect.anything());
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("ephemeral tabs: a failed pause retries and then surfaces an accurate overflow state", async () => {
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
      ephemeral: true,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 5 * (4 << 20) }),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // The pause RPC failed every retry: the client redelivers the refused
    // suffix from the tab's recovery ring, never reaps the PTY, and the full
    // sequence arrives contiguously.
    expect(harness.detach).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: 4 * (4 << 20),
    });
    // The replay arrives with a synthetic anchor that ends the episode. The
    // writer drains to make room for it, and the refused chunk is restored
    // in order.
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    // Caught up: the client resumes the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
    });
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
    vi.useRealTimers();
  });

  it("winrm line tabs: a failed pause recovers the refused suffix instead of detaching", async () => {
    // Production-shaped WinRM overflow: the first attach goes through
    // session_open_line_tab, whose tab is ephemeral. A cap refusal must take
    // the recovery path — an ordinary detach here would reap the line tab's
    // process — and the full sequence stays contiguous.
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve(undefined),
    );
    vi.useFakeTimers();
    await show({ winrm: true });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.openLineTab).toHaveBeenCalledWith("session-1", 80, 30, expect.anything());
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: expect.any(Number),
    });
    // The pause RPC failed every retry: the client redelivers the refused
    // suffix from the tab's recovery ring. An ordinary detach would reap the
    // line tab's process, so it must never be called.
    expect(harness.detach).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: 4 * (4 << 20),
    });
    // The synthetic anchor ends the episode; the refused chunk is restored
    // behind it in order, still with no detach (no reap).
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    expect(harness.detach).not.toHaveBeenCalled();
    // The pause-failure toast may fire, but the recovery itself must not
    // report unrecoverable output: the replay restored the full sequence.
    expect(harness.pushToast).not.toHaveBeenCalledWith(
      "error",
      "终端输出积压过长，早期输出无法恢复，画面可能不完整",
    );
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
    vi.useRealTimers();
  });

  it("ephemeral tabs: the refusal episode persists through the low-water drain until the recovery anchor", async () => {
    // A refused frame must stay refused across the low-water drain: ending
    // the episode there accepts a later old frame and breaks the contiguous
    // prefix, which the scalar skipLeading cannot represent.
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
      ephemeral: true,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 22 << 20 }),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const a = new Uint8Array(4 << 20).fill(0x61);
    const b = new Uint8Array(4 << 20).fill(0x62);
    const c = new Uint8Array(1 << 20).fill(0x63);
    const d = new Uint8Array(1 << 20).fill(0x64);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(a));
    }
    act(() => harness.channel?.onBytes?.(b));
    act(() => harness.channel?.onBytes?.(c));
    // The writer drains to low water: the episode must NOT end here — only a
    // real recovery anchor ends it.
    act(() => t.drainWrites());
    act(() => harness.channel?.onBytes?.(d));
    // The recovery anchor ends the episode; the replay restores the refused
    // suffix (b+c+d) in order, so the full sequence stays contiguous.
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    const replay = new Uint8Array(6 << 20);
    replay.set(b, 0);
    replay.set(c, 4 << 20);
    replay.set(d, 5 << 20);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    const rendered = t.writeLog.join("");
    expect(rendered.length).toBe(22 << 20);
    // Contiguous original sequence: a(16 MiB) → b(4 MiB) → c(1 MiB) → d(1 MiB),
    // checked at the segment boundaries.
    expect(rendered.slice((16 << 20) - 4, (16 << 20) + 4)).toBe("aaaabbbb");
    expect(rendered.slice((20 << 20) - 4, (20 << 20) + 4)).toBe("bbbbcccc");
    expect(rendered.slice((21 << 20) - 4, (21 << 20) + 4)).toBe("ccccdddd");
    // Caught up: the client resumes the feed.
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
    });
    vi.useRealTimers();
  });

  it("ephemeral tabs: a refusal during an in-flight recover schedules a serial re-recovery", async () => {
    // A replay larger than the writer cap is refused again while the first
    // recover RPC is still in flight. The follow-up refusal must be recorded
    // and serially recovered with the latest accepted offset — never dropped.
    let resolveRecover: ((value: unknown) => void) | undefined;
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
      ephemeral: true,
    });
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.reject(new Error("no such command"));
      if (command === "terminal_recover") {
        return new Promise((resolve) => {
          resolveRecover = resolve;
        });
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // The first recover is in flight (deferred). The anchor ends the episode.
    expect(harness.call).toHaveBeenCalledTimes(2);
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: 4 * (4 << 20),
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
    });
    // A 20 MiB replay arrives: the writer accepts 16 MiB of it, then refuses
    // the last 4 MiB while the first RPC is still in flight.
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    // Complete the first RPC: the pending refusal must now serially fire a
    // second recover with the latest accepted offset (32 MiB).
    resolveRecover?.({ flushedOffset: 36 << 20 });
    await act(async () => {
      await flushMacrotask();
    });
    // The pending refusal serially fired a second recover with the latest
    // accepted offset (32 MiB).
    expect(harness.call).toHaveBeenLastCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      acceptedBytes: 32 << 20,
    });
    // Complete the second RPC as well; the second replay restores the last
    // 4 MiB and the client resumes.
    resolveRecover?.({ flushedOffset: 36 << 20 });
    await act(async () => {
      await flushMacrotask();
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 32 << 20,
      controlBytes: 0,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
    });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(9 * (4 << 20));
    vi.useRealTimers();
  });

  it("visibility and controller-only changes do not detach or recreate the terminal", async () => {
    await show();
    await attachOk();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
    const rerender = async (props: Record<string, unknown>) => {
      await act(async () => {
        mounted!.root.render(
          createElement(
            Profiler,
            { id: "xterm-view", onRender: () => { commits += 1; } },
            createElement(XtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
          ),
        );
        await flushMacrotask();
      });
    };
    // Only `visible` changed: no remount, no detach, no new xterm instance.
    await rerender({ visible: false });
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
    // Only `canResize` changed: same expectations.
    await rerender({ canResize: false });
    expect(harness.detach).not.toHaveBeenCalled();
    expect(harness.terminals.length).toBe(1);
    expect(harness.attach).toHaveBeenCalledTimes(1);
  });

  it("docker exec accepts replay that arrives before the attach response", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    act(() => t.drainWrites());
    // The resume RPC stays in flight while the replay arrives: the episode
    // must already be closed so the replay is accepted, not discarded.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => harness.reopenCb?.());
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: false,
      ephemeral: true,
    });
    await act(async () => {
      await flushMacrotask();
    });
    expect(t.writeLog.length).toBe(5);
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("docker exec pauses via detach and resumes through attachTab/ResumeExec", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    // The high watermark pauses the exec stream through DetachStream.
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.pushToast).toHaveBeenCalledWith("info", expect.stringContaining("重新同步"));
    act(() => t.drainWrites());
    // The reopen resumes the exec through attachTab/ResumeExec, which
    // replays the bounded backlog; the episode ends at that boundary.
    harness.attachTab.mockClear();
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledWith("kernel-1", expect.anything(), -1, undefined, undefined);
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
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
