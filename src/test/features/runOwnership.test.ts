import { describe, expect, it } from "vitest";
import {
  aiRunBlocksStart,
  bindAiRunJob,
  confirmAiRunTerminal,
  createAiRun,
  finishAiRunSpawn,
  isCurrentAiRun,
  markAiRunCancellationUnconfirmed,
  requestAiRunCancellation,
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

  it("keeps cancellation pending for chat and takeover until a terminal is confirmed", () => {
    for (const kind of ["chat", "takeover"] as const) {
      const bound = bindAiRunJob(createAiRun(4, kind), `job-${kind}`);
      const cancelling = requestAiRunCancellation(bound);
      expect(cancelling).toMatchObject({ settled: false, cancellation: "cancelling" });
      expect(aiRunBlocksStart(cancelling)).toBe(true);

      const unconfirmed = markAiRunCancellationUnconfirmed(cancelling);
      expect(unconfirmed).toMatchObject({ settled: false, cancellation: "unconfirmed" });
      expect(aiRunBlocksStart(unconfirmed)).toBe(true);

      const terminal = confirmAiRunTerminal(unconfirmed);
      expect(terminal).toMatchObject({ settled: true, cancellation: "idle" });
      expect(aiRunBlocksStart(terminal)).toBe(false);
    }
  });

  it("preserves a local cancel intent while spawn is pending and binds the job later", () => {
    const pending = requestAiRunCancellation(createAiRun(8));
    expect(pending).toMatchObject({ jobId: null, spawnPending: true, cancellation: "cancelling" });

    const bound = bindAiRunJob(pending, "job-pending");
    expect(bound).toMatchObject({
      jobId: "job-pending",
      spawnPending: false,
      settled: false,
      cancellation: "cancelling",
    });
  });
});
