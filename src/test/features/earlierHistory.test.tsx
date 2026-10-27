/** @vitest-environment jsdom */

import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./reactTestUtils";
import type { TerminalHistoryChunk } from "../../ipc/types";

const terminals = vi.hoisted(() => ({ instances: [] as FakeTerminal[] }));

interface FakeTerminal {
  options: Record<string, unknown>;
  content: string;
  resets: number;
  disposed: boolean;
}

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options: Record<string, unknown>;
    content = "";
    resets = 0;
    disposed = false;
    buffer = { active: { length: 0, viewportY: 0 } };
    parser = { registerCsiHandler: () => ({ dispose: () => undefined }) };
    constructor(options: Record<string, unknown>) {
      this.options = options;
      terminals.instances.push(this as unknown as FakeTerminal);
    }
    open() {}
    write(data: Uint8Array, callback?: () => void) {
      this.content += new TextDecoder().decode(data);
      callback?.();
    }
    reset() {
      this.resets += 1;
      this.content = "";
    }
    scrollToBottom() {}
    scrollToLine() {}
    dispose() {
      this.disposed = true;
    }
  },
}));

vi.mock("../../ipc/commands", () => ({ terminalApi: { historyRead: vi.fn() } }));

import {
  applyEarlierHistoryPage,
  canLoadEarlierHistory,
  earlierHistoryBytes,
  earlierHistoryOffer,
  EarlierHistoryMismatchError,
  initialEarlierHistory,
  terminalHistoryInfo,
} from "../../features/terminal/earlierHistory";
import { EarlierHistory } from "../../features/terminal/EarlierHistoryPanel";
import { resetTerminalView, setTerminalAtTop } from "../../app/terminalViewStore";

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
  it("offers earlier history only when output before the first rendered byte is kept", () => {
    expect(earlierHistoryOffer(terminalHistoryInfo({}))).toBe("none");
    expect(earlierHistoryOffer(terminalHistoryInfo({ historyFrom: 300, historyBase: 100 }))).toBe("available");
    expect(earlierHistoryOffer(terminalHistoryInfo({ historyFrom: 300 }))).toBe("available");
    expect(
      earlierHistoryOffer(terminalHistoryInfo({ historyFrom: 100, historyBase: 100, historyTruncated: true })),
    ).toBe("truncated");
    // A drop within the stream is marked where it happened, not at the top.
    expect(earlierHistoryOffer(terminalHistoryInfo({ historyTruncated: true }))).toBe("none");
  });

  it("prepends pages in output order until the start of the session", () => {
    let state = initialEarlierHistory(9);
    state = applyEarlierHistoryPage(state, chunk(5, "ccdd"));
    expect(canLoadEarlierHistory(state)).toBe(true);
    state = applyEarlierHistoryPage(state, chunk(0, "aabbc"));
    expect(new TextDecoder().decode(earlierHistoryBytes(state))).toBe("aabbcccdd");
    expect(state).toMatchObject({ start: 0, end: 9, exhausted: true, truncated: false, capped: false });
    expect(canLoadEarlierHistory(state)).toBe(false);
  });

  it("marks the history truncated when the read reaches a dropped base", () => {
    let state = initialEarlierHistory(300);
    state = applyEarlierHistoryPage(state, chunk(100, "x".repeat(200), { historyBase: 100, truncated: true }));
    expect(state).toMatchObject({ exhausted: true, truncated: true });
    // A read at the base itself returns no data and the same verdict.
    const empty = applyEarlierHistoryPage(initialEarlierHistory(100), {
      ...chunk(100, ""),
      historyBase: 100,
      truncated: true,
    });
    expect(empty).toMatchObject({ bytes: 0, exhausted: true, truncated: true });
  });

  it("stops at the byte cap while more is recorded", () => {
    const state = applyEarlierHistoryPage(initialEarlierHistory(1000), chunk(990, "0123456789"), 8);
    expect(state).toMatchObject({ capped: true, exhausted: false });
    expect(canLoadEarlierHistory(state)).toBe(false);
  });

  it("refuses a page that does not end where the loaded output starts", () => {
    expect(() => applyEarlierHistoryPage(initialEarlierHistory(50), chunk(10, "abc"))).toThrow(EarlierHistoryMismatchError);
  });
});

describe("EarlierHistory", () => {
  let mounted: MountedView | undefined;
  const read = vi.fn<(tabId: string, before: number, maxBytes?: number) => Promise<TerminalHistoryChunk>>();
  const dimensions = () => ({ cols: 100, rows: 30 });

  beforeEach(() => {
    terminals.instances.length = 0;
    read.mockReset();
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
  const loadButton = () =>
    Array.from(mounted?.container.querySelectorAll("button") ?? []).find((button) =>
      ["加载更早", "重试"].includes(button.textContent?.trim() ?? ""),
    ) as HTMLButtonElement | undefined;

  it("appears at the top of the scrollback and loads pages before the first rendered byte", async () => {
    read
      .mockResolvedValueOnce(chunk(200, "B".repeat(100), { historyBase: 100 }))
      .mockResolvedValueOnce(chunk(100, "A".repeat(100), { historyBase: 100, truncated: true }));
    mounted = mount(
      <EarlierHistory
        viewKey="pane-1"
        tabId="kernel-1"
        info={{ from: 300, base: 100, truncated: true }}
        dimensions={dimensions}
        read={read}
      />,
    );
    expect(entry()).toBeNull();
    act(() => setTerminalAtTop("pane-1", true));
    expect(entry()?.textContent).toContain("加载更早历史");

    act(() => (entry() as HTMLButtonElement).click());
    await flush();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 300, 192 * 1024);
    const terminal = terminals.instances[0];
    expect(terminal.options).toMatchObject({ cols: 100, rows: 30, disableStdin: true });
    expect(terminal.content).toBe("B".repeat(100));
    expect(status()).toContain("已加载");

    act(() => loadButton()?.click());
    await flush();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 200, 192 * 1024);
    expect(terminal.content).toBe("A".repeat(100) + "B".repeat(100));
    expect(status()).toContain("早期历史已截断");
    expect(loadButton()?.disabled).toBe(true);
  });

  it("shows a failed read and retries from the same offset", async () => {
    read.mockRejectedValueOnce(new Error("helper offline")).mockResolvedValueOnce(chunk(0, "hello"));
    mounted = mount(
      <EarlierHistory viewKey="pane-1" tabId="kernel-1" info={{ from: 5, base: 0, truncated: false }} dimensions={dimensions} read={read} />,
    );
    act(() => setTerminalAtTop("pane-1", true));
    act(() => (entry() as HTMLButtonElement).click());
    await flush();
    expect(status()).toContain("加载失败");
    expect(status()).toContain("helper offline");
    act(() => loadButton()?.click());
    await flush();
    expect(read).toHaveBeenLastCalledWith("kernel-1", 5, 192 * 1024);
    expect(terminals.instances[0].content).toBe("hello");
    expect(status()).toContain("已到会话开头");
  });

  it("closes with Escape and disposes the replay terminal", async () => {
    read.mockResolvedValue(chunk(0, "hello"));
    mounted = mount(
      <EarlierHistory viewKey="pane-1" tabId="kernel-1" info={{ from: 5, base: 0, truncated: false }} dimensions={dimensions} read={read} />,
    );
    act(() => setTerminalAtTop("pane-1", true));
    act(() => (entry() as HTMLButtonElement).click());
    await flush();
    act(() => {
      panel()?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(panel()).toBeNull();
    expect(terminals.instances[0].disposed).toBe(true);
    expect(entry()).not.toBeNull();
  });

  it("states that earlier output was dropped when none is kept", () => {
    mounted = mount(
      <EarlierHistory viewKey="pane-1" tabId="kernel-1" info={{ from: 100, base: 100, truncated: true }} dimensions={dimensions} read={read} />,
    );
    act(() => setTerminalAtTop("pane-1", true));
    expect(entry()?.textContent).toContain("早期历史已截断");
    expect(entry()?.tagName).not.toBe("BUTTON");
  });

  it("offers nothing for a tab without earlier history", () => {
    mounted = mount(
      <EarlierHistory viewKey="pane-1" tabId="kernel-1" info={{ from: 0, base: 0, truncated: false }} dimensions={dimensions} read={read} />,
    );
    act(() => setTerminalAtTop("pane-1", true));
    expect(entry()).toBeNull();
  });
});
