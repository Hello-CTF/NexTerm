/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const harness = vi.hoisted(() => ({
  terminals: [] as Array<{ element: HTMLElement | null }>,
  frames: new Map<number, () => void>(),
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

function host(): HTMLElement {
  const host = container?.querySelector(".xterm-viewport")?.parentElement;
  if (!host) throw new Error("terminal host not found");
  return host as HTMLElement;
}

function png(name: string): File {
  return new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], name, { type: "image/png" });
}

function pasteEvent(files: File[], text = ""): Event {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  const items = files.map((file) => ({
    kind: "file",
    type: file.type,
    getAsFile: () => file,
  }));
  Object.defineProperty(event, "clipboardData", {
    value: {
      items,
      files,
      getData: (type: string) => (type === "text/plain" ? text : ""),
    },
  });
  return event;
}

function dropEvent(files: File[], text = ""): Event {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  const items: { kind: string; type: string; getAsFile: () => File | null }[] = files.map(
    (file) => ({
      kind: "file",
      type: file.type,
      getAsFile: () => file,
    }),
  );
  if (text) items.push({ kind: "string", type: "text/plain", getAsFile: () => null });
  Object.defineProperty(event, "dataTransfer", {
    value: {
      items,
      files,
      types: files.length > 0 ? ["Files"] : [],
      getData: (type: string) => (type === "text/plain" ? text : ""),
    },
  });
  return event;
}

function dragOverEvent(types: string[]): Event {
  const event = new Event("dragover", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", { value: { types } });
  return event;
}

beforeEach(() => {
  harness.terminals.length = 0;
  harness.frames.clear();
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

describe("XtermView image paste interception", () => {
  it("invokes onPasteImages and prevents default paste for an image-only clipboard", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = pasteEvent([png("shot.png")]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).toHaveBeenCalledTimes(1);
    expect(onPasteImages.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["shot.png"]);
    expect(event.defaultPrevented).toBe(true);
    expect(harness.write).not.toHaveBeenCalled();
  });

  it("does not intercept a mixed clipboard containing plain text and preserves normal text paste", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = pasteEvent([png("shot.png")], "echo already-text");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not intercept a mixed image and non-image file clipboard and falls back to default handling as a whole", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = pasteEvent([
      png("shot.png"),
      new File(["x"], "notes.txt", { type: "text/plain" }),
    ]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not intercept non-image file drops", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = dropEvent([new File(["x"], "notes.txt", { type: "text/plain" })]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("does not intercept mixed image and non-image file drops and preserves non-image content", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = dropEvent([
      png("shot.png"),
      new File(["x"], "notes.txt", { type: "text/plain" }),
    ]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("passes mixed image and text drops through like paste without upload or preventDefault", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = dropEvent([png("shot.png")], "https://example.com/page");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("invokes onPasteImages and prevents default behavior for image drops", async () => {
    const onPasteImages = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteImages });

    const event = dropEvent([png("dropped.png")]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteImages).toHaveBeenCalledTimes(1);
    expect(onPasteImages.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["dropped.png"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("prevents default dragover behavior only for files to allow dropping", async () => {
    await show({ sessionId: "session", tabId: "pending", onPasteImages: vi.fn() });

    const withFiles = dragOverEvent(["Files"]);
    act(() => {
      host().dispatchEvent(withFiles);
    });
    expect(withFiles.defaultPrevented).toBe(true);

    const withText = dragOverEvent(["text/plain"]);
    act(() => {
      host().dispatchEvent(withText);
    });
    expect(withText.defaultPrevented).toBe(false);
  });
});
