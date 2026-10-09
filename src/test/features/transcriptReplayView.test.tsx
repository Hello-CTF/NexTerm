/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { flush, mount, setInputValue, setSelectValue, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({ transcriptRead: vi.fn() }));

const terminalMocks = vi.hoisted(() => ({ instances: [] as FakeTerminal[] }));

interface FakeTerminal {
  options?: unknown;
  writes: Uint8Array[];
  resizes: { cols: number; rows: number }[];
  resets: number;
  disposed: boolean;
}

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options?: unknown;
    writes: Uint8Array[] = [];
    resizes: { cols: number; rows: number }[] = [];
    resets = 0;
    disposed = false;
    constructor(options?: unknown) {
      this.options = options;
      terminalMocks.instances.push(this as unknown as FakeTerminal);
    }
    open() {}
    write(data: Uint8Array) {
      this.writes.push(data);
    }
    resize(cols: number, rows: number) {
      this.resizes.push({ cols, rows });
    }
    reset() {
      this.resets += 1;
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
});

describe("TranscriptReplayView", () => {
  it("加载后应用初始帧,input 不回放", async () => {
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await flush();
    const terminal = terminalMocks.instances[0];
    expect(terminal).toBeDefined();
    expect(terminal.resets).toBe(1);
    expect(terminal.resizes).toEqual([{ cols: 80, rows: 24 }]);
    expect(writtenText(terminal)).toBe("hello ");
    expect(mocks.transcriptRead).toHaveBeenCalledWith("t-1", 0, 4 * 1024 * 1024);
    const clock = mounted.container.querySelector("[data-testid='replay-clock']");
    expect(clock?.textContent).toBe("00:00 / 00:01");
  });

  it("播放推进到结尾自动暂停,倍速生效", async () => {
    vi.useFakeTimers();
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const terminal = terminalMocks.instances[0];
    const playButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "播放回放",
    );
    expect(playButton).toBeDefined();

    setSelectValue(mounted.container.querySelector("select[aria-label='回放倍速']") as HTMLSelectElement, "5");
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

  it("后退 seek 重置终端并重放起始帧", async () => {
    vi.useFakeTimers();
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const terminal = terminalMocks.instances[0];
    const playButton = [...mounted.container.querySelectorAll("button")].find(
      (candidate) => candidate.getAttribute("aria-label") === "播放回放",
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
    expect(terminal.resets).toBe(resetsBefore + 1);
    expect(writtenText(terminal)).toBe("hello worldhello ");
    const clock = mounted.container.querySelector("[data-testid='replay-clock']");
    expect(clock?.textContent).toBe("00:00 / 00:01");
  });

  it("加载失败展示错误", async () => {
    mocks.transcriptRead.mockRejectedValue(new Error("boom"));
    mounted = mount(<TranscriptReplayView transcriptId="t-1" />);
    await flush();
    expect(mounted.container.textContent).toContain("加载回放数据失败");
  });
});
