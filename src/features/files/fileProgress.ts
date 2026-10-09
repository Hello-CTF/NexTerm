import type { FsProgressEvent } from "../../ipc/events";

export type FileProgressMap = Readonly<Record<string, FsProgressEvent>>;

export function reduceFileProgress(
  current: FileProgressMap,
  event: FsProgressEvent,
): FileProgressMap {
  if (event.done || event.error) {
    if (!(event.taskId in current)) return current;
    const next = { ...current };
    delete next[event.taskId];
    return next;
  }
  if (event.total <= 0) return current;
  return { ...current, [event.taskId]: event };
}

export function visibleFileProgress(progress: FileProgressMap): FsProgressEvent[] {
  return Object.values(progress);
}

export function progressPercent(event: { transferred: number; total: number }): number {
  if (event.total <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((event.transferred / event.total) * 100)));
}
