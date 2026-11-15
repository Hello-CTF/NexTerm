import { describe, expect, it } from "vitest";
import {
  applyAiEvent,
  beginRun,
  createConversation,
  historyToItems,
  pendingInteraction,
  type ChatItem,
  type ConversationState,
} from "../../features/ai/conversation";

function apply(state: ConversationState, ev: Record<string, unknown>): ConversationState {
  return applyAiEvent(state, 1, ev).state;
}

function withConfirm(): ConversationState {
  let state = beginRun(createConversation(), 1);
  state = apply(state, { type: "toolCall", id: "call-1", name: "exec_commands", display: "执行命令" });
  return apply(state, {
    type: "confirmRequired",
    id: "call-1",
    tool: "exec_commands",
    rendered: "$ rm x",
    confirmationNonce: "nonce-1",
    requestId: "req-1",
    attempt: 1,
  });
}

function withQuestion(): ConversationState {
  let state = beginRun(createConversation(), 1);
  state = apply(state, { type: "toolCall", id: "call-q", name: "ask_user", display: "提问" });
  return apply(state, {
    type: "questionRequired",
    id: "call-q",
    question: { question: "继续吗？", options: ["继续"] },
    confirmationNonce: "nonce-q",
  });
}

function resolutionOf(state: ConversationState, role: "confirm" | "question"): string | undefined {
  const item = state.items.find((candidate) => candidate.role === role);
  return item && (item.role === "confirm" || item.role === "question") ? item.resolution : undefined;
}

describe("interaction resolution from the run stream", () => {
  it("resolves a confirmation another client decided", () => {
    const result = applyAiEvent(withConfirm(), 1, { type: "interactionResolved", callId: "call-1", decision: "allow_session", seq: 3 });
    expect(result.accepted).toBe(true);
    expect(pendingInteraction(result.state, 1, "confirm")).toBeNull();
    expect(resolutionOf(result.state, "confirm")).toBe("本会话已允许此类");
  });

  it("resolves an answered question and an expired confirmation with their outcome", () => {
    const answered = apply(withQuestion(), { type: "interactionResolved", callId: "call-q", decision: "answered" });
    expect(pendingInteraction(answered, 1, "question")).toBeNull();
    expect(resolutionOf(answered, "question")).toBe("已回答");

    const expired = apply(withConfirm(), { type: "interactionResolved", callId: "call-1", decision: "expired" });
    expect(pendingInteraction(expired, 1, "confirm")).toBeNull();
    expect(resolutionOf(expired, "confirm")).toBe("等待已过期，无需再处理");
  });

  it("keeps a resolution this client already stamped and ignores unknown calls", () => {
    let state = withConfirm();
    state = {
      ...state,
      items: state.items.map((item): ChatItem => (item.role === "confirm" ? { ...item, resolution: "已允许一次" } : item)),
    };
    expect(applyAiEvent(state, 1, { type: "interactionResolved", callId: "call-1", decision: "deny" }).accepted).toBe(false);
    expect(resolutionOf(state, "confirm")).toBe("已允许一次");
    expect(applyAiEvent(withConfirm(), 1, { type: "interactionResolved", callId: "other", decision: "deny" }).accepted).toBe(false);
  });

  it("resolves a card when the tool result of its call arrives (servers without interactionResolved)", () => {
    const confirmed = apply(withConfirm(), { type: "toolResult", id: "call-1", ok: true, summary: "完成", text: "完成" });
    expect(pendingInteraction(confirmed, 1, "confirm")).toBeNull();
    expect(resolutionOf(confirmed, "confirm")).toBe("已在其他地方处理");
    const tool = confirmed.items.find((item) => item.role === "tool");
    expect(tool && tool.role === "tool" ? tool.summary : undefined).toBe("完成");

    const answered = apply(withQuestion(), { type: "toolResult", id: "call-q", ok: true, summary: "继续", text: "继续" });
    expect(pendingInteraction(answered, 1, "question")).toBeNull();
  });

  it("resolves a card from a tool result even when its tool card is missing", () => {
    let state = beginRun(createConversation(), 1);
    state = apply(state, { type: "confirmRequired", id: "call-2", tool: "exec_commands", rendered: "$ ls", confirmationNonce: "n" });
    const result = applyAiEvent(state, 1, { type: "toolResult", id: "call-2", ok: true, summary: "ok" });
    expect(result.accepted).toBe(true);
    expect(pendingInteraction(result.state, 1, "confirm")).toBeNull();
  });
});

describe("interim assistant text in history", () => {
  const messages = [
    { id: "m1", role: "user", content: { role: "user", content: "查看磁盘", jobId: "job-1" } },
    { id: "m2", role: "assistant", content: { role: "assistant", content: "先看一下分区。", jobId: "job-1", interim: true } },
    { id: "m3", role: "toolCall", content: { type: "toolCall", jobId: "job-1", id: "c1", name: "exec_commands" } },
    { id: "m4", role: "toolResult", content: { type: "toolResult", jobId: "job-1", id: "c1", tool: "exec_commands", ok: true, summary: "df 完成" } },
    { id: "m5", role: "assistant", content: { role: "assistant", content: "磁盘空间充足。", jobId: "job-1" } },
  ];

  function shape(items: ChatItem[]): string[] {
    return items.map((item) => (item.role === "tool" ? `tool:${item.callId}` : `${item.role}:${"text" in item ? item.text : ""}`));
  }

  it("renders a completed run's interim text before its tool cards", () => {
    expect(shape(historyToItems(createConversation(), messages))).toEqual([
      "user:查看磁盘",
      "assistant:先看一下分区。",
      "tool:c1",
      "assistant:磁盘空间充足。",
    ]);
  });

  it("skips interim rows of a replayable run, whose text comes back from its events", () => {
    const unfinished = messages.slice(0, 4);
    expect(shape(historyToItems(createConversation(), unfinished, new Set(["job-1"])))).toEqual(["user:查看磁盘"]);
  });
});

describe("partial assistant answers in history", () => {
  const messages = [
    { id: "m1", role: "user", content: { role: "user", content: "查看磁盘", jobId: "job-1" } },
    { id: "m2", role: "assistant", content: { role: "assistant", content: "先看一下分区。", jobId: "job-1", interim: true } },
    { id: "m3", role: "toolCall", content: { type: "toolCall", jobId: "job-1", id: "c1", name: "exec_commands" } },
    { id: "m4", role: "toolResult", content: { type: "toolResult", jobId: "job-1", id: "c1", tool: "exec_commands", ok: true, summary: "df 完成" } },
    { id: "m5", role: "assistant", content: { role: "assistant", content: "磁盘快满了，", jobId: "job-1", partial: true } },
  ];

  it("shows the partial answer of an old run as interrupted assistant text after its tools", () => {
    const items = historyToItems(createConversation(), messages);
    expect(items.map((item) => item.role === "tool" ? `tool:${item.callId}` : `${item.role}:${"text" in item ? item.text : ""}`)).toEqual([
      "user:查看磁盘",
      "assistant:先看一下分区。",
      "tool:c1",
      "assistant:磁盘快满了，",
    ]);
    const partial = items[items.length - 1];
    expect(partial.role === "assistant" && partial.interrupted).toBe(true);
    const interim = items[1];
    expect(interim.role === "assistant" && interim.interrupted).toBeFalsy();
  });

  it("skips the partial answer of a replayable run, whose text comes back from its events", () => {
    const items = historyToItems(createConversation(), messages, new Set(["job-1"]));
    expect(items.filter((item) => item.role === "assistant")).toEqual([]);
  });

  it("ignores a blank partial answer", () => {
    const blank = [messages[0], { id: "m9", role: "assistant", content: { role: "assistant", content: " \n", jobId: "job-1", partial: true } }];
    expect(historyToItems(createConversation(), blank).map((item) => item.role)).toEqual(["user"]);
  });
});
