/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{ element: HTMLElement | null }>,
  frames: new Map<number, () => void>(),
  keyHandler: null as ((event: KeyboardEvent) => boolean) | null,
  attach: vi.fn(),
  attachTab: vi.fn(),
  write: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
  resize: vi.fn(),
  flush: vi.fn(),
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options = { scrollback: 1000 };
    element: HTMLElement | null = null;
    modes = { bracketedPasteMode: false };
    buffer = { active: { type: "normal", baseY: 0, viewportY: 0 } };
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
    attachCustomKeyEventHandler(handler: (event: KeyboardEvent) => boolean) {
      harness.keyHandler = handler;
    }
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
    scrollToBottom() {}
    resize() {}
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
vi.mock("../../features/terminal/commandBlocks", () => ({
  CommandBlockManager: class {
    copyBlock() {
      return "";
    }
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
vi.mock("../../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: () => ({
    viewport: { widthPx: 360, heightPx: 400 },
    metrics: { widthPx: 10, heightPx: 20 },
    grid: { cols: 36, rows: 20 },
  }),
  resizeTerminalToGridPreservingSelection: () => {},
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
  dockerApi: { execAttach: vi.fn() },
  sessionApi: { openLineTab: vi.fn() },
}));
vi.mock("../../ipc/events", () => ({
  channelIdOf: () => "channel-1",
  createBinaryChannel: () => ({}),
  disposeChannel: vi.fn(),
  onChannelReopen: () => vi.fn(),
}));
vi.mock("../../ipc/env", () => ({ clientId: () => "me" }));

import { XtermView, type XtermViewProps } from "../../features/terminal/XtermView";
import { resetAllKeybindings, setKeybinding } from "../../app/keybindings";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let frameId = 0;

async function show(props: XtermViewProps): Promise<void> {
  if (!root) {
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
  }
  await act(async () => {
    root?.render(createElement(XtermView, props));
  });
  await act(async () => {
    const callbacks = [...harness.frames.values()];
    harness.frames.clear();
    for (const callback of callbacks) callback();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function handledByXterm(init: KeyboardEventInit & { isComposing?: boolean }): boolean {
  if (!harness.keyHandler) throw new Error("xterm custom key handler not registered");
  return harness.keyHandler(new KeyboardEvent("keydown", init));
}

beforeEach(() => {
  harness.terminals.length = 0;
  harness.frames.clear();
  harness.keyHandler = null;
  harness.attach.mockReset().mockResolvedValue("tab-new");
  harness.attachTab.mockReset().mockResolvedValue({
    tabId: "tab-existing",
    cols: 90,
    rows: 30,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
  });
  harness.write.mockReset().mockResolvedValue(undefined);
  harness.detach.mockReset().mockResolvedValue(undefined);
  harness.setVisible.mockReset().mockResolvedValue(undefined);
  frameId = 0;
  window.localStorage.clear();
  resetAllKeybindings();

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
  resetAllKeybindings();
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
});

describe("XtermView 应用级快捷键放行", () => {
  it("注册 xterm custom key handler 并放行 Mod+K 给 window 级 App 处理", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(harness.keyHandler).not.toBeNull();
    expect(handledByXterm({ key: "k", code: "KeyK", ctrlKey: true })).toBe(false);
    expect(handledByXterm({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true })).toBe(false);
  });

  it("普通输入与非应用键位仍由 xterm 处理", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(handledByXterm({ key: "x", code: "KeyX" })).toBe(true);
    expect(handledByXterm({ key: "Enter", code: "Enter" })).toBe(true);
    // terminalSearch(Primary+f)不在应用级键位里, 由 TerminalPane 捕获层单独处理。
    expect(handledByXterm({ key: "f", code: "KeyF", ctrlKey: true })).toBe(true);
    expect(handledByXterm({ key: "f", code: "KeyF", metaKey: true })).toBe(true);
  });

  it("repeat 与 IME 组合期间不放行", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(handledByXterm({ key: "k", code: "KeyK", ctrlKey: true, repeat: true })).toBe(true);
    expect(handledByXterm({ key: "k", code: "KeyK", ctrlKey: true, isComposing: true })).toBe(
      true,
    );
  });

  it("键位覆盖同步后按新绑定放行", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    setKeybinding("globalSearch", "Mod+Shift+g");
    expect(handledByXterm({ key: "G", code: "KeyG", ctrlKey: true, shiftKey: true })).toBe(false);
    expect(handledByXterm({ key: "k", code: "KeyK", ctrlKey: true })).toBe(true);

    resetAllKeybindings();
    expect(handledByXterm({ key: "k", code: "KeyK", ctrlKey: true })).toBe(false);
  });
});
