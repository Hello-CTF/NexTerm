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
      msg("tool", { role: "tool", content: "Not part of history" }),
      msg("assistant", { role: "assistant", content: "" }),
      msg("assistant", "Legacy plain-text row"),
    ]);
    expect(items.map((i) => i.role)).toEqual(["user", "assistant"]);
    expect(items[0]).toMatchObject({ text: "带图", imageCount: 2, attempt: null });
    expect(items[1]).toMatchObject({ text: "历史回答", attempt: null });
  });

  it("marks the trigger note of a terminal event run so it is not shown as user input", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "我输入的", jobId: "j0" }),
      msg("user", { role: "user", content: "[NexTerm terminal event] ...", jobId: "j1", source: "event" }),
    ]);
    expect(items[0]).not.toHaveProperty("event");
    expect(items[1]).toMatchObject({ role: "user", event: true, jobId: "j1" });
  });

  it("restores the saved reasoning before the turn it belongs to", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "看下负载" }),
      msg("assistant", { role: "assistant", content: "先看进程", jobId: "j1", interim: true, reasoning: "先确认进程" }),
      msg("toolCall", { type: "toolCall", jobId: "j1", id: "c1", name: "exec", reasoning: "" }),
      msg("toolCall", { type: "toolCall", jobId: "j1", id: "c2", name: "exec", reasoning: "再看内存" }),
      msg("toolResult", { type: "toolResult", jobId: "j1", id: "c2", ok: true, summary: "ok" }),
      msg("assistant", { role: "assistant", content: "负载正常", jobId: "j1", reasoning: "结论" }),
    ]);
    expect(items.map((i) => [i.role, "text" in i ? i.text : ""])).toEqual([
      ["user", "看下负载"],
      ["reasoning", "先确认进程"],
      ["assistant", "先看进程"],
      ["tool", ""],
      ["reasoning", "再看内存"],
      ["tool", ""],
      ["reasoning", "结论"],
      ["assistant", "负载正常"],
    ]);
    expect(items[5]).toMatchObject({ callId: "c2", ok: true, summary: "ok" });
  });

  it("maps a /compact summary row to a compact marker", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "旧问题" }),
      msg("summary", { type: "summary", summary: "已重启 nginx，下一步看日志", messages: 6 }),
      msg("user", { role: "user", content: "新问题" }),
    ]);
    expect(items.map((i) => i.role)).toEqual(["user", "compact", "user"]);
    expect(items[1]).toMatchObject({ text: "已重启 nginx，下一步看日志", messages: 6 });
  });

  it("shows an automatic summary before the message of the run it was made for", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "旧问题", jobId: "job-1" }),
      msg("assistant", { role: "assistant", content: "旧回答", jobId: "job-1" }),
      msg("user", { role: "user", content: "新问题", jobId: "job-2" }),
      msg("summary", { type: "summary", summary: "此前排查了 nginx", messages: 2, auto: true, beforeJobId: "job-2" }),
      msg("assistant", { role: "assistant", content: "新回答", jobId: "job-2" }),
    ]);
    expect(items.map((i) => (i.role === "compact" ? "compact" : `${i.role}:${"text" in i ? i.text : ""}`))).toEqual([
      "user:旧问题",
      "assistant:旧回答",
      "compact",
      "user:新问题",
      "assistant:新回答",
    ]);
  });

  it("folds tool call/result rows into a single tool item", () => {
    const items = historyToItems(state, [
      msg("user", { role: "user", content: "Inspect logs" }),
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
        text: "Full output",
        summary: "Output summary",
        truncated: false,
        exitCode: 0,
        panic: false,
      }),
      msg("assistant", { role: "assistant", content: "Finished reviewing" }),
    ]);
    expect(items.map((i) => i.role)).toEqual(["user", "tool", "assistant"]);
    expect(items[1]).toMatchObject({
      callId: "call-1",
      name: "exec_commands",
      summary: "Output summary",
      text: "Full output",
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
        text: "Read failed",
        summary: "Read failed",
        exitCode: 1,
        panic: false,
      }),
    ]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({
      role: "tool",
      name: "read_file",
      summary: "Read failed",
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
      { id: "m-1", role: "user", content: { role: "user", content: "Question" } },
      { role: "user", content: { role: "user", content: "Legacy row without an id" } },
    ]);
    expect(items[0]).toMatchObject({ messageId: "m-1" });
    const second = items[1];
    expect(second.role === "user" ? second.messageId : "not-a-user-item").toBeUndefined();
  });

  it("keeps Chinese tool text and ignores modelText in new and legacy rows", () => {
    const items = historyToItems(state, [
      msg("toolResult", {
        type: "toolResult",
        jobId: "job-1",
        id: "new",
        tool: "db_list_tables",
        ok: true,
        text: "无表",
        modelText: "no tables",
        summary: "无表",
        exitCode: 0,
      }),
      msg("toolResult", {
        type: "toolResult",
        jobId: "job-1",
        id: "legacy",
        tool: "search_files",
        ok: true,
        text: "无匹配",
        summary: "无匹配",
        exitCode: 0,
      }),
    ]);
    expect(items.map((item) => item.role)).toEqual(["tool", "tool"]);
    expect(items[0]).toMatchObject({ text: "无表", summary: "无表" });
    expect(items[1]).toMatchObject({ text: "无匹配", summary: "无匹配" });
    expect(items[0]).not.toHaveProperty("modelText");
    expect(items[1]).not.toHaveProperty("modelText");
  });
});

describe("truncateItemsAfter", () => {
  it("keeps items through the anchor and prunes later attempts/status/todos/usage", () => {
    const history = historyToItems(state, [
      { id: "m-1", role: "user", content: { role: "user", content: "旧问题" } },
    ]);
    let s: ConversationState = { ...state, items: history, seq: 1 };
    s = appendUserMessage(s, 1, "New question");
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
    const anchor = appendUserMessage(s, 1, "New question");
    const truncated = truncateItemsAfter(anchor, s.items[0].id);
    expect(truncated.items.map((i) => i.role)).toEqual(["tool"]);
    expect(truncated.attempts).toHaveLength(1);
  });

  it("is a no-op for unknown ids and for the last item", () => {
    let s = beginRun(createConversation(), 1);
    s = applyAiEvent(s, 1, { type: "delta", text: "Answer" }).state;
    expect(truncateItemsAfter(s, "missing")).toBe(s);
    expect(truncateItemsAfter(s, s.items[0].id)).toBe(s);
  });
});
