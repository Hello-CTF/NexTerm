/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import type { LogTailEvent } from "../../ipc/logTailApi";
import {
  click,
  clickButton,
  deferred,
  flush,
  flushUntil,
  mountWithClient,
  setInputValue,
  type MountedView,
} from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  start: vi.fn(),
  cancel: vi.fn(),
  save: vi.fn(),
  createId: vi.fn(),
  assetList: vi.fn(),
  dispose: vi.fn(),
  saveBlob: vi.fn(),
  onEvent: null as null | ((event: Record<string, unknown>) => void),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    call: vi.fn(),
    assetApi: { list: mocks.assetList },
    dbApi: {},
    vaultApi: {},
    sessionApi: { connect: vi.fn(), reconnect: vi.fn(), disconnect: vi.fn(), list: vi.fn().mockResolvedValue([]) },
    terminalApi: {
      listLive: vi.fn().mockResolvedValue([]),
      listDurable: vi.fn().mockResolvedValue([]),
      write: vi.fn().mockResolvedValue(undefined),
      detach: vi.fn().mockResolvedValue(undefined),
      setVisible: vi.fn().mockResolvedValue(undefined),
      closeTab: vi.fn().mockResolvedValue(undefined),
    },
  });
});

vi.mock("../../ipc/logTailApi", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/logTailApi")>();
  return {
    ...actual,
    logTailApi: { start: mocks.start, cancel: mocks.cancel, save: mocks.save },
    createLogTailRunId: mocks.createId,
  };
});

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    createJsonChannel: vi.fn((onEvent: (event: Record<string, unknown>) => void) => {
      mocks.onEvent = onEvent;
      return { id: "channel-log-tail", onmessage: vi.fn(), toJSON: () => "channel-log-tail" };
    }),
    disposeChannel: mocks.dispose,
  };
});

vi.mock("../../ipc/webFiles", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/webFiles")>();
  return { ...actual, saveBrowserBlob: mocks.saveBlob };
});

import { MultiHostLogTail } from "../../features/logs/MultiHostLogTail";
import { MergedLogBuffer, type MergedLogLine } from "../../features/logs/mergedLog";

const sessions: SessionInfo[] = [
  { id: "s1", assetId: null, name: "host-one", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  { id: "s2", assetId: null, name: "host-two", kind: "ssh", status: "connected", tabs: [], createdAt: 0 },
  { id: "s3", assetId: null, name: "offline", kind: "ssh", status: "disconnected", tabs: [], createdAt: 0 },
  { id: "s4", assetId: null, name: "local", kind: "local", status: "connected", tabs: [], createdAt: 0 },
  { id: "s5", assetId: "device/d1", name: "edge-device", kind: "device", status: "connected", tabs: [], createdAt: 0 },
];

let mounted: MountedView | undefined;

function mountPanel(): MountedView {
  return mountWithClient(createElement(MultiHostLogTail, { sessions }));
}

function panel(): MountedView {
  if (!mounted) throw new Error("panel is not mounted");
  return mounted;
}

function openPopover(label: string): void {
  const trigger = panel().container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (trigger?.getAttribute("aria-expanded") === "false") click(trigger);
}

function target(id: string): HTMLInputElement {
  openPopover("选择主机与来源");
  const input = panel().container.querySelector<HTMLInputElement>(`input[value="${id}"]`);
  if (!input) throw new Error(`target ${id} not found`);
  return input;
}

function source(host: string): HTMLInputElement {
  openPopover("选择主机与来源");
  const input = panel().container.querySelector<HTMLInputElement>(`input[aria-label="来源 ${host}"]`);
  if (!input) throw new Error(`source ${host} not found`);
  return input;
}

function clickMore(text: string): void {
  openPopover("更多选项");
  clickButton(panel().container, text);
}

function logText(): string {
  const log = panel().container.querySelector('[role="log"]');
  return log?.textContent ?? "";
}

function emit(event: Omit<LogTailEvent, "runId" | "timeMs"> & { runId?: string }): void {
  act(() => {
    mocks.onEvent?.({ runId: "run-test", timeMs: 1, ...event });
  });
}

async function startTwoSources(): Promise<void> {
  click(target("s1"));
  click(target("s2"));
  setInputValue(source("host-one"), "/var/log/one.log");
  setInputValue(source("host-two"), "/var/log/two.log");
  clickButton(panel().container, "开始跟随");
  await flushUntil(() => mocks.start.mock.calls.length === 1);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ workspaces: [] });
  mocks.createId.mockReturnValue("run-test");
  mocks.start.mockResolvedValue({ runId: "run-test" });
  mocks.cancel.mockResolvedValue({ runId: "run-test", canceled: true });
  mocks.save.mockResolvedValue({ path: "/tmp/merged.log", bytes: 20 });
  mocks.assetList.mockResolvedValue([]);
  mocks.saveBlob.mockResolvedValue(true);
  mocks.onEvent = null;
});

afterEach(() => {
  vi.useRealTimers();
  mounted?.unmount();
  mounted = undefined;
});

describe("MultiHostLogTail", () => {
  it("lists connected managed devices as selectable log sources", () => {
    mounted = mountPanel();
    expect(target("s5").disabled).toBe(false);
    expect(panel().container.textContent).toContain("edge-device");
    expect(panel().container.querySelector('[aria-label*="Windows 设备暂不支持"]')).not.toBeNull();
  });

  it("preselects connected initial targets and drops local or offline ones", () => {
    mounted = mountWithClient(createElement(MultiHostLogTail, { sessions, initialSelected: ["s2", "s3", "s4"] }));
    expect(target("s2").checked).toBe(true);
    expect(target("s1").checked).toBe(false);
    expect(source("host-two")).toBeTruthy();
    expect(panel().container.querySelector('input[aria-label="来源 host-one"]')).toBeNull();
  });

  it("keeps failed hosts visible while merging and prefixing healthy sources", async () => {
    mounted = mountPanel();
    expect(panel().container.querySelector('input[value="s3"]')).toBeNull();
    expect(panel().container.querySelector('input[value="s4"]')).toBeNull();
    await startTwoSources();
    expect(mocks.start.mock.calls[0][0]).toEqual({
      runId: "run-test",
      maxConcurrent: 4,
      sources: [
        { sourceId: "s1", sessionId: "s1", kind: "file", value: "/var/log/one.log" },
        { sourceId: "s2", sessionId: "s2", kind: "file", value: "/var/log/two.log" },
      ],
    });

    emit({ sequence: 2, type: "line", sourceId: "s2", sessionId: "s2", text: "second" });
    emit({ sequence: 1, type: "line", sourceId: "s1", sessionId: "s1", text: "first" });
    emit({ sequence: 3, type: "status", sourceId: "s1", status: "failed", error: "permission denied" });
    await flushUntil(() => logText().includes("first") && logText().includes("second"));

    expect(logText().indexOf("[host-one]first")).toBeLessThan(logText().indexOf("[host-two]second"));
    expect(panel().container.textContent).toContain("permission denied");
    expect(panel().container.textContent).toContain("失败");
  });

  it("pauses only the view, resumes with retained lines, and saves host prefixes", async () => {
    mounted = mountPanel();
    await startTwoSources();
    emit({ sequence: 1, type: "line", sourceId: "s1", text: "before" });
    await flushUntil(() => logText().includes("before"));
    clickButton(panel().container, "暂停显示");
    emit({ sequence: 2, type: "line", sourceId: "s2", text: "buffered" });
    await flushUntil(() => panel().container.textContent?.includes("保留 2 / 5000") === true);
    expect(logText()).not.toContain("buffered");

    clickButton(panel().container, "继续显示");
    await flushUntil(() => logText().includes("buffered"));
    clickMore("保存日志");
    await flush();
    await flushUntil(() => mocks.saveBlob.mock.calls.length === 1);
    await flushUntil(() => panel().container.textContent?.includes("已保存 2 行合并日志") === true);
    const blob = mocks.saveBlob.mock.calls[0][1] as Blob;
    expect(await blob.text()).toBe("[host-one] before\n[host-two] buffered\n");
  });

  it("clears the save confirmation after a few seconds", async () => {
    mounted = mountPanel();
    await startTwoSources();
    emit({ sequence: 1, type: "line", sourceId: "s1", text: "before" });
    await flushUntil(() => logText().includes("before"));
    vi.useFakeTimers();
    clickMore("保存日志");
    await flushUntil(() => panel().container.textContent?.includes("已保存 1 行合并日志") === true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3_600);
    });
    expect(panel().container.textContent).not.toContain("已保存 1 行合并日志");
  });

  it("cancels all sources on unmount even while the start RPC is pending", async () => {
    const pending = deferred<{ runId: string }>();
    mocks.start.mockReturnValue(pending.promise);
    mounted = mountPanel();
    click(target("s1"));
    setInputValue(source("host-one"), "/var/log/one.log");
    clickButton(panel().container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 1);

    mounted.unmount();
    mounted = undefined;
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    expect(mocks.cancel).toHaveBeenCalledWith("run-test");
    await act(async () => {
      pending.resolve({ runId: "run-test" });
      await Promise.resolve();
    });
    await flushUntil(() => mocks.cancel.mock.calls.length === 2);
    expect(mocks.cancel).toHaveBeenNthCalledWith(2, "run-test");
  });

  it("shows the final line and failure before leaving the running state on done", async () => {
    mounted = mountPanel();
    await startTwoSources();
    emit({ sequence: 1, type: "line", sourceId: "s2", text: "final-line" });
    emit({ sequence: 2, type: "status", sourceId: "s1", status: "failed", error: "start failed" });
    emit({ sequence: 3, type: "done" });

    await flushUntil(() => logText().includes("final-line") && panel().container.textContent?.includes("开始跟随") === true);
    expect(panel().container.textContent).toContain("start failed");
    expect(panel().container.textContent).not.toContain("停止全部");
  });

  it("keeps the channel until final events arrive after an explicit stop", async () => {
    mounted = mountPanel();
    await startTwoSources();
    clickButton(panel().container, "停止全部");
    await flush();
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    expect(mocks.dispose).not.toHaveBeenCalled();
    await flushUntil(() => panel().container.textContent?.includes("停止全部") === true);

    emit({ sequence: 1, type: "line", sourceId: "s2", text: "after-stop" });
    emit({ sequence: 2, type: "done" });
    await flushUntil(() => mocks.dispose.mock.calls.length === 1 && panel().container.textContent?.includes("开始跟随") === true);
    expect(logText()).toContain("after-stop");
  });

  it("settles a successful cancel without done at the timeout boundary and allows a new run", async () => {
    vi.useFakeTimers();
    mounted = mountPanel();
    await startTwoSources();
    clickButton(panel().container, "停止全部");
    await flush();
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_999);
    });
    expect(mocks.dispose).not.toHaveBeenCalled();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
    });
    await flushUntil(() => panel().container.textContent?.includes("日志流已停止，但结束状态未确认") === true);
    expect(mocks.dispose).toHaveBeenCalledTimes(1);
    expect(panel().container.textContent).toContain("开始跟随");

    mocks.createId.mockReturnValue("run-next");
    mocks.start.mockResolvedValue({ runId: "run-next" });
    clickButton(panel().container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 2);
    expect(mocks.start.mock.calls[1][0].runId).toBe("run-next");
  });

  it("settles stop even when the start RPC remains pending", async () => {
    vi.useFakeTimers();
    const pending = deferred<{ runId: string }>();
    mocks.start.mockReturnValue(pending.promise);
    mounted = mountPanel();
    click(target("s1"));
    setInputValue(source("host-one"), "/var/log/one.log");
    clickButton(panel().container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 1);
    clickButton(panel().container, "停止全部");
    await flush();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    await flushUntil(() => panel().container.textContent?.includes("日志流已停止，但结束状态未确认") === true);
    expect(mocks.dispose).toHaveBeenCalledTimes(1);

    mocks.createId.mockReturnValue("run-next");
    mocks.start.mockResolvedValue({ runId: "run-next" });
    clickButton(panel().container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 2);
    expect(mocks.start.mock.calls[1][0].runId).toBe("run-next");
  });

  it("keeps an unconfirmed run retryable when cancel rejects after the timeout", async () => {
    vi.useFakeTimers();
    const pendingCancel = deferred<{ runId: string; canceled: boolean }>();
    mocks.cancel.mockReturnValue(pendingCancel.promise);
    mounted = mountPanel();
    await startTwoSources();
    clickButton(panel().container, "停止全部");
    await flush();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    await flushUntil(() => panel().container.textContent?.includes("取消未确认，远端可能仍在运行") === true);
    expect(mocks.dispose).not.toHaveBeenCalled();
    expect(panel().container.textContent).toContain("停止全部");

    await act(async () => {
      pendingCancel.reject(new Error("network lost"));
      await Promise.resolve();
    });
    await flush();
    expect(mocks.dispose).not.toHaveBeenCalled();
    expect(panel().container.textContent).toContain("取消未确认，远端可能仍在运行");
    expect(panel().container.textContent).toContain("停止全部");

    mocks.cancel.mockResolvedValue({ runId: "run-test", canceled: true });
    clickButton(panel().container, "停止全部");
    await flush();
    await flushUntil(() => mocks.cancel.mock.calls.length === 2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(31_000);
    });
    await flushUntil(() => panel().container.textContent?.includes("日志流已停止，但结束状态未确认") === true);
    expect(mocks.dispose).toHaveBeenCalledTimes(1);
    expect(panel().container.textContent).toContain("开始跟随");
  });

  it("does not let an old cancel promise change a new run state", async () => {
    const firstCancel = deferred<{ runId: string; canceled: boolean }>();
    const secondCancel = deferred<{ runId: string; canceled: boolean }>();
    mocks.cancel
      .mockImplementationOnce(() => firstCancel.promise)
      .mockImplementationOnce(() => secondCancel.promise);
    mounted = mountPanel();
    await startTwoSources();
    clickButton(panel().container, "停止全部");
    await flushUntil(() => mocks.cancel.mock.calls.length === 1);
    emit({ sequence: 1, type: "done" });
    await flushUntil(() => panel().container.textContent?.includes("开始跟随") === true);

    mocks.createId.mockReturnValue("run-next");
    mocks.start.mockResolvedValue({ runId: "run-next" });
    clickButton(panel().container, "开始跟随");
    await flushUntil(() => mocks.start.mock.calls.length === 2);
    clickButton(panel().container, "停止全部");
    await flushUntil(() => mocks.cancel.mock.calls.length === 2 && panel().container.textContent?.includes("正在取消") === true);

    await act(async () => {
      firstCancel.reject(new Error("late failure"));
      await Promise.resolve();
    });
    await flush();
    expect(panel().container.textContent).toContain("正在取消");
    expect(panel().container.textContent).not.toContain("late failure");

    await act(async () => {
      secondCancel.resolve({ runId: "run-next", canceled: true });
      await Promise.resolve();
    });
    emit({ runId: "run-next", sequence: 1, type: "done" });
    await flushUntil(() => panel().container.textContent?.includes("开始跟随") === true);
  });
});

describe("MergedLogBuffer sequence contract", () => {
  it("keeps cursors ordered and deduplicated while applying the retention bound", () => {
    const buffer = new MergedLogBuffer(2);
    const line = (sequence: number): MergedLogLine => ({
      sequence,
      sourceId: "source-1",
      sessionId: "session-1",
      host: "host-one",
      text: `line-${sequence}`,
      stderr: false,
      timeMs: sequence,
    });

    buffer.append(line(2));
    buffer.append(line(1));
    buffer.append({ ...line(2), text: "duplicate" });
    buffer.append(line(3));

    expect(buffer.retainedLines().map((entry) => entry.sequence)).toEqual([2, 3]);
    expect(buffer.retainedLines()[0]?.text).toBe("line-2");
    expect(buffer.droppedCount).toBe(1);
  });
});
