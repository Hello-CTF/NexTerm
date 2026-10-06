import { describe, expect, it } from "vitest";
import {
  appendHitlInterrupt,
  applyAiEvent,
  beginRun,
  createConversation,
  pendingInteraction,
  type ConfirmItem,
  type ConversationState,
  type QuestionItem,
} from "../../features/ai/conversation";
import {
  applyHitlReplay,
  hitlTerminalLabel,
  planHitlReplay,
} from "../../features/ai/hitlReplay";
import { createConversationStream } from "../../features/ai/conversationStream";
import type { AiHitlEventDto, AiHitlInterruptDto, AiHitlSnapshotDto } from "../../ipc/types";

function confirmEvent(id: string, nonce: string, requestId?: string) {
  return {
    type: "confirmRequired",
    id,
    tool: "exec_commands",
    rendered: `$ ${id}`,
    confirmationNonce: nonce,
    ...(requestId ? { requestId, attempt: 1 } : {}),
  };
}

function questionEvent(id: string, nonce: string, requestId?: string) {
  return {
    type: "questionRequired",
    id,
    question: { question: "继续吗？", options: ["继续"] },
    confirmationNonce: nonce,
    ...(requestId ? { requestId, attempt: 1 } : {}),
  };
}

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

function snapshotOf(
  status: string,
  pending: AiHitlInterruptDto[],
  extra: Partial<AiHitlSnapshotDto> = {},
): AiHitlSnapshotDto {
  return { runId: "job-1", checkpointId: "job-1", status, attempt: 1, seq: 1, pending, ...extra };
}

function eventOf(overrides: Partial<AiHitlEventDto> = {}): AiHitlEventDto {
  return {
    runId: "job-1",
    checkpointId: "job-1",
    kind: "interrupted",
    reason: "interrupted",
    attempt: 1,
    seq: 1,
    ...overrides,
  };
}

function withConfirmCard(requestId: string | null = "req-1"): ConversationState {
  let state = createConversation();
  state = beginRun(state, 1);
  return applyAiEvent(state, 1, confirmEvent("call-1", "nonce-1", requestId ?? undefined)).state;
}

function resolutionsOf(state: ConversationState): (string | undefined)[] {
  return state.items
    .filter((i) => i.role === "confirm" || i.role === "question")
    .map((i) => (i as ConfirmItem | QuestionItem).resolution);
}

describe("planHitlReplay", () => {
  it("captures the seq high-water mark and the open interaction identities", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 4);
    expect(plan.afterSeq).toBe(4);
    expect(plan.sweepable).toEqual([
      { itemId: "g1:confirm:call-1", requestId: "req-1", nonce: "nonce-1", callId: "call-1" },
    ]);
  });

  it("captures nothing for an unknown or settled generation", () => {
    expect(planHitlReplay(createConversation(), 9, 0).sweepable).toEqual([]);
  });
});

describe("applyHitlReplay: event folding by per-run seq", () => {
  it("closes the matching card on resumed and never reprocesses the same seq", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 0);
    const resumed = eventOf({ kind: "resumed", reason: "", requestId: "req-1", attempt: 2, seq: 2 });
    const first = applyHitlReplay(state, 1, plan, [resumed], null, 0);
    expect(first.changed).toBe(true);
    expect(first.lastSeq).toBe(2);
    expect(resolutionsOf(first.state)).toEqual(["已在服务端回答"]);

    const replay = applyHitlReplay(first.state, 1, plan, [resumed], null, first.lastSeq);
    expect(replay.changed).toBe(false);
    expect(replay.lastSeq).toBe(2);
    expect(resolutionsOf(replay.state)).toEqual(["已在服务端回答"]);
  });

  it("processes out-of-order events by seq and ignores invalid identities", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 0);
    const outOfOrder = [
      eventOf({ kind: "terminal", reason: "completed", attempt: 2, seq: 3 }),
      eventOf({ kind: "resumed", reason: "", requestId: "req-1", attempt: 2, seq: 2 }),
      eventOf({ kind: "terminal", reason: "failed", attempt: 2, seq: -1 }),
      eventOf({ kind: "terminal", reason: "failed", attempt: 2, seq: 1.5 }),
    ];
    const result = applyHitlReplay(state, 1, plan, outOfOrder, null, 0);
    expect(result.lastSeq).toBe(3);
    expect(resolutionsOf(result.state)).toEqual(["已在服务端回答"]);
  });

  it("closes every open card on terminal, including cards created after the plan", () => {
    let state = withConfirmCard();
    state = applyAiEvent(state, 1, questionEvent("q-9", "nonce-q", "req-q")).state;
    const plan = planHitlReplay(state, 1, 0);
    state = applyAiEvent(state, 1, confirmEvent("call-2", "nonce-2", "req-2")).state;
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [eventOf({ kind: "terminal", reason: "failed", attempt: 2, seq: 2 })],
      null,
      0,
    );
    expect(result.changed).toBe(true);
    expect(resolutionsOf(result.state)).toEqual([
      "本轮已出错，交互已关闭",
      "本轮已出错，交互已关闭",
      "本轮已出错，交互已关闭",
    ]);
  });

  it("maps terminal reasons to the same closing labels as the local paths", () => {
    expect(hitlTerminalLabel("completed")).toBe("本轮已结束，交互已关闭");
    expect(hitlTerminalLabel("canceled")).toBe("本轮已停止，交互已关闭");
    expect(hitlTerminalLabel("failed")).toBe("本轮已出错，交互已关闭");
    expect(hitlTerminalLabel("expired")).toBe("等待已过期，交互已关闭");
    expect(hitlTerminalLabel("interrupted")).toBe("本轮已结束，交互已关闭");
  });

  it("leaves cards of other generations untouched", () => {
    let state = withConfirmCard();
    state = beginRun(state, 2);
    state = applyAiEvent(state, 2, confirmEvent("call-2", "nonce-2", "req-2")).state;
    const plan = planHitlReplay(state, 2, 0);
    const result = applyHitlReplay(
      state,
      2,
      plan,
      [eventOf({ kind: "terminal", reason: "completed", attempt: 2, seq: 2 })],
      null,
      0,
    );
    const confirmCards = result.state.items.filter((i) => i.role === "confirm");
    expect(confirmCards[0]).toMatchObject({ callId: "call-1" });
    expect((confirmCards[0] as ConfirmItem).resolution).toBeUndefined();
    expect(confirmCards[1]).toMatchObject({ callId: "call-2", resolution: "本轮已结束，交互已关闭" });
  });
});

describe("applyHitlReplay: snapshot reconciliation", () => {
  it("synthesizes a confirm card from the pending list with full identity", () => {
    const state = beginRun(createConversation(), 1);
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [],
      snapshotOf("interrupted", [interruptOf()]),
      0,
    );
    expect(result.changed).toBe(true);
    const card = pendingInteraction(result.state, 1, "confirm");
    expect(card).toMatchObject({
      callId: "call-1",
      nonce: "nonce-1",
      requestId: "req-1",
      hitlAttempt: 1,
      tool: "exec_commands",
      rendered: JSON.stringify({ command: "rm x" }, null, 2),
      preview: null,
    });
    expect(result.lastSeq).toBe(1);
  });

  it("synthesizes a question card from the question payload", () => {
    const state = beginRun(createConversation(), 1);
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [],
      snapshotOf("interrupted", [
        interruptOf({
          id: "req-q",
          callId: "q-1",
          tool: "ask_user",
          kind: "question",
          question: { id: "req-q", text: "继续吗？", options: ["继续", "停止"] },
        }),
      ]),
      0,
    );
    const card = pendingInteraction(result.state, 1, "question");
    expect(card).toMatchObject({
      callId: "q-1",
      question: "继续吗？",
      options: ["继续", "停止"],
      requestId: "req-q",
    });
  });

  it("does not duplicate a card the live stream already created for the same request", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [],
      snapshotOf("interrupted", [interruptOf()]),
      0,
    );
    const confirms = result.state.items.filter((i) => i.role === "confirm");
    expect(confirms).toHaveLength(1);
    expect(result.changed).toBe(false);
    const replayed = applyAiEvent(result.state, 1, confirmEvent("call-1", "nonce-1", "req-1"));
    expect(replayed.accepted).toBe(false);
  });

  it("sweeps a plan-time card the server no longer has pending", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(state, 1, plan, [], snapshotOf("running", []), 0);
    expect(result.changed).toBe(true);
    expect(resolutionsOf(result.state)).toEqual(["该交互已在服务端结束"]);
  });

  it("sweeps a plan-time card without a requestId and re-appends the pending request", () => {
    const state = withConfirmCard(null);
    const plan = planHitlReplay(state, 1, 0);
    expect(plan.sweepable[0]?.requestId).toBeUndefined();
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [],
      snapshotOf("interrupted", [interruptOf()]),
      0,
    );
    expect(result.changed).toBe(true);
    const cards = result.state.items.filter((i) => i.role === "confirm");
    expect(cards).toHaveLength(2);
    expect(cards[0]).toMatchObject({ resolution: "该交互已在服务端结束" });
    expect(cards[1]).toMatchObject({ requestId: "req-1" });
    expect(pendingInteraction(result.state, 1, "confirm")?.id).toBe(cards[1].id);
  });

  it("never sweeps a racing card created after the plan while the run is alive", () => {
    const state = beginRun(createConversation(), 1);
    const plan = planHitlReplay(state, 1, 0);
    const raced = applyAiEvent(state, 1, confirmEvent("call-2", "nonce-2", "req-2")).state;
    const result = applyHitlReplay(raced, 1, plan, [], snapshotOf("running", []), 0);
    expect(result.changed).toBe(false);
    expect(pendingInteraction(result.state, 1, "confirm")).not.toBeNull();
  });

  it("closes racing cards too when the snapshot says the run is over", () => {
    const state = beginRun(createConversation(), 1);
    const plan = planHitlReplay(state, 1, 0);
    const raced = applyAiEvent(state, 1, confirmEvent("call-2", "nonce-2", "req-2")).state;
    const result = applyHitlReplay(
      raced,
      1,
      plan,
      [],
      snapshotOf("completed", [], {
        seq: 3,
        terminal: eventOf({ kind: "terminal", reason: "completed", attempt: 2, seq: 3 }),
      }),
      0,
    );
    expect(resolutionsOf(result.state)).toEqual(["本轮已结束，交互已关闭"]);
  });

  it("labels a swept card with the resumed fact when the events proved consumption", () => {
    const state = withConfirmCard();
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [eventOf({ kind: "resumed", reason: "", requestId: "req-1", attempt: 2, seq: 2 })],
      snapshotOf("running", [], { seq: 2 }),
      0,
    );
    expect(resolutionsOf(result.state)).toEqual(["已在服务端回答"]);
  });

  it("does not revive cards for an unknown or settled generation", () => {
    const settled = applyAiEvent(withConfirmCard(), 1, { type: "done", answer: "完" }).state;
    const plan = planHitlReplay(settled, 1, 0);
    const result = applyHitlReplay(
      settled,
      1,
      plan,
      [],
      snapshotOf("interrupted", [interruptOf()]),
      0,
    );
    expect(result.changed).toBe(false);
    expect(result.state).toBe(settled);
    expect(pendingInteraction(result.state, 1, "confirm")).toBeNull();
    expect(result.lastSeq).toBe(1);
  });

  it("ignores unknown pending kinds instead of inventing cards", () => {
    const state = beginRun(createConversation(), 1);
    const plan = planHitlReplay(state, 1, 0);
    const result = applyHitlReplay(
      state,
      1,
      plan,
      [],
      snapshotOf("interrupted", [interruptOf({ kind: "future_kind" })]),
      0,
    );
    expect(result.changed).toBe(false);
  });
});

describe("appendHitlInterrupt identity", () => {
  it("reuses the stream id scheme so a late live event dedups against it", () => {
    let state = beginRun(createConversation(), 1);
    state = appendHitlInterrupt(state, 1, interruptOf());
    const card = pendingInteraction(state, 1, "confirm");
    expect(card?.id).toBe("g1:confirm:call-1");
    const dup = applyAiEvent(state, 1, confirmEvent("call-1", "nonce-1", "req-1"));
    expect(dup.accepted).toBe(false);
    expect(state.items.filter((i) => i.role === "confirm")).toHaveLength(1);
  });

  it("falls back to a fresh local id when the kernel id is taken by a settled card", () => {
    let state = withConfirmCard();
    state = applyAiEvent(state, 1, { type: "done", answer: "完" }).state;
    state = beginRun(state, 2);
    state = appendHitlInterrupt(state, 2, interruptOf({ id: "req-2", nonce: "nonce-2" }));
    const cards = state.items.filter((i) => i.role === "confirm");
    expect(cards).toHaveLength(2);
    expect(cards[1].id).not.toBe(cards[0].id);
    expect(cards[1]).toMatchObject({ requestId: "req-2", nonce: "nonce-2" });
  });

  it("refuses to append into a settled generation", () => {
    const settled = applyAiEvent(withConfirmCard(), 1, { type: "done", answer: "完" }).state;
    expect(appendHitlInterrupt(settled, 1, interruptOf())).toBe(settled);
  });
});

describe("conversationStream HITL replay", () => {
  it("tracks the seq high-water mark per generation and publishes only on change", () => {
    const s = createConversationStream(() => () => {});
    s.beginRun(1);
    let publishes = 0;
    s.subscribe(() => {
      publishes += 1;
    });
    expect(s.hitlSeq(1)).toBe(0);
    const plan = s.planHitlReplay(1);
    expect(plan.afterSeq).toBe(0);

    s.applyHitlReplay(1, plan, [], snapshotOf("interrupted", [interruptOf()]));
    expect(publishes).toBe(1);
    expect(s.hitlSeq(1)).toBe(1);
    expect(pendingInteraction(s.getState(), 1, "confirm")).not.toBeNull();

    s.applyHitlReplay(1, s.planHitlReplay(1), [], snapshotOf("interrupted", [interruptOf()]));
    expect(publishes).toBe(1);
    expect(s.hitlSeq(1)).toBe(1);
    expect(s.getState().items.filter((i) => i.role === "confirm")).toHaveLength(1);
  });

  it("drops the seq-tracking state when the generation settles or resets", () => {
    const s = createConversationStream(() => () => {});
    s.beginRun(1);
    s.applyHitlReplay(1, s.planHitlReplay(1), [], snapshotOf("interrupted", [], { seq: 5 }));
    expect(s.hitlSeq(1)).toBe(5);
    s.pushEvent(1, { type: "done", answer: "完" });
    expect(s.hitlSeq(1)).toBe(0);

    s.beginRun(2);
    s.applyHitlReplay(2, s.planHitlReplay(2), [], snapshotOf("interrupted", [], { seq: 3 }));
    expect(s.hitlSeq(2)).toBe(3);
    s.reset();
    expect(s.hitlSeq(2)).toBe(0);
  });

  it("flushes pending stream text before folding the replay", () => {
    const frames: (() => void)[] = [];
    const s = createConversationStream((cb) => {
      frames.push(cb);
      return () => {};
    });
    s.beginRun(1);
    s.pushEvent(1, { type: "delta", text: "半截输出" });
    s.applyHitlReplay(1, s.planHitlReplay(1), [], snapshotOf("interrupted", [interruptOf()]));
    const roles = s.getState().items.map((i) => i.role);
    expect(roles).toEqual(["assistant", "confirm"]);
  });
});
