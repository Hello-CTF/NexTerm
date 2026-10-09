import { create } from "zustand";
import { fsApi } from "../../ipc/commands";
import { EVENTS, listenEvent, type FsProgressEvent } from "../../ipc/events";
import { discardStaged, finishSave } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { baseName } from "./pathUtils";
import type { TransferDirection, TransferTask } from "./transferQueue";

export interface UploadItem {
  localPath: string;
  remotePath: string;
}

export interface DownloadItem {
  remotePath: string;
  localPath: string;
}

interface TransferStoreState {
  tasks: TransferTask[];
  runningId: string | null;
}

export const useTransferStore = create<TransferStoreState>()(() => ({
  tasks: [],
  runningId: null,
}));

let seq = 0;
let batchSeq = 0;
let pumping = false;
let progressSubscribed = false;

type SettleHandler = (task: TransferTask) => void;
const settleHandlers = new Set<SettleHandler>();

export function subscribeTransferSettled(fn: SettleHandler): () => void {
  settleHandlers.add(fn);
  return () => {
    settleHandlers.delete(fn);
  };
}

function ensureProgressSubscription(): void {
  if (progressSubscribed) return;
  progressSubscribed = true;
  void listenEvent<FsProgressEvent>(EVENTS.fsProgress, (event) => {
    const { runningId } = useTransferStore.getState();
    if (!runningId) return;
    useTransferStore.setState((s) => ({
      tasks: s.tasks.map((t) =>
        t.id === runningId ? { ...t, transferred: event.transferred, total: event.total } : t,
      ),
    }));
  });
}

function newTask(
  batchId: string,
  sessionId: string,
  direction: TransferDirection,
  item: UploadItem | DownloadItem,
  status: TransferTask["status"],
  refreshDir?: string,
): TransferTask {
  seq += 1;
  return {
    id: `t${seq}`,
    batchId,
    sessionId,
    direction,
    remotePath: item.remotePath,
    localPath: item.localPath,
    status,
    transferred: 0,
    total: 0,
    refreshDir,
  };
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
  const tasks = [
    ...ready.map((item) => newTask(batchId, sessionId, direction, item, "queued", refreshDir)),
    ...skipped.map((item) => ({
      ...newTask(batchId, sessionId, "upload", item, "skipped", refreshDir),
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

export function cancelTransferTask(id: string): void {
  const st = useTransferStore.getState();
  const task = st.tasks.find((t) => t.id === id);
  if (!task || task.status !== "queued") return;
  useTransferStore.setState({
    tasks: st.tasks.map((t) =>
      t.id === id ? { ...t, status: "cancelled" as const, resultText: `已取消：${t.remotePath}` } : t,
    ),
  });
  if (task.direction === "upload") {
    void discardStaged(task.localPath).catch(() => undefined);
  }
  batchSettleCheck(task.batchId);
}

export function clearFinishedTransfers(): void {
  useTransferStore.setState((s) => ({
    tasks: s.tasks.filter((t) => t.status === "queued" || t.status === "running"),
  }));
}

async function pump(): Promise<void> {
  if (pumping) return;
  pumping = true;
  try {
    for (;;) {
      const st = useTransferStore.getState();
      const next = st.tasks.find((t) => t.status === "queued");
      if (!next) break;
      useTransferStore.setState({
        runningId: next.id,
        tasks: st.tasks.map((t) => (t.id === next.id ? { ...t, status: "running" as const } : t)),
      });
      await execute(next.id);
    }
  } finally {
    pumping = false;
    useTransferStore.setState({ runningId: null });
  }
}

async function execute(id: string): Promise<void> {
  const current = () => useTransferStore.getState().tasks.find((t) => t.id === id);
  const task = current();
  if (!task) return;
  let status: TransferTask["status"] = "done";
  let error: string | undefined;
  let resultText: string;
  try {
    if (task.direction === "upload") {
      await fsApi.upload(task.sessionId, task.localPath, task.remotePath, false);
      resultText = `已上传到 ${task.remotePath}`;
    } else {
      await fsApi.download(task.sessionId, task.remotePath, task.localPath);
      const where = await finishSave(task.localPath, baseName(task.remotePath));
      if (where) {
        resultText = `已下载到 ${where}`;
      } else {
        status = "cancelled";
        resultText = "已取消保存";
      }
    }
  } catch (e) {
    status = "failed";
    error = describeError(e);
    resultText = `${task.direction === "upload" ? "上传" : "下载"}失败：${task.remotePath}：${error}`;
  } finally {
    if (task.direction === "upload") {
      await discardStaged(task.localPath).catch(() => undefined);
    }
  }
  const latest = current();
  if (!latest) return;
  const settled: TransferTask = { ...latest, status, error, resultText };
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
