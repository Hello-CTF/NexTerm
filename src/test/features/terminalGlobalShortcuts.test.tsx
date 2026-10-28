/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{ element: HTMLElement | null }>,
  frames: new Map<number, () => void>(),
  keyHandler: null as ((event: KeyboardEvent) => boolean) | null,
  input: vi.fn(),
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
    buffer = { active: { type: "normal", baseY: 0, viewportY: 0 }, onBufferChange: () => ({ dispose: () => {} }) };
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
    attachCustomKeyEventHandler(handler: (event: KeyboardEvent) => boolean) {
      harness.keyHandler = handler;
    }
    input(data: string, wasUserInput: boolean) {
      harness.input(data, wasUserInput);
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
vi.mock("../../ipc/env", () => ({ clientId: () => "me", WEB: false }));

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
    await flushMacrotask();
  });
}

function keydown(init: KeyboardEventInit & { isComposing?: boolean }): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

function handledByXterm(init: KeyboardEventInit & { isComposing?: boolean }): boolean {
  if (!harness.keyHandler) throw new Error("xterm custom key handler not registered");
  return harness.keyHandler(keydown(init));
}

// Dispatches through the handler from a real DOM node so propagation to window listeners is observable.
function dispatchThroughHandler(init: KeyboardEventInit): { handled: boolean; reachedWindow: boolean; event: KeyboardEvent } {
  if (!harness.keyHandler) throw new Error("xterm custom key handler not registered");
  const handler = harness.keyHandler;
  const target = document.createElement("textarea");
  document.body.append(target);
  let handled = true;
  let reachedWindow = false;
  const onTarget = (event: Event) => {
    handled = handler(event as KeyboardEvent);
  };
  const onWindow = () => {
    reachedWindow = true;
  };
  target.addEventListener("keydown", onTarget);
  window.addEventListener("keydown", onWindow);
  const event = keydown(init);
  target.dispatchEvent(event);
  window.removeEventListener("keydown", onWindow);
  target.remove();
  return { handled, reachedWindow, event };
}

beforeEach(() => {
  harness.terminals.length = 0;
  harness.frames.clear();
  harness.keyHandler = null;
  harness.input.mockReset();
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-new",
    sessionId: "session",
    cols: 80,
    rows: 24,
    sizeOwner: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
  });
  harness.attachTab.mockReset().mockResolvedValue({
    tabId: "tab-existing",
    cols: 90,
    rows: 30,
    sizeOwner: "me",
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

describe("XtermView app-level shortcut passthrough", () => {
  it("registers the xterm custom key handler and passes Mod+Shift+P / Mod+Shift+K to the window-level App handler", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(harness.keyHandler).not.toBeNull();
    expect(handledByXterm({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true })).toBe(false);
    expect(handledByXterm({ key: "K", code: "KeyK", ctrlKey: true, shiftKey: true })).toBe(false);
  });

  it("passes Mod+Shift+W to the app while it closes panes, and to xterm once unbound", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    const bound = dispatchThroughHandler({ key: "W", code: "KeyW", ctrlKey: true, shiftKey: true });
    expect(bound.handled).toBe(false);
    expect(bound.reachedWindow).toBe(true);
    expect(harness.input).not.toHaveBeenCalled();
    setKeybinding("closePane", null);
    expect(handledByXterm({ key: "W", code: "KeyW", ctrlKey: true, shiftKey: true })).toBe(true);
  });

  it("sends override bytes in place of xterm's encoding, unless an app shortcut owns the chord", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(handledByXterm({ key: "Enter", code: "Enter", shiftKey: true })).toBe(false);
    expect(handledByXterm({ key: "Backspace", code: "Backspace", ctrlKey: true })).toBe(false);
    expect(harness.input.mock.calls).toEqual([
      ["\x1b\r", true],
      ["\x17", true],
    ]);

    harness.input.mockReset();
    setKeybinding("newTerminal", "Shift+Enter");
    expect(handledByXterm({ key: "Enter", code: "Enter", shiftKey: true })).toBe(false);
    expect(harness.input).not.toHaveBeenCalled();
  });

  it("leaves ordinary input and non-app shortcuts to xterm", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    expect(handledByXterm({ key: "x", code: "KeyX" })).toBe(true);
    expect(handledByXterm({ key: "Enter", code: "Enter" })).toBe(true);
    // Ctrl+F is readline's forward-char; terminal search (Ctrl+Shift+F here) is withheld instead.
    expect(handledByXterm({ key: "f", code: "KeyF", ctrlKey: true })).toBe(true);
    expect(handledByXterm({ key: "f", code: "KeyF", metaKey: true })).toBe(true);
    expect(handledByXterm({ key: "F", code: "KeyF", ctrlKey: true, shiftKey: true })).toBe(false);
  });

  it("drops auto-repeats of an app shortcut instead of typing them into the terminal or re-running the action", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    const repeat = dispatchThroughHandler({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true, repeat: true });
    expect(repeat.handled).toBe(false);
    expect(repeat.reachedWindow).toBe(false);
    expect(repeat.event.defaultPrevented).toBe(true);
    const first = dispatchThroughHandler({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true });
    expect(first.reachedWindow).toBe(true);
    expect(first.event.defaultPrevented).toBe(false);
  });

  it("lets held navigation shortcuts repeat through to the app", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    const nextTab = dispatchThroughHandler({ key: "Tab", code: "Tab", ctrlKey: true, repeat: true });
    expect(nextTab.handled).toBe(false);
    expect(nextTab.reachedWindow).toBe(true);
    const focusPane = dispatchThroughHandler({ key: "ArrowLeft", code: "ArrowLeft", ctrlKey: true, shiftKey: true, altKey: true, repeat: true });
    expect(focusPane.reachedWindow).toBe(true);
    const split = dispatchThroughHandler({ key: "\\", code: "Backslash", ctrlKey: true, repeat: true });
    expect(split.reachedWindow).toBe(false);
    expect(harness.input).not.toHaveBeenCalled();
  });

  it("leaves IME composition to the input method", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    // keyCode 229 stays with xterm's composition helper; a real key mid-composition only commits the composition.
    expect(handledByXterm({ key: "Process", keyCode: 229, ctrlKey: true, shiftKey: true })).toBe(true);
    expect(handledByXterm({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true, isComposing: true })).toBe(false);
    expect(handledByXterm({ key: "Enter", code: "Enter", isComposing: true })).toBe(false);
    expect(harness.input).not.toHaveBeenCalled();
  });

  it("passes through updated bindings after keybinding overrides sync", async () => {
    await show({ sessionId: "session", tabId: "pending" });

    setKeybinding("commandPalette", "Mod+Shift+g");
    expect(handledByXterm({ key: "G", code: "KeyG", ctrlKey: true, shiftKey: true })).toBe(false);
    expect(handledByXterm({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true })).toBe(true);

    resetAllKeybindings();
    expect(handledByXterm({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true })).toBe(false);
    expect(handledByXterm({ key: "G", code: "KeyG", ctrlKey: true, shiftKey: true })).toBe(true);
  });
});
