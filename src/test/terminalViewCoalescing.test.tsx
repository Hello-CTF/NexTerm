/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Profiler, act, createElement } from "react";
import { deferred, flushMacrotask, mount, type MountedView } from "./features/reactTestUtils";

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
  host: HTMLElement | null;
}

const harness = vi.hoisted(() => ({
  terminals: [] as unknown[],
  channel: null as {
    onBytes?: (bytes: Uint8Array) => void;
    onControl?: (control: unknown) => void;
    onReplay?: (control: unknown) => void;
    onClass?: (control: unknown) => void;
    onInputOwner?: (control: unknown) => void;
  } | null,
  channels: [] as { onBytes?: (bytes: Uint8Array) => void; onControl?: (control: unknown) => void }[],
  // When set, the fake terminal answers DA1 and DSR 6 queries through onData
  // at the moment the write that carries them is parsed (its callback runs).
  emitReplies: false,
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
    buffer = { active: { baseY: 0, viewportY: 0 }, onBufferChange: () => ({ dispose: () => {} }) };
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
    parser = { registerOscHandler: () => ({ dispose: () => undefined }) };
    host: HTMLElement | null = null;
    open(host: HTMLElement) {
      this.host = host;
    }
    loadAddon() {}
    attachCustomKeyEventHandler() {}
    write(data: string | Uint8Array, callback?: () => void) {
      this.writeLog.push(typeof data === "string" ? data : new TextDecoder().decode(data));
      if (harness.emitReplies) {
        const text = typeof data === "string" ? data : new TextDecoder().decode(data);
        this.pendingWrites.push(() => {
          for (const query of text.match(/\x1b\[(?:c|6n)/g) ?? []) {
            this.handlers.data?.(query === "\x1b[c" ? "\x1b[?1;2c" : "\x1b[1;1R");
          }
          for (const title of text.matchAll(/\x1b\]0;([^\x07]*)\x07/g)) this.handlers.title?.(title[1]);
          callback?.();
        });
        return;
      }
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
    onReplay?: (control: unknown) => void,
    onClass?: (control: unknown) => void,
    onInputOwner?: (control: unknown) => void,
  ) => {
    const entry = { onBytes, onControl, onReplay, onClass, onInputOwner };
    harness.channel = entry;
    harness.channels.push(entry);
    return {};
  },
  disposeChannel: vi.fn(),
  sendChannelInput: (
    _channel: unknown,
    _tabId: string,
    data: Uint8Array,
    http: (data: Uint8Array) => Promise<unknown>,
  ) => void http(data),
  onChannelReopen: (_channel: unknown, callback: () => void) => {
    harness.reopenCb = callback;
    return () => {
      harness.reopenCb = null;
    };
  },
}));

vi.mock("../ipc/env", () => ({ WEB: false, clientId: () => "me" }));
vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
  openExternalURL: vi.fn(),
}));
vi.mock("../app/keybindings", () => ({ matchAppKeybinding: () => null }));
vi.mock("../app/store", () => ({
  useUi: {
    getState: () => ({
      sessions: [],
      pushToast: harness.pushToast,
      registerHelperOperationIntent: (
        _tabId: string,
        _kind: "deploy" | "reattach",
        _originalTabId?: string,
      ) => undefined,
    }),
  },
}));
vi.mock("../app/preferences", () => ({
  getAppearancePrefs: () => ({ terminalFontSize: 13 }),
  getInputPrefs: () => ({ selectionAutoCopy: false }),
  terminalDefaultUsesAccount: () => false,
  getTerminalPrefs: () => ({ closeAction: "ask", defaultMode: "durable", gpuAcceleration: "auto" }),
  subscribeTerminalPrefs: () => () => undefined,
  waitAccountPreferences: () => null,
  getResolvedTerminalTheme: () => "dark",
  subscribeAppearancePrefs: () => () => undefined,
}));
vi.mock("../ui/clipboard", () => ({ copyText: vi.fn().mockResolvedValue(undefined) }));

import { XtermView, type XtermViewProps } from "../features/terminal/XtermView";
import { getTerminalLinesBelow, resetTerminalView, useTerminalViewStore } from "../app/terminalViewStore";

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

function buttonOccurrences(text: string): number {
  return [...(mounted?.container.querySelectorAll("button") ?? [])]
    .map((b) => b.textContent ?? "")
    .filter((t) => t.includes(text)).length;
}

// Counts every render of the XtermView element driven from above: with the view state
// in the root store the component has no own subscriptions, so this stays flat across
// output bursts — only the badge's precise selector re-renders.
const xtermViewRenders = new Map<string, number>();

function CountingXtermView(props: XtermViewProps) {
  xtermViewRenders.set(props.tabId, (xtermViewRenders.get(props.tabId) ?? 0) + 1);
  return createElement(XtermView, props);
}

function stubFrames() {
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
}

async function show(props: Record<string, unknown> = {}) {
  stubFrames();
  commits = 0;
  mounted = mount(
    createElement(
      Profiler,
      { id: "xterm-view", onRender: () => { commits += 1; } },
      createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
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
  xtermViewRenders.clear();
  act(() => resetTerminalView());
  harness.terminals.length = 0;
  harness.channel = null;
  harness.channels.length = 0;
  harness.reopenCb = null;
  harness.emitReplies = false;
  harness.attach.mockResolvedValue({
    tabId: "kernel-1",
    cols: 80,
    rows: 30,
    sizeOwner: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
    retainable: true,
    ephemeral: false,
    attachmentId: 11,
  });
  harness.attachTab.mockResolvedValue({
    tabId: "kernel-1",
    cols: 80,
    rows: 30,
    sizeOwner: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    retainable: true,
    ephemeral: false,
    attachmentId: 11,
  });
  harness.detach.mockResolvedValue(undefined);
  harness.setVisible.mockResolvedValue(undefined);
  harness.write.mockResolvedValue(undefined);
  harness.call.mockReset().mockImplementation((command: string) => {
    if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
    if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
    return Promise.resolve(undefined);
  });
  harness.execAttach.mockReset().mockResolvedValue("kernel-1");
  harness.openLineTab.mockReset().mockResolvedValue({
    tabId: "kernel-1",
    sessionId: "session-1",
    cols: 80,
    rows: 30,
    sizeOwner: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
    retainable: false,
    ephemeral: true,
    attachmentId: 7,
  });
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

  it("rate-limits an automatic reply flood without any toast and logs it for diagnostics", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    await show();
    await attachOk();
    const t = term();
    // Freeze the limiter's clock so the bucket cannot refill mid-flood: the
    // outcome must not depend on how fast this machine runs the loop.
    vi.useFakeTimers({ toFake: ["Date"] });
    act(() => {
      for (let i = 0; i < 5000; i += 1) t.handlers.data?.("\x1b[?1;2c");
    });
    const upstreamBytes = harness.write.mock.calls.reduce((n, [, bytes]) => n + (bytes as Uint8Array).length, 0);
    expect(upstreamBytes).toBeGreaterThan(0);
    // Only the burst goes out: far below the 35 KB the flood asked for.
    expect(upstreamBytes).toBeLessThanOrEqual(20 * 1024);
    // Keystrokes still go straight through while replies are held back.
    act(() => t.handlers.data?.("q"));
    expect(harness.write).toHaveBeenLastCalledWith("kernel-1", enc.encode("q"));
    mounted?.unmount();
    mounted = undefined;
    expect(harness.pushToast).not.toHaveBeenCalled();
    const reports = warn.mock.calls.filter(([message]) => String(message).includes("automatic query replies"));
    expect(reports).toHaveLength(1);
    expect(reports[0][1]).toMatchObject({ coalesced: expect.any(Number), dropped: 1 });
    vi.useRealTimers();
    warn.mockRestore();
  });

  it("suppresses pre-expiry OSC events whose write callbacks fire after the expired control frame", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]52;c;stale\x07");
    expect(t.writeLog).toEqual(["before"]);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true });
    expect(harness.blocksClear).toHaveBeenCalledTimes(1);
    expect(t.writeLog.some((entry) => entry.includes("输出历史已过期"))).toBe(true);
    feedBytes("after\x1b]52;c;fresh\x07");
    act(() => t.drainWrites());
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;fresh");
    expect(t.writeLog[0]).toBe("before");
    expect(t.writeLog[t.writeLog.length - 1]).toBe("after");
  });

  it("keeps pre-expiry events when the reattach control frame is not expired", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedBytes("before\x1b]52;c;valid\x07");
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 7, controlBytes: 0 });
    feedBytes("after");
    act(() => t.drainWrites());
    expect(onClipboard).toHaveBeenCalledWith("c;valid");
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
      clientId: "me",
      attachmentId: 11,
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
    // Draining reattaches the detached subscription without a transport
    // reopen. A resume RPC alone cannot recreate that subscription.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledWith(
      "kernel-1",
      expect.anything(),
      -1,
      { seq: 4 * (4 << 20), epoch: "e1" },
      "session-1",
    );
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * (4 << 20), controlBytes: 0, attachmentId: 12 });
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
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
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

  it("keeps a replay overflow refused after the attach response and resumes before the gap", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const attach = deferred<Record<string, unknown>>();
    const recovery = deferred<Record<string, unknown>>();
    harness.attachTab.mockReturnValueOnce(attach.promise).mockReturnValueOnce(recovery.promise);
    act(() => harness.reopenCb?.());
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, attachmentId: 12 });

    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let index = 0; index < 5; index++) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    expect(harness.detach).toHaveBeenCalledTimes(1);
    // Free enough capacity for the late small chunk while keeping the
    // writer paused, so a replacement cannot end the refusal episode yet.
    act(() => t.drainWrites(1));
    await act(async () => {
      attach.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, retainable: true, ephemeral: false, attachmentId: 12 });
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    const later = new Uint8Array(1024).fill(0x62);
    act(() => harness.channel?.onBytes?.(later));
    act(() => t.drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(t.writeLog.reduce((total, entry) => total + entry.length, 0)).toBe(4 * chunk.length);
    expect(harness.attachTab).toHaveBeenNthCalledWith(
      2, "kernel-1", expect.anything(), -1,
      { seq: 4 * chunk.length, epoch: "e1" }, "session-1",
    );

    // The next ordered anchor restores the refused suffix first; the late
    // bytes then follow it instead of being rendered across the missing gap.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * chunk.length, controlBytes: 0, attachmentId: 13 });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => harness.channel?.onBytes?.(later));
    act(() => t.drainWrites());
    await act(async () => {
      recovery.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, retainable: true, ephemeral: false, attachmentId: 13 });
      await flushMacrotask();
    });
    expect(t.writeLog.reduce((total, entry) => total + entry.length, 0)).toBe(5 * chunk.length + later.length);
    expect(t.writeLog.at(-1)).toBe("b".repeat(later.length));
  });

  it("ephemeral tabs: a working pause keeps the full sequence lossless with no refusal", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
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
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    // The feed is paused (no more bytes arrive); the FIFO drains below the
    // low watermark and the client resumes the feed.
    act(() => t.drainWrites());
    // The pause response carrying the episode token lands, then the resume
    // fires with it.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
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
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 }),
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
      clientId: "me",
      attachmentId: 11,
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
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
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
      clientId: "me",
      attachmentId: 7,
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
      clientId: "me",
      attachmentId: 7,
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
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 22 << 20, episodeToken: 31 }),
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
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
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
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
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
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_recover")).toHaveLength(1);
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
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
    resolveRecover?.({ flushedOffset: 36 << 20, episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    // The pending refusal serially fired a second recover with the latest
    // accepted offset (32 MiB).
    expect(harness.call).toHaveBeenLastCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: 32 << 20,
    });
    // Complete the second RPC as well; the second replay restores the last
    // 4 MiB and the client resumes.
    resolveRecover?.({ flushedOffset: 36 << 20, episodeToken: 31 });
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
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(9 * (4 << 20));
    vi.useRealTimers();
  });

  it("unmount stops the pause retry: no new pause RPCs after unmount", async () => {
    // The pause retry must not keep calling a detached channel after the
    // component unmounts: the timer is cleared and the retry entry checks
    // disposed, so no new terminal_pause calls fire post-unmount.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve(undefined),
    );
    vi.useFakeTimers();
    await show();
    await attachOk();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    // Reach the high watermark so the pause retry loop starts (and keeps
    // failing, so it would retry forever if not torn down).
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    const callsBeforeUnmount = harness.call.mock.calls.length;
    mounted!.unmount();
    mounted = undefined;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(350);
    });
    expect(harness.call.mock.calls.length).toBe(callsBeforeUnmount);
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
            createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1", ...props }),
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
    // The resume RPC stays in flight while the replay arrives: the episode
    // must already be closed so the replay is accepted, not discarded.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => t.drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalled();
    const replay = new Uint8Array(4 << 20).fill(0x62);
    act(() => harness.channel?.onBytes?.(replay));
    act(() => t.drainWrites());
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: false,
      ephemeral: true,
      attachmentId: 12,
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
    // The drain resumes the exec through attachTab/ResumeExec, which
    // replays the bounded backlog without reopening the transport.
    await act(async () => {
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

  it("reattaches Docker after the high watermark drains without overflowing or reopening transport", async () => {
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    expect(harness.detach).toHaveBeenCalledWith("kernel-1", "channel-1");
    expect(harness.attachTab).not.toHaveBeenCalled();
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledWith("kernel-1", expect.anything(), -1, undefined, undefined);
  });

  it.each([false, true])("waits for a pending detach before drain/reopen can replace the subscription (Docker=%s)", async (docker) => {
    const detach = deferred<void>();
    harness.detach.mockReturnValueOnce(detach.promise);
    await show(docker ? { containerId: "container-1" } : {});
    await attachOk();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let index = 0; index < (docker ? 1 : 5); index++) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => term().drainWrites());
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
    });
    expect(harness.attachTab).not.toHaveBeenCalled();
    await act(async () => {
      detach.resolve();
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    expect(harness.attachTab).toHaveBeenCalledWith(
      "kernel-1", expect.anything(), -1,
      docker ? undefined : { seq: 4 * chunk.length, epoch: "e1" },
      docker ? undefined : "session-1",
    );
  });

  it("does not reattach a closed view after its backpressure detach completes", async () => {
    const detach = deferred<void>();
    harness.detach.mockReturnValueOnce(detach.promise);
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    act(() => term().drainWrites());
    mounted?.unmount();
    mounted = undefined;
    await act(async () => { detach.resolve(); await flushMacrotask(); });
    expect(harness.attachTab).not.toHaveBeenCalled();
  });

  it("recovers a detached subscription even when the detach response is lost", async () => {
    harness.detach.mockRejectedValueOnce(new Error("response lost"));
    await show({ containerId: "container-1" });
    await attachOk();
    act(() => harness.channel?.onBytes?.(new Uint8Array(4 << 20).fill(0x61)));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
  });

  it("preserves another drain while a backpressure reattach is still replaying", async () => {
    const attach = deferred<Record<string, unknown>>();
    harness.attachTab.mockReturnValueOnce(attach.promise);
    await show({ containerId: "container-1" });
    await attachOk();
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => term().drainWrites());
    await act(async () => { await flushMacrotask(); });
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    await act(async () => {
      attach.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, attachmentId: 12 });
      await flushMacrotask();
    });
    expect(harness.attachTab).toHaveBeenCalledTimes(2);
  });

  it("normal attach: every stream ACK carries the response's attachment incarnation", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array((1 << 20) + 4096).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    const acks = harness.call.mock.calls.filter((c) => c[0] === "terminal_stream_ack");
    expect(acks.length).toBeGreaterThan(0);
    for (const ack of acks) {
      expect(ack[1]).toMatchObject({
        tabId: "kernel-1",
        channelId: "channel-1",
        clientId: "me",
        attachmentId: 11,
      });
    }
    // The periodic ACK reports the accepted offset in absolute coordinates.
    expect(acks[acks.length - 1][1].acceptedBytes).toBe(chunk.length);
  });

  it("reopen: the response's new incarnation is adopted and re-reported immediately", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    // The reopen response adopted the new incarnation: the immediate
    // re-report carries it — never the previous incarnation and never a
    // default 0 — and does not wait for the next 1 MiB boundary.
    const acks = harness.call.mock.calls.filter((c) => c[0] === "terminal_stream_ack");
    expect(acks.length).toBeGreaterThan(0);
    expect(acks[acks.length - 1][1]).toMatchObject({
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 12,
      acceptedBytes: 4 << 20,
    });
  });

  it("deferred response: the anchor's incarnation is adopted and a stale response cannot override it", async () => {
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    // The reattach RPC stays in flight while the anchor arrives first.
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(
      () => new Promise((resolve) => { finish = resolve; }),
    );
    act(() => harness.reopenCb?.());
    // The anchor carries the new incarnation: the immediate re-report uses it
    // even though the attach response has not arrived.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 4 * (4 << 20), controlBytes: 0, attachmentId: 12 });
    let acks = harness.call.mock.calls.filter((c) => c[0] === "terminal_stream_ack");
    expect(acks[acks.length - 1][1]).toMatchObject({
      attachmentId: 12,
      acceptedBytes: 4 * (4 << 20),
    });
    // A stale deferred response (older incarnation) must not override it.
    finish?.({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 11,
    });
    await act(async () => {
      await flushMacrotask();
    });
    const more = new Uint8Array((1 << 20) + 4096).fill(0x62);
    act(() => harness.channel?.onBytes?.(more));
    act(() => t.drainWrites());
    acks = harness.call.mock.calls.filter((c) => c[0] === "terminal_stream_ack");
    expect(acks[acks.length - 1][1]).toMatchObject({
      attachmentId: 12,
      acceptedBytes: 4 * (4 << 20) + more.length,
    });
  });

  it("the complete continuous sequence keeps every control frame on the current incarnation", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) =>
      command === "terminal_pause"
        ? Promise.reject(new Error("no such command"))
        : Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 }),
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
    await act(async () => {
      await vi.advanceTimersByTimeAsync(300);
    });
    // The refusal episode runs entirely on incarnation 11.
    expect(harness.call).toHaveBeenCalledWith("terminal_recover", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: 4 * (4 << 20),
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    expect(harness.call).toHaveBeenCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    expect(t.writeLog.length).toBe(5);
    // Reopen: incarnation 12 takes over mid-stream.
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 5 * (4 << 20), controlBytes: 0, attachmentId: 12 });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    // Every post-reopen frame carries incarnation 12, and the accepted
    // sequence stays contiguous across the transition.
    const acks = harness.call.mock.calls.filter((c) => c[0] === "terminal_stream_ack");
    expect(acks[acks.length - 1][1]).toMatchObject({
      attachmentId: 12,
      acceptedBytes: 6 * (4 << 20),
    });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(6 * (4 << 20));
    vi.useRealTimers();
  });

  it("a completed recovery emits exactly one terminal_resume, carrying the learned episode token", async () => {
    // The server's episode token comes back with the pause and recover
    // responses; the resume must present it, and the two resume triggers
    // (caught-up maybeResume and the low-water drain) must dedup to a single
    // effective resume for the episode.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      return Promise.resolve(undefined);
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.call).toHaveBeenCalledWith("terminal_pause", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      acceptedBytes: expect.any(Number),
    });
    // The recover names the episode it opened; the client catches up and both
    // resume triggers fire — exactly one resume leaves, with that token.
    await act(async () => {
      await flushMacrotask();
    });
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    const resumes = harness.call.mock.calls.filter((c) => c[0] === "terminal_resume");
    expect(resumes).toHaveLength(1);
    expect(resumes[0][1]).toMatchObject({
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    // The full sequence is restored contiguously.
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("each new episode's resume carries its own token; a prior episode's token is never reused", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    // The server side of the token contract: pause joins the open episode
    // (idempotent), resume closes only the episode matching the token.
    let currentToken = 0;
    let nextToken = 31;
    harness.call.mockImplementation((command: string, args: { episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (currentToken === 0) currentToken = nextToken++;
        return Promise.resolve({ episodeToken: currentToken });
      }
      if (command === "terminal_resume") {
        if ((args.episodeToken ?? -1) !== currentToken) return Promise.reject(new Error("stale episode token"));
        currentToken = 0;
        return Promise.resolve(undefined);
      }
      return Promise.resolve(undefined);
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Episode 1: saturate past the high watermark, then drain to low water.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenLastCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    // Episode 2: a later saturation is a new server episode with a new token.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call).toHaveBeenLastCalledWith("terminal_resume", {
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 32,
    });
    // Each episode got exactly one effective resume — the token-less and
    // stale-token resumes the server rejected don't count.
    const tokens = harness.call.mock.calls
      .filter((c) => c[0] === "terminal_resume")
      .map((c) => (c[1] as { episodeToken?: number }).episodeToken);
    expect(tokens.filter((token) => token === 31)).toHaveLength(1);
    expect(tokens.filter((token) => token === 32)).toHaveLength(1);
    expect(tokens[tokens.length - 1]).toBe(32);
  });

  it("a pause token learned mid-recovery never resumes early (deferred pause, then deferred recover)", async () => {
    // The r15 counterexample: the writer drains while the recover is still
    // pending, and the deferred pause response lands at that moment. The
    // learned token must NOT fire a resume until the recover completes and
    // the replay is accepted — then exactly one effective resume fires.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resolvePause: ((value: unknown) => void) | undefined;
    let resolveRecover: ((value: unknown) => void) | undefined;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") {
        return new Promise((resolve) => {
          resolvePause = resolve;
        });
      }
      if (command === "terminal_recover") {
        return new Promise((resolve) => {
          resolveRecover = resolve;
        });
      }
      return Promise.resolve(undefined);
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_recover")).toHaveLength(1);
    // The writer drains while the recover is still pending.
    act(() => t.drainWrites());
    // The deferred pause response lands now, mid-recovery: no resume.
    resolvePause?.({ episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_resume")).toHaveLength(0);
    // The recover completes: the replay arrives, is accepted, and exactly one
    // resume fires once the client catches up.
    resolveRecover?.({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    const resumes = harness.call.mock.calls.filter((c) => c[0] === "terminal_resume");
    expect(resumes).toHaveLength(1);
    expect(resumes[0][1]).toMatchObject({
      tabId: "kernel-1",
      channelId: "channel-1",
      clientId: "me",
      attachmentId: 11,
      episodeToken: 31,
    });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("a recover completing before the deferred pause still resumes exactly once (deferred recover, then deferred pause)", async () => {
    // The other response order: the recover response lands first (the replay
    // is accepted and the resume fires with the recover's token); the stale
    // deferred pause response lands afterwards and must not fire a second
    // resume for the same episode.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resolvePause: ((value: unknown) => void) | undefined;
    let resolveRecover: ((value: unknown) => void) | undefined;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") {
        return new Promise((resolve) => {
          resolvePause = resolve;
        });
      }
      if (command === "terminal_recover") {
        return new Promise((resolve) => {
          resolveRecover = resolve;
        });
      }
      return Promise.resolve(undefined);
    });
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    for (let i = 0; i < 4; i += 1) {
      act(() => harness.channel?.onBytes?.(chunk));
    }
    act(() => harness.channel?.onBytes?.(chunk));
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_recover")).toHaveLength(1);
    act(() => t.drainWrites());
    // The recover response lands first: the replay is accepted and the
    // resume fires with the recover's token.
    resolveRecover?.({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_resume")).toHaveLength(1);
    // The stale deferred pause response lands after the resume: it carries
    // the same generation, so it must not fire a second resume.
    resolvePause?.({ episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    const resumes = harness.call.mock.calls.filter((c) => c[0] === "terminal_resume");
    expect(resumes).toHaveLength(1);
    expect(resumes[0][1]).toMatchObject({ attachmentId: 11, episodeToken: 31 });
    const total = t.writeLog.reduce((n, entry) => n + entry.length, 0);
    expect(total).toBe(5 * (4 << 20));
  });

  it("a transient resume failure retries with backoff and the second resume actually unpauses the channel", async () => {
    // The r16 counterexample: the recovery completes, the first resume
    // fails transiently before the handler, and the bounded backoff retry
    // lands the second resume — the server actually unpauses.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episodeOpen = true;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (resumeCalls === 1) return Promise.reject(new Error("transient"));
        if (!episodeOpen) return Promise.resolve(undefined);
        episodeOpen = false;
        return Promise.resolve(undefined);
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // The recover completed and the first resume fired — it failed
    // transiently before the handler, so the channel is still paused.
    expect(resumeCalls).toBe(1);
    expect(episodeOpen).toBe(true);
    // The bounded backoff retry fires: the second resume actually unpauses.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(150);
    });
    expect(resumeCalls).toBe(2);
    expect(episodeOpen).toBe(false);
    vi.useRealTimers();
  });

  it("a resume that executed but lost its response retries with the current token idempotently", async () => {
    // The first resume executes server-side (the episode closes) but the
    // response is lost; the retry with the same token is an idempotent OK —
    // exactly one effective state change.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episodeOpen = true;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (resumeCalls === 1) {
          episodeOpen = false; // executed before the response was lost
          return Promise.reject(new Error("response lost"));
        }
        return Promise.resolve(undefined); // idempotent no-op
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(1);
    expect(episodeOpen).toBe(false); // the first resume executed
    // The retry with the same token is an idempotent OK — no double change.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(150);
    });
    expect(resumeCalls).toBe(2);
    expect(episodeOpen).toBe(false);
    vi.useRealTimers();
  });

  it("a stale episode's late resume catch never clears the new episode's dedup or retry state", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episode2 = false;
    let rejectResume: ((reason: unknown) => void) | undefined;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: episode2 ? 32 : 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (resumeCalls === 1) {
          // Deferred rejection: the catch lands after episode 2 started.
          return new Promise((_, reject) => {
            rejectResume = reject;
          });
        }
        return Promise.resolve(undefined);
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Episode 1's resume is in flight (deferred rejection).
    expect(resumeCalls).toBe(1);
    // Episode 2: a new saturation opens a new episode (token 32), and its
    // resume fires and succeeds.
    episode2 = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(2);
    // Episode 1's late rejection lands now: it must not clear episode 2's
    // dedup state or arm a retry for the moot episode.
    rejectResume?.(new Error("stale"));
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(2);
    vi.useRealTimers();
  });

  it("unmount with a pending resume retry stops the timer: zero new calls", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        return Promise.reject(new Error("transient"));
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(1);
    // Unmount with the backoff timer armed: no new resume calls after it.
    mounted!.unmount();
    mounted = undefined;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(resumeCalls).toBe(1);
    vi.useRealTimers();
  });

  it("retry exhaustion surfaces the visible error state and keeps a slow retry alive", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        return Promise.reject(new Error("transient"));
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Every resume fails: the bounded backoff exhausts, the visible error
    // state is surfaced once, and the slow retry keeps trying.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(resumeCalls).toBeGreaterThan(1);
    expect(harness.pushToast).toHaveBeenCalledWith("error", expect.stringContaining("恢复失败"));
    const callsAtToast = resumeCalls;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(resumeCalls).toBeGreaterThan(callsAtToast);
    vi.useRealTimers();
  });

  it("a stale episode's late success never clears the new episode's retry; the current-token resume actually unpauses", async () => {
    // The r17 counterexample: token 31's resume executed but its response
    // is deferred; episode 32's first resume fails transiently and arms the
    // retry; the late success of 31 must not clear 32's retry — the retry
    // fires 32 and actually unpauses the channel.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episode2 = false;
    let episode2Open = false;
    let resolveResume31: ((value: unknown) => void) | undefined;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: episode2 ? 32 : 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (resumeCalls === 1) {
          // Token 31's resume executes server-side; the response is held.
          return new Promise((resolve) => {
            resolveResume31 = resolve;
          });
        }
        if (resumeCalls === 2) return Promise.reject(new Error("transient"));
        episode2Open = false;
        return Promise.resolve(undefined);
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Token 31's resume is in flight (deferred response).
    expect(resumeCalls).toBe(1);
    // Episode 32: a new saturation opens a new episode; its first resume
    // fails transiently and arms the bounded retry.
    episode2 = true;
    episode2Open = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(2);
    // Token 31's late success lands now: it must not clear episode 32's
    // retry. The retry fires token 32 and actually unpauses the channel.
    resolveResume31?.(undefined);
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(150);
    });
    expect(resumeCalls).toBe(3);
    expect(episode2Open).toBe(false);
    vi.useRealTimers();
  });

  it("a deferred re-pause never re-sends the stale token; only the refreshed token fires", async () => {
    // The r17 counterexample: the server advanced to episode 32, so the
    // resume with token 31 is forbidden. The re-pause (token refresh) stays
    // pending: no resume with the known-stale token may fire meanwhile, and
    // only one refresh request is in flight. When the refresh returns 32,
    // exactly the current token fires and unpauses.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episode2 = false;
    let episode2Open = false;
    let resolveRefresh: ((value: unknown) => void) | undefined;
    let pauseCalls = 0;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") {
        pauseCalls += 1;
        if (episode2) {
          // The refresh after the forbidden resume: held pending.
          return new Promise((resolve) => {
            resolveRefresh = resolve;
          });
        }
        return Promise.resolve({ episodeToken: 31 });
      }
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (!episode2) {
          episode2 = true;
          return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
        }
        episode2Open = false;
        return Promise.resolve(undefined);
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // The forbidden resume armed the single-flight refresh, which is pending.
    expect(resumeCalls).toBe(1);
    const pausesAtForbidden = pauseCalls;
    // Advance the clock: no resume with the known-stale token, and no new
    // refresh request piled up behind the pending one.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(resumeCalls).toBe(1);
    expect(pauseCalls).toBe(pausesAtForbidden);
    // The refresh returns the current token: exactly it fires and unpauses.
    episode2Open = true;
    resolveRefresh?.({ episodeToken: 32 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toBe(2);
    expect(episode2Open).toBe(false);
    vi.useRealTimers();
  });

  it("forbidden then a quick refresh and a transient failure keep one chain; unmount cancels it with zero new calls", async () => {
    // The r17 multi-chain probe: forbidden → the refresh returns 32 at once →
    // the new-token resume fails transiently → exactly one retry chain
    // exists, and unmount cancels it with zero new calls.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let episode2 = false;
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: episode2 ? 32 : 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        if (!episode2) {
          episode2 = true;
          return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
        }
        return Promise.reject(new Error("transient"));
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Forbidden (31) then the refreshed token's transient failure (32):
    // exactly two resume calls, one retry chain.
    expect(resumeCalls).toBe(2);
    // Unmount cancels the chain: no third resume after unmount.
    mounted!.unmount();
    mounted = undefined;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(resumeCalls).toBe(2);
    vi.useRealTimers();
  });

  it("the exhausted slow phase keeps a single controlled in-flight request rate", async () => {
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resumeCalls = 0;
    harness.call.mockImplementation((command: string) => {
      if (command === "terminal_pause") return Promise.resolve({ episodeToken: 31 });
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls += 1;
        return Promise.reject(new Error("transient"));
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Exhaust the bounded backoff: the slow phase shows the visible state
    // and keeps exactly one in-flight request at a time — no bursts.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(harness.pushToast).toHaveBeenCalledWith("error", expect.stringContaining("恢复失败"));
    const callsAtExhaustion = resumeCalls;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(resumeCalls).toBe(callsAtExhaustion + 1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(resumeCalls).toBe(callsAtExhaustion + 2);
    vi.useRealTimers();
  });

  it("a delayed pause response from the old attachment never pollutes the new incarnation's smaller legitimate token", async () => {
    // The r18 counterexample: attachment 11's pause response (token 31) is
    // held pending; the writer drains and the view reopens to attachment 12,
    // whose legitimate generation restarts at 1. The old response must be
    // dropped — never learned — and attachment 12's own token 1 must be
    // learned and actually lift the server-side pause.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resolvePause11: ((value: unknown) => void) | undefined;
    let episode12Open = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    let pauseCalls12 = 0;
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (args.attachmentId === 11) {
          // The old attachment's pause is held pending.
          return new Promise((resolve) => {
            resolvePause11 = resolve;
          });
        }
        pauseCalls12 += 1;
        return Promise.resolve({ episodeToken: 1 });
      }
      if (command === "terminal_recover") return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 31 });
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 12 && args.episodeToken === 1) {
          episode12Open = false;
          return Promise.resolve(undefined);
        }
        if (args.attachmentId === 11 && args.episodeToken === 31) {
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
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
    act(() => t.drainWrites());
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "recovery-kernel-1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 11,
    });
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    // Attachment 11's resume fired with its own token and closed its episode;
    // its pause response is still pending.
    expect(resumeCalls).toEqual([{ attachmentId: 11, episodeToken: 31 }]);
    // Reopen to attachment 12.
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    // The old attachment's pause response lands now: it must be dropped, not
    // learned — attachment 12's episode space stays clean.
    resolvePause11?.({ episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([{ attachmentId: 11, episodeToken: 31 }]);
    expect(pauseCalls12).toBe(0);
    // Attachment 12's own flow: a new saturation pauses with its first
    // generation, and the resume with the legitimate smaller token 1
    // actually lifts the server-side pause.
    episode12Open = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(1);
    expect(resumeCalls).toEqual([
      { attachmentId: 11, episodeToken: 31 },
      { attachmentId: 12, episodeToken: 1 },
    ]);
    expect(episode12Open).toBe(false);
    vi.useRealTimers();
  });

  it("a delayed recover response from the old attachment never pollutes the new incarnation's recovery or episode state", async () => {
    // The r19 counterexample: attachment 11's recover response
    // ({flushedOffset: 20 MiB, episodeToken: 31}) is held pending; the view
    // reopens to attachment 12 and the old response lands afterwards. It
    // must be dropped — never clearing the new attachment's recovery state,
    // never writing the old flushed offset, never feeding the large token —
    // and attachment 12's own first-generation token 1 must actually lift
    // the server-side pause.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resolveRecover11: ((value: unknown) => void) | undefined;
    let episode12Open = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    let pauseCalls12 = 0;
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (args.attachmentId === 12) pauseCalls12 += 1;
        return Promise.resolve({ episodeToken: 1 });
      }
      if (command === "terminal_recover") {
        if (args.attachmentId === 11) {
          // The old attachment's recover is held pending.
          return new Promise((resolve) => {
            resolveRecover11 = resolve;
          });
        }
        return Promise.resolve({ flushedOffset: 5 * (4 << 20), episodeToken: 1 });
      }
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 12 && args.episodeToken === 1) {
          episode12Open = false;
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
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
    expect(harness.call.mock.calls.filter((c) => c[0] === "terminal_recover")).toHaveLength(1);
    act(() => t.drainWrites());
    // Reopen to attachment 12 while attachment 11's recover is in flight.
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    // The ordered anchor ends the old refusal episode; the attach response
    // alone must never make later bytes jump across the refused gap.
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 4 * (4 << 20),
      controlBytes: 0,
      attachmentId: 12,
    });
    // Attachment 11's old recover response lands now: dropped, not learned.
    resolveRecover11?.({ flushedOffset: 20 << 20, episodeToken: 31 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([]);
    expect(pauseCalls12).toBe(0);
    // Attachment 12's own flow: a new saturation pauses with its first
    // generation, and the resume with the legitimate smaller token 1
    // actually lifts the server-side pause.
    episode12Open = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(1);
    expect(resumeCalls).toEqual([{ attachmentId: 12, episodeToken: 1 }]);
    expect(episode12Open).toBe(false);
    vi.useRealTimers();
  });

  it("an old attachment's late refresh success never clears the new attachment's in-flight refresh; the current token actually unpauses", async () => {
    // The r20 counterexample: attachment 11's refresh is pending; reopen to
    // attachment 12, whose own refresh is also pending. The old refresh's
    // late success must not clear the new attachment's single-flight marker
    // — no duplicate refresh fires — and the current token's resume
    // actually lifts the server-side pause.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let resolveRefresh11: ((value: unknown) => void) | undefined;
    let resolveRefresh12: ((value: unknown) => void) | undefined;
    let pauseCalls11 = 0;
    let pauseCalls12 = 0;
    let episode12Open = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (args.attachmentId === 11) {
          pauseCalls11 += 1;
          if (pauseCalls11 === 1) return Promise.resolve({ episodeToken: 31 });
          return new Promise((resolve) => {
            resolveRefresh11 = resolve;
          });
        }
        pauseCalls12 += 1;
        if (pauseCalls12 === 1) return Promise.resolve({ episodeToken: 1 });
        return new Promise((resolve) => {
          resolveRefresh12 = resolve;
        });
      }
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 12 && args.episodeToken === 2) {
          episode12Open = false;
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Attachment 11: one chunk saturates the writer; the pause returns token
    // 31; the drain's resume is forbidden; its refresh is pending.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([{ attachmentId: 11, episodeToken: 31 }]);
    expect(pauseCalls11).toBe(2);
    // Reopen to attachment 12; its own resume is forbidden too, so its own
    // refresh is pending.
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    episode12Open = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([
      { attachmentId: 11, episodeToken: 31 },
      { attachmentId: 12, episodeToken: 1 },
    ]);
    expect(pauseCalls12).toBe(2);
    // Attachment 11's late refresh success lands now: it must not clear
    // attachment 12's in-flight marker.
    resolveRefresh11?.({ episodeToken: 32 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toHaveLength(2);
    expect(pauseCalls12).toBe(2);
    // A drain cycle adds one normal pause, never a duplicate refresh.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(3);
    expect(resumeCalls).toHaveLength(2);
    // Attachment 12's refresh returns the current token: its resume
    // actually lifts the server-side pause.
    resolveRefresh12?.({ episodeToken: 2 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([
      { attachmentId: 11, episodeToken: 31 },
      { attachmentId: 12, episodeToken: 1 },
      { attachmentId: 12, episodeToken: 2 },
    ]);
    expect(episode12Open).toBe(false);
    vi.useRealTimers();
  });

  it("an old attachment's late refresh failure never clears the new attachment's in-flight refresh or schedules a retry on it", async () => {
    // The same shape with the old refresh failing: the failure callback is
    // bound to the old attachment as well — it clears nothing and schedules
    // nothing on the new attachment.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let rejectRefresh11: ((reason: unknown) => void) | undefined;
    let resolveRefresh12: ((value: unknown) => void) | undefined;
    let pauseCalls11 = 0;
    let pauseCalls12 = 0;
    let episode12Open = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (args.attachmentId === 11) {
          pauseCalls11 += 1;
          if (pauseCalls11 === 1) return Promise.resolve({ episodeToken: 31 });
          return new Promise((_, reject) => {
            rejectRefresh11 = reject;
          });
        }
        pauseCalls12 += 1;
        if (pauseCalls12 === 1) return Promise.resolve({ episodeToken: 1 });
        return new Promise((resolve) => {
          resolveRefresh12 = resolve;
        });
      }
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 12 && args.episodeToken === 2) {
          episode12Open = false;
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([{ attachmentId: 11, episodeToken: 31 }]);
    expect(pauseCalls11).toBe(2);
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    episode12Open = true;
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([
      { attachmentId: 11, episodeToken: 31 },
      { attachmentId: 12, episodeToken: 1 },
    ]);
    expect(pauseCalls12).toBe(2);
    // Attachment 11's late refresh failure lands now: it clears nothing and
    // schedules nothing on attachment 12.
    rejectRefresh11?.(new Error("transient"));
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toHaveLength(2);
    expect(pauseCalls12).toBe(2);
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(3);
    expect(resumeCalls).toHaveLength(2);
    resolveRefresh12?.({ episodeToken: 2 });
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([
      { attachmentId: 11, episodeToken: 31 },
      { attachmentId: 12, episodeToken: 1 },
      { attachmentId: 12, episodeToken: 2 },
    ]);
    expect(episode12Open).toBe(false);
    vi.useRealTimers();
  });

  it("an old attachment's late pause failure never builds a second retry chain on the new attachment", async () => {
    // The r21 counterexample: attachment 11's normal pause is pending; the
    // view reopens to attachment 12, whose own pause fails transiently and
    // arms a retry. Attachment 11's late failure must not schedule anything
    // on attachment 12 — one retry chain only — and the current legitimate
    // token's resume actually lifts the server-side pause.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let rejectPause11: ((reason: unknown) => void) | undefined;
    let pauseCalls12 = 0;
    let pauseSucceeds = false;
    let episode12Open = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        if (args.attachmentId === 11) {
          // The old attachment's pause is held pending, then fails late.
          return new Promise((_, reject) => {
            rejectPause11 = reject;
          });
        }
        pauseCalls12 += 1;
        if (pauseSucceeds) return Promise.resolve({ episodeToken: 1 });
        return Promise.reject(new Error("transient"));
      }
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 12 && args.episodeToken === 1) {
          episode12Open = false;
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Attachment 11: one chunk saturates the writer; its pause is pending.
    act(() => harness.channel?.onBytes?.(chunk));
    act(() => t.drainWrites());
    // Reopen to attachment 12; its own pause fails transiently and arms a
    // retry.
    harness.attachTab.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      retainable: true,
      ephemeral: false,
      attachmentId: 12,
    });
    await act(async () => {
      harness.reopenCb?.();
      await flushMacrotask();
      await flushMacrotask();
    });
    act(() => harness.channel?.onBytes?.(chunk));
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(1);
    // Attachment 11's late pause failure lands now: it must not schedule a
    // retry on attachment 12.
    rejectPause11?.(new Error("transient"));
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls12).toBe(1);
    // Two 100ms cycles: exactly one retry chain fires, one pause per cycle.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(pauseCalls12).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(pauseCalls12).toBe(3);
    // The chain keeps a single in-flight request and the current legitimate
    // token's resume actually lifts the server-side pause.
    pauseSucceeds = true;
    episode12Open = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(pauseCalls12).toBe(4);
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([{ attachmentId: 12, episodeToken: 1 }]);
    expect(episode12Open).toBe(false);
    vi.useRealTimers();
  });

  it("a new same-attachment pause that succeeds ends the old request's retry chain", async () => {
    // The r22 counterexample: pause 1 fails transiently and arms a retry;
    // the writer drains and a new saturation's pause 2 succeeds with token 1
    // — the old chain must die (two cycles stay [2, 2], not [3, 4]), and the
    // current legitimate token's resume actually lifts the server-side pause.
    harness.attach.mockResolvedValue({
      tabId: "kernel-1",
      cols: 80,
      rows: 30,
      sizeOwner: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell: "unknown",
      retainable: false,
      ephemeral: true,
      attachmentId: 11,
    });
    let pauseCalls = 0;
    let episodeOpen = false;
    const resumeCalls: { attachmentId?: number; episodeToken?: number }[] = [];
    harness.call.mockImplementation((command: string, args: { attachmentId?: number; episodeToken?: number }) => {
      if (command === "terminal_pause") {
        pauseCalls += 1;
        if (pauseCalls === 1) return Promise.reject(new Error("transient"));
        return Promise.resolve({ episodeToken: 1 });
      }
      if (command === "terminal_resume") {
        resumeCalls.push({ attachmentId: args.attachmentId, episodeToken: args.episodeToken });
        if (args.attachmentId === 11 && args.episodeToken === 1) {
          episodeOpen = false;
          return Promise.resolve(undefined);
        }
        return Promise.reject(Object.assign(new Error("stale episode token"), { code: "forbidden" }));
      }
      return Promise.resolve(undefined);
    });
    vi.useFakeTimers();
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0 });
    const chunk = new Uint8Array(4 << 20).fill(0x61);
    // Pause 1 fails transiently and arms a retry.
    act(() => harness.channel?.onBytes?.(chunk));
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls).toBe(1);
    // The writer drains; a new saturation's pause 2 succeeds with token 1 —
    // the old chain ends.
    act(() => t.drainWrites());
    act(() => harness.channel?.onBytes?.(chunk));
    await act(async () => {
      await flushMacrotask();
    });
    expect(pauseCalls).toBe(2);
    // Two 100ms cycles: the old retry is dead, no new pause fires.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(pauseCalls).toBe(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(pauseCalls).toBe(2);
    // The current legitimate token's resume actually lifts the server pause.
    episodeOpen = true;
    act(() => t.drainWrites());
    await act(async () => {
      await flushMacrotask();
    });
    expect(resumeCalls).toEqual([{ attachmentId: 11, episodeToken: 1 }]);
    expect(episodeOpen).toBe(false);
    vi.useRealTimers();
  });
});

describe("XtermView render coalescing (T-09 component portion)", () => {
  it("coalesces scroll-driven linesBelow updates into one render per frame", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    const before = commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (const y of [10, 20, 30, 40, 50]) {
      t.buffer.active.viewportY = y;
      act(() => t.handlers.scroll?.());
    }
    expect(commits).toBe(before);
    act(() => runFrames());
    expect(commits).toBe(before + 1);
    expect(buttonText()).toContain("50 行");
    // The coalesced value lands in the root view store, and the burst re-renders
    // only the badge's precise selector — never the XtermView element above it.
    expect(getTerminalLinesBelow("tab-1")).toBe(50);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("writes nothing and re-renders nothing for frames where linesBelow is unchanged", async () => {
    await show();
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    t.buffer.active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    const before = commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(commits).toBe(before);
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("reports the top of the normal screen's scrollback, never of the alternate screen", async () => {
    await show();
    await attachOk();
    const t = term();
    const active = t.buffer.active as { baseY: number; viewportY: number; type?: string; length?: number };
    const history = () => useTerminalViewStore.getState().history["tab-1"];
    t.options.scrollback = 1000;
    active.type = "normal";
    active.baseY = 100;
    active.viewportY = 0;
    active.length = 124;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()).toMatchObject({ atTop: true, trimmed: false });
    active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()?.atTop).toBe(false);
    active.type = "alternate";
    active.baseY = 0;
    active.viewportY = 0;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(history()?.atTop).toBe(false);
  });

  it("reports a full scrollback as trimmed", async () => {
    await show();
    await attachOk();
    const t = term();
    t.options.scrollback = 1000;
    const active = t.buffer.active as { baseY: number; viewportY: number; type?: string; length?: number };
    active.type = "normal";
    active.baseY = 1000;
    active.viewportY = 1000;
    active.length = 1000 + t.rows;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(useTerminalViewStore.getState().history["tab-1"]?.trimmed).toBe(true);
  });

  it("keeps the view's durable history start from full-replay anchors only", async () => {
    await show();
    await attachOk();
    const from = () => useTerminalViewStore.getState().history["tab-1"]?.from ?? null;
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, historyFrom: 4096 });
    expect(from()).toBe(4096);
    // A resumed anchor keeps the view's content and its start.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 10, controlBytes: 0 });
    expect(from()).toBe(4096);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, expired: true, historyFrom: 9000 });
    expect(from()).toBe(9000);
    // An expired anchor without a start (no durable history) clears it.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true });
    expect(from()).toBeNull();
    // A full replay (clear prelude) without a start clears it as well.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 0, expired: true, historyFrom: 7000 });
    expect(from()).toBe(7000);
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 4 });
    expect(from()).toBeNull();
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
    expect(getTerminalLinesBelow("tab-1")).toBe(42);
  });

  it("unmount clears the view-store entry and late scroll/title signals never write stale values", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    t.buffer.active.baseY = 100;
    t.buffer.active.viewportY = 10;
    act(() => t.handlers.scroll?.());
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(90);
    mounted?.unmount();
    mounted = undefined;
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    // The fake terminal's disposables are no-ops, so its handlers still fire after
    // unmount: without the disposal guard these would schedule a flush and write
    // the stale value (and a stale title) back into the store for a dead tab.
    act(() => {
      t.handlers.scroll?.();
      t.handlers.title?.("late title");
    });
    act(() => runFrames());
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    expect(onTitle).not.toHaveBeenCalled();
  });

  it("a scroll burst on one tab re-renders only that tab's badge", async () => {
    stubFrames();
    let commitsA = 0;
    let commitsB = 0;
    mounted = mount(
      createElement(
        "div",
        null,
        createElement(
          Profiler,
          { id: "view-a", onRender: () => { commitsA += 1; } },
          createElement(CountingXtermView, { sessionId: "session-1", tabId: "tab-1" }),
        ),
        createElement(
          Profiler,
          { id: "view-b", onRender: () => { commitsB += 1; } },
          createElement(CountingXtermView, { sessionId: "session-2", tabId: "tab-2" }),
        ),
      ),
    );
    await act(async () => {
      await flushMacrotask();
      await flushMacrotask();
    });
    expect(harness.terminals).toHaveLength(2);
    const t = harness.terminals[1] as FakeTerminal;
    t.buffer.active.baseY = 100;
    const beforeA = commitsA;
    const beforeB = commitsB;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (const y of [10, 20, 30]) {
      t.buffer.active.viewportY = y;
      act(() => t.handlers.scroll?.());
    }
    act(() => runFrames());
    expect(commitsB).toBe(beforeB + 1);
    expect(commitsA).toBe(beforeA);
    expect(getTerminalLinesBelow("tab-2")).toBe(70);
    expect(getTerminalLinesBelow("tab-1")).toBe(0);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
  });

  it("concurrent attaching tabs: pre-response replay/scroll stays per-tab and survives the attach transition", async () => {
    // Both panes hang in the shared "pending" placeholder phase (the attach RPC has
    // not returned), exactly as TerminalPane renders them before kernelTabId arrives.
    const attachA = deferred<Record<string, unknown>>();
    const attachB = deferred<Record<string, unknown>>();
    harness.attach
      .mockImplementationOnce(() => attachA.promise)
      .mockImplementationOnce(() => attachB.promise);
    stubFrames();
    let commitsA = 0;
    let commitsB = 0;
    const onAttachA = vi.fn();
    const onAttachB = vi.fn();
    const renderViews = (tabIdA: string, tabIdB: string) =>
      createElement(
        "div",
        null,
        createElement(
          Profiler,
          { id: "view-a", onRender: () => { commitsA += 1; } },
          createElement(XtermView, { sessionId: "session-1", tabId: tabIdA, viewTabId: "tab-a", onAttach: onAttachA }),
        ),
        createElement(
          Profiler,
          { id: "view-b", onRender: () => { commitsB += 1; } },
          createElement(XtermView, { sessionId: "session-2", tabId: tabIdB, viewTabId: "tab-b", onAttach: onAttachB }),
        ),
      );
    mounted = mount(renderViews("pending", "pending"));
    // The attach timers fire but both RPCs stay in flight.
    await act(async () => {
      await flushMacrotask();
    });
    expect(harness.attach).toHaveBeenCalledTimes(2);
    expect(harness.channels).toHaveLength(2);

    // Pre-response replay on A: an anchor frame plus replayed output, then a scroll
    // leaving the viewport 90 lines above the bottom — all before any attach
    // response. B receives nothing.
    const tA = harness.terminals[0] as FakeTerminal;
    act(() => {
      harness.channels[0]?.onControl?.({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, controlBytes: 0, attachmentId: 11 });
    });
    act(() => {
      harness.channels[0]?.onBytes?.(enc.encode("replayed output\r\n"));
    });
    tA.buffer.active.baseY = 100;
    tA.buffer.active.viewportY = 10;
    const commitsABefore = commitsA;
    const commitsBBefore = commitsB;
    act(() => tA.handlers.scroll?.());
    act(() => runFrames());

    // Isolation: only A's per-pane key was written; B's key, badge, and render
    // count are untouched even though both panes share the "pending" placeholder.
    expect(getTerminalLinesBelow("tab-a")).toBe(90);
    expect(getTerminalLinesBelow("tab-b")).toBe(0);
    expect(commitsA).toBe(commitsABefore + 1);
    expect(commitsB).toBe(commitsBBefore);
    expect(buttonOccurrences("90 行")).toBe(1);

    // A's attach response lands and the pane switches tabId to the real kernel id:
    // the current value must carry over with no reset and no new signal.
    await act(async () => {
      attachA.resolve({ tabId: "kernel-1", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, shell: "unknown", retainable: true, ephemeral: false, attachmentId: 11 });
      await flushMacrotask();
    });
    expect(onAttachA).toHaveBeenCalledWith("kernel-1");
    await act(async () => {
      mounted!.root.render(renderViews("kernel-1", "pending"));
      await flushMacrotask();
    });
    expect(getTerminalLinesBelow("tab-a")).toBe(90);
    expect(buttonOccurrences("90 行")).toBe(1);

    // B attaches afterwards and still shows nothing: A's pre-response state never
    // leaked into B's key at any point of the lifecycle.
    await act(async () => {
      attachB.resolve({ tabId: "kernel-2", cols: 80, rows: 30, sizeOwner: "me", subscribers: 1, viewers: 1, exited: false, shell: "unknown", retainable: true, ephemeral: false, attachmentId: 12 });
      await flushMacrotask();
    });
    await act(async () => {
      mounted!.root.render(renderViews("kernel-1", "kernel-2"));
      await flushMacrotask();
    });
    expect(onAttachB).toHaveBeenCalledWith("kernel-2");
    expect(getTerminalLinesBelow("tab-b")).toBe(0);
    expect(buttonOccurrences("90 行")).toBe(1);
  });

  it("realistic burst: 600 scroll events, cycling OSC titles, and streaming output coalesce to one commit per frame", async () => {
    const onTitle = vi.fn();
    await show({ onTitle });
    await attachOk();
    const t = term();
    // A 100k-line scrollback with the viewport scrolled far up while output
    // keeps arriving — the exact shape the per-tab view store exists for.
    t.buffer.active.baseY = 100_000;
    const before = commits;
    const rendersBefore = xtermViewRenders.get("tab-1") ?? 0;
    for (let frame = 0; frame < 60; frame += 1) {
      for (let e = 0; e < 10; e += 1) {
        t.buffer.active.viewportY = 50_000 - frame * 100 - e;
        act(() => t.handlers.scroll?.());
      }
      // The cwd title holds steady for 10-frame runs: repeated identical OSC
      // titles must be deduped, not delivered once per frame.
      const title = `user@host: ~/projects/app${Math.floor(frame / 10)}`;
      act(() => {
        t.handlers.title?.(title);
        t.handlers.title?.(title);
      });
      feedBytes(`output line ${frame}\r\n`);
      act(() => t.drainWrites());
      act(() => runFrames());
    }
    // Every frame carried a real value change, so each produced exactly one
    // badge commit — never one per scroll event, title, or write completion.
    expect(commits).toBe(before + 60);
    expect(buttonText()).toContain("55909 行");
    expect(getTerminalLinesBelow("tab-1")).toBe(55_909);
    expect(xtermViewRenders.get("tab-1")).toBe(rendersBefore);
    // Only the 6 distinct cwd titles were delivered; the repeated ones were
    // deduped on top of the per-frame coalescing.
    expect(onTitle).toHaveBeenCalledTimes(6);
    expect(onTitle).toHaveBeenLastCalledWith("user@host: ~/projects/app5");
  });
});

describe("XtermView live clipboard on the last input device", () => {
  const owner = (active: boolean) =>
    act(() => {
      harness.channel?.onInputOwner?.({ type: "nexterm.input-owner", active });
    });

  it("writes live OSC 52 only while this view is the input owner, judged where the bytes arrived", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0 });
    // Bytes that arrived before the announcement still write, even though
    // xterm parses them after it.
    feedBytes("\x1b]52;c;YQ==\x07");
    owner(false);
    feedBytes("\x1b]52;c;Yg==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;YQ=="]]);

    owner(true);
    feedBytes("\x1b]52;c;Yw==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;YQ=="], ["c;Yw=="]]);
  });

  it("takes the state from every anchor: redelivered bytes of a passive view write nothing", async () => {
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, inputPassive: true });
    const first = "\x1b]52;c;YQ==\x07";
    feedBytes(first);
    // A recovery anchor redelivers the same bytes: still passive.
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 0, inputPassive: true });
    feedBytes(first + "\x1b]52;c;Yg==\x07");
    act(() => t.drainWrites());
    expect(onClipboard).not.toHaveBeenCalled();

    // An anchor without the flag (this view typed last, or an older server)
    // is active.
    const seq = enc.encode(first + "\x1b]52;c;Yg==\x07").length;
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: seq });
    feedBytes("\x1b]52;c;Yw==\x07");
    act(() => t.drainWrites());
    expect(onClipboard.mock.calls).toEqual([["c;Yw=="]]);
  });
});

describe("XtermView replayed output", () => {
  // Replayed: DA1 and DSR 6 queries, an OSC 52 clipboard write, BEL, an OSC 9
  // notification and an OSC 0 title.
  const REPLAYED = "old\x1b[c\x1b[6n\x1b]52;c;b2xk\x07\x07\x1b]9;old note\x07\x1b]0;Replayed title\x07";
  const LIVE = "new\x1b[c\x1b]52;c;bmV3\x07\x1b]9;new note\x07";

  function upstream(): string[] {
    return harness.write.mock.calls.map(([, bytes]) => new TextDecoder().decode(bytes as Uint8Array));
  }

  it("renders a session replay without replies, clipboard writes or notifications; live output still acts", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    const onTitle = vi.fn();
    await show({ onClipboard, onTitle });
    await attachOk();
    const t = term();
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 0,
      expired: true,
      classes: [{ from: 0, to: enc.encode(REPLAYED).length, kind: "replay" }],
    });
    // One chunk carries the end of the replay and the first live bytes.
    feedBytes(REPLAYED + LIVE);
    act(() => t.drainWrites());
    act(() => runFrames());
    expect(t.writeLog.join("")).toContain("old");
    expect(t.writeLog.join("")).toContain("new");
    // Only the live DA1 query is answered upstream, through the limiter.
    expect(upstream()).toEqual(["\x1b[?1;2c"]);
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;bmV3");
    // Titles are display state: the replayed title still reaches the tab,
    // where a custom title keeps precedence (applyRemoteTabTitle).
    expect(onTitle).toHaveBeenCalledWith("Replayed title");

    // The same bytes arriving live afterwards behave as before.
    feedBytes(REPLAYED);
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[?1;2c", "\x1b[?1;2c", "\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenLastCalledWith("c;b2xk");
  });

  it("renders a recovery anchor's refused suffix with its effects: it was never shown", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 0 });
    feedBytes("\x1b[6n\x1b]52;c;cmVm\x07");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenCalledWith("c;cmVm");
  });

  it("counts replayed bytes across frames", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    const suffix = "\x1b[6n\x1b]52;c;cmVw\x07";
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 0,
      classes: [{ from: 0, to: enc.encode(suffix).length, kind: "replay" }],
    });
    // The replay arrives split across frames: the count spans them.
    feedBytes(suffix.slice(0, 6));
    feedBytes(suffix.slice(6));
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    expect(onClipboard).not.toHaveBeenCalled();
    feedBytes("\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
  });

  it("closes the replay once its last byte is parsed, with no live output after it", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, expired: true, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    // Idle shell: nothing live follows. A focus report and a modified F3
    // (which looks like a cursor report) still reach the shell.
    act(() => t.handlers.data?.("\x1b[I"));
    act(() => t.handlers.data?.("\x1b[1;5R"));
    expect(upstream()).toEqual(["\x1b[I", "\x1b[1;5R"]);
  });

  it("passes reply-shaped user input while a replay is still being parsed", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the open marker only: xterm is inside the replayed bytes.
    act(() => t.drainWrites(1));
    const textarea = document.createElement("textarea");
    t.host?.appendChild(textarea);
    // xterm's own key listener on its textarea raises onData.
    textarea.addEventListener("keydown", () => t.handlers.data?.("\x1b[1;5R"));
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", ctrlKey: true, bubbles: true }));
    });
    // Once the event is over, the same bytes are a reply to replay.
    act(() => t.handlers.data?.("\x1b[1;1R"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;5R"]);
  });

  it("classifies bytes by stream sequence across class frames: replay, answered and live", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    // A resumed stream at sequence 100: the first 4 bytes were answered by
    // the server while nobody was attached.
    feedControl({
      type: "nexterm.stream-anchor",
      epoch: "e1",
      streamSeq: 100,
      classes: [{ from: 100, to: 104, kind: "answered" }],
    });
    feedBytes("\x1b[6n");
    // A recovered tail continues on the live fanout: its class frame comes
    // ahead of the bytes.
    const tail = "\x1b[c\x1b]52;c;b2xk\x07\x1b]9;x\x07";
    act(() => {
      harness.channel?.onClass?.({ type: "nexterm.stream-class", from: 104, to: 104 + enc.encode(tail).length, kind: "replay" });
    });
    feedBytes(tail + "\x1b[6n\x1b]52;c;bGl2ZQ==\x07");
    act(() => t.drainWrites());
    // Answered and replayed queries get no reply; the live one does.
    expect(upstream()).toEqual(["\x1b[1;1R"]);
    expect(onClipboard).toHaveBeenCalledTimes(1);
    expect(onClipboard).toHaveBeenCalledWith("c;bGl2ZQ==");
  });

  it("classifies held bytes by the recovery anchor's spans", async () => {
    harness.emitReplies = true;
    const onClipboard = vi.fn();
    await show({ onClipboard });
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0 });
    feedBytes("abcd");
    // A replayed tail is announced while the server holds its bytes.
    const tail = "\x1b[6n\x1b]52;c;b2xk\x07";
    const tailSpan = { from: 4, to: 4 + enc.encode(tail).length, kind: "replay" };
    act(() => {
      harness.channel?.onClass?.({ type: "nexterm.stream-class", ...tailSpan });
    });
    // A recovery anchor redelivers from sequence 2 and covers everything fed
    // up to now, including the held bytes.
    feedControl({ type: "nexterm.stream-anchor", epoch: "recovery-kernel-1", streamSeq: 2, classes: [tailSpan] });
    feedBytes("cd" + tail);
    act(() => t.drainWrites());
    expect(upstream()).toEqual([]);
    expect(onClipboard).not.toHaveBeenCalled();
  });

  it("does not carry spans into a new stream that is not marked expired", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 64, kind: "replay" }] });
    feedBytes("old");
    // A fresh attach (clear-screen control, empty ring) starts another
    // sequence space at 0; the old replay span must not cover it.
    feedControl({ type: "nexterm.stream-anchor", epoch: "e2", streamSeq: 0, controlBytes: 3 });
    feedBytes("\x1b[H\x1b[6n");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;1R"]);
  });

  it("passes a key reply raised by a handler that stops propagation, and nothing after it", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the open marker only: xterm is inside the replayed bytes.
    act(() => t.drainWrites(1));
    const textarea = document.createElement("textarea");
    t.host?.appendChild(textarea);
    // Like xterm's _keyDown: stop propagation and raise onData on the spot.
    textarea.addEventListener("keydown", (event) => {
      event.stopPropagation();
      t.handlers.data?.("\x1b[1;5R");
    });
    act(() => {
      textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "F3", ctrlKey: true, bubbles: true }));
    });
    // The mark ended with the event, before any timer: a reply-shaped report
    // parsed now is a reply to replay.
    act(() => t.handlers.data?.("\x1b[1;1R"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[1;5R"]);
  });

  it("keeps keystrokes typed while a replay is parsed", async () => {
    harness.emitReplies = true;
    await show();
    await attachOk();
    const t = term();
    feedControl({ type: "nexterm.stream-anchor", epoch: "e1", streamSeq: 0, expired: true, classes: [{ from: 0, to: 4, kind: "replay" }] });
    feedBytes("\x1b[6n");
    // Parse the start marker only: xterm is now inside the replayed bytes.
    act(() => t.drainWrites(1));
    act(() => t.handlers.data?.("ls\r"));
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["ls\r"]);
  });

  it("renders a docker exec backlog announced as replay without replies", async () => {
    harness.emitReplies = true;
    await show({ containerId: "container-1" });
    await attachOk();
    const t = term();
    const backlog = "\x1b[c\x1b]9;backlog\x07";
    act(() => {
      harness.channel?.onReplay?.({ type: "nexterm.stream-replay", bytes: enc.encode(backlog).length });
    });
    feedBytes(backlog + "\x1b[c");
    act(() => t.drainWrites());
    expect(upstream()).toEqual(["\x1b[?1;2c"]);
  });
});
