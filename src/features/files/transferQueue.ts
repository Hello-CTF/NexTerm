import type { FileEntryDto } from "../../ipc/types";

export type TransferDirection = "upload" | "download";

export type TransferStatus = "queued" | "running" | "done" | "failed" | "skipped" | "cancelled";

export type ConflictPolicy = "skip" | "overwrite" | "keepBoth";

export interface TransferTask {
  id: string;
  batchId: string;
  sessionId: string;
  direction: TransferDirection;
  remotePath: string;
  localPath: string;
  status: TransferStatus;
  transferred: number;
  total: number;
  error?: string;
  resultText?: string;
  refreshDir?: string;
}

export const TRANSFER_STATUS_LABEL: Record<TransferStatus, string> = {
  queued: "排队中",
  running: "进行中",
  done: "已完成",
  failed: "失败",
  skipped: "已跳过",
  cancelled: "已取消",
};

export function findConflict(name: string, siblings: FileEntryDto[]): FileEntryDto | undefined {
  return siblings.find((e) => e.name === name);
}

export function uniqueRemoteName(name: string, taken: ReadonlySet<string>): string {
  const dot = name.lastIndexOf(".");
  const base = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : "";
  for (let n = 1; ; n += 1) {
    const candidate = `${base} (${n})${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
}
