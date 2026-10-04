import type { AiHitlSnapshotDto, AiRunDto } from "../../ipc/types";

export function replayableRuns(runs: AiRunDto[]): AiRunDto[] {
  return runs
    .filter((run) => run.status !== "completed")
    .slice()
    .sort((a, b) => a.createdAt - b.createdAt);
}

export function resumableCandidates(runs: AiRunDto[]): AiRunDto[] {
  return runs
    .filter((run) => run.status === "interrupted" && run.finishedAt == null)
    .slice()
    .sort((a, b) => b.createdAt - a.createdAt);
}

export function snapshotHasPending(snapshot: AiHitlSnapshotDto | null): boolean {
  return !!snapshot && !snapshot.terminal && Array.isArray(snapshot.pending) && snapshot.pending.length > 0;
}

export async function findResumableRun(
  runs: AiRunDto[],
  snapshotOf: (jobId: string) => Promise<AiHitlSnapshotDto | null>,
): Promise<AiRunDto | null> {
  for (const run of resumableCandidates(runs)) {
    if (snapshotHasPending(await snapshotOf(run.id))) {
      return run;
    }
  }
  return null;
}
