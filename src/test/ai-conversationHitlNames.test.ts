import { describe, expect, it } from "vitest";
import {
  appendHitlInterrupt,
  applyAiEvent,
  beginRun,
  createConversation,
  pendingInteraction,
} from "../features/ai/conversation";
import type { AiHitlInterruptDto } from "../ipc/types";

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

describe("appendHitlInterrupt requiredEnvironmentNames fold", () => {
  function interruptOf(overrides: Partial<AiHitlInterruptDto> = {}): AiHitlInterruptDto {
    return {
      id: "req-1",
      runId: "job-1",
      checkpointId: "job-1",
      checkpointHash: "hash-1",
      targetId: "target-1",
      callId: "call-1",
      tool: "exec_commands",
      kind: "confirm",
      parameters: { command: "rm x" },
      parameterHash: "phash-1",
      nonce: "nonce-1",
      createdAt: "2026-10-03T10:00:00Z",
      expiresAt: "2026-10-03T10:05:00Z",
      attempt: 1,
      seq: 1,
      ...overrides,
    };
  }

  it("restores the typed environment names on the card from a snapshot interrupt", () => {
    let state = beginRun(createConversation(), 1);
    state = appendHitlInterrupt(state, 1, interruptOf({ requiredEnvironmentNames: ["production"] }));
    const card = pendingInteraction(state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toEqual(["production"]);
  });

  it("restores a multi-name snapshot challenge", () => {
    let state = beginRun(createConversation(), 1);
    state = appendHitlInterrupt(state, 1, interruptOf({ requiredEnvironmentNames: ["live", "production"] }));
    const card = pendingInteraction(state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toEqual(["live", "production"]);
  });

  it("leaves the field undefined for a legacy snapshot interrupt without it", () => {
    let state = beginRun(createConversation(), 1);
    state = appendHitlInterrupt(state, 1, interruptOf());
    const card = pendingInteraction(state, 1, "confirm");
    expect(card?.requiredEnvironmentNames).toBeUndefined();
    expect("requiredEnvironmentNames" in (card ?? {})).toBe(false);
  });

  it("fails closed to undefined on a malformed snapshot field", () => {
    let state = beginRun(createConversation(), 1);
    for (const malformed of [
      { requiredEnvironmentNames: [] },
      { requiredEnvironmentNames: "production" },
      { requiredEnvironmentNames: [42] },
      { requiredEnvironmentNames: ["production", "  "] },
    ]) {
      state = appendHitlInterrupt(state, 1, interruptOf(malformed as unknown as Partial<AiHitlInterruptDto>));
      const card = pendingInteraction(state, 1, "confirm");
      expect(card?.requiredEnvironmentNames).toBeUndefined();
    }
  });
});
