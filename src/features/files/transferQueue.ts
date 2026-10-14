import { isMac, isWindows } from "../../app/platform";
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
  // file carries the upload content when no OS path is available (desktop drops); the task
  // writes bytes through fs_write instead of a path-based fs_upload.
  file?: File;
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

// Upload targets whose filesystem folds filename case: winrm is always NTFS semantics; a local
// session follows the desktop OS, so a case-sensitive APFS volume may get an extra prompt.
export function conflictCaseInsensitive(sessionKind: string): boolean {
  if (sessionKind === "winrm") return true;
  if (sessionKind === "local") return isWindows() || isMac();
  return false;
}

function foldName(name: string, caseInsensitive: boolean): string {
  return caseInsensitive ? name.toLowerCase() : name;
}

export function findConflict(
  name: string,
  siblings: FileEntryDto[],
  caseInsensitive = false,
): FileEntryDto | undefined {
  return siblings.find(
    (e) => foldName(e.name, caseInsensitive) === foldName(name, caseInsensitive),
  );
}

export interface TakenNames {
  has(name: string): boolean;
  add(name: string): void;
}

export function takenNames(names: Iterable<string>, caseInsensitive: boolean): TakenNames {
  const pool = new Set<string>();
  for (const name of names) pool.add(foldName(name, caseInsensitive));
  return {
    has: (name) => pool.has(foldName(name, caseInsensitive)),
    add: (name) => pool.add(foldName(name, caseInsensitive)),
  };
}

export function uniqueRemoteName(name: string, taken: TakenNames): string {
  const dot = name.lastIndexOf(".");
  const base = dot > 0 ? name.slice(0, dot) : name;
  const ext = dot > 0 ? name.slice(dot) : "";
  for (let n = 1; ; n += 1) {
    const candidate = `${base} (${n})${ext}`;
    if (!taken.has(candidate)) return candidate;
  }
}
