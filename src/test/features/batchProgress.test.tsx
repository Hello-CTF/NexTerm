/** @vitest-environment jsdom */

import { afterEach, describe, expect, it, vi } from "vitest";
import { act, createElement, StrictMode, useLayoutEffect, type ReactNode } from "react";
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
    startupGraceMs?: number;
    ref?: { current: import("../../features/execbatch/BatchProgress").BatchProgressHandle | null };
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
    startupGraceMs: options.startupGraceMs,
    ref: options.ref,
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

  it("keeps polling through the rerun startup not_found window", async () => {
    const rerunCall = deferred<Awaited<ReturnType<BatchProgressApi["rerun"]>>>();
    let batch2Polls = 0;
    const api = fakeApi({
      progress: vi.fn((batchId: string) => {
        if (batchId === "batch-2") {
          batch2Polls += 1;
          if (batch2Polls === 1) {
            return Promise.reject({ code: "not_found", message: "批量执行不存在或已结束" });
          }
          return Promise.resolve(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 }));
        }
        return Promise.resolve(snapshot());
      }),
      rerun: vi.fn(() => rerunCall.promise),
    });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");

    // The first batch-2 poll sees the startup not_found; polling must
    // continue and render the running snapshot once the tracker publishes.
    await flushUntil(() => batch2Polls >= 2);
    await flushUntil(() => mounted?.container.textContent?.includes("取消执行") ?? false);

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
  });

  it("does not render a late final batch after a batch switch", async () => {
    const rerunCall = deferred<Awaited<ReturnType<BatchProgressApi["rerun"]>>>();
    const api = fakeApi({ rerun: vi.fn(() => rerunCall.promise) });
    mounted = mount(progressElement(api));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flush();
    await flushUntil(() =>
      (api.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-2"),
    );

    await act(async () => {
      mounted!.root.render(progressElement(api, { batchId: "batch-3" }));
    });
    await flushUntil(() =>
      (api.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-3"),
    );

    await act(async () => {
      rerunCall.resolve({
        batchId: "batch-2",
        command: "deploy",
        startedAt: 1,
        finishedAt: 2,
        results: [{ sessionId: "old-target", status: "success", stdout: "stale-rerun-output" }],
      });
    });
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("批次 batch-2 已结束");
    expect(text).not.toContain("stale-rerun-output");
    expect(text).not.toContain("old-target");
    await flush();
  });

  it("does not render a late final batch after an api switch", async () => {
    const rerunCall = deferred<Awaited<ReturnType<BatchProgressApi["rerun"]>>>();
    const apiA = fakeApi({ rerun: vi.fn(() => rerunCall.promise) });
    const apiB = fakeApi();
    mounted = mount(progressElement(apiA));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flush();
    await flushUntil(() =>
      (apiA.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-2"),
    );

    await act(async () => {
      mounted!.root.render(progressElement(apiB, { batchId: "batch-2" }));
    });
    await flushUntil(() => (apiB.progress as ReturnType<typeof vi.fn>).mock.calls.length > 0);

    await act(async () => {
      rerunCall.resolve({
        batchId: "batch-2",
        command: "deploy",
        startedAt: 1,
        finishedAt: 2,
        results: [{ sessionId: "old-target", status: "success", stdout: "stale-rerun-output" }],
      });
    });
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("批次 batch-2 已结束");
    expect(text).not.toContain("stale-rerun-output");
    await flush();
  });

  it("keeps polling through the ordinary execute startup not_found window", async () => {
    let calls = 0;
    const api = fakeApi({
      progress: vi.fn(() => {
        calls += 1;
        if (calls === 1) {
          return Promise.reject({ code: "not_found", message: "批量执行不存在或已结束" });
        }
        return Promise.resolve(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 }));
      }),
    });
    mounted = mount(progressElement(api, { startupGraceMs: 60_000 }));
    // The first poll sees the pre-tracker not_found; the startup grace keeps
    // polling and renders the running snapshot once the tracker publishes.
    await flushUntil(() => mounted?.container.textContent?.includes("取消执行") ?? false);
    expect(calls).toBeGreaterThanOrEqual(2);
  });

  it("stops polling an unknown batch once the startup grace expires", async () => {
    vi.useFakeTimers();
    try {
      const api = fakeApi({
        progress: vi.fn().mockRejectedValue({ code: "not_found", message: "批量执行不存在或已结束" }),
      });
      mounted = mount(progressElement(api, { startupGraceMs: 100, pollIntervalMs: 5 }));
      // Within the grace the not_found is temporary and polling continues.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10);
      });
      const callsWithinGrace = (api.progress as ReturnType<typeof vi.fn>).mock.calls.length;
      expect(callsWithinGrace).toBeGreaterThanOrEqual(2);
      // Past the grace the next not_found is terminal and polling stops.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(120);
      });
      const callsAfterGrace = (api.progress as ReturnType<typeof vi.fn>).mock.calls.length;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30);
      });
      expect((api.progress as ReturnType<typeof vi.fn>).mock.calls.length).toBe(callsAfterGrace);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops immediately when an observed batch later returns not_found", async () => {
    const api = fakeApi({
      progress: vi
        .fn()
        .mockResolvedValueOnce(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 }))
        .mockRejectedValueOnce({ code: "not_found", message: "批量执行不存在或已结束" }),
    });
    mounted = mount(progressElement(api, { startupGraceMs: 60_000 }));
    await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
    const calls = (api.progress as ReturnType<typeof vi.fn>).mock.calls.length;
    await flush();
    expect((api.progress as ReturnType<typeof vi.fn>).mock.calls.length).toBe(calls);
  });

  it("restarts polling when a confirmed retry arrives after the startup grace expired", async () => {
    vi.useFakeTimers();
    try {
      const handleRef: { current: import("../../features/execbatch/BatchProgress").BatchProgressHandle | null } = {
        current: null,
      };
      const api = fakeApi({
        progress: vi
          .fn()
          .mockRejectedValueOnce({ code: "not_found", message: "批量执行不存在或已结束" })
          .mockResolvedValue(snapshot({ phase: "running", succeeded: 0, failed: 0, canceled: 0 })),
        rerun: vi.fn().mockResolvedValue({
          batchId: "batch-2",
          command: "deploy",
          startedAt: 1,
          finishedAt: 2,
          results: [{ sessionId: "session-bad", status: "success", stdout: "confirmed-final" }],
        }),
      });
      mounted = mount(progressElement(api, { startupGraceMs: 0, ref: handleRef }));
      // Zero grace: the first not_found stops the poll for the old attempt.
      await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
      const callsBefore = (api.progress as ReturnType<typeof vi.fn>).mock.calls.length;

      // A confirmed retry arrives later through the handle, same batch ID:
      // polling restarts, the running snapshot shows, and the final batch
      // from the confirmed attempt is preserved.
      await act(async () => {
        handleRef.current?.rerun({
          batchId: "batch-2",
          command: "deploy",
          results: [{ sessionId: "session-bad", status: "exit_error" }],
        });
      });
      await flushUntil(() => mounted?.container.textContent?.includes("取消执行") ?? false);
      expect((api.progress as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(callsBefore);
      await flushUntil(() => mounted?.container.textContent?.includes("confirmed-final") ?? false);
      expect(mounted.container.textContent).toContain("批次 batch-2 已结束");
    } finally {
      vi.useRealTimers();
    }
  });

  it("hides a rendered final batch after an api switch", async () => {
    const apiA = fakeApi();
    const apiB = fakeApi();
    mounted = mount(progressElement(apiA));
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flushUntil(() => mounted?.container.textContent?.includes("fixed") ?? false);
    expect(mounted.container.textContent).toContain("批次 batch-2 已结束");

    await act(async () => {
      mounted!.root.render(progressElement(apiB, { batchId: "batch-2" }));
    });
    await flushUntil(() => (apiB.progress as ReturnType<typeof vi.fn>).mock.calls.length > 0);
    const text = mounted.container.textContent ?? "";
    expect(text).not.toContain("批次 batch-2 已结束");
    expect(text).not.toContain("fixed");
  });

  it("hides a rendered final batch from the first layout after an external batch switch", async () => {
    const api = fakeApi();
    const layouts: string[] = [];
    function LayoutProbe({ children }: { children: ReactNode }) {
      useLayoutEffect(() => {
        layouts.push(document.querySelector("[data-batch-progress-probe]")?.textContent ?? "");
      });
      return <div data-batch-progress-probe="">{children}</div>;
    }
    mounted = mount(
      createElement(LayoutProbe, null, progressElement(api)),
    );
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flushUntil(() => mounted?.container.textContent?.includes("fixed") ?? false);
    expect(mounted.container.textContent).toContain("批次 batch-2 已结束");

    // An external switch to batch-3 must hide the old final synchronously:
    // the first committed frame after the prop change already excludes it,
    // captured from a layout effect before any passive effect clears state.
    const beforeSwitch = layouts.length;
    await act(async () => {
      mounted!.root.render(
        createElement(LayoutProbe, null, progressElement(api, { batchId: "batch-3" })),
      );
    });
    const firstLayout = layouts[beforeSwitch] ?? "";
    expect(firstLayout).not.toContain("批次 batch-2 已结束");
    expect(firstLayout).not.toContain("fixed");
    await flushUntil(() =>
      (api.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-3"),
    );
    const settled = mounted.container.textContent ?? "";
    expect(settled).not.toContain("批次 batch-2 已结束");
    expect(settled).not.toContain("fixed");
  });

  it("hides a rendered final batch from the first layout after an external batch switch in StrictMode", async () => {
    const api = fakeApi();
    const layouts: string[] = [];
    function LayoutProbe({ children }: { children: ReactNode }) {
      useLayoutEffect(() => {
        layouts.push(document.querySelector("[data-batch-progress-probe]")?.textContent ?? "");
      });
      return <div data-batch-progress-probe="">{children}</div>;
    }
    mounted = mount(
      createElement(StrictMode, null, createElement(LayoutProbe, null, progressElement(api))),
    );
    await flushUntil(() => mounted?.container.textContent?.includes("重跑失败目标") ?? false);
    clickButton(mounted.container, "重跑失败目标");
    await flushUntil(() => mounted?.container.textContent?.includes("fixed") ?? false);
    expect(mounted.container.textContent).toContain("批次 batch-2 已结束");

    // StrictMode double-renders the switch commit; both renders must detect
    // the external transition and hide the old final from the first frame.
    const beforeSwitch = layouts.length;
    await act(async () => {
      mounted!.root.render(
        createElement(StrictMode, null, createElement(LayoutProbe, null, progressElement(api, { batchId: "batch-3" }))),
      );
    });
    const firstLayout = layouts[beforeSwitch] ?? "";
    expect(firstLayout).not.toContain("批次 batch-2 已结束");
    expect(firstLayout).not.toContain("fixed");
    await flushUntil(() =>
      (api.progress as ReturnType<typeof vi.fn>).mock.calls.some((call) => call[0] === "batch-3"),
    );
    const settled = mounted.container.textContent ?? "";
    expect(settled).not.toContain("批次 batch-2 已结束");
    expect(settled).not.toContain("fixed");
  });

  it("stops polling when the batch is not found", async () => {
    vi.useFakeTimers();
    try {
      const api = fakeApi({
        progress: vi.fn().mockRejectedValue({ code: "not_found", message: "批量执行不存在或已结束" }),
      });
      mounted = mount(progressElement(api, { startupGraceMs: 0 }));
      // Zero grace stops at the first response, not on a scheduling race.
      await flushUntil(() => mounted?.container.querySelector("[role='alert']") !== null);
      expect(mounted.container.querySelector("[role='alert']")?.textContent).toContain("批量执行不存在或已结束");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30);
      });
      expect(api.progress).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
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
    expect(onConfirmationRequired).toHaveBeenCalledTimes(1);
    expect(onConfirmationRequired.mock.calls[0][0]).toEqual(detail);
    expect(onConfirmationRequired.mock.calls[0][1]).toEqual(
      expect.objectContaining({ batchId: "batch-2", command: "deploy" }),
    );
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
