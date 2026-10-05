import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockInvoke } from "../demo/mock";
import { subscribe } from "../demo";

interface ProgressEvent {
  taskId: string;
  transferred: number;
  total: number;
  done: boolean;
  error?: string;
}

beforeEach(() => {
  vi.stubGlobal("window", globalThis);
});

function recordProgress(): { events: ProgressEvent[]; stop: () => void } {
  const events: ProgressEvent[] = [];
  const stop = subscribe("fs://progress", (payload) => {
    events.push(payload as ProgressEvent);
  });
  return { events, stop };
}

describe("demo fs://progress 失败形态与生产一致", () => {
  it("上传成功：进度事件后 done 完成、无 error", async () => {
    const { events, stop } = recordProgress();
    const total = (await mockInvoke("fs_upload", {
      args: { sessionId: "s", localPath: "/tmp/x.bin", remotePath: "/home/deploy/ok.bin" },
    })) as number;
    stop();
    expect(total).toBe(4_812_640);
    expect(events.length).toBeGreaterThan(1);
    const last = events[events.length - 1];
    expect(last.done).toBe(true);
    expect(last.error).toBeUndefined();
    expect(events.slice(0, -1).every((e) => !e.done)).toBe(true);
  });

  it("上传到缺失目录：早期失败 done+error 且命令抛错", async () => {
    const { events, stop } = recordProgress();
    await expect(
      mockInvoke("fs_upload", {
        args: { sessionId: "s", localPath: "/tmp/x.bin", remotePath: "/no/such/dir/x.bin" },
      }),
    ).rejects.toThrow("目录不存在");
    stop();
    expect(events).toHaveLength(1);
    expect(events[0].done).toBe(true);
    expect(events[0].error).toContain("目录不存在");
    expect(events[0].total).toBe(0);
  });

  it("名称含 fail 的上传：中途失败 done+error 且命令抛错", async () => {
    const { events, stop } = recordProgress();
    await expect(
      mockInvoke("fs_upload", {
        args: { sessionId: "s", localPath: "/tmp/x.bin", remotePath: "/home/deploy/fail.bin" },
      }),
    ).rejects.toThrow("模拟传输中断");
    stop();
    const last = events[events.length - 1];
    expect(last.done).toBe(true);
    expect(last.error).toContain("模拟传输中断");
    expect(last.transferred).toBeGreaterThan(0);
    expect(last.transferred).toBeLessThan(last.total);
    expect(events.some((e) => !e.done && e.transferred > 0)).toBe(true);
  });

  it("下载缺失文件：早期失败 done+error 且命令抛错", async () => {
    const { events, stop } = recordProgress();
    await expect(
      mockInvoke("fs_download", {
        args: { sessionId: "s", remotePath: "/home/deploy/nope.bin", localPath: "/tmp/y.bin" },
      }),
    ).rejects.toThrow("文件不存在");
    stop();
    expect(events).toHaveLength(1);
    expect(events[0].done).toBe(true);
    expect(events[0].error).toContain("文件不存在");
  });
});
