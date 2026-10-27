/** @vitest-environment jsdom */

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";

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
import { setTerminalConfirmMultilinePaste } from "../../app/preferences";

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

function dropEvent(files: File[], text = "", folders: string[] = []): Event {
  const event = new Event("drop", { bubbles: true, cancelable: true });
  const items: {
    kind: string;
    type: string;
    getAsFile: () => File | null;
    webkitGetAsEntry?: () => { isDirectory: boolean; name: string };
  }[] = files.map((file) => ({
    kind: "file",
    type: file.type,
    getAsFile: () => file,
    webkitGetAsEntry: () => ({ isDirectory: false, name: file.name }),
  }));
  for (const name of folders) {
    items.push({
      kind: "file",
      type: "",
      getAsFile: () => new File([], name),
      webkitGetAsEntry: () => ({ isDirectory: true, name }),
    });
  }
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
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-new",
    sessionId: "session",
    cols: 80,
    rows: 24,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
  });
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
  it("invokes onPasteFiles and prevents default paste for an image-only clipboard", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = pasteEvent([png("shot.png")]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).toHaveBeenCalledTimes(1);
    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["shot.png"]);
    expect(event.defaultPrevented).toBe(true);
    expect(harness.write).not.toHaveBeenCalled();
  });

  it("does not intercept a mixed clipboard containing plain text and preserves normal text paste", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = pasteEvent([png("shot.png")], "echo already-text");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("leaves a text-only paste to the default handling", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = pasteEvent([], "echo hi");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("uploads a mixed image and non-image clipboard as a whole", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = pasteEvent([png("shot.png"), new File(["x"], "notes.txt", { type: "text/plain" })]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["shot.png", "notes.txt"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("prefers a non-image file over the text that accompanies it on the clipboard", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = pasteEvent([new File(["%PDF"], "doc.pdf", { type: "application/pdf" })], "/home/me/doc.pdf");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["doc.pdf"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("intercepts non-image file drops", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = dropEvent([new File(["x"], "notes.txt", { type: "text/plain" })]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["notes.txt"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("passes mixed image and text drops through like paste without upload or preventDefault", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = dropEvent([png("shot.png")], "https://example.com/page");
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  it("invokes onPasteFiles and prevents default behavior for image drops", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = dropEvent([png("dropped.png")]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).toHaveBeenCalledTimes(1);
    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["dropped.png"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("prevents default dragover behavior only for files to allow dropping", async () => {
    await show({ sessionId: "session", tabId: "pending", onPasteFiles: vi.fn() });

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

  it("reports a dropped folder as skipped instead of uploading it", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = dropEvent([new File(["x"], "notes.txt", { type: "text/plain" })], "", ["src"]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).toHaveBeenCalledTimes(1);
    expect(onPasteFiles.mock.calls[0]?.[0].map((f: File) => f.name)).toEqual(["notes.txt"]);
    expect(onPasteFiles.mock.calls[0]?.[1]).toEqual(["文件夹「src」"]);
    expect(event.defaultPrevented).toBe(true);
  });

  it("reports a folder-only drop so it is not silently ignored", async () => {
    const onPasteFiles = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onPasteFiles });

    const event = dropEvent([], "", ["photos"]);
    act(() => {
      host().dispatchEvent(event);
    });

    expect(onPasteFiles).toHaveBeenCalledWith([], ["文件夹「photos」"]);
    expect(event.defaultPrevented).toBe(true);
  });
});

describe("XtermView multi-line text paste", () => {
  afterEach(() => {
    setTerminalConfirmMultilinePaste("unbracketed");
  });

  it("pastes without a prompt once the user turned the multi-line prompt off", async () => {
    setTerminalConfirmMultilinePaste("never");
    const onData = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onData });

    const event = pasteEvent([], "echo one\necho two");
    await act(async () => {
      host().dispatchEvent(event);
      await flushMacrotask();
    });

    expect(event.defaultPrevented).toBe(true);
    expect(onData).toHaveBeenCalledWith("echo one\necho two");
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });

  it("sends a multi-line paste bracketed without a prompt when the program turned bracketed paste on", async () => {
    setTerminalConfirmMultilinePaste("unbracketed");
    const onData = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onData });
    const terminal = harness.terminals[0] as unknown as { modes: { bracketedPasteMode: boolean } };
    terminal.modes.bracketedPasteMode = true;

    await act(async () => {
      host().dispatchEvent(pasteEvent([], "echo one\necho two"));
      await flushMacrotask();
    });

    expect(onData).toHaveBeenCalledWith("\x1b[200~echo one\necho two\x1b[201~");
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
  });

  it("keeps the bracketed paste closed against an embedded end sequence while the prompt is off", async () => {
    setTerminalConfirmMultilinePaste("never");
    const onData = vi.fn();
    await show({ sessionId: "session", tabId: "pending", onData });
    const terminal = harness.terminals[0] as unknown as { modes: { bracketedPasteMode: boolean } };
    terminal.modes.bracketedPasteMode = true;

    await act(async () => {
      host().dispatchEvent(pasteEvent([], "echo one\x1b[201~\nrm -rf ~"));
      await flushMacrotask();
    });

    expect(onData).toHaveBeenCalledWith("\x1b[200~echo one␛[201~\nrm -rf ~\x1b[201~");
  });
});
