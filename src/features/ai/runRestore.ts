import type { AiHitlSnapshotDto, AiRunDto, AiRunEventDto } from "../../ipc/types";
import { hasAgentOwnedUsageID, safeTokenCount, usageChannelOf, usageSnapshotOf } from "./conversation";
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
  let main: AiUsage | null = null;
  let summaryPromptTokens = 0;
  let summaryCompletionTokens = 0;
  let hasSummary = false;
  for (const run of ordered) {
    const { events, failed } = await loadEvents(run.id);
    if (failed) continue;
    let runMain: AiUsage | null = null;
    const seen = new Set<string>();
    for (const event of events) {
      if (event.type !== "usage") continue;
      const channel = usageChannelOf(event);
      if (channel === "tool-subagent") continue;
      const callID = typeof event.callId === "string" ? event.callId : "";
      const eventKey = hasAgentOwnedUsageID(event) ? `${channel}:${callID}` : "";
      if (eventKey && seen.has(eventKey)) continue;
      if (eventKey) seen.add(eventKey);
      if (channel === "summary") {
        hasSummary = true;
        summaryPromptTokens = safeTokenCount(summaryPromptTokens + safeTokenCount(event.promptTokens));
        summaryCompletionTokens = safeTokenCount(summaryCompletionTokens + safeTokenCount(event.completionTokens));
        continue;
      }
      runMain = usageSnapshotOf(event);
    }
    if (main === null && runMain !== null) main = runMain;
  }
  if (main === null && !hasSummary) return null;
  const result = main ?? { promptTokens: 0, completionTokens: 0, cachedTokens: 0, contextWindow: 0 };
  if (hasSummary) {
    result.summaryPromptTokens = summaryPromptTokens;
    result.summaryCompletionTokens = summaryCompletionTokens;
  }
  return result;
}

const TERMINAL_RUN_STATUSES = new Set(["completed", "canceled", "failed", "expired", "superseded"]);

export function isRunTerminal(run: AiRunDto): boolean {
  return run.finishedAt != null || TERMINAL_RUN_STATUSES.has(run.status);
}

export interface ConversationRunCancellationApi {
  runs(conversationId: string): Promise<AiRunDto[]>;
  cancel(jobId: string): Promise<void>;
}

export type ConversationRunsStopped = "confirmed" | "unconfirmed";

export async function confirmConversationRunsStopped(
  conversationId: string,
  api: ConversationRunCancellationApi,
): Promise<ConversationRunsStopped> {
  const deadline = Date.now() + 5000;
  const operation = (async (): Promise<ConversationRunsStopped> => {
    let jobIds: string[];
    try {
      const runs = await api.runs(conversationId);
      const unfinished = runs.filter((run) => !isRunTerminal(run));
      if (unfinished.length === 0) return "confirmed";
      jobIds = unfinished.map((run) => run.id);
    } catch {
      return "unconfirmed";
    }

    let cancelRejected = false;
    for (const jobId of jobIds) {
      try {
        await api.cancel(jobId);
      } catch {
        cancelRejected = true;
        break;
      }
    }
    for (;;) {
      let runs: AiRunDto[];
      try {
        runs = await api.runs(conversationId);
      } catch {
        return "unconfirmed";
      }
      const byId = new Map(runs.map((run) => [run.id, run]));
      const stopped = jobIds.every((jobId) => {
        const run = byId.get(jobId);
        return run !== undefined && isRunTerminal(run);
      });
      if (stopped) return "confirmed";
      if (cancelRejected) return "unconfirmed";
      const remaining = deadline - Date.now();
      if (remaining <= 0) return "unconfirmed";
      await new Promise<void>((resolve) => setTimeout(resolve, Math.min(100, remaining)));
    }
  })();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<ConversationRunsStopped>((resolve) => {
    timer = setTimeout(() => resolve("unconfirmed"), 5000);
  });
  try {
    return await Promise.race([operation, timeout]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
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
