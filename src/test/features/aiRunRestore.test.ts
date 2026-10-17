import { afterEach, describe, expect, it, vi } from "vitest";
import {
  confirmConversationRunsStopped,
  findResumableRun,
  isRunTerminal,
  latestRunUsage,
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
    cacheCreationTokens: 0,
    latencyMs: 0,
    retries: 0,
    failures: 0,
    createdAt: 1000,
    updatedAt: 1000,
    ...overrides,
  };
}

describe("isRunTerminal", () => {
  it("accepts only a terminal status or an explicit finish timestamp", () => {
    for (const status of ["completed", "canceled", "failed", "expired", "superseded"]) {
      expect(isRunTerminal(runOf({ status }))).toBe(true);
    }
    expect(isRunTerminal(runOf({ status: "interrupted", finishedAt: 2000 }))).toBe(true);
    expect(isRunTerminal(runOf({ status: "running" }))).toBe(false);
    expect(isRunTerminal(runOf({ status: "interrupted" }))).toBe(false);
    expect(isRunTerminal(runOf({ status: "unknown" }))).toBe(false);
  });
});

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

describe("latestRunUsage", () => {
  it("restores the most recent usage and skips newer runs without usage events", async () => {
    const runs = [
      runOf({ id: "new-no-usage", createdAt: 3000 }),
      runOf({ id: "old-with-usage", createdAt: 2000 }),
      runOf({ id: "older-with-usage", createdAt: 1000 }),
    ];
    const usage = await latestRunUsage(runs, async (jobId) => ({
      events:
        jobId === "old-with-usage"
          ? [
              { type: "usage", promptTokens: 100, completionTokens: 20, cachedTokens: 30, contextWindow: 1000 },
              { type: "usage", promptTokens: 120, completionTokens: 25, cachedTokens: 40, contextWindow: 2000 },
            ]
          : [],
      failed: false,
    }));
    expect(usage).toEqual({ promptTokens: 120, completionTokens: 25, cachedTokens: 40, contextWindow: 2000 });
  });

  it("restores the latest main snapshot and accumulates summary calls once across recovery", async () => {
    const runs = [runOf({ id: "recovery", tokensIn: 9999, tokensOut: 8888 })];
    const usage = await latestRunUsage(runs, async () => ({
      events: [
        { type: "usage", channel: "main", callId: "main:agent-old", promptTokens: 100, completionTokens: 20, cachedTokens: 30, contextWindow: 1000 },
        { type: "usage", channel: "summary", callId: "summary:agent-old-1", promptTokens: 30, completionTokens: 5, cachedTokens: 3, contextWindow: 1000 },
        { type: "usage", channel: "summary", callId: "summary:agent-old-2", promptTokens: 40, completionTokens: 7, cachedTokens: 4, contextWindow: 1000 },
        { type: "usage", channel: "summary", callId: "summary:agent-old-1", promptTokens: 30, completionTokens: 5, cachedTokens: 3, contextWindow: 1000 },
        { type: "usage", channel: "main", callId: "main:agent-resumed", promptTokens: 120, completionTokens: 25, cachedTokens: 40, contextWindow: 2000 },
        { type: "usage", channel: "summary", callId: "summary:agent-failed", promptTokens: 50, completionTokens: 9, cachedTokens: 5, contextWindow: 2000 },
      ],
      failed: false,
    }));
    expect(usage).toEqual({
      promptTokens: 120,
      completionTokens: 25,
      cachedTokens: 40,
      contextWindow: 2000,
      summaryPromptTokens: 120,
      summaryCompletionTokens: 21,
    });
  });

  it("keeps an older main snapshot when a newer run has only summaries", async () => {
    const oldMain = { type: "usage", channel: "main", callId: "main:agent-old", promptTokens: 120, completionTokens: 25, cachedTokens: 40, contextWindow: 2000 };
    const firstSummary = { type: "usage", callId: "summary:provider-shared", promptTokens: 50, completionTokens: 9, cachedTokens: 5, contextWindow: 2000 };
    const secondSummary = { type: "usage", callId: "summary:provider-shared", promptTokens: 60, completionTokens: 11, cachedTokens: 6, contextWindow: 2000 };
    const usage = await latestRunUsage(
      [runOf({ id: "new-summary", createdAt: 2000 }), runOf({ id: "old-main", createdAt: 1000 })],
      async (jobId) => ({
        events: jobId === "new-summary" ? [firstSummary, secondSummary] : [oldMain],
        failed: false,
      }),
    );
    expect(usage).toEqual(liveUsage([oldMain, firstSummary, secondSummary]));
  });

  it("accumulates summaries across runs without adding run totals", async () => {
    const oldEvents = [
      { type: "usage", channel: "main", callId: "main:agent-old", promptTokens: 100, completionTokens: 20, cachedTokens: 30, contextWindow: 1000 },
      { type: "usage", channel: "summary", callId: "summary:agent-old-1", promptTokens: 30, completionTokens: 5, cachedTokens: 3, contextWindow: 1000 },
      { type: "usage", channel: "summary", callId: "summary:agent-old-2", promptTokens: 40, completionTokens: 7, cachedTokens: 4, contextWindow: 1000 },
    ];
    const newEvents = [
      { type: "usage", channel: "main", callId: "main:agent-new", promptTokens: 120, completionTokens: 25, cachedTokens: 40, contextWindow: 2000 },
      { type: "usage", channel: "summary", callId: "summary:agent-new", promptTokens: 50, completionTokens: 9, cachedTokens: 5, contextWindow: 2000 },
    ];
    const usage = await latestRunUsage(
      [runOf({ id: "new", createdAt: 2000, tokensIn: 9999, tokensOut: 8888 }), runOf({ id: "old", createdAt: 1000, tokensIn: 7777, tokensOut: 6666 })],
      async (jobId) => ({ events: jobId === "new" ? newEvents : oldEvents, failed: false }),
    );
    expect(usage).toEqual(liveUsage([...oldEvents, ...newEvents]));
  });
});

function liveUsage(events: Record<string, unknown>[]) {
  const stream = createConversationStream(() => () => undefined);
  stream.beginRun(1);
  for (const event of events) stream.pushEvent(1, event);
  const usage = stream.getState().usage;
  stream.dispose();
  return usage;
}

describe("run journal replay", () => {
  const journal = [
    { type: "status", seq: 1, phase: "thinking", turn: 0 },
    { type: "delta", seq: 2, text: "先看一下" },
    { type: "toolCall", seq: 3, id: "call-1", name: "exec_commands", display: "$ ls", args: {} },
    { type: "toolResult", seq: 4, id: "call-1", ok: true, summary: "ok", text: "file", truncated: false, exitCode: 0 },
    { type: "questionRequired", seq: 5, id: "ask-1", question: { question: "Continue?", options: ["Continue"] }, confirmationNonce: "nonce-1", requestId: "req-1", attempt: 1 },
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
    stream.pushEvent(7, { type: "done", seq: 6, answer: "Done", turns: 1, tokensIn: 3, tokensOut: 5 });
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
    stream.pushEvent(7, { type: "error", seq: 6, message: "AI task interrupted by application restart", retryable: true });
    const state = stream.getState();
    expect(state.attempts[0]?.outcome).toBe("error");
    expect(pendingInteraction(state, 7, "question")).toBeNull();
    stream.dispose();
  });
});

describe("confirmConversationRunsStopped", () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it("cancels only unfinished runs and confirms them after a later terminal query", async () => {
    const active = runOf({ id: "job-live", status: "running" });
    const runs = vi.fn()
      .mockResolvedValueOnce([active, runOf({ id: "job-done", status: "completed" })])
      .mockResolvedValueOnce([active])
      .mockResolvedValueOnce([runOf({ id: "job-live", status: "canceled", finishedAt: 2000 })]);
    const cancel = vi.fn().mockResolvedValue(undefined);

    await expect(confirmConversationRunsStopped("conv-1", { runs, cancel })).resolves.toBe("confirmed");
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(cancel).toHaveBeenCalledWith("job-live");
    expect(runs).toHaveBeenCalledTimes(3);
    expect(runs).toHaveBeenCalledWith("conv-1");
  });

  it("confirms a terminal run after cancel rejects not_found", async () => {
    const runs = vi.fn()
      .mockResolvedValueOnce([runOf({ id: "job-live", status: "running" })])
      .mockResolvedValueOnce([runOf({ id: "job-live", status: "canceled", finishedAt: 2000 })]);
    const cancel = vi.fn().mockRejectedValue(new Error("not_found"));

    await expect(confirmConversationRunsStopped("conv-1", { runs, cancel })).resolves.toBe("confirmed");
    expect(runs).toHaveBeenCalledTimes(2);
    expect(cancel).toHaveBeenCalledWith("job-live");
  });

  it("returns unconfirmed when cancel is rejected and the follow-up query is still running", async () => {
    const runs = vi.fn().mockResolvedValue([runOf({ id: "job-live", status: "running" })]);
    const cancel = vi.fn().mockRejectedValue(new Error("rejected"));

    await expect(confirmConversationRunsStopped("conv-1", { runs, cancel })).resolves.toBe("unconfirmed");
    expect(runs).toHaveBeenCalledTimes(2);
    expect(cancel).toHaveBeenCalledWith("job-live");
  });

  it("returns unconfirmed when the follow-up query after cancel rejection fails", async () => {
    const runs = vi.fn()
      .mockResolvedValueOnce([runOf({ id: "job-live", status: "running" })])
      .mockRejectedValue(new Error("offline"));
    const cancel = vi.fn().mockRejectedValue(new Error("not_found"));

    await expect(confirmConversationRunsStopped("conv-1", { runs, cancel })).resolves.toBe("unconfirmed");
    expect(runs).toHaveBeenCalledTimes(2);
  });

  it("returns unconfirmed when the terminal query fails", async () => {
    const runs = vi.fn()
      .mockResolvedValueOnce([runOf({ id: "job-live", status: "running" })])
      .mockRejectedValue(new Error("offline"));
    const cancel = vi.fn().mockResolvedValue(undefined);

    await expect(confirmConversationRunsStopped("conv-1", { runs, cancel })).resolves.toBe("unconfirmed");
    expect(runs).toHaveBeenCalledTimes(2);
  });

  it("returns unconfirmed instead of timing out as a success", async () => {
    vi.useFakeTimers();
    const runs = vi.fn().mockResolvedValue([runOf({ id: "job-live", status: "running" })]);
    const cancel = vi.fn().mockResolvedValue(undefined);
    const pending = confirmConversationRunsStopped("conv-1", { runs, cancel });
    await vi.waitFor(() => expect(cancel).toHaveBeenCalled());

    await vi.advanceTimersByTimeAsync(5000);
    await expect(pending).resolves.toBe("unconfirmed");
  });

  it("times out a cancel request that never settles", async () => {
    vi.useFakeTimers();
    const runs = vi.fn().mockResolvedValue([runOf({ id: "job-live", status: "running" })]);
    const cancel = vi.fn().mockImplementation(() => new Promise<void>(() => undefined));
    const pending = confirmConversationRunsStopped("conv-1", { runs, cancel });
    await vi.waitFor(() => expect(cancel).toHaveBeenCalled());

    await vi.advanceTimersByTimeAsync(5000);
    await expect(Promise.race([pending, Promise.resolve("still-pending")])).resolves.toBe("unconfirmed");
  });
});
