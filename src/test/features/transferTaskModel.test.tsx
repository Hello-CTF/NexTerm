/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  upload: vi.fn(),
  download: vi.fn(),
  write: vi.fn(),
  cancel: vi.fn(),
  finishSave: vi.fn(),
  discardStaged: vi.fn(),
  pickLocalFile: vi.fn(),
  pickSavePath: vi.fn(),
  isStagedPath: vi.fn(),
  listenEvent: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async () => {
  const { createFakeApi } = await import("../helpers/createFakeApi");
  return createFakeApi({
    fsApi: {
      upload: mocks.upload,
      download: mocks.download,
      write: mocks.write,
      cancel: mocks.cancel,
    },
  });
});

vi.mock("../../features/files/chunkedUpload", () => ({
  fileChunkSource: (file: File) => ({ size: file.size }),
  uploadChunked: mocks.write,
}));

vi.mock("../../ipc/events", () => ({
  EVENTS: { fsProgress: "fs://progress" },
  listenEvent: mocks.listenEvent,
}));

vi.mock("../../ui/dialogs", () => ({
  finishSave: mocks.finishSave,
  discardStaged: mocks.discardStaged,
  pickLocalFile: mocks.pickLocalFile,
  pickSavePath: mocks.pickSavePath,
  describeTarget: (path: string, name: string) => path || name,
}));

vi.mock("../../ipc/webFiles", () => ({
  isStagedPath: mocks.isStagedPath,
}));

import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import { TransferPanel } from "../../features/files/TransferPanel";
import {
  clearFinishedTransfers as clearFinishedTransfersAction,
  enqueueDownload as enqueueDownloadAction,
  enqueueUploads as enqueueUploadsAction,
  resetTransferQueue as resetTransferQueueAction,
  useTransferStore,
} from "../../features/files/transferStore";

function actSync<T>(callback: () => T): T {
  let result!: T;
  act(() => {
    result = callback();
  });
  return result;
}

function enqueueUploads(...args: Parameters<typeof enqueueUploadsAction>): string {
  return actSync(() => enqueueUploadsAction(...args));
}

function enqueueDownload(...args: Parameters<typeof enqueueDownloadAction>): string {
  return actSync(() => enqueueDownloadAction(...args));
}

function resetTransferQueue(): void {
  actSync(() => resetTransferQueueAction());
}

function clearFinishedTransfers(...args: Parameters<typeof clearFinishedTransfersAction>): void {
  actSync(() => clearFinishedTransfersAction(...args));
}

let progressHandler: ((event: { taskId: string; transferred: number; total: number; done: boolean; error?: string }) => void) | undefined;
let mounted: MountedView | undefined;

function session(index: number): SessionInfo {
  return {
    id: `s${index}`,
    assetId: `asset-${index}`,
    name: `host-${index}`,
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: 0,
  };
}

function emitProgress(taskId: string, transferred: number, total: number, done = false): void {
  act(() => {
    progressHandler?.({ taskId, transferred, total, done, ...(done ? { error: "stopped" } : {}) });
  });
}

function clickResume(taskId: string): void {
  act(() => {
    useTransferStore.setState((state) => ({
      tasks: state.tasks.map((task) =>
        task.id === taskId && task.direction === "upload" ? { ...task, resume: true } : task,
      ),
    }));
  });
  click(button(mounted!.container, "续传这个传输任务"));
}

function mountPanel(): void {
  mounted = mount(createElement(TransferPanel, { sessionId: "s1" }));
}

function button(container: HTMLElement, title: string): HTMLButtonElement {
  const result = [...container.querySelectorAll("button")].find((candidate) => candidate.title === title);
  if (!result) throw new Error(`button not found: ${title}`);
  return result as HTMLButtonElement;
}

function hasButton(container: HTMLElement, title: string): boolean {
  return [...container.querySelectorAll("button")].some((candidate) => candidate.title === title);
}

beforeEach(() => {
  resetTransferQueue();
  vi.resetAllMocks();
  mounted = undefined;
  mocks.finishSave.mockImplementation(async (path: string) => path);
  mocks.discardStaged.mockResolvedValue(undefined);
  mocks.pickLocalFile.mockResolvedValue("/staged/new-source");
  mocks.pickSavePath.mockImplementation(async (name: string) => `/staged/new-${name}`);
  mocks.isStagedPath.mockReturnValue(true);
  mocks.write.mockResolvedValue(undefined);
  mocks.cancel.mockResolvedValue(true);
  mocks.listenEvent.mockImplementation(
    async (_topic: string, handler: (event: { taskId: string; transferred: number; total: number; done: boolean }) => void) => {
      progressHandler = handler;
      return () => {};
    },
  );
  useUi.setState({
    sessions: Array.from({ length: 6 }, (_, index) => session(index + 1)),
    workspaces: [],
    pushToast: mocks.toast,
  });
  useTransferStore.setState({ tasks: [] });
});

afterEach(async () => {
  mounted?.unmount();
  mounted = undefined;
  vi.useRealTimers();
  await flush();
  resetTransferQueue();
});

describe("transfer task scheduling and progress", () => {
  it("runs two hosts concurrently, keeps their progress separate, and serializes the same host", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    const third = deferred<number>();
    mocks.upload
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
      .mockReturnValueOnce(third.promise);

    enqueueUploads("s1", [{ localPath: "/local/a1", remotePath: "/remote/a1" }]);
    enqueueUploads("s1", [{ localPath: "/local/a2", remotePath: "/remote/a2" }]);
    enqueueUploads("s2", [{ localPath: "/local/b1", remotePath: "/remote/b1" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));

    const tasks = useTransferStore.getState().tasks;
    expect(tasks.map((task) => task.status)).toEqual(["running", "queued", "running"]);
    emitProgress(tasks[0].id, 25, 100);
    emitProgress(tasks[2].id, 60, 200);
    expect(useTransferStore.getState().tasks.map((task) => [task.transferred, task.total])).toEqual([
      [25, 100],
      [0, 0],
      [60, 200],
    ]);

    first.resolve(100);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(3));
    expect(useTransferStore.getState().tasks[1].status).toBe("running");
    second.resolve(200);
    third.resolve(100);
    await waitFor(() =>
      expect(useTransferStore.getState().tasks.every((task) => task.status === "done")).toBe(true),
    );
  });

  it("caps global running transfers at four and admits queued work as slots settle", async () => {
    const pending = Array.from({ length: 6 }, () => deferred<number>());
    for (const item of pending) mocks.upload.mockReturnValueOnce(item.promise);
    for (let index = 0; index < 6; index += 1) {
      enqueueUploads(`s${index + 1}`, [
        { localPath: `/local/${index}`, remotePath: `/remote/${index}` },
      ]);
    }
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(4));
    expect(useTransferStore.getState().tasks.map((task) => task.status)).toEqual([
      "running",
      "running",
      "running",
      "running",
      "queued",
      "queued",
    ]);

    pending[0].resolve(100);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(5));
    pending[1].resolve(100);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(6));
    for (const item of pending.slice(2)) item.resolve(100);
    await waitFor(() =>
      expect(useTransferStore.getState().tasks.every((task) => task.status === "done")).toBe(true),
    );
  });

  it("cancels a running task by id without releasing its host slot before the IPC settles", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/a1", remotePath: "/remote/a1" }]);
    enqueueUploads("s1", [{ localPath: "/local/a2", remotePath: "/remote/a2" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];

    click(button(mounted!.container, "取消这个传输任务"));
    await waitFor(() => expect(mocks.cancel).toHaveBeenCalledWith(task.id, task.attemptId));
    expect(useTransferStore.getState().tasks[0].cancelling).toBe(true);
    expect(mocks.upload).toHaveBeenCalledTimes(1);

    first.reject({ code: "cancelled", message: "文件传输已取消" });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("cancelled"));
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    second.resolve(100);
    await waitFor(() => expect(useTransferStore.getState().tasks[1].status).toBe("done"));
  });

  it("does not accept a false cancel result or disguise a later real failure", async () => {
    const pending = deferred<number>();
    mocks.upload.mockReturnValueOnce(pending.promise);
    mocks.cancel.mockResolvedValueOnce(false);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/false-cancel", remotePath: "/remote/false-cancel" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));

    click(button(mounted!.container, "取消这个传输任务"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].error).toContain("未生效"));
    expect(useTransferStore.getState().tasks[0].status).toBe("running");
    expect(useTransferStore.getState().tasks[0].cancelling).toBe(false);
    pending.reject(new Error("real failure"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(useTransferStore.getState().tasks[0].error).toBe("real failure");
  });

  it("shows a persistent result when an accepted cancellation does not settle", async () => {
    const pending = deferred<number>();
    mocks.upload.mockReturnValueOnce(pending.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/stuck-cancel", remotePath: "/remote/stuck-cancel" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    vi.useFakeTimers();
    click(button(mounted!.container, "取消这个传输任务"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(useTransferStore.getState().tasks[0].cancelAccepted).toBe(true);
    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(useTransferStore.getState().tasks[0].status).toBe("running");
    expect(useTransferStore.getState().tasks[0].cancelling).toBe(false);
    expect(useTransferStore.getState().tasks[0].error).toContain("尚未停止");
    expect(mounted!.container.textContent).toContain("尚未停止");
    vi.useRealTimers();
    pending.reject({ code: "cancelled", message: "文件传输已取消" });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("cancelled"));
  });

  it("waits for cancel acceptance when the transfer cancelled response arrives first", async () => {
    const transfer = deferred<number>();
    const cancel = deferred<boolean>();
    mocks.upload.mockReturnValueOnce(transfer.promise);
    mocks.cancel.mockReturnValueOnce(cancel.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/reordered", remotePath: "/remote/reordered" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    click(button(mounted!.container, "取消这个传输任务"));

    transfer.reject({ code: "cancelled", message: "文件传输已取消" });
    await flush();
    expect(useTransferStore.getState().tasks[0].status).toBe("running");
    cancel.resolve(true);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("cancelled"));
  });

  it("settles an unconfirmed cancellation after 10 seconds, releases the slot, and ignores late acceptance", async () => {
    const firstTransfer = deferred<number>();
    const secondTransfer = deferred<number>();
    const cancel = deferred<boolean>();
    mocks.upload.mockReturnValueOnce(firstTransfer.promise).mockReturnValueOnce(secondTransfer.promise);
    mocks.cancel.mockReturnValueOnce(cancel.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/hung-cancel", remotePath: "/remote/hung-cancel" }]);
    enqueueUploads("s1", [{ localPath: "/local/next", remotePath: "/remote/next" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    vi.useFakeTimers();
    click(button(mounted!.container, "取消这个传输任务"));
    firstTransfer.reject({ code: "cancelled", message: "文件传输已取消" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(useTransferStore.getState().tasks[0].status).toBe("running");

    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    vi.useRealTimers();
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(useTransferStore.getState().tasks[0].error).toContain("取消结果未确认");
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));

    cancel.resolve(true);
    await flush();
    expect(useTransferStore.getState().tasks[0].status).toBe("failed");
    expect(useTransferStore.getState().tasks[0].error).toContain("取消结果未确认");
    expect(useTransferStore.getState().tasks[1].status).toBe("running");
    secondTransfer.resolve(100);
    await waitFor(() => expect(useTransferStore.getState().tasks[1].status).toBe("done"));
  });

  for (const lateOutcome of ["resolve_true", "resolve_false", "reject"] as const) {
    it(`keeps the first unconfirmed outcome when ${lateOutcome} arrives after timeout but before the transfer ends`, async () => {
      const firstTransfer = deferred<number>();
      const secondTransfer = deferred<number>();
      const cancel = deferred<boolean>();
      mocks.upload.mockReturnValueOnce(firstTransfer.promise).mockReturnValueOnce(secondTransfer.promise);
      mocks.cancel.mockReturnValueOnce(cancel.promise);
      mountPanel();
      enqueueUploads("s1", [{ localPath: "/local/timeout-first", remotePath: "/remote/timeout-first" }]);
      enqueueUploads("s1", [{ localPath: "/local/after-timeout", remotePath: "/remote/after-timeout" }]);
      await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
      vi.useFakeTimers();
      click(button(mounted!.container, "取消这个传输任务"));
      act(() => {
        vi.advanceTimersByTime(10_000);
      });
      const timeoutError = useTransferStore.getState().tasks[0].error;
      expect(timeoutError).toContain("尚未得到后端确认");
      expect(useTransferStore.getState().tasks[0].status).toBe("running");
      expect(mocks.upload).toHaveBeenCalledTimes(1);

      if (lateOutcome === "reject") {
        cancel.reject(new Error("late rejection"));
      } else {
        cancel.resolve(lateOutcome === "resolve_true");
      }
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(useTransferStore.getState().tasks[0].error).toBe(timeoutError);
      expect(useTransferStore.getState().tasks[0].cancelAccepted).not.toBe(true);
      expect(mocks.upload).toHaveBeenCalledTimes(1);

      vi.useRealTimers();
      firstTransfer.reject({ code: "cancelled", message: "文件传输已取消" });
      await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
      expect(useTransferStore.getState().tasks[0].error).toContain("取消结果未确认");
      await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
      secondTransfer.resolve(100);
      await waitFor(() => expect(useTransferStore.getState().tasks[1].status).toBe("done"));
    });
  }

  it("reset wakes a pending cancellation wait and isolates the next task from late acceptance", async () => {
    const firstTransfer = deferred<number>();
    const secondTransfer = deferred<number>();
    const cancel = deferred<boolean>();
    mocks.upload.mockReturnValueOnce(firstTransfer.promise).mockReturnValueOnce(secondTransfer.promise);
    mocks.cancel.mockReturnValueOnce(cancel.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/reset-cancel", remotePath: "/remote/reset-cancel" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    click(button(mounted!.container, "取消这个传输任务"));
    firstTransfer.reject({ code: "cancelled", message: "文件传输已取消" });
    await flush();
    expect(useTransferStore.getState().tasks[0].status).toBe("running");

    resetTransferQueue();
    enqueueUploads("s1", [{ localPath: "/local/after-reset", remotePath: "/remote/after-reset" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    cancel.resolve(true);
    await flush();
    expect(useTransferStore.getState().tasks).toHaveLength(1);
    expect(useTransferStore.getState().tasks[0].remotePath).toBe("/remote/after-reset");
    expect(useTransferStore.getState().tasks[0].status).toBe("running");
    expect(useTransferStore.getState().tasks[0].error).toBeUndefined();
    secondTransfer.resolve(100);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("does not let a late cancel rejection replace a real transfer failure", async () => {
    const transfer = deferred<number>();
    const cancel = deferred<boolean>();
    mocks.upload.mockReturnValueOnce(transfer.promise);
    mocks.cancel.mockReturnValueOnce(cancel.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/local/real-error", remotePath: "/remote/real-error" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    click(button(mounted!.container, "取消这个传输任务"));

    transfer.reject(new Error("real failure"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(useTransferStore.getState().tasks[0].error).toBe("real failure");
    cancel.reject(new Error("late cancel rejection"));
    await flush();
    expect(useTransferStore.getState().tasks[0].error).toBe("real failure");
  });

  it("ignores a stale cancel rejection and preserves the new attempt timeout", async () => {
    const firstTransfer = deferred<number>();
    const secondTransfer = deferred<number>();
    const firstCancel = deferred<boolean>();
    const secondCancel = deferred<boolean>();
    mocks.upload.mockReturnValueOnce(firstTransfer.promise).mockReturnValueOnce(secondTransfer.promise);
    mocks.cancel.mockReturnValueOnce(firstCancel.promise).mockReturnValueOnce(secondCancel.promise);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/stale-cancel", remotePath: "/remote/stale-cancel" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    click(button(mounted!.container, "取消这个传输任务"));
    emitProgress(task.id, 30, 100, true);
    firstTransfer.reject(new Error("first failure"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

    clickResume(task.id);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    vi.useFakeTimers();
    click(button(mounted!.container, "取消这个传输任务"));
    secondCancel.resolve(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    firstCancel.reject(new Error("stale cancel rejection"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(useTransferStore.getState().tasks[0].cancelling).toBe(true);
    expect(useTransferStore.getState().tasks[0].error).toBeUndefined();
    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(useTransferStore.getState().tasks[0].error).toContain("尚未停止");
    vi.useRealTimers();
    secondTransfer.reject({ code: "cancelled", message: "文件传输已取消" });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("cancelled"));
  });

  it("reset discards queued paths immediately but an active download only after it settles", async () => {
    const active = deferred<number>();
    mocks.download.mockReturnValueOnce(active.promise);
    enqueueDownload("s1", { remotePath: "/remote/old", localPath: "/staged/old" });
    enqueueUploads("s1", [{ localPath: "/staged/queued", remotePath: "/remote/queued" }]);
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));

    resetTransferQueue();
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/queued");
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/old");
    active.resolve(100);
    await waitFor(() => expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/old"));
    expect(mocks.finishSave).not.toHaveBeenCalled();
  });
});

describe("transfer resume eligibility", () => {
  it("resumes a download with the same task id only while its staged partial remains available", async () => {
    const first = deferred<number>();
    mocks.download.mockReturnValueOnce(first.promise).mockResolvedValueOnce(100);
    mountPanel();
    enqueueDownload("s1", { remotePath: "/remote/file", localPath: "/staged/file" });
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 40, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    await waitFor(() => expect(button(mounted!.container, "续传这个传输任务")).toBeTruthy());
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/file");

    clickResume(task.id);
    await waitFor(() =>
      expect(mocks.download).toHaveBeenNthCalledWith(2, "s1", "/remote/file", "/staged/file", true, task.id, expect.any(String)),
    );
    expect(mocks.download.mock.calls[1][5]).not.toBe(task.attemptId);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
    expect(mocks.finishSave).toHaveBeenCalledWith("/staged/file", "file");
  });

  it("offers a full restart instead of resume after an atomic upload failure", async () => {
    const first = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockResolvedValueOnce(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/atomic", remotePath: "/remote/atomic" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 30, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(false);
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(true);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/atomic");

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(2, "s1", "/staged/atomic", "/remote/atomic", false, task.id, expect.any(String)),
    );
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("offers a full restart after an empty file commit failure", async () => {
    mocks.upload.mockRejectedValueOnce(new Error("rename failed")).mockResolvedValueOnce(0);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/empty", remotePath: "/remote/empty" }]);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    const task = useTransferStore.getState().tasks[0];
    expect(task.transferred).toBe(0);
    expect(task.total).toBe(0);
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(false);
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(true);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/empty");

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(2, "s1", "/staged/empty", "/remote/empty", false, task.id, expect.any(String)),
    );
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("offers a full restart after a rename failure at 100 percent", async () => {
    const first = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockResolvedValueOnce(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/complete", remotePath: "/remote/complete" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 100, 100, true);
    first.reject(new Error("rename failed"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(true);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/complete");

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(2, "s1", "/staged/complete", "/remote/complete", false, task.id, expect.any(String)),
    );
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("offers a full restart after cancellation at 100 percent", async () => {
    const first = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockResolvedValueOnce(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/cancel-complete", remotePath: "/remote/cancel-complete" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    click(button(mounted!.container, "取消这个传输任务"));
    emitProgress(task.id, 100, 100, true);
    first.reject({ code: "cancelled", message: "cancelled" });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("cancelled"));
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(true);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/cancel-complete");

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(2, "s1", "/staged/cancel-complete", "/remote/cancel-complete", false, task.id, expect.any(String)),
    );
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("resumes a path upload but hides resume after its staged source is released", async () => {
    const first = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockResolvedValueOnce(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/source", remotePath: "/remote/file" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 30, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    clickResume(task.id);
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(2, "s1", "/staged/source", "/remote/file", true, task.id, expect.any(String)),
    );
    expect(mocks.upload.mock.calls[1][5]).not.toBe(task.attemptId);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));

    const rejected = deferred<number>();
    mocks.upload.mockReturnValueOnce(rejected.promise);
    enqueueUploads("s1", [{ localPath: "/staged/released", remotePath: "/remote/released" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(3));
    const releasedId = useTransferStore.getState().tasks[1].id;
    mocks.isStagedPath.mockReturnValue(false);
    emitProgress(releasedId, 20, 100, true);
    rejected.reject(new Error("source released"));
    await waitFor(() => expect(useTransferStore.getState().tasks[1].status).toBe("failed"));
    expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')?.textContent).not.toContain("续传");
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/released");
  });

  it("turns an oversized resume target into a full restart with a new attempt", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/mismatch", remotePath: "/remote/mismatch" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 25, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

    clickResume(task.id);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    const resumeAttempt = useTransferStore.getState().tasks[0].attemptId;
    second.reject({
      code: "conflict",
      message: "remote resume target is larger than source (14 > 3)",
      detail: { resource: "partial", reason: "resume_mismatch" },
    });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].resumeIssue).toBe("partial_mismatch"));
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(false);
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(true);
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/mismatch");

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(
        3,
        "s1",
        "/staged/mismatch",
        "/remote/mismatch",
        false,
        task.id,
        expect.any(String),
      ),
    );
    expect(mocks.upload.mock.calls[2][5]).not.toBe(resumeAttempt);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("cleans an expired upload source and requires a new source for full restart", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/expired", remotePath: "/remote/expired" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 25, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

    clickResume(task.id);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    const resumeAttempt = useTransferStore.getState().tasks[0].attemptId;
    second.reject({
      code: "not_found",
      message: "暂存文件不存在或已过期",
      detail: { resource: "local_staging", reason: "staged_not_found" },
    });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].resumeIssue).toBe("local_staging_missing"));
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/expired");
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(false);

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() => expect(mocks.pickLocalFile).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(mocks.upload).toHaveBeenNthCalledWith(
        3,
        "s1",
        "/staged/new-source",
        "/remote/expired",
        false,
        task.id,
        expect.any(String),
      ),
    );
    expect(mocks.upload.mock.calls[2][5]).not.toBe(resumeAttempt);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  for (const eventOrder of ["before", "after"] as const) {
    it(`preserves a download partial when an early 0/0 error arrives ${eventOrder} the IPC failure`, async () => {
      const first = deferred<number>();
      const second = deferred<number>();
      mocks.download.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(100);
      mountPanel();
      const localPath = `/staged/missing-${eventOrder}`;
      enqueueDownload("s1", { remotePath: "/remote/missing", localPath });
      await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));
      const task = useTransferStore.getState().tasks[0];
      emitProgress(task.id, 40, 100, true);
      first.reject(new Error("connection reset"));
      await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

      clickResume(task.id);
      await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(2));
      if (eventOrder === "before") emitProgress(task.id, 0, 0, true);
      second.reject({ code: "not_found", message: "remote missing", detail: { resource: "path" } });
      await waitFor(() => expect(useTransferStore.getState().tasks[0].error).toContain("未找到对应内容，可能已被删除"));
      if (eventOrder === "after") emitProgress(task.id, 0, 0, true);
      expect(useTransferStore.getState().tasks[0].transferred).toBe(40);
      expect(useTransferStore.getState().tasks[0].total).toBe(100);
      expect(mocks.discardStaged).not.toHaveBeenCalledWith(localPath);
      expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(true);

      clickResume(task.id);
      await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(3));
      await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
    });
  }

  it("does not mistake a remote not_found for a released local source", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    mocks.upload.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(100);
    mountPanel();
    enqueueUploads("s1", [{ localPath: "/staged/remote-error", remotePath: "/remote/missing" }]);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 25, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

    clickResume(task.id);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(2));
    second.reject({ code: "not_found", message: "remote missing", detail: { resource: "path" } });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].error).toContain("未找到对应内容，可能已被删除"));
    expect(useTransferStore.getState().tasks[0].resumeIssue).toBeUndefined();
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/remote-error");
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(true);
    expect(hasButton(mounted!.container, "全量重传这个传输任务")).toBe(false);

    clickResume(task.id);
    await waitFor(() => expect(mocks.upload).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
  });

  it("selects a new download target after the staged partial expires", async () => {
    const first = deferred<number>();
    const second = deferred<number>();
    mocks.download.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockResolvedValue(100);
    mountPanel();
    enqueueDownload("s1", { remotePath: "/remote/file", localPath: "/staged/expired-download" });
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    emitProgress(task.id, 25, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));

    clickResume(task.id);
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(2));
    const resumeAttempt = useTransferStore.getState().tasks[0].attemptId;
    second.reject({
      code: "not_found",
      message: "暂存文件不存在或已过期",
      detail: { resource: "local_staging", reason: "staged_not_found" },
    });
    await waitFor(() => expect(useTransferStore.getState().tasks[0].resumeIssue).toBe("local_staging_missing"));
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/expired-download");
    expect(hasButton(mounted!.container, "续传这个传输任务")).toBe(false);

    click(button(mounted!.container, "全量重传这个传输任务"));
    await waitFor(() => expect(mocks.pickSavePath).toHaveBeenCalledWith("file"));
    await waitFor(() =>
      expect(mocks.download).toHaveBeenNthCalledWith(
        3,
        "s1",
        "/remote/file",
        "/staged/new-file",
        false,
        task.id,
        expect.any(String),
      ),
    );
    expect(mocks.download.mock.calls[2][5]).not.toBe(resumeAttempt);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("done"));
    expect(mocks.finishSave).toHaveBeenCalledWith("/staged/new-file", "file");
  });

  it("releases a retained partial when its finished task is cleared", async () => {
    const first = deferred<number>();
    mocks.download.mockReturnValueOnce(first.promise);
    enqueueDownload("s1", { remotePath: "/remote/cleared", localPath: "/staged/cleared" });
    await waitFor(() => expect(mocks.download).toHaveBeenCalledTimes(1));
    const taskId = useTransferStore.getState().tasks[0].id;
    emitProgress(taskId, 10, 100, true);
    first.reject(new Error("connection reset"));
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(mocks.discardStaged).not.toHaveBeenCalledWith("/staged/cleared");

    clearFinishedTransfers("global");
    expect(useTransferStore.getState().tasks).toEqual([]);
    expect(mocks.discardStaged).toHaveBeenCalledWith("/staged/cleared");
  });

  it("does not offer resume for File content uploads even when a progress-looking event exists", async () => {
    mocks.write.mockRejectedValueOnce(new Error("write failed"));
    mountPanel();
    enqueueUploads("s1", [
      { localPath: "", remotePath: "/remote/content", file: new File(["content"], "content.txt") },
    ]);
    await waitFor(() => expect(mocks.write).toHaveBeenCalledTimes(1));
    const task = useTransferStore.getState().tasks[0];
    expect(mocks.write.mock.calls[0][0].taskId).toBe(task.id);
    expect(mocks.write.mock.calls[0][0].attemptId).toBe(task.attemptId);
    emitProgress(task.id, 3, 7, true);
    await waitFor(() => expect(useTransferStore.getState().tasks[0].status).toBe("failed"));
    expect(mounted!.container.querySelector('[data-testid="transfer-panel"]')?.textContent).not.toContain("续传");
    expect(mocks.upload).not.toHaveBeenCalled();
  });
});
