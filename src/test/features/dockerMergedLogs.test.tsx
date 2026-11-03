/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, waitFor, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ps: vi.fn(),
  images: vi.fn(),
  logsAttach: vi.fn(),
  closeTab: vi.fn(),
  disposeChannel: vi.fn(),
  callbacks: [] as Array<(bytes: Uint8Array) => void>,
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dockerApi: {
      ps: mocks.ps,
      images: mocks.images,
      inspect: vi.fn(),
      stats: vi.fn(),
      containerListDir: vi.fn(),
      action: vi.fn(),
      imageRemove: vi.fn(),
      imagePull: vi.fn(),
      logsAttach: mocks.logsAttach,
      execAttach: vi.fn(),
    },
    terminalApi: { closeTab: mocks.closeTab },
  };
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createBinaryChannel: vi.fn((cb: (bytes: Uint8Array) => void) => {
      mocks.callbacks.push(cb);
      return { id: `ch-${mocks.callbacks.length - 1}` };
    }),
    disposeChannel: mocks.disposeChannel,
    onChannelReopen: () => () => undefined,
  };
});

import {
  MERGED_LOG_LINE_LIMIT,
  MERGED_LOG_MAX_CONTAINERS,
  createMergedLogBuffer,
  parseTimestampedLine,
} from "../../features/docker/mergedLogs";
import { DockerMergedLogs } from "../../features/docker/DockerMergedLogs";
import { DockerPanel } from "../../features/docker/DockerPanel";
import type { ContainerSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";

describe("parseTimestampedLine", () => {
  it("parses a Z timestamp and strips it from the text", () => {
    const parsed = parseTimestampedLine("2026-11-03T07:00:00.123456789Z hello world");
    expect(parsed.text).toBe("hello world");
    expect(parsed.ts).not.toBeNull();
    expect(Math.floor(parsed.ts!)).toBe(Date.parse("2026-11-03T07:00:00.123Z"));
  });

  it("honors a numeric offset", () => {
    const parsed = parseTimestampedLine("2026-11-03T15:00:00.5+08:00 x");
    expect(parsed.ts).toBe(Date.parse("2026-11-03T07:00:00.500Z"));
    expect(parsed.text).toBe("x");
  });

  it("keeps sub-millisecond order", () => {
    const a = parseTimestampedLine("2026-11-03T07:00:00.123100000Z a");
    const b = parseTimestampedLine("2026-11-03T07:00:00.123900000Z b");
    expect(b.ts!).toBeGreaterThan(a.ts!);
  });

  it("returns null for lines without a timestamp and strips a trailing CR", () => {
    expect(parseTimestampedLine("plain line\r")).toEqual({ ts: null, text: "plain line" });
  });
});

describe("createMergedLogBuffer", () => {
  it("orders interleaved sources by timestamp", () => {
    const buffer = createMergedLogBuffer();
    buffer.push(0, ["2026-11-03T07:00:01Z a1", "2026-11-03T07:00:03Z a3"]);
    buffer.push(1, ["2026-11-03T07:00:02Z b2", "2026-11-03T07:00:00Z b0"]);
    expect(buffer.snapshot().map((line) => line.text)).toEqual(["b0", "a1", "b2", "a3"]);
  });

  it("keeps arrival order for equal timestamps", () => {
    const buffer = createMergedLogBuffer();
    buffer.push(0, ["2026-11-03T07:00:00Z first"]);
    buffer.push(1, ["2026-11-03T07:00:00Z second"]);
    buffer.push(0, ["2026-11-03T07:00:00Z third"]);
    expect(buffer.snapshot().map((line) => line.text)).toEqual(["first", "second", "third"]);
  });

  it("lets an untimestamped line follow the previous line of its source", () => {
    const buffer = createMergedLogBuffer();
    buffer.push(0, ["2026-11-03T07:00:01Z a1", "continuation"]);
    buffer.push(1, ["2026-11-03T07:00:02Z b2"]);
    expect(buffer.snapshot().map((line) => line.text)).toEqual(["a1", "continuation", "b2"]);
  });

  it("drops the oldest lines beyond the limit", () => {
    const buffer = createMergedLogBuffer();
    const lines = Array.from({ length: MERGED_LOG_LINE_LIMIT + 10 }, (_, i) =>
      `2026-11-03T07:00:00.${String(i).padStart(6, "0")}Z line${i}`);
    buffer.push(0, lines);
    const snap = buffer.snapshot();
    expect(snap).toHaveLength(MERGED_LOG_LINE_LIMIT);
    expect(snap[0].text).toBe("line10");
    expect(snap.at(-1)!.text).toBe(`line${MERGED_LOG_LINE_LIMIT + 9}`);
  });

  it("caps containers at 8", () => {
    expect(MERGED_LOG_MAX_CONTAINERS).toBe(8);
  });
});

const T = (s: number, text: string) => `2026-11-03T07:00:0${s}.000000000Z ${text}\n`;

function emit(index: number, text: string): void {
  act(() => {
    mocks.callbacks[index](new TextEncoder().encode(text));
  });
}

let mounted: MountedView | undefined;
const containers = [
  { id: "c1", name: "web" },
  { id: "c2", name: "db" },
];

function mountLogs(items = containers, onClose = vi.fn()) {
  mounted = mount(createElement(DockerMergedLogs, { sessionId: "s1", containers: items, onClose }));
  return { view: mounted, onClose };
}

const text = () => document.body.textContent ?? "";

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.callbacks.length = 0;
  useUi.setState({ pushToast: vi.fn(), resyncSessions: vi.fn().mockResolvedValue(undefined), sessions: [] });
  mocks.logsAttach.mockImplementation(async (_s: string, id: string) => `tab-${id}`);
  mocks.closeTab.mockResolvedValue(undefined);
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("DockerMergedLogs", () => {
  it("attaches every container with timestamps and shows prefixed merged lines", async () => {
    mountLogs();
    await flush();
    expect(mocks.logsAttach).toHaveBeenCalledTimes(2);
    expect(mocks.logsAttach).toHaveBeenCalledWith("s1", "c1", expect.any(Number), expect.anything(), { timestamps: true });
    expect(mocks.logsAttach).toHaveBeenCalledWith("s1", "c2", expect.any(Number), expect.anything(), { timestamps: true });
    emit(0, T(1, "alpha") + T(3, "gamma"));
    emit(1, T(2, "beta"));
    await waitForRendered(() => expect(text()).toContain("gamma"));
    const log = document.body.querySelector('[role="log"]')!;
    expect(log.textContent).toContain("web");
    expect(log.textContent).toContain("db");
    const order = ["alpha", "beta", "gamma"].map((w) => log.textContent!.indexOf(w));
    expect(order[0]).toBeLessThan(order[1]);
    expect(order[1]).toBeLessThan(order[2]);
  });

  it("highlights keyword matches with mark", async () => {
    mountLogs();
    await flush();
    emit(0, T(1, "Connection refused"));
    await waitForRendered(() => expect(text()).toContain("Connection refused"));
    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="高亮关键字"]')!;
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(input, "refused");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const marks = document.body.querySelectorAll("mark");
    expect(marks.length).toBeGreaterThan(0);
    expect(marks[0].textContent?.toLowerCase()).toBe("refused");
  });

  it("freezes the display while paused and catches up on resume", async () => {
    mountLogs();
    await flush();
    emit(0, T(1, "before"));
    await waitForRendered(() => expect(text()).toContain("before"));
    const toggle = [...document.body.querySelectorAll("button")].find((b) => b.textContent === "暂停")!;
    click(toggle);
    emit(0, T(2, "during"));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 120));
    });
    expect(text()).toContain("before");
    expect(text()).not.toContain("during");
    click([...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes("继续"))!);
    await waitForRendered(() => expect(text()).toContain("during"));
  });

  it("closes and disposes every stream on unmount", async () => {
    const { view } = mountLogs();
    await flush();
    view.unmount();
    mounted = undefined;
    expect(mocks.closeTab).toHaveBeenCalledWith("tab-c1", "kill", "s1");
    expect(mocks.closeTab).toHaveBeenCalledWith("tab-c2", "kill", "s1");
    expect(mocks.disposeChannel).toHaveBeenCalledTimes(2);
  });

  it("closes a late attach that resolves after unmount", async () => {
    let resolve!: (id: string) => void;
    mocks.logsAttach.mockImplementationOnce(() => new Promise<string>((r) => { resolve = r; }));
    const { view } = mountLogs([containers[0]]);
    view.unmount();
    mounted = undefined;
    expect(mocks.closeTab).not.toHaveBeenCalled();
    await act(async () => { resolve("late"); await Promise.resolve(); });
    await flush();
    expect(mocks.closeTab).toHaveBeenCalledWith("late", "kill", "s1");
    expect(mocks.disposeChannel).toHaveBeenCalledTimes(1);
  });

  it("marks only the failing container and keeps the other", async () => {
    mocks.logsAttach.mockImplementation(async (_s: string, id: string) => {
      if (id === "c1") throw new Error("boom");
      return `tab-${id}`;
    });
    mountLogs();
    await flush();
    emit(1, T(1, "still-here"));
    await waitForRendered(() => expect(text()).toContain("still-here"));
    expect(text()).toContain("web");
    expect(text()).toContain("失败");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("tab-c1", expect.anything(), expect.anything());
  });

  it("closes on Escape", async () => {
    const { onClose } = mountLogs();
    await flush();
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(onClose).toHaveBeenCalled();
  });
});

describe("DockerPanel merged logs entry", () => {
  const mk = (n: number): ContainerSummary => ({
    id: `id${n}0000000000`, name: `ct${n}`, image: "img", state: "running", status: "Up", ports: "", composeProject: null,
  });

  async function mountPanel(count: number) {
    mocks.ps.mockResolvedValue(Array.from({ length: count }, (_, i) => mk(i)));
    mocks.images.mockResolvedValue([]);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(createElement(QueryClientProvider, { client }, createElement(DockerPanel, { sessionId: "s1", visible: true })));
    await waitFor(() => expect(document.body.querySelectorAll('input[aria-label^="选择 ct"]').length).toBe(count));
    for (let i = 0; i < count; i++) {
      click(document.body.querySelector<HTMLInputElement>(`input[aria-label="选择 ct${i}"]`)!);
    }
  }

  const mergeButton = () => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes("合并日志"));

  it("enables the button for two selected containers and opens the view", async () => {
    await mountPanel(2);
    await waitFor(() => expect(mergeButton()).toBeDefined());
    expect(mergeButton()!.disabled).toBe(false);
    click(mergeButton()!);
    await flush();
    expect(mocks.logsAttach).toHaveBeenCalledTimes(2);
  });

  it("disables the button beyond 8 containers", async () => {
    await mountPanel(9);
    await waitFor(() => expect(mergeButton()).toBeDefined());
    expect(mergeButton()!.disabled).toBe(true);
    expect(mergeButton()!.title).toContain("最多 8 个容器");
  });
});
