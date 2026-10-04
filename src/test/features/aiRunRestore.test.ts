import { describe, expect, it } from "vitest";
import {
  findResumableRun,
  pendingDeadline,
  replayableRuns,
  resumableCandidates,
  snapshotHasPending,
} from "../../features/ai/runRestore";
import { createConversationStream } from "../../features/ai/conversationStream";
import { pendingInteraction } from "../../features/ai/conversation";
import type { AiHitlSnapshotDto, AiRunDto } from "../../ipc/types";

function runOf(overrides: Partial<AiRunDto>): AiRunDto {
  return {
    id: "run-1",
    conversationId: "conv-1",
    status: "completed",
    attempt: 1,
    seq: 0,
    planMode: false,
    source: "chat",
    answer: "",
    turns: 0,
    tokensIn: 0,
    tokensOut: 0,
    createdAt: 1000,
    updatedAt: 1000,
    ...overrides,
  };
}

describe("replayableRuns", () => {
  it("keeps unfinished runs oldest first", () => {
    const runs = [
      runOf({ id: "b", status: "interrupted", createdAt: 2000 }),
      runOf({ id: "a", status: "expired", createdAt: 1000 }),
      runOf({ id: "c", status: "completed", createdAt: 3000 }),
      runOf({ id: "d", status: "failed", createdAt: 2500 }),
    ];
    expect(replayableRuns(runs).map((run) => run.id)).toEqual(["a", "b", "d"]);
  });

  it("does not mutate the input", () => {
    const runs = [runOf({ id: "b", status: "interrupted", createdAt: 2000 }), runOf({ id: "a", status: "interrupted", createdAt: 1000 })];
    replayableRuns(runs);
    expect(runs.map((run) => run.id)).toEqual(["b", "a"]);
  });
});

describe("resumableCandidates", () => {
  it("keeps only unfinished interrupted runs, newest first", () => {
    const runs = [
      runOf({ id: "old", status: "interrupted", createdAt: 1000 }),
      runOf({ id: "finished", status: "interrupted", createdAt: 4000, finishedAt: 4000 }),
      runOf({ id: "new", status: "interrupted", createdAt: 3000 }),
      runOf({ id: "expired", status: "expired", createdAt: 5000 }),
      runOf({ id: "running", status: "running", createdAt: 4500 }),
    ];
    expect(resumableCandidates(runs).map((run) => run.id)).toEqual(["new", "old"]);
  });

  it("returns an empty list without candidates", () => {
    expect(resumableCandidates([runOf({ status: "completed" })])).toEqual([]);
    expect(resumableCandidates([])).toEqual([]);
  });
});

describe("findResumableRun", () => {
  const snapshotOf =
    (pendingByJob: Record<string, AiHitlSnapshotDto | null>) => async (jobId: string) =>
      pendingByJob[jobId] ?? null;

  it("picks the older run when only it is still pending", async () => {
    const pending = { id: "req-1" } as unknown as AiHitlSnapshotDto["pending"][number];
    const runs = [
      runOf({ id: "old", status: "interrupted", createdAt: 1000 }),
      runOf({ id: "new", status: "interrupted", createdAt: 3000 }),
    ];
    const found = await findResumableRun(
      runs,
      snapshotOf({
        new: { runId: "new", checkpointId: "new", status: "interrupted", attempt: 1, seq: 1, pending: [] },
        old: { runId: "old", checkpointId: "old", status: "interrupted", attempt: 1, seq: 1, pending: [pending] },
      }),
    );
    expect(found?.run.id).toBe("old");
    expect(found?.snapshot.runId).toBe("old");
  });

  it("skips runs whose snapshot is terminal or missing", async () => {
    const pending = { id: "req-1" } as unknown as AiHitlSnapshotDto["pending"][number];
    const runs = [
      runOf({ id: "terminal", status: "interrupted", createdAt: 3000 }),
      runOf({ id: "pending", status: "interrupted", createdAt: 1000 }),
    ];
    const found = await findResumableRun(
      runs,
      snapshotOf({
        terminal: {
          runId: "terminal",
          checkpointId: "terminal",
          status: "expired",
          attempt: 1,
          seq: 2,
          pending: [],
          terminal: { runId: "terminal", checkpointId: "terminal", kind: "terminal", reason: "expired", attempt: 1, seq: 2 },
        },
        pending: { runId: "pending", checkpointId: "pending", status: "interrupted", attempt: 1, seq: 1, pending: [pending] },
      }),
    );
    expect(found?.run.id).toBe("pending");
  });

  it("returns null when no candidate is pending", async () => {
    const runs = [runOf({ id: "a", status: "interrupted", createdAt: 1000 })];
    expect(await findResumableRun(runs, snapshotOf({}))).toBeNull();
  });

  it("snapshotHasPending requires pending without terminal", () => {
    expect(snapshotHasPending(null)).toBe(false);
    expect(snapshotHasPending({ runId: "x", checkpointId: "x", status: "interrupted", attempt: 1, seq: 1, pending: [] })).toBe(false);
    expect(
      snapshotHasPending({
        runId: "x",
        checkpointId: "x",
        status: "interrupted",
        attempt: 1,
        seq: 1,
        pending: [{} as AiHitlSnapshotDto["pending"][number]],
      }),
    ).toBe(true);
  });
});

describe("pendingDeadline", () => {
  it("returns the earliest expiry and rejects garbage", () => {
    expect(pendingDeadline({ runId: "x", checkpointId: "x", status: "interrupted", attempt: 1, seq: 1, pending: [] })).toBeNull();
    const early = new Date("2026-10-03T10:01:00Z").toISOString();
    const late = new Date("2026-10-03T10:05:00Z").toISOString();
    expect(
      pendingDeadline({
        runId: "x",
        checkpointId: "x",
        status: "interrupted",
        attempt: 1,
        seq: 1,
        pending: [
          { expiresAt: late } as AiHitlSnapshotDto["pending"][number],
          { expiresAt: early } as AiHitlSnapshotDto["pending"][number],
        ],
      }),
    ).toBe(Date.parse(early));
    expect(
      pendingDeadline({
        runId: "x",
        checkpointId: "x",
        status: "interrupted",
        attempt: 1,
        seq: 1,
        pending: [{ expiresAt: "not-a-date" } as AiHitlSnapshotDto["pending"][number]],
      }),
    ).toBeNull();
  });
});

describe("run journal replay", () => {
  const journal = [
    { type: "status", seq: 1, phase: "thinking", turn: 0 },
    { type: "delta", seq: 2, text: "先看一下" },
    { type: "toolCall", seq: 3, id: "call-1", name: "exec_commands", display: "$ ls", args: {} },
    { type: "toolResult", seq: 4, id: "call-1", ok: true, summary: "ok", text: "file", truncated: false, exitCode: 0 },
    { type: "questionRequired", seq: 5, id: "ask-1", question: { question: "继续吗？", options: ["继续"] }, confirmationNonce: "nonce-1", requestId: "req-1", attempt: 1 },
  ];

  it("rebuilds the transcript and keeps the pending question answerable", () => {
    const stream = createConversationStream(() => () => undefined);
    stream.beginRun(7, "chat");
    stream.bindJob(7, "job-1");
    for (const event of journal) stream.pushEvent(7, event);
    stream.flush();
    const state = stream.getState();
    expect(state.items.filter((item) => item.role === "tool")).toHaveLength(1);
    expect(state.items.filter((item) => item.role === "assistant").map((item) => (item as { text: string }).text)).toEqual(["先看一下"]);
    const card = pendingInteraction(state, 7, "question");
    expect(card?.nonce).toBe("nonce-1");
    expect(card?.jobId).toBe("job-1");
    stream.dispose();
  });

  it("dedups replayed events by seq and settles on the live terminal event", () => {
    const stream = createConversationStream(() => () => undefined);
    stream.beginRun(7, "chat");
    stream.bindJob(7, "job-1");
    for (const event of journal) stream.pushEvent(7, event);
    for (const event of journal) stream.pushEvent(7, event);
    stream.flush();
    expect(stream.getState().items.filter((item) => item.role === "tool")).toHaveLength(1);
    stream.pushEvent(7, { type: "done", seq: 6, answer: "完成了", turns: 1, tokensIn: 3, tokensOut: 5 });
    const state = stream.getState();
    expect(state.attempts[0]?.outcome).toBe("done");
    expect(state.items.filter((item) => item.role === "outcome")).toHaveLength(1);
    stream.dispose();
  });

  it("closes replayed interactions on a journaled terminal error", () => {
    const stream = createConversationStream(() => () => undefined);
    stream.beginRun(7, "chat");
    stream.bindJob(7, "job-1");
    for (const event of journal) stream.pushEvent(7, event);
    stream.pushEvent(7, { type: "error", seq: 6, message: "AI 任务因应用重启而中断", retryable: true });
    const state = stream.getState();
    expect(state.attempts[0]?.outcome).toBe("error");
    expect(pendingInteraction(state, 7, "question")).toBeNull();
    stream.dispose();
  });
});
