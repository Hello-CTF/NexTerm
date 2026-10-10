import type { AiHitlSnapshotDto, AiRunDto, AiRunEventDto } from "../../ipc/types";
import { safeTokenCount } from "./conversation";
import type { AiUsage } from "./UsageRing";

export interface ResumableRun {
  run: AiRunDto;
  snapshot: AiHitlSnapshotDto;
}

export const RUN_EVENTS_PAGE_SIZE = 200;

export interface RunEventsFetch {
  events: AiRunEventDto[];
  failed: boolean;
}

function eventSeqOrNull(event: AiRunEventDto): number | null {
  const seq = event.seq;
  return typeof seq === "number" && Number.isSafeInteger(seq) && seq >= 1 ? seq : null;
}

export async function fetchRunEventsAfter(
  fetchPage: (afterSeq: number, limit: number) => Promise<AiRunEventDto[]>,
  afterSeq: number,
  pageSize: number = RUN_EVENTS_PAGE_SIZE,
): Promise<RunEventsFetch> {
  const events: AiRunEventDto[] = [];
  let cursor = afterSeq;
  for (;;) {
    let page: AiRunEventDto[];
    try {
      page = await fetchPage(cursor, pageSize);
    } catch {
      return { events, failed: true };
    }
    const pageStart = cursor;
    for (const event of page) {
      const seq = eventSeqOrNull(event);
      if (seq === null) return { events, failed: true };
      if (seq <= cursor) continue;
      if (seq !== cursor + 1) return { events, failed: true };
      events.push(event);
      cursor = seq;
    }
    if (page.length < pageSize) return { events, failed: false };
    if (cursor === pageStart) return { events, failed: true };
  }
}

export async function latestRunUsage(
  runs: AiRunDto[],
  loadEvents: (jobId: string) => Promise<RunEventsFetch>,
): Promise<AiUsage | null> {
  const ordered = runs
    .slice()
    .sort((a, b) => b.createdAt - a.createdAt || b.id.localeCompare(a.id));
  for (const run of ordered) {
    const { events, failed } = await loadEvents(run.id);
    if (failed) continue;
    for (let index = events.length - 1; index >= 0; index -= 1) {
      const event = events[index];
      if (event.type !== "usage") continue;
      return {
        promptTokens: safeTokenCount(event.promptTokens),
        completionTokens: safeTokenCount(event.completionTokens),
        cachedTokens: safeTokenCount(event.cachedTokens),
        contextWindow: safeTokenCount(event.contextWindow),
      };
    }
  }
  return null;
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
