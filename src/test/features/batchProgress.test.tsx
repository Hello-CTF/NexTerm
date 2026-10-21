/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import {
  BatchProgressPanel,
  type BatchProgressApi,
  type BatchProgressSnapshot,
  type BatchRerunRequest,
} from "../../features/execbatch/BatchProgress";
import {
  clickButton,
  deferred,
  flush,
  flushUntil,
  mount,
  type MountedView,
} from "./reactTestUtils";

function snapshot(overrides: {
  phase?: string;
  succeeded?: number;
  failed?: number;
  canceled?: number;
  finishedAt?: number;
} = {}): BatchProgressSnapshot {
  const phase = overrides.phase ?? "finished";
  return {
    batch: {
      batchId: "batch-1",
      phase,
      total: 3,
      pending: 0,
      running: phase === "running" ? 1 : 0,
      succeeded: overrides.succeeded ?? 1,
      failed: overrides.failed ?? 1,
      canceled: overrides.canceled ?? 1,
      finishedAt: overrides.finishedAt,
    },
    targets: [
      {
        sessionId: "session-ok",
        position: 0,
        phase: "finished",
        status: "success",
        output: {
          stdout: { head: "all good", tail: "", totalBytes: 8, droppedBytes: 0, truncated: false },
          stderr: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
        },
        startedAt: 1000,
        finishedAt: 1500,
        durationMs: 500,
      },
      {
        sessionId: "session-bad",
        position: 1,
        phase: "finished",
        status: "exit_error",
        exitCode: 2,
        output: {
          stdout: {
            head: "start-of-output",
            tail: "end-of-output",
            totalBytes: 100000,
            droppedBytes: 99970,
            truncated: true,
          },
          stderr: { head: "boom", tail: "", totalBytes: 4, droppedBytes: 0, truncated: false },
        },
        startedAt: 1000,
        finishedAt: 1200,
        durationMs: 200,
      },
      {
        sessionId: "session-stop",
        position: 2,
        phase: "finished",
        status: "canceled",
        error: "context canceled",
        output: {
          stdout: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
          stderr: { head: "", tail: "", totalBytes: 0, droppedBytes: 0, truncated: false },
        },
        startedAt: 1000,
        finishedAt: 1100,
        durationMs: 100,
      },
    ],
  };
}

function fakeApi(overrides: Partial<BatchProgressApi> = {}): BatchProgressApi {
  return {
    progress: vi.fn().mockResolvedValue(snapshot()),
    cancel: vi.fn().mockResolvedValue(undefined),
    rerun: vi.fn().mockResolvedValue({
      batchId: "batch-2",
      command: "deploy",
      startedAt: 1,
      finishedAt: 2,
      results: [{ sessionId: "session-bad", status: "success", stdout: "fixed" }],
    }),
    ...overrides,
  };
}

function progressElement(
  api: BatchProgressApi,
  options: {
    batchId?: string;
    original?: { command: string; timeoutMs?: number; maxConcurrent?: number };
    onConfirmationRequired?: (detail: unknown) => void;
    onRerunStart?: (batchId: string) => void;
    pollIntervalMs?: number;
    newBatchId?: () => string;
  } = {},
) {
  return createElement(BatchProgressPanel, {
    api,
    batchId: options.batchId ?? "batch-1",
    original: options.original ?? { command: "deploy", timeoutMs: 30000, maxConcurrent: 4 },
    onConfirmationRequired: options.onConfirmationRequired,
    onRerunStart: options.onRerunStart,
    pollIntervalMs: options.pollIntervalMs ?? 5,
    newBatchId: options.newBatchId ?? (() => "batch-2"),
  });
}

let mounted: MountedView | null = null;
afterEach(() => {
  mounted?.unmount();
  mounted = null;
});

describe("BatchProgressPanel", () => {
  it("renders per-target status, counts, and bounded head/tail output", async () => {
    const api = fakeApi();
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("session-bad") ?? false);

    const text = mounted.container.textContent ?? "";
    expect(text).toContain("session-ok");
    expect(text).toContain("session-bad");
    expect(text).toContain("session-stop");
    expect(text).toContain("exit_error");
    expect(text).toContain("context canceled");
    expect(text).toContain("start-of-output");
    expect(text).toContain("end-of-output");
    expect(text).toContain("99970");
    expect(text).toContain("exit 2");
  });

  it("polls until the batch finishes and then stops", async () => {
    const api = fakeApi({
      progress: vi
        .fn()
        .mockResolvedValueOnce(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 }))
        .mockResolvedValueOnce(snapshot()),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    expect(api.progress).toHaveBeenCalledTimes(2);
    await flush();
    expect(api.progress).toHaveBeenCalledTimes(2);
  });

  it("does not let a stale poll response overwrite the newer snapshot", async () => {
    const first = deferred<BatchProgressSnapshot>();
    const second = deferred<BatchProgressSnapshot>();
    const api = fakeApi({
      progress: vi
        .fn()
        .mockReturnValueOnce(first.promise)
        .mockReturnValueOnce(second.promise)
        .mockResolvedValue(snapshot()),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => (api.progress as ReturnType<typeof vi.fn>).mock.calls.length === 2);

    await act(async () => {
      second.resolve(snapshot({ succeeded: 3, failed: 0, canceled: 0 }));
    });
    expect(mounted.container.textContent).toContain("成功 3");

    await act(async () => {
      first.resolve(snapshot({ succeeded: 0, failed: 3, canceled: 0 }));
    });
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("成功 3");
    expect(text).not.toContain("失败 3");
  });

  it("cancels the running batch", async () => {
    const api = fakeApi({
      progress: vi.fn().mockResolvedValue(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 })),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("取消执行") ?? false);
    clickButton(mounted.container, "取消执行");
    await flush();
    expect(api.cancel).toHaveBeenCalledWith("batch-1");
  });

  it("reruns failed targets with the snapshot results", async () => {
    const api = fakeApi();
    const onRerunStart = vi.fn();
    mounted = mount(progressElement(api, { onRerunStart }));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flush();
    expect(api.rerun).toHaveBeenCalledTimes(1);

    const request = (api.rerun as ReturnType<typeof vi.fn>).mock.calls[0][0] as BatchRerunRequest;
    expect(request.batchId).toBe("batch-2");
    expect(request.command).toBe("deploy");
    expect(request.timeoutMs).toBe(30000);
    expect(request.maxConcurrent).toBe(4);
    expect(request.results).toEqual([
      { sessionId: "session-ok", status: "success" },
      { sessionId: "session-bad", status: "exit_error" },
      { sessionId: "session-stop", status: "canceled" },
    ]);
    expect(onRerunStart).toHaveBeenCalledWith("batch-2");
    expect(mounted.container.textContent).toContain("fixed");
  });

  it("switches to the rerun batch before the rerun resolves and renders the returned final batch", async () => {
    const rerunCall = deferred<Awaited<ReturnType<BatchProgressApi["rerun"]>>>();
    const api = fakeApi({ rerun: vi.fn(() => rerunCall.promise) });
    const onRerunStart = vi.fn();
    mounted = mount(progressElement(api, { onRerunStart }));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flush();

    // The new batch identity is announced while the rerun is still pending.
    await flushUntil(() => onRerunStart.mock.calls.length === 1);
    expect(onRerunStart).toHaveBeenCalledWith("batch-2");
    // The panel polls the NEW batch id during the pending rerun.
    await flushUntil(() =>
      (api.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-2"),
    );

    await act(async () => {
      rerunCall.resolve({
        batchId: "batch-2",
        command: "deploy",
        startedAt: 1,
        finishedAt: 2,
        results: [{ sessionId: "session-bad", status: "success", stdout: "fixed" }],
      });
    });
    await flushUntil(() => mounted?.container.textContent?.includes("fixed") ?? false);
    expect(mounted.container.textContent).toContain("session-bad");
    expect(mounted.container.textContent).toContain("批次 batch-2 已结束");
  });

  it("stops polling when the batch is not found", async () => {
    const api = fakeApi({
      progress: vi.fn().mockRejectedValue({ code: "not_found", message: "批量执行不存在或已结束" }),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
    expect(mounted.container.querySelector("[role='alert']")?.textContent).toContain("批量执行不存在或已结束");
    await flush();
    expect(api.progress).toHaveBeenCalledTimes(1);
  });

  it("renders a fast-completing batch without further polls", async () => {
    const api = fakeApi();
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    await flush();
    expect(api.progress).toHaveBeenCalledTimes(1);
    expect(mounted.container.textContent).toContain("session-bad");
  });

  it("surfaces a production re-confirmation challenge instead of failing silently", async () => {
    const detail = { kind: "exec_batch_production_confirmation", batchId: "batch-2" };
    const api = fakeApi({
      rerun: vi.fn().mockRejectedValue({ code: "needs_confirm", message: "需要确认", detail }),
    });
    const onConfirmationRequired = vi.fn();
    mounted = mount(progressElement(api, { onConfirmationRequired }));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flush();
    expect(onConfirmationRequired).toHaveBeenCalledWith(detail);
    expect(mounted.container.querySelector("[role='alert']")?.textContent).toContain("需要确认");
  });

  it("hides rerun when nothing failed and while still running", async () => {
    const api = fakeApi({
      progress: vi.fn().mockResolvedValue(snapshot({ succeeded: 3, failed: 0, canceled: 0 })),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("session-ok") ?? false);
    expect(mounted.container.textContent).not.toContain("重跑失败目标");

    mounted.unmount();
    const running = fakeApi({
      progress: vi.fn().mockResolvedValue(snapshot({ phase: "running", succeeded: 0, failed: 1, canceled: 0 })),
    });
    mounted = mount(progressElement(running));
    await flushUntil(() => mounted?.container.textContent?.includes("取消执行") ?? false);
    expect(mounted.container.textContent).not.toContain("重跑失败目标");
  });
});
