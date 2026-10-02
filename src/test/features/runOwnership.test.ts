import { describe, expect, it } from "vitest";
import {
  aiRunBlocksStart,
  bindAiRunJob,
  completeRunCancellation,
  createAiRun,
  finishAiRunSpawn,
  isCurrentAiRun,
  settleAiRun,
} from "../../features/ai/runOwnership";

describe("AI run ownership", () => {
  it("makes the previous job unusable at the start of a new generation", () => {
    const a = bindAiRunJob(createAiRun(1), "job-a");
    const b = createAiRun(2);
    expect(b.jobId).toBeNull();
    expect(isCurrentAiRun(b, 2)).toBe(true);
    expect(isCurrentAiRun(b, a.generation)).toBe(false);
    expect(aiRunBlocksStart(b)).toBe(true);
  });

  it("keeps early terminal events busy until spawn finishes", () => {
    const earlyDone = settleAiRun(createAiRun(1));
    expect(earlyDone.spawnPending).toBe(true);
    expect(aiRunBlocksStart(earlyDone)).toBe(true);
    const spawned = finishAiRunSpawn(earlyDone);
    expect(spawned.jobId).toBeNull();
    expect(aiRunBlocksStart(spawned)).toBe(false);
  });

  it("binds ids only for the active run and blocks until settled", () => {
    const bound = bindAiRunJob(createAiRun(3), "job-c");
    expect(bound).toMatchObject({ jobId: "job-c", spawnPending: false, settled: false });
    expect(aiRunBlocksStart(bound)).toBe(true);
    expect(aiRunBlocksStart(settleAiRun(bound))).toBe(false);
  });

  it("waits for takeover terminal cleanup after cancel instead of settling early", () => {
    const takeover = bindAiRunJob(createAiRun(4, "takeover"), "job-takeover");
    const cancelled = completeRunCancellation(takeover);
    expect(cancelled.waitForTerminal).toBe(true);
    expect(cancelled.run.settled).toBe(false);
    expect(aiRunBlocksStart(cancelled.run)).toBe(true);
    expect(aiRunBlocksStart(settleAiRun(cancelled.run))).toBe(false);

    const chat = completeRunCancellation(bindAiRunJob(createAiRun(5), "job-chat"));
    expect(chat.waitForTerminal).toBe(false);
    expect(chat.run.settled).toBe(true);
  });
});
