import { describe, expect, it } from "vitest";
import {
  applyAiEvent,
  beginRun,
  createConversation,
  pendingInteraction,
} from "../features/ai/conversation";

function confirmEvent(id: string, nonce: string, extra: Record<string, unknown> = {}) {
  return {
    type: "confirmRequired",
    id,
    tool: "exec_commands",
    rendered: `$ ${id}`,
    confirmationNonce: nonce,
    ...extra,
  };
}

describe("confirmRequired requiredEnvironmentNames fold", () => {
  it("captures the typed environment names on the pending card", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    const result = applyAiEvent(state, 1, confirmEvent("call-1", "n-1", {
      requiredEnvironmentNames: ["production"],
    }));
    expect(result.accepted).toBe(true);
    const card = pendingInteraction(result.state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toEqual(["production"]);
  });

  it("captures a multi-name production challenge", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    const result = applyAiEvent(state, 1, confirmEvent("call-2", "n-2", {
      requiredEnvironmentNames: ["live", "production"],
    }));
    const card = pendingInteraction(result.state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toEqual(["live", "production"]);
  });

  it("leaves the field undefined for legacy events without it", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    const result = applyAiEvent(state, 1, confirmEvent("call-3", "n-3"));
    const card = pendingInteraction(result.state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toBeUndefined();
    expect("requiredEnvironmentNames" in (card ?? {})).toBe(false);
  });

  it("fails closed to undefined on malformed fields", () => {
    let state = createConversation();
    state = beginRun(state, 1);
    for (const malformed of [
      { requiredEnvironmentNames: [] },
      { requiredEnvironmentNames: "production" },
      { requiredEnvironmentNames: [42] },
      { requiredEnvironmentNames: ["production", "  "] },
    ]) {
      const result = applyAiEvent(state, 1, confirmEvent("call-4", "n-4", malformed));
      const card = pendingInteraction(result.state, 1, "confirm");
      expect(card?.requiredEnvironmentNames).toBeUndefined();
    }
  });
});
