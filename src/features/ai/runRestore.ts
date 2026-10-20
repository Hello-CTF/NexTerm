import type { AiHitlSnapshotDto, AiRunDto, AiRunEventDto } from "../../ipc/types";
import { hasAgentOwnedUsageID, safeTokenCount, usageChannelOf, usageSnapshotOf } from "./conversation";
import type { AiUsage } from "./UsageRing";

export interface ResumableRun {
  run: AiRunDto;
  snapshot: AiHitlSnapshotDto;
}

export const RUN_RESTORE_LIMIT = 50;
export const RUN_EVENTS_PAGE_SIZE = 200;

export interface RunEventsFetch {
  events: AiRunEventDto[];
  failed: boolean;
}

export interface RunEventsReplayOptions {
  pageSize?: number;
  shouldContinue?: () => boolean;
  yieldControl?: () => Promise<void>;
}

export interface RunEventsReplay {
  failed: boolean;
  stopped: boolean;
}

function eventSeqOrNull(event: AiRunEventDto): number | null {
  const seq = event.seq;
  return typeof seq === "number" && Number.isSafeInteger(seq) && seq >= 1 ? seq : null;
}

function yieldToMainThread(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

export async function replayRunEventsAfter(
  fetchPage: (afterSeq: number, limit: number) => Promise<AiRunEventDto[]>,
  afterSeq: number,
  applyPage: (events: AiRunEventDto[]) => void | Promise<void>,
  options: RunEventsReplayOptions = {},
): Promise<RunEventsReplay> {
  const pageSize = options.pageSize ?? RUN_EVENTS_PAGE_SIZE;
  const shouldContinue = options.shouldContinue ?? (() => true);
  const yieldControl = options.yieldControl ?? yieldToMainThread;
  let cursor = afterSeq;
  for (;;) {
    if (!shouldContinue()) return { failed: false, stopped: true };
    let page: AiRunEventDto[];
    try {
      page = await fetchPage(cursor, pageSize);
    } catch {
      return { failed: true, stopped: false };
    }
    if (!shouldContinue()) return { failed: false, stopped: true };
    const pageStart = cursor;
    const accepted: AiRunEventDto[] = [];
    let failed = false;
    for (const event of page) {
      const seq = eventSeqOrNull(event);
      if (seq === null) {
        failed = true;
        break;
      }
      if (seq <= cursor) continue;
      if (seq !== cursor + 1) {
        failed = true;
        break;
      }
      accepted.push(event);
      cursor = seq;
    }
    if (accepted.length > 0) {
      try {
        await applyPage(accepted);
      } catch {
        return { failed: true, stopped: false };
      }
    }
    if (failed) return { failed: true, stopped: false };
    if (page.length < pageSize) return { failed: false, stopped: false };
    if (cursor === pageStart) return { failed: true, stopped: false };
    if (!shouldContinue()) return { failed: false, stopped: true };
    await yieldControl();
  }
}

export async function fetchRunEventsAfter(
  fetchPage: (afterSeq: number, limit: number) => Promise<AiRunEventDto[]>,
  afterSeq: number,
  pageSize: number = RUN_EVENTS_PAGE_SIZE,
): Promise<RunEventsFetch> {
  const events: AiRunEventDto[] = [];
  const result = await replayRunEventsAfter(
    fetchPage,
    afterSeq,
    (page) => {
      events.push(...page);
    },
    { pageSize },
  );
  return { events, failed: result.failed };
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
  shouldContinue: () => boolean = () => true,
): Promise<ResumableRun | null> {
  for (const run of resumableCandidates(runs)) {
    if (!shouldContinue()) return null;
    const snapshot = await snapshotOf(run.id);
    if (!shouldContinue()) return null;
    if (snapshotHasPending(snapshot)) {
      return { run, snapshot };
    }
  }
  return null;
}
