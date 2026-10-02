export type AiRunKind = "chat" | "takeover";

export interface AiRunSlot {
  generation: number;
  kind: AiRunKind;
  jobId: string | null;
  spawnPending: boolean;
  settled: boolean;
}

export function createAiRun(generation: number, kind: AiRunKind = "chat"): AiRunSlot {
  return { generation, kind, jobId: null, spawnPending: true, settled: false };
}

export function bindAiRunJob(run: AiRunSlot, jobId: string): AiRunSlot {
  return { ...run, jobId, spawnPending: false };
}

export function finishAiRunSpawn(run: AiRunSlot): AiRunSlot {
  return { ...run, spawnPending: false };
}

export function settleAiRun(run: AiRunSlot): AiRunSlot {
  return { ...run, settled: true };
}

export function completeRunCancellation(run: AiRunSlot): {
  run: AiRunSlot;
  waitForTerminal: boolean;
} {
  if (run.kind === "takeover") return { run, waitForTerminal: true };
  return { run: settleAiRun(run), waitForTerminal: false };
}

export function isCurrentAiRun(run: AiRunSlot | null, generation: number): run is AiRunSlot {
  return run?.generation === generation;
}

export function aiRunBlocksStart(run: AiRunSlot | null): boolean {
  return !!run && (!run.settled || run.spawnPending);
}
