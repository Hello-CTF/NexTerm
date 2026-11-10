import { localSessionIsMac, localSessionIsWindows } from "../../app/platform";
import type { FileEntryDto } from "../../ipc/types";
import { t } from "../../i18n";

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
  // streams bounded chunks instead of using a path-based fs_upload.
  file?: File;
  // pack marks a directory downloaded as a remote-built tar.gz; it cannot resume.
  pack?: boolean;
  status: TransferStatus;
  transferred: number;
  total: number;
  resume?: boolean;
  attemptId?: string;
  cancelling?: boolean;
  cancelAccepted?: boolean;
  resumeIssue?: "partial_mismatch" | "local_staging_missing";
  restartPreparing?: boolean;
  error?: string;
  resultText?: string;
  refreshDir?: string;
}

export const TRANSFER_STATUS_LABEL: Record<TransferStatus, string> = {
  queued: t("排队中"),
  running: t("进行中"),
  done: t("已完成"),
  failed: t("失败"),
  skipped: t("已跳过"),
  cancelled: t("已取消"),
};

export interface ConflictTarget {
  pathStyle?: string;
  capabilities?: { caseInsensitive: boolean } | null;
}

// Upload targets whose filesystem folds filename case. The backend's declared capability decides, then a Windows
// path style (an SSH server on Windows); without fs_roots, winrm is always NTFS semantics and a local session follows
// the backend OS, so a case-sensitive APFS volume may get an extra prompt.
export function conflictCaseInsensitive(sessionKind: string, target?: ConflictTarget | null): boolean {
  if (target?.capabilities) return target.capabilities.caseInsensitive;
  if (target?.pathStyle === "windows") return true;
  if (sessionKind === "winrm") return true;
  if (sessionKind === "local") return localSessionIsWindows() || localSessionIsMac();
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
