/** @vitest-environment jsdom */

// XtermView with the real xterm.js on a touch screen: a tap must focus the terminal (opening the soft keyboard)
// and typed characters must reach the PTY through the shared input pipeline.

import { act, createElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";

const harness = vi.hoisted(() => ({
  attach: vi.fn(),
  terminals: [] as Array<{ write: (data: string, callback?: () => void) => void }>,
}));

vi.mock("@xterm/xterm", async (importOriginal) => {
  const original = await importOriginal<typeof import("@xterm/xterm")>();
  class Terminal extends original.Terminal {
    constructor(...args: ConstructorParameters<typeof original.Terminal>) {
      super(...args);
      harness.terminals.push(this);
    }
  }
  return { ...original, Terminal };
});
vi.mock("@xterm/addon-webgl", () => ({ WebglAddon: class {} }));
vi.mock("@xterm/addon-search", () => ({ SearchAddon: class { findNext() {} findPrevious() {} activate() {} dispose() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class { activate() {} dispose() {} } }));
vi.mock("../../features/terminal/terminalGeometry", () => ({
  measureTerminalGeometry: () => ({
    viewport: { widthPx: 360, heightPx: 480 },
    metrics: { widthPx: 10, heightPx: 20 },
    grid: { cols: 36, rows: 24 },
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
    detach: vi.fn().mockResolvedValue(undefined),
    setVisible: vi.fn().mockResolvedValue(undefined),
    write: vi.fn().mockResolvedValue(undefined),
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

import { XtermView, type TerminalHandle } from "../../features/terminal/XtermView";
import { clearStickyModifiers, toggleStickyModifier } from "../../features/terminal/terminalInput/stickyModifiers";

let root: Root | null = null;
let container: HTMLDivElement | null = null;
let sent: string[] = [];
let handle: TerminalHandle | null = null;

async function show(onTouchTap?: () => boolean): Promise<HTMLTextAreaElement> {
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      createElement(XtermView, {
        sessionId: "session",
        tabId: "pending",
        onData: (data: string) => sent.push(data),
        onHandle: (h: TerminalHandle) => {
          handle = h;
        },
        onTouchTap,
      }),
    );
  });
  await act(async () => {
    await flushMacrotask();
  });
  const textarea = container.querySelector<HTMLTextAreaElement>("textarea.xterm-helper-textarea");
  if (!textarea) throw new Error("xterm helper textarea missing");
  return textarea;
}

function screen(): HTMLElement {
  const element = container?.querySelector<HTMLElement>(".xterm-screen");
  if (!element) throw new Error("xterm screen missing");
  return element;
}

function touch(type: "touchstart" | "touchmove" | "touchend", target: HTMLElement, point?: { x: number; y: number }) {
  const event = new TouchEvent(type, { bubbles: true, cancelable: true });
  // A TouchList stand-in (jsdom has no Touch): indexable with item(), as xterm's gesture handler reads it.
  const touchList = (items: object[]) => Object.assign([...items], { item: (index: number) => items[index] ?? null });
  const points = point ? [{ clientX: point.x, clientY: point.y, pageX: point.x, pageY: point.y, identifier: 0, target }] : [];
  const active = type === "touchend" ? [] : points;
  Object.defineProperty(event, "touches", { value: touchList(active) });
  Object.defineProperty(event, "targetTouches", { value: touchList(type === "touchstart" ? points : []) });
  Object.defineProperty(event, "changedTouches", { value: touchList(points) });
  target.dispatchEvent(event);
}

function tap(target: HTMLElement): void {
  touch("touchstart", target, { x: 40, y: 40 });
  touch("touchend", target, { x: 41, y: 40 });
}

function insert(textarea: HTMLTextAreaElement, data: string): void {
  textarea.value += data;
  textarea.setSelectionRange(textarea.value.length, textarea.value.length);
  textarea.dispatchEvent(new InputEvent("input", { inputType: "insertText", data, bubbles: true, composed: true }));
}

const settle = () =>
  act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 20));
  });

beforeEach(() => {
  sent = [];
  handle = null;
  harness.terminals = [];
  harness.attach.mockReset().mockResolvedValue({
    tabId: "tab-1",
    sessionId: "session",
    cols: 36,
    rows: 24,
    controller: "me",
    subscribers: 1,
    viewers: 1,
    exited: false,
    shell: "unknown",
  });
  localStorage.clear();
  clearStickyModifiers();
  vi.stubGlobal("requestAnimationFrame", () => 0);
  vi.stubGlobal("cancelAnimationFrame", () => {});
  vi.stubGlobal("ResizeObserver", class {
    observe() {}
    disconnect() {}
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root?.unmount());
  root = null;
  container?.remove();
  container = null;
  clearStickyModifiers();
  vi.unstubAllGlobals();
});

describe("direct input on a touch screen", () => {
  it("focuses the terminal on a tap so typed characters go straight to the PTY", async () => {
    const textarea = await show();
    (document.activeElement as HTMLElement | null)?.blur();
    expect(document.activeElement).not.toBe(textarea);

    tap(screen());
    expect(document.activeElement).toBe(textarea);

    insert(textarea, "l");
    insert(textarea, "s");
    await settle();
    expect(sent.join("")).toBe("ls");
  });

  it("applies the key bar's latched Ctrl and Alt to the next typed character", async () => {
    const textarea = await show();
    tap(screen());
    toggleStickyModifier("ctrl");
    insert(textarea, "c");
    await settle();
    toggleStickyModifier("alt");
    insert(textarea, "b");
    await settle();
    insert(textarea, "x");
    await settle();
    expect(sent).toEqual(["\x03", "\x1bb", "x"]);
  });

  it("does not focus the terminal for a swipe", async () => {
    const textarea = await show();
    (document.activeElement as HTMLElement | null)?.blur();
    touch("touchstart", screen(), { x: 40, y: 40 });
    touch("touchmove", screen(), { x: 40, y: 140 });
    touch("touchend", screen(), { x: 40, y: 140 });
    expect(document.activeElement).not.toBe(textarea);
  });

  it("does not focus the terminal for a long press", async () => {
    let clock = 1000;
    const now = vi.spyOn(performance, "now").mockImplementation(() => clock);
    try {
      const textarea = await show();
      (document.activeElement as HTMLElement | null)?.blur();
      touch("touchstart", screen(), { x: 40, y: 40 });
      clock += 600;
      touch("touchend", screen(), { x: 40, y: 40 });
      expect(document.activeElement).not.toBe(textarea);

      touch("touchstart", screen(), { x: 40, y: 40 });
      clock += 100;
      touch("touchend", screen(), { x: 40, y: 40 });
      expect(document.activeElement).toBe(textarea);
    } finally {
      now.mockRestore();
    }
  });

  it("does not focus the terminal while text is selected", async () => {
    const textarea = await show();
    await act(async () => {
      await new Promise<void>((resolve) => harness.terminals[0].write("selected output", resolve));
    });
    act(() => (harness.terminals[0] as unknown as { selectAll: () => void }).selectAll());
    (document.activeElement as HTMLElement | null)?.blur();
    tap(screen());
    expect(document.activeElement).not.toBe(textarea);
  });

  it("leaves focus alone when buffered input handles the tap", async () => {
    const onTouchTap = vi.fn(() => true);
    const textarea = await show(onTouchTap);
    (document.activeElement as HTMLElement | null)?.blur();
    tap(screen());
    expect(onTouchTap).toHaveBeenCalledTimes(1);
    expect(document.activeElement).not.toBe(textarea);
  });
});

describe("terminal handle paste", () => {
  it("sends plain text and Enter when bracketed paste is off", async () => {
    await show();
    act(() => handle?.paste("git status", true));
    act(() => handle?.paste("ls", false));
    expect(sent).toEqual(["git status\r", "ls"]);
  });

  it("wraps the text in bracketed paste markers and puts Enter after them when the terminal enabled it", async () => {
    await show();
    await act(async () => {
      await new Promise<void>((resolve) => harness.terminals[0].write("\x1b[?2004h", resolve));
    });
    act(() => handle?.paste("echo a\necho b", true));
    act(() => handle?.paste("draft", false));
    expect(sent).toEqual(["\x1b[200~echo a\necho b\x1b[201~\r", "\x1b[200~draft\x1b[201~"]);
  });
});
