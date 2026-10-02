import { describe, expect, it } from "vitest";
import type { FsProgressEvent } from "../../ipc/events";
import { progressPercent, reduceFileProgress, visibleFileProgress } from "../../features/files/fileProgress";

function event(taskId: string, transferred: number, done = false): FsProgressEvent {
  return { taskId, transferred, total: 200, done };
}

describe("per-task file progress", () => {
  it("tracks interleaved tasks independently and completes only the addressed task", () => {
    let progress = reduceFileProgress({}, event("a", 50));
    progress = reduceFileProgress(progress, event("b", 100));
    progress = reduceFileProgress(progress, event("a", 150));
    expect(visibleFileProgress(progress).map((item) => [item.taskId, item.transferred])).toEqual([
      ["a", 150],
      ["b", 100],
    ]);
    const next = reduceFileProgress(progress, event("a", 200, true));
    expect(visibleFileProgress(next).map((item) => item.taskId)).toEqual(["b"]);
    expect(reduceFileProgress(next, event("unknown", 200, true))).toBe(next);
  });

  it("ignores empty totals and clamps displayed percentages", () => {
    expect(reduceFileProgress({}, { taskId: "a", transferred: 1, total: 0, done: false })).toEqual({});
    expect(progressPercent(event("a", 500))).toBe(100);
    expect(progressPercent(event("a", -10))).toBe(0);
  });
});
