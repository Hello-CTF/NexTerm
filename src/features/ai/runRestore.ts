import type { AiHitlSnapshotDto, AiRunDto } from "../../ipc/types";

export interface ResumableRun {
  run: AiRunDto;
  snapshot: AiHitlSnapshotDto;
}

export function replayableRuns(runs: AiRunDto[]): AiRunDto[] {
  return runs
    .filter((run) => run.status !== "completed" && run.status !== "superseded")
    .slice()
    .sort((a, b) => a.createdAt - b.createdAt);
}

export function replayableJobIds(runs: AiRunDto[]): Set<string> {
  return new Set(replayableRuns(runs).map((run) => run.id));
}

export function resumableCandidates(runs: AiRunDto[]): AiRunDto[] {
  return runs
    .filter((run) => run.status === "interrupted" && run.finishedAt == null)
    .slice()
    .sort((a, b) => b.createdAt - a.createdAt);
}

export function snapshotHasPending(snapshot: AiHitlSnapshotDto | null): snapshot is AiHitlSnapshotDto {
  return !!snapshot && !snapshot.terminal && Array.isArray(snapshot.pending) && snapshot.pending.length > 0;
}

export function pendingDeadline(snapshot: AiHitlSnapshotDto): number | null {
  const deadlines = snapshot.pending
    .map((request) => Date.parse(request.expiresAt))
    .filter((ms) => Number.isFinite(ms));
  if (deadlines.length === 0) return null;
  return Math.min(...deadlines);
}

export async function findResumableRun(
  runs: AiRunDto[],
  snapshotOf: (jobId: string) => Promise<AiHitlSnapshotDto | null>,
): Promise<ResumableRun | null> {
  for (const run of resumableCandidates(runs)) {
    const snapshot = await snapshotOf(run.id);
    if (snapshotHasPending(snapshot)) {
      return { run, snapshot };
    }
  }
  return null;
}
