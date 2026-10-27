/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./reactTestUtils";
import type { TerminalHistoryChunk } from "../../ipc/types";

const terminals = vi.hoisted(() => ({ instances: [] as FakeTerminal[], fullAfterWrites: Number.POSITIVE_INFINITY }));

interface FakeTerminal {
  options: Record<string, unknown>;
  content: string;
  resets: number;
  disposed: boolean;
  selection: string;
}

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options: Record<string, unknown>;
    content = "";
    resets = 0;
    writes = 0;
    disposed = false;
    selection = "";
    rows: number;
    buffer = { active: { length: 0, viewportY: 0 } };
    parser = { registerCsiHandler: () => ({ dispose: () => undefined }) };
    constructor(options: Record<string, unknown>) {
      this.options = options;
      this.rows = options.rows as number;
      terminals.instances.push(this as unknown as FakeTerminal);
    }
    open() {}
    write(data: Uint8Array, callback?: () => void) {
      this.content += new TextDecoder().decode(data);
      this.writes += 1;
      // Simulates the scrollback filling up after the given number of writes.
      this.buffer.active.length =
        this.writes >= terminals.fullAfterWrites ? this.rows + (this.options.scrollback as number) : this.writes;
      callback?.();
    }
    reset() {
      this.resets += 1;
      this.content = "";
    }
    hasSelection() {
      return this.selection !== "";
    }
    getSelection() {
      return this.selection;
    }
    scrollToBottom() {}
    scrollToLine() {}
    dispose() {
      this.disposed = true;
    }
  },
}));

const clipboard = vi.hoisted(() => ({ copyText: vi.fn() }));
vi.mock("../../ui/clipboard", () => clipboard);
vi.mock("../../ipc/commands", () => ({ terminalApi: { historyRead: vi.fn() } }));

import {
  applyEarlierHistoryPage,
  canLoadEarlierHistory,
  capEarlierHistoryLines,
  EARLIER_HISTORY_SCROLLBACK,
  earlierHistoryBytes,
  earlierHistoryOffer,
  EarlierHistoryMismatchError,
  initialEarlierHistory,
  nextEarlierHistoryRead,
  terminalHistoryInfo,
  type EarlierHistoryEnd,
} from "../../features/terminal/earlierHistory";
import { EarlierHistory } from "../../features/terminal/EarlierHistoryPanel";
import { resetTerminalView, setTerminalHistoryView } from "../../app/terminalViewStore";

function b64(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

function chunk(start: number, text: string, extra: Partial<TerminalHistoryChunk> = {}): TerminalHistoryChunk {
  return {
    start,
    end: start + new TextEncoder().encode(text).length,
    historyBase: 0,
    historyNext: 10_000,
    truncated: false,
    dataBase64: b64(text),
    ...extra,
  };
}

describe("earlier history pages", () => {
  const info = (historyBase = 0) => terminalHistoryInfo({ historyBase });

  it("offers earlier history from the view's own start", () => {
    expect(earlierHistoryOffer(info(), { from: null, trimmed: false })).toEqual({ kind: "none" });
    expect(earlierHistoryOffer(info(), { from: 0, trimmed: false })).toEqual({ kind: "none" });
    expect(earlierHistoryOffer(info(100), { from: 300, trimmed: false })).toEqual({
      kind: "available",
      end: 300,
      overlap: false,
    });
    expect(earlierHistoryOffer(info(300), { from: 300, trimmed: false })).toEqual({ kind: "truncated" });
  });

  it("reads up to the newest output, with overlap, once the view's scrollback was trimmed", () => {
    expect(earlierHistoryOffer(info(), { from: 0, trimmed: true })).toEqual({
      kind: "available",
      end: "latest",
      overlap: true,
    });
    // A regular tab reports no start and offers nothing.
    expect(earlierHistoryOffer(info(), { from: null, trimmed: true })).toEqual({ kind: "none" });
  });

  it("prepends pages in output order until the start of the session", () => {
    let state = initialEarlierHistory(9);
    expect(nextEarlierHistoryRead(state)).toBe(9);
    state = applyEarlierHistoryPage(state, chunk(5, "ccdd"));
    expect(canLoadEarlierHistory(state)).toBe(true);
    expect(nextEarlierHistoryRead(state)).toBe(5);
    state = applyEarlierHistoryPage(state, chunk(0, "aabbc"));
    expect(new TextDecoder().decode(earlierHistoryBytes(state))).toBe("aabbcccdd");
    expect(state).toMatchObject({ start: 0, end: 9, exhausted: true, truncated: false, capped: null });
    expect(canLoadEarlierHistory(state)).toBe(false);
  });

  it("starts a read at the newest output and continues from the reply", () => {
    let state = initialEarlierHistory("latest");
    expect(nextEarlierHistoryRead(state)).toBe("latest");
    state = applyEarlierHistoryPage(state, chunk(700, "tail"));
    expect(state).toMatchObject({ start: 700, end: 704 });
    expect(nextEarlierHistoryRead(state)).toBe(700);
  });

  it("is done when the read reaches a dropped base, also after a skipped partial sequence", () => {
    let state = initialEarlierHistory(300);
    state = applyEarlierHistoryPage(state, chunk(100, "x".repeat(200), { historyBase: 100, truncated: true }));
    expect(state).toMatchObject({ exhausted: true, truncated: true });
    // The helper skipped a partial escape sequence at the base: start is past
    // the base but the reply is marked truncated.
    const skipped = applyEarlierHistoryPage(
      initialEarlierHistory(300),
      chunk(103, "y".repeat(197), { historyBase: 100, truncated: true }),
    );
    expect(skipped).toMatchObject({ exhausted: true, truncated: true });
    const empty = applyEarlierHistoryPage(initialEarlierHistory(100), {
      ...chunk(100, ""),
      historyBase: 100,
      truncated: true,
    });
    expect(empty).toMatchObject({ bytes: 0, exhausted: true, truncated: true });
  });

  it("stops at the byte cap or the replay terminal's line limit while more is recorded", () => {
    const state = applyEarlierHistoryPage(initialEarlierHistory(1000), chunk(990, "0123456789"), 8);
    expect(state).toMatchObject({ capped: "bytes", exhausted: false });
    expect(canLoadEarlierHistory(state)).toBe(false);
    const lines = capEarlierHistoryLines(applyEarlierHistoryPage(initialEarlierHistory(1000), chunk(990, "0123456789")));
    expect(lines.capped).toBe("lines");
    expect(canLoadEarlierHistory(lines)).toBe(false);
  });

  it("refuses a page that does not end where the loaded output starts", () => {
    expect(() => applyEarlierHistoryPage(initialEarlierHistory(50), chunk(10, "abc"))).toThrow(EarlierHistoryMismatchError);
  });
});

describe("EarlierHistory", () => {
  let mounted: MountedView | undefined;
  const read = vi.fn<(tabId: string, before: EarlierHistoryEnd, maxBytes?: number) => Promise<TerminalHistoryChunk>>();
  const dimensions = () => ({ cols: 100, rows: 30 });

  beforeEach(() => {
    terminals.instances.length = 0;
    terminals.fullAfterWrites = Number.POSITIVE_INFINITY;
    read.mockReset();
    clipboard.copyText.mockReset().mockResolvedValue(undefined);
    resetTerminalView();
    document.body.replaceChildren();
  });

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  const entry = () => mounted?.container.querySelector("[data-testid='earlier-history-entry']") ?? null;
  const panel = () => mounted?.container.querySelector("[data-testid='earlier-history']") ?? null;
  const status = () => mounted?.container.querySelector("[data-testid='earlier-history-status']")?.textContent ?? "";
  const button = (label: string) =>
    Array.from(mounted?.container.querySelectorAll("button") ?? []).find(
      (candidate) => candidate.textContent?.trim() === label,
    ) as HTMLButtonElement | undefined;
  const loadButton = () => button("加载更早") ?? button("重试");

  function show(props: { tabId?: string; base?: number; onClosed?: () => void } = {}) {
    mounted = mount(
      <EarlierHistory
        viewKey="pane-1"
        tabId={props.tabId ?? "kernel-1"}
        info={{ base: props.base ?? 0, truncated: (props.base ?? 0) > 0 }}
        dimensions={dimensions}
        onClosed={props.onClosed}
        read={read}
      />,
    );
  }

  async function openPanel() {
    act(() => (entry() as HTMLButtonElement).click());
    await flush();
  }

  it("appears at the top of the scrollback and loads pages before the view's first byte", async () => {
    read
      .mockResolvedValueOnce(chunk(200, "B".repeat(100), { historyBase: 100 }))
      .mockResolvedValueOnce(chunk(100, "A".repeat(100), { historyBase: 100, truncated: true }));
    act(() => setTerminalHistoryView("pane-1", { from: 300 }));
    show({ base: 100 });
    expect(entry()).toBeNull();
    act(() => setTerminalHistoryView("pane-1", { atTop: true }));
    expect(entry()?.textContent).toContain("加载更早历史");

    await openPanel();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 300, 192 * 1024);
    const terminal = terminals.instances[0];
    expect(terminal.options).toMatchObject({ cols: 100, rows: 30, disableStdin: true, scrollback: EARLIER_HISTORY_SCROLLBACK });
    expect(terminal.content).toBe("B".repeat(100));
    expect(status()).toContain("已加载");
    expect(mounted?.container.querySelector("[data-testid='earlier-history-overlap']")).toBeNull();

    act(() => loadButton()?.click());
    await flush();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 200, 192 * 1024);
    expect(terminal.content).toBe("A".repeat(100) + "B".repeat(100));
    expect(status()).toContain("早期历史已截断");
    expect(loadButton()?.disabled).toBe(true);
  });

  it("reads from the newest output and notes the overlap when the view's scrollback was trimmed", async () => {
    read.mockResolvedValueOnce(chunk(5000, "latest output"));
    act(() => setTerminalHistoryView("pane-1", { from: 0, trimmed: true, atTop: true }));
    show();
    await openPanel();
    expect(read).toHaveBeenLastCalledWith("kernel-1", "latest", 192 * 1024);
    expect(mounted?.container.querySelector("[data-testid='earlier-history-overlap']")?.textContent).toContain(
      "可能与当前终端内容有重叠",
    );
  });

  it("stops loading once the replay terminal's scrollback is full", async () => {
    terminals.fullAfterWrites = 1;
    read.mockResolvedValueOnce(chunk(9000, "x".repeat(1000)));
    act(() => setTerminalHistoryView("pane-1", { from: 10_000, atTop: true }));
    show();
    await openPanel();
    expect(status()).toContain("达到回放行数上限");
    expect(loadButton()?.disabled).toBe(true);
    expect(read).toHaveBeenCalledTimes(1);
  });

  it("shows a failed read and retries from the same offset", async () => {
    read.mockRejectedValueOnce(new Error("helper offline")).mockResolvedValueOnce(chunk(0, "hello"));
    act(() => setTerminalHistoryView("pane-1", { from: 5, atTop: true }));
    show();
    await openPanel();
    expect(status()).toContain("加载失败");
    expect(status()).toContain("helper offline");
    act(() => loadButton()?.click());
    await flush();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 5, 192 * 1024);
    expect(terminals.instances[0].content).toBe("hello");
    expect(status()).toContain("已到会话开头");
  });

  it("keeps keys and the context menu inside the panel and copies the replay selection", async () => {
    read.mockResolvedValue(chunk(0, "hello"));
    act(() => setTerminalHistoryView("pane-1", { from: 5, atTop: true }));
    show();
    const outer = vi.fn();
    document.body.addEventListener("keydown", outer);
    document.body.addEventListener("contextmenu", outer);
    await openPanel();
    terminals.instances[0].selection = "hel";
    const target = panel()!;
    const contextMenu = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    act(() => {
      target.dispatchEvent(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, shiftKey: true, bubbles: true }));
      target.dispatchEvent(new KeyboardEvent("keydown", { key: "c", ctrlKey: true, bubbles: true }));
      target.dispatchEvent(contextMenu);
    });
    await flush();
    expect(outer).not.toHaveBeenCalled();
    expect(contextMenu.defaultPrevented).toBe(true);
    expect(clipboard.copyText).toHaveBeenCalledWith("hel");
    document.body.removeEventListener("keydown", outer);
    document.body.removeEventListener("contextmenu", outer);
  });

  it("closes with Escape, disposes the replay terminal and returns focus", async () => {
    read.mockResolvedValue(chunk(0, "hello"));
    const onClosed = vi.fn();
    act(() => setTerminalHistoryView("pane-1", { from: 5, atTop: true }));
    show({ onClosed });
    await openPanel();
    act(() => {
      panel()?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(panel()).toBeNull();
    expect(terminals.instances[0].disposed).toBe(true);
    expect(onClosed).toHaveBeenCalledTimes(1);
    expect(entry()).not.toBeNull();
  });

  it("closes the panel when the pane attaches to another tab", async () => {
    read.mockResolvedValue(chunk(0, "hello"));
    act(() => setTerminalHistoryView("pane-1", { from: 5, atTop: true }));
    show();
    await openPanel();
    expect(panel()).not.toBeNull();
    act(() =>
      mounted!.root.render(
        <EarlierHistory viewKey="pane-1" tabId="kernel-2" info={{ base: 0, truncated: false }} dimensions={dimensions} read={read} />,
      ),
    );
    act(() =>
      mounted!.root.render(
        <EarlierHistory viewKey="pane-1" tabId="kernel-1" info={{ base: 0, truncated: false }} dimensions={dimensions} read={read} />,
      ),
    );
    expect(panel()).toBeNull();
  });

  it("states that earlier output was dropped when none is kept", () => {
    act(() => setTerminalHistoryView("pane-1", { from: 100, atTop: true }));
    show({ base: 100 });
    expect(entry()?.textContent).toContain("早期历史已截断");
    expect(entry()?.tagName).not.toBe("BUTTON");
  });
});
