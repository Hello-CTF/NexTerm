/** @vitest-environment jsdom */

// XtermView with the real xterm.js: keys are dispatched on xterm's helper textarea and the assertions are on the
// bytes XtermView hands to the PTY (its onData prop).

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  mac: false,
  attach: vi.fn(),
  detach: vi.fn(),
  setVisible: vi.fn(),
  write: vi.fn(),
}));

vi.mock("@xterm/addon-webgl", () => ({ WebglAddon: class {} }));
vi.mock("@xterm/addon-search", () => ({ SearchAddon: class { findNext() {} findPrevious() {} activate() {} dispose() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class { activate() {} dispose() {} } }));
vi.mock("../../app/platform", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/platform")>()),
  isMac: () => harness.mac,
}));
vi.mock("../../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: () => ({
    viewport: { widthPx: 800, heightPx: 480 },
    metrics: { widthPx: 10, heightPx: 20 },
    grid: { cols: 80, rows: 24 },
  }),
  resizeTerminalToGridPreservingSelection: () => {},
}));
vi.mock("../../features/terminal/gridRuntimeAdapter", () => ({
  productionGridRuntime: { resize: vi.fn(), flush: vi.fn() },
}));
vi.mock("../../ipc/commands", () => ({
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
vi.mock("../../ipc/events", () => ({
  channelIdOf: () => "channel-1",
  createBinaryChannel: () => ({}),
  disposeChannel: vi.fn(),
  onChannelReopen: () => vi.fn(),
  sendChannelInput: vi.fn(),
}));
vi.mock("../../ipc/env", () => ({ clientId: () => "me", WEB: false }));

import { XtermView } from "../../features/terminal/XtermView";
import { resetAllKeybindings, setKeybinding } from "../../app/keybindings";
import {
  clearStickyModifiers,
  getStickyModifiers,
  toggleStickyModifier,
} from "../../features/terminal/terminalInput/stickyModifiers";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let sent: string[] = [];

type KeyInit = KeyboardEventInit & { keyCode?: number; charCode?: number };

async function show(): Promise<HTMLTextAreaElement> {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(createElement(XtermView, { sessionId: "session", tabId: "pending", onData: (data: string) => sent.push(data) }));
  });
  await act(async () => {
    await flushMacrotask();
  });
  const textarea = container.querySelector<HTMLTextAreaElement>("textarea.xterm-helper-textarea");
  if (!textarea) throw new Error("xterm helper textarea missing");
  textarea.focus();
  return textarea;
}

function key(target: HTMLElement, type: "keydown" | "keypress" | "keyup", init: KeyInit): KeyboardEvent {
  const event = new KeyboardEvent(type, { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
}

// A keystroke the way browsers deliver it: keydown, keypress (printable keys xterm leaves to keypress), keyup.
function stroke(target: HTMLElement, init: KeyInit): void {
  const down = key(target, "keydown", init);
  if (!down.defaultPrevented && init.key && init.key.length === 1 && !init.ctrlKey && !init.metaKey) {
    key(target, "keypress", { ...init, charCode: init.key.charCodeAt(0) });
  }
  key(target, "keyup", init);
}

function insert(textarea: HTMLTextAreaElement, data: string): void {
  textarea.value += data;
  textarea.setSelectionRange(textarea.value.length, textarea.value.length);
  textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", data, bubbles: true, composed: true }));
}

const SAFARI_UA =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15";
const ANDROID_CHROME_UA =
  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36";

function useUserAgent(userAgent: string): void {
  Object.defineProperty(window.navigator, "userAgent", { value: userAgent, configurable: true });
}

function commitComposition(textarea: HTMLTextAreaElement, text: string): void {
  textarea.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
  textarea.value = text;
  textarea.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: text }));
}

const settle = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
  });

beforeEach(() => {
  sent = [];
  harness.mac = false;
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-1",
    sessionId: "session",
    cols: 80,
    rows: 24,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
  });
  harness.detach.mockReset().mockResolvedValue(undefined);
  harness.setVisible.mockReset().mockResolvedValue(undefined);
  harness.write.mockReset().mockResolvedValue(undefined);
  localStorage.clear();
  resetAllKeybindings();
  clearStickyModifiers();
  // Rendering is not under test, and jsdom's canvas has no 2D context for xterm's overview ruler.
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  Reflect.deleteProperty(window.navigator, "userAgent");
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  resetAllKeybindings();
  clearStickyModifiers();
  vi.unstubAllGlobals();
});

describe("XtermView input pipeline bytes", () => {
  it("sends ESC+CR for Shift+Enter and a bare CR for Enter", async () => {
    const textarea = await show();
    stroke(textarea, { key: "Enter", code: "Enter", keyCode: 13, shiftKey: true });
    stroke(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    expect(sent).toEqual(["\x1b\r", "\r"]);
  });

  it("deletes the previous word on Ctrl+Backspace and one character on Backspace", async () => {
    const textarea = await show();
    stroke(textarea, { key: "Backspace", code: "Backspace", keyCode: 8, ctrlKey: true });
    stroke(textarea, { key: "Backspace", code: "Backspace", keyCode: 8 });
    expect(sent).toEqual(["\x17", "\x7f"]);
  });

  it("moves by word with Option+arrows and by line with Cmd+arrows on macOS", async () => {
    harness.mac = true;
    const textarea = await show();
    stroke(textarea, { key: "ArrowLeft", code: "ArrowLeft", keyCode: 37, altKey: true });
    stroke(textarea, { key: "ArrowRight", code: "ArrowRight", keyCode: 39, altKey: true });
    stroke(textarea, { key: "ArrowLeft", code: "ArrowLeft", keyCode: 37, metaKey: true });
    stroke(textarea, { key: "ArrowRight", code: "ArrowRight", keyCode: 39, metaKey: true });
    expect(sent).toEqual(["\x1bb", "\x1bf", "\x01", "\x05"]);
  });

  it("keeps xterm's modified arrow encoding for Alt+arrows off macOS", async () => {
    const textarea = await show();
    stroke(textarea, { key: "ArrowLeft", code: "ArrowLeft", keyCode: 37, altKey: true });
    expect(sent).toEqual(["\x1b[1;3D"]);
  });

  it("does not submit when Enter commits a Chinese composition", async () => {
    const textarea = await show();
    textarea.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true, data: "" }));
    key(textarea, "keydown", { key: "Process", keyCode: 229, isComposing: true });
    textarea.value = "nihao";
    textarea.dispatchEvent(new CompositionEvent("compositionupdate", { bubbles: true, data: "nihao" }));
    // Firefox reports the committing Enter with its real keyCode while isComposing is still true.
    key(textarea, "keydown", { key: "Enter", code: "Enter", keyCode: 13, isComposing: true });
    textarea.value = "你好";
    textarea.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "你好" }));
    key(textarea, "keyup", { key: "Enter", code: "Enter", keyCode: 13 });
    await settle();
    expect(sent.join("")).toBe("你好");
  });

  it("swallows the Enter WebKit delivers after compositionend, but not a later Enter", async () => {
    useUserAgent(SAFARI_UA);
    const textarea = await show();
    commitComposition(textarea, "中文");
    stroke(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    await settle();
    expect(sent.join("")).toBe("中文");
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 80));
    });
    stroke(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    expect(sent.join("")).toBe("中文\r");
  });

  it("submits an Enter that follows compositionend on Chromium and Android keyboards", async () => {
    useUserAgent(ANDROID_CHROME_UA);
    const textarea = await show();
    textarea.value = "";
    insert(textarea, "l");
    insert(textarea, "s");
    commitComposition(textarea, "ls");
    stroke(textarea, { key: "Enter", code: "Enter", keyCode: 13 });
    await settle();
    expect(sent.join("").endsWith("\r")).toBe(true);
    expect(sent.filter((data) => data === "\r")).toHaveLength(1);
  });

  it("types an auto-repeated Shift letter once per repeat", async () => {
    const textarea = await show();
    key(textarea, "keydown", { key: "Shift", code: "ShiftLeft", keyCode: 16, shiftKey: true });
    for (let i = 0; i < 4; i++) {
      const repeat = i > 0;
      key(textarea, "keydown", { key: "H", code: "KeyH", keyCode: 72, shiftKey: true, repeat });
      key(textarea, "keypress", { key: "H", code: "KeyH", charCode: 72, shiftKey: true, repeat });
      insert(textarea, "H");
      // Linux and Windows also auto-repeat the held Shift itself between the letter repeats.
      key(textarea, "keydown", { key: "Shift", code: "ShiftLeft", keyCode: 16, shiftKey: true, repeat: true });
    }
    await settle();
    key(textarea, "keyup", { key: "H", code: "KeyH", keyCode: 72, shiftKey: true });
    key(textarea, "keyup", { key: "Shift", code: "ShiftLeft", keyCode: 16 });
    await settle();
    expect(sent).toEqual(["H", "H", "H", "H"]);
  });

  it("withholds a bound app shortcut and its repeats from the terminal, and delivers the chord once unbound", async () => {
    const textarea = await show();
    const closeTab = { key: "w", code: "KeyW", keyCode: 87, ctrlKey: true };
    stroke(textarea, closeTab);
    key(textarea, "keydown", { ...closeTab, repeat: true });
    stroke(textarea, { key: "W", code: "KeyW", keyCode: 87, ctrlKey: true, shiftKey: true });
    expect(sent).toEqual([]);

    setKeybinding("closeTab", null);
    stroke(textarea, closeTab);
    expect(sent).toEqual(["\x17"]);
  });

  it("applies a key-bar Ctrl latch to the next typed character only", async () => {
    const textarea = await show();
    toggleStickyModifier("ctrl");
    stroke(textarea, { key: "c", code: "KeyC", keyCode: 67 });
    stroke(textarea, { key: "c", code: "KeyC", keyCode: 67 });
    expect(sent).toEqual(["\x03", "c"]);
    expect(getStickyModifiers()).toEqual({ ctrl: false, alt: false });

    toggleStickyModifier("alt");
    stroke(textarea, { key: "x", code: "KeyX", keyCode: 88 });
    expect(sent).toEqual(["\x03", "c", "\x1bx"]);

    // Committed IME text has no Ctrl form: it is sent as typed and releases the latch.
    toggleStickyModifier("ctrl");
    insert(textarea, "。");
    await settle();
    stroke(textarea, { key: "c", code: "KeyC", keyCode: 67 });
    expect(sent).toEqual(["\x03", "c", "\x1bx", "。", "c"]);
  });

  it("clips the IME helper so the candidate window cannot scroll the terminal sideways", async () => {
    await show();
    const host = container?.querySelector<HTMLElement>(".nx-xterm-host");
    expect(host && getComputedStyle(host).overflow).toBe("clip");
  });
});
