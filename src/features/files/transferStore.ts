import { create } from "zustand";
import { fsApi } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { EVENTS, listenEvent, type FsProgressEvent } from "../../ipc/events";
import { isStagedPath } from "../../ipc/webFiles";
import { discardStaged, finishSave, pickLocalFile, pickSavePath } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { fileChunkSource, uploadChunked } from "./chunkedUpload";
import { baseName } from "./pathUtils";
import type { TransferDirection, TransferTask } from "./transferQueue";
import { createTransferTaskId } from "./transferTaskId";

export { createTransferTaskId } from "./transferTaskId";

export interface ScopedTransferTask extends TransferTask {
  hostId: string | null;
  targetName: string;
}

interface TransferTarget {
  hostId: string | null;
  targetName: string;
}

export interface UploadItem {
  localPath: string;
  remotePath: string;
  // file uploads content instead of a local path (desktop drops, where the webview exposes no
  // OS path); localPath stays empty in that case.
  file?: File;
}

export interface DownloadItem {
  remotePath: string;
  localPath: string;
  // pack downloads the directory at remotePath as a remote-built tar.gz.
  pack?: boolean;
}

interface TransferStoreState {
  tasks: ScopedTransferTask[];
}

export const useTransferStore = create<TransferStoreState>()(() => ({
  tasks: [],
}));

type CancellationOutcome = "accepted" | "not_accepted" | "unconfirmed";

interface CancellationRequest {
  attemptId?: string;
  result: Promise<CancellationOutcome>;
  settle: (outcome: CancellationOutcome) => boolean;
  waiting: boolean;
}

interface CancellationTimeout {
  request: CancellationRequest;
  timeout: ReturnType<typeof setTimeout>;
}

const MAX_RUNNING_TRANSFERS = 4;
const CANCEL_SETTLE_TIMEOUT_MS = 10_000;
const running = new Map<string, string>();
const cancellationRequests = new Map<string, CancellationRequest>();
const cancellationTimeouts = new Map<string, CancellationTimeout>();
let batchSeq = 0;
let pumping = false;
let progressSubscribed = false;
let transferEpoch = 0;

type SettleHandler = (task: ScopedTransferTask) => void;
const settleHandlers = new Set<SettleHandler>();

export function subscribeTransferSettled(fn: SettleHandler): () => void {
  settleHandlers.add(fn);
  return () => {
    settleHandlers.delete(fn);
  };
}

function applyTransferProgress(task: ScopedTransferTask, event: FsProgressEvent): ScopedTransferTask {
  if (
    event.done &&
    event.error &&
    event.transferred === 0 &&
    event.total === 0 &&
    task.transferred > 0 &&
    task.total > task.transferred
  ) {
    return task;
  }
  return { ...task, transferred: event.transferred, total: event.total };
}

function ensureProgressSubscription(): void {
  if (progressSubscribed) return;
  progressSubscribed = true;
  void listenEvent<FsProgressEvent>(EVENTS.fsProgress, (event) => {
    if (!event.taskId) return;
    useTransferStore.setState((s) => ({
      tasks: s.tasks.map((t) => (t.id === event.taskId ? applyTransferProgress(t, event) : t)),
    }));
  });
}

function newTask(
  batchId: string,
  sessionId: string,
  target: TransferTarget,
  direction: TransferDirection,
  item: UploadItem | DownloadItem,
  status: TransferTask["status"],
  refreshDir?: string,
): ScopedTransferTask {
  return {
    id: createTransferTaskId(),
    batchId,
    sessionId,
    ...target,
    direction,
    remotePath: item.remotePath,
    localPath: item.localPath,
    file: "file" in item ? item.file : undefined,
    pack: "pack" in item ? item.pack : undefined,
    status,
    transferred: 0,
    total: 0,
    refreshDir,
  };
}

function resolveTransferTarget(sessionId: string): TransferTarget {
  const { sessions, workspaces } = useUi.getState();
  const session = sessions?.find((item) => item.id === sessionId);
  const workspace = workspaces?.find((item) => item.sessionId === sessionId);
  return {
    hostId: session?.assetId ?? workspace?.assetId ?? null,
    targetName: session?.name?.trim() || workspace?.title?.trim() || `会话 ${sessionId}`,
  };
}

export function transferMatchesScope(
  task: ScopedTransferTask,
  sessionId: string,
  hostId: string | null,
): boolean {
  return task.sessionId === sessionId || Boolean(hostId && task.hostId === hostId);
}

function enqueueBatch(
  sessionId: string,
  direction: TransferDirection,
  ready: (UploadItem | DownloadItem)[],
  skipped: UploadItem[],
  refreshDir?: string,
): string {
  ensureProgressSubscription();
  batchSeq += 1;
  const batchId = `b${batchSeq}`;
  const target = resolveTransferTarget(sessionId);
  const tasks = [
    ...ready.map((item) => newTask(batchId, sessionId, target, direction, item, "queued", refreshDir)),
    ...skipped.map((item) => ({
      ...newTask(batchId, sessionId, target, "upload", item, "skipped", refreshDir),
      resultText: `已跳过：${item.remotePath}`,
    })),
  ];
  useTransferStore.setState((s) => ({ tasks: [...s.tasks, ...tasks] }));
  void pump();
  batchSettleCheck(batchId);
  return batchId;
}

export function enqueueUploads(sessionId: string, items: UploadItem[], refreshDir?: string): string {
  return enqueueBatch(sessionId, "upload", items, [], refreshDir);
}

export function enqueueUploadBatch(
  sessionId: string,
  uploads: UploadItem[],
  skipped: UploadItem[],
  refreshDir?: string,
): string {
  return enqueueBatch(sessionId, "upload", uploads, skipped, refreshDir);
}

export function enqueueDownload(sessionId: string, item: DownloadItem): string {
  return enqueueBatch(sessionId, "download", [item], []);
}

export function enqueuePackDownload(sessionId: string, item: Omit<DownloadItem, "pack">): string {
  return enqueueBatch(sessionId, "download", [{ ...item, pack: true }], []);
}

function transferHostKey(task: ScopedTransferTask): string {
  return task.hostId ? `host:${task.hostId}` : `session:${task.sessionId}`;
}

function hasValidLocalPath(task: ScopedTransferTask): boolean {
  return task.localPath !== "" && (!WEB || isStagedPath(task.localPath));
}

function hasPartial(task: ScopedTransferTask): boolean {
  return task.transferred > 0 && task.total > task.transferred;
}

function transferErrorInfo(error: unknown): { code?: string; resource?: string; reason?: string } {
  if (!error || typeof error !== "object") return {};
  const value = error as { code?: unknown; detail?: unknown };
  const detail = value.detail && typeof value.detail === "object"
    ? value.detail as { resource?: unknown; reason?: unknown }
    : undefined;
  return {
    code: typeof value.code === "string" ? value.code : undefined,
    resource: typeof detail?.resource === "string" ? detail.resource : undefined,
    reason: typeof detail?.reason === "string" ? detail.reason : undefined,
  };
}

function resumeIssueFromError(error: unknown): TransferTask["resumeIssue"] {
  const detail = transferErrorInfo(error);
  if (detail.resource === "partial" && detail.reason === "resume_mismatch") return "partial_mismatch";
  if (detail.resource === "local_staging" && detail.reason === "staged_not_found") {
    return "local_staging_missing";
  }
  return undefined;
}

function createCancellationRequest(attemptId: string | undefined): CancellationRequest {
  let settled = false;
  let resolve!: (outcome: CancellationOutcome) => void;
  const result = new Promise<CancellationOutcome>((resolveResult) => {
    resolve = resolveResult;
  });
  return {
    attemptId,
    result,
    waiting: false,
    settle: (outcome) => {
      if (settled) return false;
      settled = true;
      resolve(outcome);
      return true;
    },
  };
}

function clearCancellationTimeout(id: string, request?: CancellationRequest): void {
  const current = cancellationTimeouts.get(id);
  if (!current || (request && current.request !== request)) return;
  clearTimeout(current.timeout);
  cancellationTimeouts.delete(id);
}

function clearCancellationRequest(id: string, request?: CancellationRequest): void {
  const current = request ?? cancellationRequests.get(id);
  if (request && cancellationRequests.get(id) !== request) return;
  current?.settle("unconfirmed");
  if (!request || cancellationRequests.get(id) === request) cancellationRequests.delete(id);
  clearCancellationTimeout(id, current);
}

function isCurrentCancellationRequest(id: string, attemptId: string | undefined, request: CancellationRequest): boolean {
  if (cancellationRequests.get(id) !== request) return false;
  const task = useTransferStore.getState().tasks.find((item) => item.id === id);
  return task !== undefined && task.attemptId === attemptId && task.status === "running";
}

function cancellationAccepted(id: string, attemptId: string | undefined): Promise<CancellationOutcome> {
  const request = cancellationRequests.get(id);
  if (!request || request.attemptId !== attemptId) return Promise.resolve("not_accepted");
  request.waiting = true;
  return request.result;
}

function armCancellationTimeout(id: string, attemptId: string | undefined, request: CancellationRequest): void {
  clearCancellationTimeout(id);
  const timeout = setTimeout(() => {
    if (cancellationTimeouts.get(id)?.request !== request) return;
    cancellationTimeouts.delete(id);
    if (cancellationRequests.get(id) !== request) return;
    request.settle("unconfirmed");
    if (request.waiting) return;
    const task = useTransferStore.getState().tasks.find((item) => item.id === id);
    if (!task || task.attemptId !== attemptId || task.status !== "running" || !task.cancelling) return;
    const message = task.cancelAccepted
      ? `取消「${task.remotePath}」的请求已接受，但传输尚未停止；请重试取消或断开会话`
      : `取消「${task.remotePath}」尚未得到后端确认，且传输尚未停止；请重试取消或断开会话`;
    useTransferStore.setState((state) => ({
      tasks: state.tasks.map((item) => item.id === id ? { ...item, cancelling: false, error: message } : item),
    }));
    useUi.getState().pushToast("error", message);
  }, CANCEL_SETTLE_TIMEOUT_MS);
  cancellationTimeouts.set(id, { request, timeout });
}

export function canResumeTransfer(task: ScopedTransferTask): boolean {
  return (
    (task.status === "failed" || task.status === "cancelled") &&
    !task.cancelling &&
    !task.restartPreparing &&
    !task.resumeIssue &&
    (task.direction === "download" || task.resume === true) &&
    !task.pack &&
    hasPartial(task) &&
    hasValidLocalPath(task) &&
    !(task.direction === "upload" && task.file)
  );
}

export function canRestartTransfer(task: ScopedTransferTask): boolean {
  return (
    (task.status === "failed" || task.status === "cancelled") &&
    !task.restartPreparing &&
    (task.resumeIssue !== undefined || (task.direction === "upload" && task.resume !== true)) &&
    (task.resumeIssue === "local_staging_missing" || hasValidLocalPath(task)) &&
    !(task.direction === "upload" && task.file)
  );
}

export function resumeTransferTask(id: string): void {
  const task = useTransferStore.getState().tasks.find((item) => item.id === id);
  if (!task || !canResumeTransfer(task)) return;
  clearCancellationRequest(id);
  useTransferStore.setState((state) => ({
    tasks: state.tasks.map((item) =>
      item.id === id
        ? {
            ...item,
            status: "queued" as const,
            resume: true,
            cancelling: false,
            cancelAccepted: false,
            resumeIssue: undefined,
            restartPreparing: false,
            error: undefined,
            resultText: undefined,
          }
        : item,
    ),
  }));
  pump();
}

export async function restartTransferTask(id: string): Promise<void> {
  const task = useTransferStore.getState().tasks.find((item) => item.id === id);
  if (!task || !canRestartTransfer(task)) return;
  clearCancellationRequest(id);
  useTransferStore.setState((state) => ({
    tasks: state.tasks.map((item) => item.id === id ? { ...item, restartPreparing: true } : item),
  }));
  let localPath = task.localPath;
  try {
    if (task.resumeIssue === "local_staging_missing") {
      const selected = task.direction === "upload"
        ? await pickLocalFile()
        : await pickSavePath(baseName(task.remotePath));
      if (!selected) {
        useTransferStore.setState((state) => ({
          tasks: state.tasks.map((item) => item.id === id ? { ...item, restartPreparing: false } : item),
        }));
        return;
      }
      localPath = selected;
    }
    const latest = useTransferStore.getState().tasks.find((item) => item.id === id);
    if (!latest || latest.resumeIssue !== task.resumeIssue ||
        (latest.status !== "failed" && latest.status !== "cancelled")) {
      if (localPath !== task.localPath) await discardStaged(localPath).catch(() => undefined);
      return;
    }
    useTransferStore.setState((state) => ({
      tasks: state.tasks.map((item) =>
        item.id === id
          ? {
              ...item,
              localPath,
              status: "queued" as const,
              resume: false,
              transferred: 0,
              total: 0,
              cancelling: false,
              cancelAccepted: false,
              resumeIssue: undefined,
              restartPreparing: false,
              error: undefined,
              resultText: undefined,
            }
          : item,
      ),
    }));
    pump();
  } catch (error) {
    const message = `全量重传准备失败：${describeError(error)}`;
    useTransferStore.setState((state) => ({
      tasks: state.tasks.map((item) =>
        item.id === id ? { ...item, restartPreparing: false, error: message } : item,
      ),
    }));
    useUi.getState().pushToast("error", message);
  }
}

export function cancelTransferTask(id: string): void {
  const task = useTransferStore.getState().tasks.find((item) => item.id === id);
  if (!task || task.cancelling) return;
  if (task.status === "running") {
    const attemptId = task.attemptId;
    clearCancellationRequest(id);
    useTransferStore.setState((state) => ({
      tasks: state.tasks.map((item) =>
        item.id === id ? { ...item, cancelling: true, cancelAccepted: false, error: undefined } : item,
      ),
    }));
    const request = createCancellationRequest(attemptId);
    cancellationRequests.set(id, request);
    armCancellationTimeout(id, attemptId, request);
    void fsApi.cancel(id, attemptId).then((accepted) => {
      if (!isCurrentCancellationRequest(id, attemptId, request)) return;
      const latest = useTransferStore.getState().tasks.find((item) => item.id === id);
      if (!latest) return;
      if (!request.settle(accepted ? "accepted" : "not_accepted")) return;
      if (accepted) {
        useTransferStore.setState((state) => ({
          tasks: state.tasks.map((item) => item.id === id ? { ...item, cancelAccepted: true } : item),
        }));
        return;
      }
      clearCancellationRequest(id, request);
      const message = `取消「${latest.remotePath}」未生效：后端未找到该运行任务，传输仍在进行`;
      useTransferStore.setState((state) => ({
        tasks: state.tasks.map((item) =>
          item.id === id ? { ...item, cancelling: false, cancelAccepted: false, error: message } : item,
        ),
      }));
      useUi.getState().pushToast("error", message);
    }).catch((error) => {
      if (!isCurrentCancellationRequest(id, attemptId, request)) return;
      if (!request.settle("not_accepted")) return;
      clearCancellationRequest(id, request);
      const message = `取消传输失败：${describeError(error)}`;
      useTransferStore.setState((state) => ({
        tasks: state.tasks.map((item) =>
          item.id === id ? { ...item, cancelling: false, cancelAccepted: false, error: message } : item,
        ),
      }));
      useUi.getState().pushToast("error", message);
    });
    return;
  }
  if (task.status !== "queued") return;
  useTransferStore.setState((state) => ({
    tasks: state.tasks.map((item) =>
      item.id === id
        ? { ...item, status: "cancelled" as const, resultText: `已取消：${item.remotePath}` }
        : item,
    ),
  }));
  if (task.localPath) void discardStaged(task.localPath).catch(() => undefined);
  batchSettleCheck(task.batchId);
}

export function clearFinishedTransfers(scope: { sessionId: string; hostId: string | null } | "global"): void {
  const removed = useTransferStore.getState().tasks.filter(
    (task) =>
      (scope === "global" || transferMatchesScope(task, scope.sessionId, scope.hostId)) &&
      task.status !== "queued" &&
      task.status !== "running",
  );
  const ids = new Set(removed.map((task) => task.id));
  useTransferStore.setState((state) => ({ tasks: state.tasks.filter((task) => !ids.has(task.id)) }));
  for (const task of removed) {
    clearCancellationRequest(task.id);
    if (task.localPath) void discardStaged(task.localPath).catch(() => undefined);
  }
}

export function resetTransferQueue(): void {
  transferEpoch += 1;
  const { tasks } = useTransferStore.getState();
  useTransferStore.setState({ tasks: [] });
  for (const task of tasks) {
    clearCancellationRequest(task.id);
    if (task.localPath && !running.has(task.id)) {
      void discardStaged(task.localPath).catch(() => undefined);
    }
  }
}

function pump(): void {
  if (pumping) return;
  pumping = true;
  try {
    const busyHosts = new Set(running.values());
    for (const task of useTransferStore.getState().tasks) {
      if (running.size >= MAX_RUNNING_TRANSFERS) break;
      if (task.status !== "queued" || running.has(task.id)) continue;
      const hostKey = transferHostKey(task);
      if (busyHosts.has(hostKey)) continue;
      running.set(task.id, hostKey);
      busyHosts.add(hostKey);
      useTransferStore.setState((state) => ({
        tasks: state.tasks.map((item) =>
          item.id === task.id && item.status === "queued" ? { ...item, status: "running" as const } : item,
        ),
      }));
      const epoch = transferEpoch;
      void execute(task.id, epoch).finally(() => {
        running.delete(task.id);
        pump();
      });
    }
  } finally {
    pumping = false;
  }
}

async function execute(id: string, epoch: number): Promise<void> {
  const current = () => useTransferStore.getState().tasks.find((t) => t.id === id);
  const active = () => epoch === transferEpoch && current() !== undefined;
  const attemptId = createTransferTaskId();
  useTransferStore.setState((state) => ({
    tasks: state.tasks.map((item) =>
      item.id === id
        ? { ...item, attemptId, cancelling: false, cancelAccepted: false, restartPreparing: false }
        : item,
    ),
  }));
  const task = current();
  if (!task) return;
  let status: TransferTask["status"] = "done";
  let error: string | undefined;
  let resultText: string;
  let resumeIssue: TransferTask["resumeIssue"];
  let cancellationRequest: CancellationRequest | undefined;
  try {
    if (task.direction === "upload") {
      if (task.file) {
        await uploadChunked({
          sessionId: task.sessionId,
          remotePath: task.remotePath,
          source: fileChunkSource(task.file),
          taskId: id,
          attemptId,
        });
      } else {
        await fsApi.upload(task.sessionId, task.localPath, task.remotePath, task.resume === true, id, attemptId);
      }
      resultText = `已上传到 ${task.remotePath}`;
    } else {
      if (task.pack) {
        await fsApi.packDownload(task.sessionId, task.remotePath, task.localPath, id, attemptId);
      } else {
        await fsApi.download(task.sessionId, task.remotePath, task.localPath, task.resume === true, id, attemptId);
      }
      if (!active()) return;
      const where = await finishSave(task.localPath, task.pack ? `${baseName(task.remotePath)}.tar.gz` : baseName(task.remotePath));
      if (where) {
        resultText = `已下载到 ${where}`;
      } else {
        status = "cancelled";
        resultText = "已取消保存";
      }
    }
  } catch (e) {
    cancellationRequest = cancellationRequests.get(id);
    let cancellationOutcome: CancellationOutcome = "not_accepted";
    if (transferErrorInfo(e).code === "cancelled") {
      cancellationOutcome = await cancellationAccepted(id, attemptId);
    }
    if (cancellationOutcome === "accepted") {
      status = "cancelled";
      resultText = `已取消：${task.remotePath}`;
    } else if (cancellationOutcome === "unconfirmed") {
      status = "failed";
      error = `取消结果未确认：${task.remotePath}，请检查目标状态后重试`;
      resultText = error;
    } else {
      status = "failed";
      error = describeError(e);
      resultText = `${task.direction === "upload" ? "上传" : "下载"}失败：${task.remotePath}：${error}`;
      resumeIssue = resumeIssueFromError(e);
    }
  } finally {
    if (cancellationRequest) {
      clearCancellationRequest(id, cancellationRequest);
    } else {
      clearCancellationTimeout(id);
    }
    const latest = current();
    const keepPartial =
      active() &&
      latest !== undefined &&
      (status === "failed" || status === "cancelled") &&
      resumeIssue === undefined &&
      (latest.direction === "download" || latest.resume === true) &&
      hasPartial(latest) &&
      hasValidLocalPath(latest) &&
      !(latest.direction === "upload" && latest.file);
    const keepForRestart =
      active() &&
      latest !== undefined &&
      (status === "failed" || status === "cancelled") &&
      (resumeIssue === "partial_mismatch" || (latest.direction === "upload" && latest.resume !== true)) &&
      hasValidLocalPath(latest) &&
      !(latest.direction === "upload" && latest.file);
    if (task.localPath && !keepPartial && !keepForRestart) {
      await discardStaged(task.localPath).catch(() => undefined);
    }
  }
  if (!active()) return;
  const latest = current();
  if (!latest) return;
  const settled: ScopedTransferTask = {
    ...latest,
    status,
    cancelling: false,
    cancelAccepted: false,
    resumeIssue,
    restartPreparing: false,
    error,
    resultText,
  };
  useTransferStore.setState((s) => ({ tasks: s.tasks.map((t) => (t.id === id ? settled : t)) }));
  for (const fn of settleHandlers) fn(settled);
  batchSettleCheck(latest.batchId);
}

function batchSettleCheck(batchId: string): void {
  const { tasks } = useTransferStore.getState();
  const batch = tasks.filter((t) => t.batchId === batchId);
  if (batch.length === 0) return;
  if (batch.some((t) => t.status === "queued" || t.status === "running")) return;
  const pushToast = useUi.getState().pushToast;
  if (batch.length === 1) {
    const only = batch[0];
    if (only.status === "done") pushToast("success", only.resultText ?? "传输完成");
    else if (only.status === "failed") pushToast("error", only.resultText ?? "传输失败");
    else pushToast("info", only.resultText ?? only.remotePath);
    return;
  }
  const done = batch.filter((t) => t.status === "done").length;
  const failed = batch.filter((t) => t.status === "failed");
  const skipped = batch.filter((t) => t.status === "skipped" || t.status === "cancelled").length;
  const head = batch[0].direction === "upload" ? "上传" : "下载";
  const parts = [`完成 ${done}`];
  if (skipped > 0) parts.push(`跳过/取消 ${skipped}`);
  if (failed.length > 0) parts.push(`失败 ${failed.length}`);
  if (failed.length > 0) {
    pushToast("error", `${head}批次结束：${parts.join("，")}。${failed[0].resultText ?? ""}`);
  } else {
    pushToast(done > 0 ? "success" : "info", `${head}批次结束：${parts.join("，")}`);
  }
}
