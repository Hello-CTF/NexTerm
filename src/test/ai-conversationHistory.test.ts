import { describe, expect, it } from "vitest";
import {
  appendUserMessage,
  applyAiEvent,
  beginRun,
  createConversation,
  historyToItems,
  truncateItemsAfter,
  type ConversationState,
} from "../features/ai/conversation";

const state = createConversation();

function msg(role: string, content: unknown) {
  return { role, content };
}

describe("structured history mapping", () => {
  it("maps structured user/assistant rows only", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "带图", imageCount: 2 }),
      msg("assistant", { role: "assistant", content: "历史回答" }),
      msg("tool", { role: "tool", content: "不入历史" }),
      msg("assistant", { role: "assistant", content: "" }),
      msg("assistant", "纯文本老行"),
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
    const items = historyToItems({ ...state, seq: 41 }, [
      msg("assistant", { role: "assistant", content: "a" }),
    ]);
    expect(items[0].id).toBe("h41");
  });

  it("carries the source message id on user items for edit-resend", () => {
    const items = historyToItems(state, [
      { id: "m-1", role: "user", content: { role: "user", content: "问题" } },
      { role: "user", content: { role: "user", content: "无 id 的老数据" } },
    ]);
    expect(items[0]).toMatchObject({ messageId: "m-1" });
    const second = items[1];
    expect(second.role === "user" ? second.messageId : "not-a-user-item").toBeUndefined();
  });
});

describe("truncateItemsAfter", () => {
  it("keeps items through the anchor and prunes later attempts/status/todos/usage", () => {
    const history = historyToItems(state, [
      { id: "m-1", role: "user", content: { role: "user", content: "旧问题" } },
    ]);
    let s: ConversationState = { ...state, items: history, seq: 1 };
    s = appendUserMessage(s, 1, "新问题");
    s = beginRun(s, 1);
    s = applyAiEvent(s, 1, { type: "toolCall", id: "c1", name: "read_file", display: "read_file" }).state;
    s = applyAiEvent(s, 1, { type: "usage", promptTokens: 5, contextWindow: 10 }).state;
    s = applyAiEvent(s, 1, { type: "todos", items: [{ content: "t", status: "pending" }] }).state;

    const truncated = truncateItemsAfter(s, history[0].id);
    expect(truncated.items.map((i) => i.role)).toEqual(["user"]);
    expect(truncated.items[0]).toMatchObject({ text: "旧问题", messageId: "m-1" });
    expect(truncated.attempts).toHaveLength(0);
    expect(truncated.usage).toBeNull();
    expect(truncated.todos).toEqual([]);
    expect(truncated.status).toBeNull();
  });

  it("keeps attempts that still own items before the anchor", () => {
    let s = beginRun(createConversation(), 1);
    s = applyAiEvent(s, 1, { type: "toolCall", id: "c1", name: "read_file", display: "read_file" }).state;
    const anchor = appendUserMessage(s, 1, "新问题");
    const truncated = truncateItemsAfter(anchor, s.items[0].id);
    expect(truncated.items.map((i) => i.role)).toEqual(["tool"]);
    expect(truncated.attempts).toHaveLength(1);
  });

  it("is a no-op for unknown ids and for the last item", () => {
    let s = beginRun(createConversation(), 1);
    s = applyAiEvent(s, 1, { type: "delta", text: "回答" }).state;
    expect(truncateItemsAfter(s, "missing")).toBe(s);
    expect(truncateItemsAfter(s, s.items[0].id)).toBe(s);
  });
});
