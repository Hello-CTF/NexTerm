import { describe, expect, it } from "vitest";
import { createConversation, historyToItems } from "../features/ai/conversation";

const state = createConversation();

function msg(role: string, content: unknown) {
  return { role, content };
}

describe("structured history mapping", () => {
  it("preserves legacy user/assistant mapping", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "带图", imageCount: 2 }),
      msg("assistant", "历史回答"),
      msg("tool", "不入历史"),
      msg("assistant", ""),
    ]);
    expect(items.map((i) => i.role)).toEqual(["user", "assistant"]);
    expect(items[0]).toMatchObject({ text: "带图", imageCount: 2, attempt: null });
    expect(items[1]).toMatchObject({ text: "历史回答", attempt: null });
  });

  it("folds tool call/result rows into a single tool item", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "查日志" }),
      msg("toolCall", {
        type: "toolCall",
        jobId: "job-1",
        id: "call-1",
        name: "exec_commands",
        args: {},
      }),
      msg("toolResult", {
        type: "toolResult",
        jobId: "job-1",
        id: "call-1",
        tool: "exec_commands",
        ok: true,
        text: "完整输出",
        summary: "输出摘要",
        truncated: false,
        exitCode: 0,
        panic: false,
      }),
      msg("assistant", { role: "assistant", content: "看完了" }),
    ]);
    expect(items.map((i) => i.role)).toEqual(["user", "tool", "assistant"]);
    expect(items[1]).toMatchObject({
      callId: "call-1",
      name: "exec_commands",
      summary: "输出摘要",
      text: "完整输出",
      ok: true,
      exitCode: 0,
      panic: false,
    });
  });

  it("renders orphan tool results as standalone tool items", () => {
    const items = historyToItems(state, [
      msg("toolResult", {
        type: "toolResult",
        jobId: "job-1",
        id: "call-x",
        tool: "read_file",
        ok: false,
        text: "读取失败",
        summary: "读取失败",
        exitCode: 1,
        panic: false,
      }),
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      role: "tool",
      name: "read_file",
      summary: "读取失败",
      ok: false,
      exitCode: 1,
    });
  });

  it("renders fileChange rows as diff items and planSubmitted rows as plan items", () => {
    const items = historyToItems(state, [
      msg("fileChange", {
        type: "fileChange",
        jobId: "job-1",
        id: "chg-1",
        path: "/etc/nginx/nginx.conf",
        before: "worker_processes 1;",
        after: "worker_processes 4;",
      }),
      msg("planSubmitted", { type: "planSubmitted", jobId: "job-1", plan: "1. 先备份" }),
    ]);
    expect(items[0]).toMatchObject({
      role: "diff",
      path: "/etc/nginx/nginx.conf",
      before: "worker_processes 1;",
      after: "worker_processes 4;",
    });
    expect(items[1]).toMatchObject({ role: "plan", text: "1. 先备份" });
  });

  it("skips structured rows whose job is event-replayed, but keeps user rows", () => {
    const items = historyToItems(
      state,
      [
        msg("user", { role: "user", content: "问题", jobId: "job-2" }),
        msg("toolCall", { type: "toolCall", jobId: "job-2", id: "call-2", name: "exec_commands" }),
        msg("toolResult", {
          type: "toolResult",
          jobId: "job-2",
          id: "call-2",
          tool: "exec_commands",
          ok: true,
          text: "x",
          summary: "x",
        }),
        msg("fileChange", { type: "fileChange", jobId: "job-2", id: "chg-2", path: "/tmp/x" }),
        msg("toolCall", { type: "toolCall", jobId: "job-1", id: "call-1", name: "read_file" }),
      ],
      new Set(["job-2"]),
    );
    expect(items.map((i) => i.role)).toEqual(["user", "tool"]);
    expect(items[0]).toMatchObject({ text: "问题" });
    expect(items[1]).toMatchObject({ callId: "call-1", name: "read_file" });
  });

  it("skips unknown structured types", () => {
    const items = historyToItems(state, [msg("weird", { type: "somethingElse", jobId: "j" })]);
    expect(items).toHaveLength(0);
  });

  it("continues history ids from the conversation sequence", () => {
    const items = historyToItems({ ...state, seq: 41 }, [msg("assistant", "a")]);
    expect(items[0].id).toBe("h41");
  });
});
