/** @vitest-environment jsdom */

import { act } from "react";
import { createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";
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
    write: (data: unknown, callback?: () => void) => void;
    writeln: (line?: unknown) => void;
    selection: string;
    clearSelection: () => void;
    getSelection: () => string;
    fireScroll: () => void;
  }>,
  observers: [] as Array<() => void>,
  frames: new Map<number, () => void>(),
  channels: [] as Array<{
    onBytes: (bytes: Uint8Array) => void;
    onControl?: (control: {
      type: string;
      epoch: string;
      streamSeq: number;
      controlBytes?: number;
      expired?: boolean;
    }) => void;
  }>,
  commandBlocks: [] as Array<{ exitCode: number | null; ended: boolean }>,
  blockClearCount: 0,
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
  assetGet: vi.fn(),
  web: true,
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
    private scrollCallback: (() => void) | null = null;
    fireScroll() {
      this.scrollCallback?.();
    }

    constructor() {
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
    selection = "";
    clearSelection() {
      this.selection = "";
    }
    getSelection() {
      return this.selection;
    }
    onData() {
      return { dispose: vi.fn() };
    }
    onScroll(callback: () => void) {
      this.scrollCallback = callback;
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
vi.mock("../../features/terminal/commandBlocks", () => ({
  CommandBlockManager: class {
    copyBlock() { return ""; }
    getBlockText() { return ""; }
    scrollTo() {}
    navigate() { return null; }
    clear() {
      harness.blockClearCount += 1;
      harness.commandBlocks.length = 0;
    }
    dispose() {}
    feedInput() {}
    feedOSC133(phase: "A" | "B" | "C" | "D", exitCode: number | null) {
      if (phase === "C") {
        harness.commandBlocks.push({ exitCode: null, ended: false });
      } else if (phase === "D") {
        const last = harness.commandBlocks[harness.commandBlocks.length - 1];
        if (last && !last.ended) {
          last.ended = true;
          last.exitCode = exitCode;
        }
      }
    }
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
  resizeTerminalToGridPreservingSelection: (
    term: { resize: (cols: number, rows: number) => void },
    grid: { cols: number; rows: number },
  ) => term.resize(grid.cols, grid.rows),
}));
vi.mock("../../features/terminal/gridRuntimeAdapter", () => ({
  productionGridRuntime: { resize: harness.resize, flush: harness.flush },
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: { get: harness.assetGet },
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
  createBinaryChannel: (
    onBytes: (bytes: Uint8Array) => void,
    onControl?: (control: {
      type: string;
      epoch: string;
      streamSeq: number;
      controlBytes?: number;
      expired?: boolean;
    }) => void,
  ) => {
    const channel = { onBytes, onControl };
    harness.channels.push(channel);
    return channel;
  },
  disposeChannel: vi.fn(),
  sendChannelInput: (
    _channel: unknown,
    _tabId: string,
    data: Uint8Array,
    http: (data: Uint8Array) => Promise<unknown>,
  ) => void http(data),
  onChannelReopen: (_channel: unknown, callback: () => void) => {
    harness.reopen = callback;
    return vi.fn();
  },
}));
vi.mock("../../ipc/env", () => ({ clientId: () => "me", get WEB() { return harness.web; } }));
vi.mock("../../ui/dialogs", () => ({ ask: vi.fn() }));

import { XtermView, type TerminalHandle, type XtermViewProps } from "../../features/terminal/XtermView";
import { ask } from "../../ui/dialogs";
import { registerAccountPreferenceStore, setTerminalDefaultMode } from "../../app/preferences";
import { useUi } from "../../app/store";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let frameId = 0;

async function flushWork(): Promise<void> {
  await act(async () => {
    const callbacks = [...harness.frames.values()];
    harness.frames.clear();
    for (const callback of callbacks) callback();
    await flushMacrotask();
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
  harness.web = true;
  registerAccountPreferenceStore(null);
  window.localStorage.clear();
  registerAccountPreferenceStore(null);
  useUi.setState({ sessions: [] });
  harness.assetGet.mockReset().mockResolvedValue({ options: {} });
  harness.terminals.length = 0;
  harness.observers.length = 0;
  harness.channels.length = 0;
  harness.frames.clear();
  harness.commandBlocks.length = 0;
  harness.blockClearCount = 0;
  harness.reopen = null;
  harness.geometry = { widthPx: 360, heightPx: 400 };
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-new",
    sessionId: "session",
    cols: 36,
    rows: 20,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "powershell",
    retainable: true,
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
  harness.execAttach.mockReset().mockResolvedValue("tab-exec");
  harness.openLineTab.mockReset().mockResolvedValue({
    tabId: "tab-line",
    sessionId: "session-1",
    cols: 80,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
    retainable: false,
    ephemeral: true,
    attachmentId: 7,
  });
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
  it.each([
    { assetMode: undefined, regular: true },
    { assetMode: "auto", regular: true },
    { assetMode: "durable", regular: false },
    { assetMode: "regular", regular: true },
  ])("applies the unregistered desktop default with asset mode $assetMode", async ({ assetMode, regular }) => {
    harness.web = false;
    await setTerminalDefaultMode("regular");
    useUi.setState({ sessions: [{ id: "session", assetId: "asset", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    harness.assetGet.mockResolvedValue({ options: { terminalMode: assetMode } });
    await show(props());

    expect(harness.assetGet).toHaveBeenCalledWith("asset");
    expect(harness.attach).toHaveBeenCalledWith("session", 36, 20, expect.anything(), ...(regular ? [false] : []));
  });

  it("lets the server resolve account defaults instead of forcing a device default", async () => {
    harness.web = false;
    await setTerminalDefaultMode("regular");
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: {} }), putOverrides: async () => {}, deleteOverrides: async () => {} });
    await show(props());

    expect(harness.assetGet).not.toHaveBeenCalled();
    expect(harness.attach).toHaveBeenCalledWith("session", 36, 20, expect.anything());
  });

  it.each([undefined, "durable"])("applies the desktop account regular default while honoring asset mode %s", async (assetMode) => {
    harness.web = false;
    registerAccountPreferenceStore({ getView: async () => ({ defaults: {}, overrides: { "terminal.defaultMode": "regular" } }), putOverrides: async () => {}, deleteOverrides: async () => {} });
    await flushMacrotask();
    useUi.setState({ sessions: [{ id: "session", assetId: "asset", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }] });
    harness.assetGet.mockResolvedValue({ options: { terminalMode: assetMode } });
    await show(props());

    expect(harness.assetGet).toHaveBeenCalledWith("asset");
    expect(harness.attach).toHaveBeenCalledWith("session", 36, 20, expect.anything(), ...(assetMode === "durable" ? [] : [false]));
  });

  it("keeps opening a terminal and reports when asset mode lookup fails", async () => {
    harness.web = false;
    await setTerminalDefaultMode("regular");
    useUi.setState({ sessions: [{ id: "session", assetId: "asset", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }], toasts: [] });
    harness.assetGet.mockRejectedValue(new Error("lookup failed"));
    await show(props());

    expect(harness.attach).toHaveBeenCalledWith("session", 36, 20, expect.anything());
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("无法读取资产终端模式"))).toBe(true);
  });

  it("opens a regular shell when the tab requested one because the durable helper was not ready", async () => {
    await show(props({ requestRegular: true }));

    expect(harness.attach).toHaveBeenCalledWith("session", 36, 20, expect.anything(), false);
  });

  it("still resumes an existing attachment of a tab that requested a regular shell", async () => {
    await show(props({ requestRegular: true, resumeTabId: "tab-existing" }));

    expect(harness.attachTab).toHaveBeenCalledWith("tab-existing", expect.anything(), -1, undefined, "session");
    expect(harness.attach).not.toHaveBeenCalled();
  });

  it("reports the durable fallback notice with the attach info", async () => {
    harness.attach.mockResolvedValue({
      tabId: "tab-new", sessionId: "session", cols: 36, rows: 20, controller: "me", subscribers: 1, viewers: 1,
      exited: false, shell: "posix", retainable: false, durable: false, fallbackNotice: "持久终端不可用",
    });
    const notices: Array<[string | undefined, boolean | undefined]> = [];
    await show(props({ onAttachInfo: (info) => notices.push([info.fallbackNotice, info.durableRequested]) }));

    expect(notices).toEqual([["持久终端不可用", true]]);
  });

  it("reports that a regular shell was requested", async () => {
    const requested: Array<boolean | undefined> = [];
    await show(props({ requestRegular: true, onAttachInfo: (info) => requested.push(info.durableRequested) }));

    expect(requested).toEqual([false]);
  });

  it("reports attached shell and background capability before the attach callback", async () => {
    const events: string[] = [];
    await show(
      props({
        onAttachInfo: (info) => events.push(`info:${String(info.shell)}:${String(info.retainable)}`),
        onAttach: (id) => events.push(`attach:${id}`),
      }),
    );

    expect(events).toEqual(["info:powershell:true", "attach:tab-new"]);
  });

  it.each([
    { name: "Docker exec", overrides: { containerId: "container" } },
    { name: "WinRM line", overrides: { winrm: true } },
  ])("marks $name as not retainable", async ({ overrides }) => {
    let retainable: boolean | undefined;
    await show(props({ ...overrides, onAttachInfo: (info) => { retainable = info.retainable; } }));
    expect(retainable).toBe(false);
  });

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
    expect(harness.attachTab).toHaveBeenCalledWith("tab-existing", expect.anything(), -1, undefined, "session");
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

    expect(harness.attachTab).toHaveBeenCalledWith("tab-new", expect.anything(), -1, undefined, "session");
    expect(harness.resize).toHaveBeenCalledWith("tab-new", { cols: 36, rows: 20 });
    expect(harness.attach).toHaveBeenCalledTimes(1);
  });

  it("keeps Docker resume scoped to its existing backend tab", async () => {
    await show(props({ containerId: "container", resumeTabId: "tab-existing" }));
    expect(harness.attachTab).toHaveBeenCalledWith("tab-existing", expect.anything(), -1, undefined, undefined);
    expect(harness.execAttach).not.toHaveBeenCalled();
    harness.reopen?.();
    await flushWork();
    expect(harness.attachTab).toHaveBeenLastCalledWith("tab-existing", expect.anything(), -1, undefined, undefined);
  });

  it("detaches a reopened channel that finishes after unmount", async () => {
    await show(props());
    let finish: ((value: unknown) => void) | undefined;
    harness.attachTab.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    harness.reopen?.();
    await act(async () => root?.unmount());
    root = null;
    harness.detach.mockClear();
    finish?.({ tabId: "tab-new", cols: 90, rows: 30, controller: "me", subscribers: 1, viewers: 1, exited: false });
    await flushWork();
    expect(harness.detach).toHaveBeenCalledWith("tab-new", "channel-1");
    expect(harness.attach).toHaveBeenCalledTimes(1);
  });

  it.each(["powershell", "posix"] as const)("forwards the %s classification on ordinary channel reopen", async (shell) => {
    const reported: string[] = [];
    await show(props({ onAttachInfo: (info) => reported.push(String(info.shell)) }));
    reported.length = 0;
    harness.attachTab.mockResolvedValue({
      tabId: "tab-new",
      cols: 90,
      rows: 30,
      controller: "me",
      subscribers: 1,
      viewers: 1,
      exited: false,
      shell,
    });

    harness.reopen?.();
    await flushWork();

    expect(reported).toEqual([shell]);
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

  function firePaste(host: HTMLElement, text: string): Event {
    const event = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", {
      value: { files: [], getData: () => text },
    });
    act(() => {
      host.dispatchEvent(event);
    });
    return event;
  }

  it("confirms multi-line clipboard pastes and writes only after approval", async () => {
    const { term } = await showWithHandle();
    const host = term.element;
    if (!host) throw new Error("terminal host not found");

    vi.mocked(ask).mockResolvedValue(false);
    const denied = firePaste(host, "echo one\necho two");
    expect(denied.defaultPrevented).toBe(true);
    await flushWork();
    expect(writtenText()).toEqual([]);

    vi.mocked(ask).mockResolvedValue(true);
    firePaste(host, "echo one\necho two");
    await flushWork();
    expect(vi.mocked(ask).mock.calls[0]?.[0]).toContain("2 行");
    expect(writtenText()).toEqual(["echo one\necho two"]);

    term.modes.bracketedPasteMode = true;
    firePaste(host, "echo three\necho four");
    await flushWork();
    expect(writtenText()).toEqual([
      "echo one\necho two",
      "\x1b[200~echo three\necho four\x1b[201~",
    ]);
  });

  it("leaves single-line clipboard pastes to the default handler untouched", async () => {
    const { term } = await showWithHandle();
    const host = term.element;
    if (!host) throw new Error("terminal host not found");

    const event = firePaste(host, "echo one");
    await flushWork();
    expect(event.defaultPrevented).toBe(false);
    expect(ask).not.toHaveBeenCalled();
  });

  it("offers return-to-bottom with the lines below and hides it at the bottom", async () => {
    const { term } = await showWithHandle();
    expect(container?.querySelector("button")).toBeNull();

    await act(async () => {
      term.buffer.active.baseY = 50;
      term.buffer.active.viewportY = 20;
      term.fireScroll();
    });
    await flushWork();
    const button = container?.querySelector("button");
    expect(button?.textContent).toContain("回到底部");
    expect(button?.textContent).toContain("30");

    await act(async () => {
      (button as HTMLButtonElement).click();
    });
    expect(term.scrollToBottom).toHaveBeenCalledOnce();

    await act(async () => {
      term.buffer.active.viewportY = term.buffer.active.baseY;
      term.fireScroll();
    });
    await flushWork();
    expect(container?.querySelector("button")).toBeNull();
  });

  it("grows the lines-below count when output arrives while scrolled up", async () => {
    const { term } = await showWithHandle();

    await act(async () => {
      term.buffer.active.baseY = 50;
      term.buffer.active.viewportY = 44;
      term.fireScroll();
    });
    await flushWork();
    expect(container?.querySelector("button")?.textContent).toContain("6");

    await act(async () => {
      term.buffer.active.baseY = 58;
      harness.channels[0]?.onBytes(new Uint8Array([104, 105]));
    });
    await flushWork();
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
      await flushMacrotask();
    });

    expect(harness.resize).not.toHaveBeenCalled();
    expect(harness.flush).not.toHaveBeenCalled();
  });
});

describe("XtermView stream anchors", () => {
  const anchor = (overrides: Record<string, unknown> = {}) => ({
    type: "nexterm.stream-anchor",
    epoch: "e1",
    streamSeq: 100,
    ...overrides,
  });
  const attachedInfo = {
    tabId: "tab-new",
    cols: 90,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
  };

  it("resumes from the consumed sequence when the channel reopens", async () => {
    await show(props());
    const channel = harness.channels[0];
    act(() => {
      channel.onControl?.(anchor());
      channel.onBytes(new Uint8Array(50));
    });
    harness.attachTab.mockClear().mockResolvedValue(attachedInfo);

    harness.reopen?.();
    await flushWork();

    expect(harness.attachTab).toHaveBeenCalledWith("tab-new", expect.anything(), -1, {
      seq: 150,
      epoch: "e1",
    }, "session");
  });

  it("does not duplicate in-flight bytes rendered before the resumed stream arrives", async () => {
    await show(props());
    const channel = harness.channels[0];
    const term = harness.terminals[0];
    const written: number[] = [];
    term.write = (data: unknown, callback?: () => void) => {
      if (data instanceof Uint8Array) written.push(...data);
      callback?.();
    };
    act(() => {
      channel.onControl?.(anchor());
      channel.onBytes(new Uint8Array([1, 2, 3]));
    });
    harness.attachTab.mockClear().mockResolvedValue(attachedInfo);
    harness.reopen?.();
    await flushWork();

    act(() => {
      channel.onBytes(new Uint8Array([4, 5]));
      channel.onControl?.(anchor({ streamSeq: 103 }));
      channel.onBytes(new Uint8Array([4, 5, 6, 7]));
    });

    expect(written).toEqual([1, 2, 3, 4, 5, 6, 7]);
  });

  it("announces an expired anchor only after the clear-screen control bytes", async () => {
    await show(props());
    const channel = harness.channels[0];
    const term = harness.terminals[0];
    const writes: string[] = [];
    term.write = (data, callback) => {
      writes.push(typeof data === "string" ? data : `<${(data as Uint8Array).length} bytes>`);
      callback?.();
    };

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 0, controlBytes: 11, expired: true }));
    });
    expect(writes).toEqual([]);

    act(() => {
      channel.onBytes(new Uint8Array(11));
      channel.onBytes(new Uint8Array([65]));
    });
    expect(writes).toEqual([
      "<11 bytes>",
      "<51 bytes>",
      "<1 bytes>",
    ]);
  });

  it("reopens without a resume anchor when the stream never carried one", async () => {
    await show(props());
    harness.attachTab.mockClear().mockResolvedValue(attachedInfo);

    harness.reopen?.();
    await flushWork();

    expect(harness.attachTab).toHaveBeenCalledWith("tab-new", expect.anything(), -1, undefined, "session");
  });

  it("single-flights connected and channel reattach with the original tab and resume anchor", async () => {
    const onAttach = vi.fn();
    const onAttachInfo = vi.fn();
    await show(props({ sessionConnected: false, onAttach, onAttachInfo }));
    const channel = harness.channels[0];
    act(() => {
      channel.onControl?.(anchor());
      channel.onBytes(new Uint8Array(50));
    });
    let finish: ((value: typeof attachedInfo) => void) | undefined;
    harness.attachTab.mockClear().mockImplementation(
      () => new Promise<typeof attachedInfo>((resolve) => { finish = resolve; }),
    );

    await show(props({ sessionConnected: true, onAttach, onAttachInfo }));
    harness.reopen?.();
    await flushWork();

    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    expect(harness.attachTab).toHaveBeenCalledWith("tab-new", expect.anything(), -1, {
      seq: 150,
      epoch: "e1",
    }, "session");
    finish?.(attachedInfo);
    await flushWork();

    expect(harness.attach).toHaveBeenCalledTimes(1);
    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    expect(onAttach).toHaveBeenNthCalledWith(2, "tab-new");
    expect(onAttachInfo).toHaveBeenLastCalledWith(expect.objectContaining({ tabId: "tab-new", exited: false }));
  });

  it("does not invent a resume target on connected before any tab has attached", async () => {
    await show(props({ visible: false, sessionConnected: false }));
    await show(props({ visible: false, sessionConnected: true }));

    expect(harness.attach).not.toHaveBeenCalled();
    expect(harness.attachTab).not.toHaveBeenCalled();
  });

  it("drops incomplete OSC state before expired clear and replay", async () => {
    const notifications = vi.fn();
    await show(props({ onNotification: notifications }));
    const channel = harness.channels[0];
    const term = harness.terminals[0];
    const written: number[] = [];
    term.write = (data, callback) => {
      if (typeof data === "string") written.push(...new TextEncoder().encode(data));
      else written.push(...(data as Uint8Array));
      callback?.();
    };
    act(() => {
      channel.onBytes(new TextEncoder().encode("old\x1b]9;stale"));
    });
    written.length = 0;
    const replay = "\x1b[2J\x1b[3J\x1b[HREPLAY";

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 0, controlBytes: 11, expired: true }));
    });
    act(() => {
      channel.onBytes(new TextEncoder().encode(replay));
    });
    act(() => {
      channel.onBytes(new Uint8Array([7]));
    });

    expect(harness.blockClearCount).toBe(1);
    expect(written).toEqual([
      ...Array.from(new TextEncoder().encode(replay)),
      ...Array.from(new TextEncoder().encode("\x1b[33m[输出历史已过期，已刷新屏幕]\x1b[0m\n")),
      7,
    ]);
    expect(notifications).not.toHaveBeenCalled();
  });

  it("rebuilds command blocks only once on expired OSC133 replay", async () => {
    await show(props());
    const channel = harness.channels[0];
    const sequence = "\x1b]133;A\x07\x1b]133;C\x07\x1b]133;D;0\x07";
    act(() => {
      channel.onBytes(new TextEncoder().encode(sequence));
    });
    expect(harness.commandBlocks).toHaveLength(1);

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 0, controlBytes: 11, expired: true }));
      channel.onBytes(new TextEncoder().encode("\x1b[2J\x1b[3J\x1b[H" + sequence));
    });

    expect(harness.blockClearCount).toBe(1);
    expect(harness.commandBlocks).toHaveLength(1);
    expect(harness.commandBlocks[0]).toMatchObject({ ended: true, exitCode: 0 });
  });

  it("preserves pending OSC state across continuous resume", async () => {
    const notifications = vi.fn();
    await show(props({ onNotification: notifications }));
    const channel = harness.channels[0];
    act(() => {
      channel.onBytes(new TextEncoder().encode("old\x1b]9;contin"));
    });

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 100, controlBytes: 0, expired: false }));
      channel.onBytes(new TextEncoder().encode("ued\x07"));
    });

    expect(notifications).toHaveBeenCalledWith("continued");
    expect(harness.blockClearCount).toBe(0);
  });

  it("clears selection on expired full replay", async () => {
    await show(props());
    const channel = harness.channels[0];
    const term = harness.terminals[0];
    term.selection = "OLD";

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 0, controlBytes: 11, expired: true }));
      channel.onBytes(new TextEncoder().encode("\x1b[2J\x1b[3J\x1b[HREPLAY"));
    });

    expect(term.getSelection()).toBe("");
  });

  it("preserves selection on continuous resume", async () => {
    await show(props());
    const channel = harness.channels[0];
    const term = harness.terminals[0];
    term.selection = "OLD";

    act(() => {
      channel.onControl?.(anchor({ streamSeq: 100, controlBytes: 0, expired: false }));
      channel.onBytes(new TextEncoder().encode("NEXT"));
    });

    expect(term.getSelection()).toBe("OLD");
  });

  it("does not resurrect a deleted terminal on a normal channel close", async () => {
    const onAttachFailed = vi.fn();
    await show(props({ onAttachFailed }));
    harness.attachTab.mockClear().mockRejectedValue({ code: "not_found" });

    harness.reopen?.();
    await flushWork();

    expect(harness.attachTab).toHaveBeenCalledTimes(1);
    expect(onAttachFailed).toHaveBeenCalledWith("not_found");
  });
});
