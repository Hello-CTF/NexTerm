import type { AiRunDto } from "../../ipc/types";

export function replayableRuns(runs: AiRunDto[]): AiRunDto[] {
  return runs
    .filter((run) => run.status !== "completed")
    .slice()
    .sort((a, b) => a.createdAt - b.createdAt);
}

export function latestResumableRun(runs: AiRunDto[]): AiRunDto | null {
  const interrupted = runs.filter((run) => run.status === "interrupted");
  if (interrupted.length === 0) return null;
  return interrupted.reduce((latest, run) => (run.createdAt > latest.createdAt ? run : latest));
}
