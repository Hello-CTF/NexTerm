import { describe, expect, it } from "vitest";
import {
  applyAiEvent,
  beginRun,
  createConversation,
  type ChatItem,
  type ConversationState,
} from "../features/ai/conversation";

function outcomeOf(state: ConversationState): Extract<ChatItem, { role: "outcome" }> | undefined {
  return state.items.find((item): item is Extract<ChatItem, { role: "outcome" }> => item.role === "outcome");
}

describe("MaxIterations terminal outcome", () => {
  it("marks the outcome item explicitly and keeps it non-retryable", () => {
    const state = beginRun(createConversation(), 1);
    const result = applyAiEvent(state, 1, {
      type: "error",
      message: "AI task reached the maximum iteration count (24) and was stopped",
      retryable: false,
      maxIterations: true,
    });
    expect(result.accepted).toBe(true);
    expect(result.terminal).toBe("error");
    expect(outcomeOf(result.state)).toMatchObject({
      outcome: "error",
      retryable: false,
      maxIterations: true,
      text: "AI task reached the maximum iteration count (24) and was stopped",
    });
  });

  it("does not mark plain errors as maxIterations", () => {
    const state = beginRun(createConversation(), 1);
    const retryable = applyAiEvent(state, 1, { type: "error", message: "Network interrupted", retryable: true });
    expect(outcomeOf(retryable.state)).toMatchObject({ outcome: "error", retryable: true });
    expect(outcomeOf(retryable.state)?.maxIterations).toBeUndefined();
    const fatal = applyAiEvent(beginRun(createConversation(), 1), 1, {
      type: "error",
      message: "Authentication failed",
      retryable: false,
    });
    expect(outcomeOf(fatal.state)?.maxIterations).toBeUndefined();
  });

  it("closes pending interactions and settles open tools on maxIterations", () => {
    let state = beginRun(createConversation(), 1);
    state = applyAiEvent(state, 1, {
      type: "confirmRequired",
      id: "call-1",
      tool: "exec_commands",
      rendered: "$ rm -rf /tmp/x",
      confirmationNonce: "n-1",
    }).state;
    state = applyAiEvent(state, 1, {
      type: "toolCall",
      id: "call-2",
      name: "exec_commands",
      display: "systemctl reload nginx",
    }).state;
    const result = applyAiEvent(state, 1, {
      type: "error",
      message: "AI task reached the maximum iteration count (2) and was stopped",
      retryable: false,
      maxIterations: true,
    });
    const confirm = result.state.items.find((item) => item.role === "confirm");
    expect(confirm && confirm.role === "confirm" ? confirm.resolution : undefined).toBe(
      "本轮已出错，无需再处理",
    );
    const tool = result.state.items.find((item) => item.role === "tool");
    expect(tool && tool.role === "tool" ? tool.summary : undefined).toBe("本轮出错中断");
  });
});
