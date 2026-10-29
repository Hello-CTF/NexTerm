/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushUntil, mount, setInputValue, chooseSelectOption, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ transcriptRead: vi.fn() }));

const terminalMocks = vi.hoisted(() => ({ instances: [] as FakeTerminal[] }));

interface FakeTerminal {
  options?: unknown;
  writes: Uint8Array[];
  resizes: { cols: number; rows: number }[];
  resets: number;
  disposed: boolean;
  content: string;
  deferWrites: boolean;
  readonly pendingWrites: number;
  completeNextWrite: () => void;
}

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options?: unknown;
    writes: Uint8Array[] = [];
    resizes: { cols: number; rows: number }[] = [];
    resets = 0;
    disposed = false;
    content = "";
    deferWrites = false;
    private callbacks: (() => void)[] = [];
    constructor(options?: unknown) {
      this.options = options;
      terminalMocks.instances.push(this as unknown as FakeTerminal);
    }
    open() {}
    write(data: Uint8Array, callback?: () => void) {
      this.writes.push(data);
      this.content += new TextDecoder().decode(data);
      if (!callback) return;
      if (this.deferWrites) this.callbacks.push(callback);
      else callback();
    }
    get pendingWrites(): number {
      return this.callbacks.length;
    }
    completeNextWrite() {
      const callback = this.callbacks.shift();
      if (!callback) throw new Error("No pending write");
      callback();
    }
    resize(cols: number, rows: number) {
      this.resizes.push({ cols, rows });
    }
    reset() {
      this.resets += 1;
      this.content = "";
    }
    dispose() {
      this.disposed = true;
    }
  },
}));

vi.mock("../../ipc/commands", () => ({
  transcriptApi: { read: mocks.transcriptRead },
}));

import { TranscriptReplayView } from "../../features/terminal/TranscriptReplayView";

function b64(text: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(text)));
}

const RESIZE_80 = b64('{"cols":80,"rows":24}');
const RESIZE_120 = b64('{"cols":120,"rows":40}');

function readResult() {
  return {
    chunks: [
      { seq: 0, tabId: "tab-1", ts: 1000, kind: 2, dataBase64: RESIZE_80 },
      { seq: 1, tabId: "tab-1", ts: 1000, kind: 0, dataBase64: b64("hello ") },
      { seq: 2, tabId: "tab-1", ts: 1500, kind: 1, dataBase64: b64("secret\r") },
      { seq: 3, tabId: "tab-1", ts: 2000, kind: 0, dataBase64: b64("world") },
      { seq: 4, tabId: "tab-1", ts: 2500, kind: 2, dataBase64: RESIZE_120 },
    ],
    nextSeq: 5,
    done: true,
    totalBytes: 64,
  };
}

function writtenText(terminal: FakeTerminal): string {
  return terminal.writes.map((data) => new TextDecoder().decode(data)).join("");
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  terminalMocks.instances.length = 0;
  document.body.replaceChildren();
  mocks.transcriptRead.mockResolvedValue(readResult());
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("TranscriptReplayView", () => {
  it("applies the initial frame after load without replaying input", async () => {
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await flush();
    const terminal = terminalMocks.instances[0];
    expect(terminal).toBeDefined();
    expect(terminal.resets).toBe(1);
    expect(terminal.resizes).toEqual([{ cols: 80, rows: 24 }]);
    expect(writtenText(terminal)).toBe("hello ");
    expect(mocks.transcriptRead).toHaveBeenCalledWith("t-1", 0, 1024 * 1024);
    const clock = mounted.container.querySelector("[data-testid='replay-clock']");
    expect(clock?.textContent).toBe("00:00 / 00:01");
  });

  it("loads multiple pages by nextSeq and preserves byte order across page boundaries", async () => {
    const encode = (bytes: number[]) => btoa(String.fromCharCode(...bytes));
    mocks.transcriptRead.mockImplementation(async (_id: string, afterSeq: number) => {
      if (afterSeq === 0) {
        return {
          chunks: [
            { seq: 0, tabId: "tab-1", ts: 1000, kind: 0, dataBase64: encode([0, 1, 2]) },
            { seq: 1, tabId: "tab-1", ts: 1000, kind: 0, dataBase64: encode([65]) },
          ],
          nextSeq: 2,
          done: false,
          totalBytes: 7,
        };
      }
      return {
        chunks: [
          { seq: 2, tabId: "tab-1", ts: 1000, kind: 0, dataBase64: encode([66, 67]) },
          { seq: 3, tabId: "tab-1", ts: 1000, kind: 0, dataBase64: encode([255]) },
        ],
        nextSeq: 4,
        done: true,
        totalBytes: 7,
      };
    });

    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    // The pages are separated by a macrotask yield, so wait for the data instead of counting flushes.
    await flushUntil(() => (terminalMocks.instances[0]?.writes.reduce((total, data) => total + data.length, 0) ?? 0) >= 7);

    expect(mocks.transcriptRead).toHaveBeenCalledTimes(2);
    expect(mocks.transcriptRead).toHaveBeenNthCalledWith(1, "t-1", 0, 1024 * 1024);
    expect(mocks.transcriptRead).toHaveBeenNthCalledWith(2, "t-1", 2, 1024 * 1024);
    const terminal = terminalMocks.instances[0];
    expect(terminal.writes.flatMap((data) => Array.from(data))).toEqual([0, 1, 2, 65, 66, 67, 255]);
  });

  it("automatically pauses at the end during playback and applies the speed multiplier", async () => {
    vi.useFakeTimers();
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const terminal = terminalMocks.instances[0];
    const playButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "开始回放",
    );
    expect(playButton).toBeDefined();

    chooseSelectOption(mounted.container, "回放倍速", "5x");
    act(() => {
      playButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(writtenText(terminal)).toContain("world");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(160);
    });
    expect(terminal.resizes).toEqual([
      { cols: 80, rows: 24 },
      { cols: 120, rows: 40 },
    ]);
    const pauseButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "暂停回放",
    );
    expect(pauseButton).toBeUndefined();
    const clock = mounted.container.querySelector("[data-testid='replay-clock']");
    expect(clock?.textContent).toBe("00:01 / 00:01");
  });

  it("resets the terminal and replays the initial frame when seeking backward", async () => {
    vi.useFakeTimers();
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const terminal = terminalMocks.instances[0];
    const playButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "开始回放",
    );
    act(() => {
      playButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1600);
    });
    expect(writtenText(terminal)).toContain("world");

    const resetsBefore = terminal.resets;
    const range = mounted.container.querySelector("input[aria-label='回放进度']") as HTMLInputElement;
    setInputValue(range, "0");
    await act(async () => {
      await Promise.resolve();
    });
    expect(terminal.resets).toBe(resetsBefore + 1);
    expect(writtenText(terminal)).toBe("hello worldhello ");
    const clock = mounted.container.querySelector("[data-testid='replay-clock']");
    expect(clock?.textContent).toBe("00:00 / 00:01");
  });

  it("coalesces 20 seek inputs behind pending terminal writes", async () => {
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    const terminal = terminalMocks.instances[0];
    terminal.deferWrites = true;
    await flush();
    expect(terminal.pendingWrites).toBe(1);
    const resetsBefore = terminal.resets;
    const range = mounted.container.querySelector("input[aria-label='回放进度']") as HTMLInputElement;
    for (let i = 1; i <= 20; i += 1) setInputValue(range, String(i * 50));
    expect(terminal.resets).toBe(resetsBefore);

    await act(async () => {
      terminal.completeNextWrite();
      await Promise.resolve();
    });
    expect(terminal.resets).toBe(resetsBefore + 1);
    await act(async () => {
      while (terminal.pendingWrites > 0) {
        terminal.completeNextWrite();
        await Promise.resolve();
      }
    });

    expect(terminal.resets).toBe(resetsBefore + 1);
    expect(terminal.content).toBe("hello world");
    expect(terminal.content).not.toContain("secret");
    expect(mounted.container.querySelector("[data-testid='replay-clock']")?.textContent).toBe(
      "00:01 / 00:01",
    );
  });

  it("shows an error when loading fails", async () => {
    mocks.transcriptRead.mockRejectedValue(new Error("boom"));
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await flush();
    expect(mounted.container.textContent).toContain("未能加载回放数据");
  });

  it("advances playback using real elapsed time instead of fixed ticks", async () => {
    vi.useFakeTimers();
    let now = 0;
    vi.spyOn(performance, "now").mockImplementation(() => now);
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const terminal = terminalMocks.instances[0];
    const playButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "开始回放",
    );
    act(() => {
      playButton?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    now = 1100;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50);
    });

    expect(writtenText(terminal)).toContain("world");
    expect(mounted.container.querySelector("[data-testid='replay-clock']")?.textContent).toBe(
      "00:01 / 00:01",
    );
  });

  it("skips the empty content read for contentOmitted and shows the metadata-only state", async () => {
    mounted = mount(<TranscriptReplayView transcriptId="t-1" contentOmitted />);
    await flush();

    expect(mocks.transcriptRead).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("仅保留元数据");
    expect(mounted.container.textContent).not.toContain("没有任何可回放的内容");
    expect(
      mounted.container.querySelector<HTMLButtonElement>('button[aria-label="开始回放"]')?.disabled,
    ).toBe(true);
  });

  it("gives the seek slider a pan-y touch policy and its own row on phones", async () => {
    mounted = mount(createElement(TranscriptReplayView, { transcriptId: "t1" }));
    await flush();
    const slider = mounted.container.querySelector<HTMLInputElement>('[data-testid="replay-seek"]')!;
    expect(slider.classList.contains("touch-pan-y")).toBe(true);
    expect(slider.classList.contains("max-[560px]:basis-full")).toBe(true);
    expect(slider.parentElement!.classList.contains("flex-wrap")).toBe(true);
    const clock = mounted.container.querySelector('[data-testid="replay-clock"]')!;
    expect(clock.classList.contains("whitespace-nowrap")).toBe(true);
  });

  it("uses a smaller terminal font on phone-width viewports and keeps 12 otherwise", async () => {
    mounted = mount(createElement(TranscriptReplayView, { transcriptId: "t1" }));
    await flush();
    expect((terminalMocks.instances[0].options as { fontSize: number }).fontSize).toBe(12);
    mounted.unmount();
    const original = window.innerWidth;
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 390 });
    try {
      mounted = mount(createElement(TranscriptReplayView, { transcriptId: "t1" }));
      await flush();
      expect((terminalMocks.instances[1].options as { fontSize: number }).fontSize).toBe(11);
    } finally {
      Object.defineProperty(window, "innerWidth", { configurable: true, value: original });
    }
  });
});
