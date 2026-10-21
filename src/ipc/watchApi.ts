import { call } from "./commands";
import { EVENTS, listenEvent } from "./events";
import type { WatchSummary, WatchTargetSummary } from "./generated/types";

// The wire shape of one delivered watch event: untrusted terminal data that
// travels as tool data, never as a user message.
export interface WatchEventPayload {
  kind: "watchEvent";
  deliveryId: string;
  runId: string;
  watchId: string;
  targetId: string;
  identity?: string;
  name?: string;
  condition: { type: string; pattern: string };
  matchedFrom: number;
  matchedTo: number;
  excerpt: string;
  firedAt: number;
}

export interface WatchToolDataPayload {
  channel: "tool";
  untrusted: true;
  event: WatchEventPayload;
}

// WatchToolDataEvent is the ai://watch-tool-data payload: the server filters
// by subscriber identity and the desktop delivers locally with no identity.
export interface WatchToolDataEventPayload {
  ownerId: string;
  data: WatchToolDataPayload;
}

// The panel-facing enums mirror the reviewed contract in
// src/features/ai/watchTypes.ts; the conformance assignment in
// src/test/ipc-watchApi.test.ts breaks the build if either side drifts.
const watchStatuses = ["armed", "matched", "cancelled", "expired", "unverifiable"] as const;
export type WatchStatusValue = (typeof watchStatuses)[number];
const watchTargetStates = [
  "attached",
  "disconnected",
  "gone",
  "replaced",
  "exited",
  "unverifiable",
  "nonpersistent",
] as const;
export type WatchTargetStateValue = (typeof watchTargetStates)[number];
const watchConditionTypes = ["contains", "regex"] as const;
export type WatchConditionTypeValue = (typeof watchConditionTypes)[number];

export interface WatchSummaryView {
  id: string;
  targetId: string;
  name: string;
  condition: { type: WatchConditionTypeValue; pattern: string };
  status: WatchStatusValue;
  repeat: boolean;
  anchorSeq: number;
  generation: number;
  revision: number;
  lastError?: string;
  updatedAt: number;
}

export interface WatchTargetView {
  targetId: string;
  identity: string;
  state: WatchTargetStateValue;
  evidence?: string;
}

function asMember<Value extends string>(value: string, allowed: readonly Value[], what: string): Value {
  if ((allowed as readonly string[]).includes(value)) {
    return value as Value;
  }
  throw new Error(`watch ${what} 来自后端的值无法识别: ${value}`);
}

function toWatchSummaryView(summary: WatchSummary): WatchSummaryView {
  return {
    ...summary,
    condition: {
      type: asMember(summary.condition.type, watchConditionTypes, "条件类型"),
      pattern: summary.condition.pattern,
    },
    status: asMember(summary.status, watchStatuses, "状态"),
  };
}

function toWatchTargetView(target: WatchTargetSummary): WatchTargetView {
  return { ...target, state: asMember(target.state, watchTargetStates, "目标状态") };
}

function isWatchToolDataEvent(payload: unknown): payload is WatchToolDataEventPayload {
  if (!payload || typeof payload !== "object") return false;
  const candidate = payload as Partial<WatchToolDataEventPayload>;
  const data = candidate.data as Partial<WatchToolDataPayload> | undefined;
  if (typeof candidate.ownerId !== "string" || !data || typeof data !== "object") return false;
  const event = data.event as Partial<WatchEventPayload> | undefined;
  return (
    data.channel === "tool" &&
    data.untrusted === true &&
    !!event &&
    event.kind === "watchEvent" &&
    typeof event.deliveryId === "string" &&
    event.deliveryId.length > 0
  );
}

// watchApi is the production watch surface: typed ai_watch_* calls checked
// against the generated contract, and the tool-data subscription filtered to
// the subscriber's owner (the backend scopes every operation to the caller's
// identity, so the owner argument only filters the event stream).
export const watchApi = {
  createWatch: (args: { targetId: string; name: string; condition: { type: string; pattern: string }; repeat: boolean; expiresAt: number }) =>
    call("ai_watch_create", args),
  listWatches: (): Promise<WatchSummary[]> => call("ai_watch_list"),
  listTargets: (): Promise<WatchTargetSummary[]> => call("ai_watch_targets"),
  cancelWatch: (watchId: string, expectedRevision: number): Promise<WatchSummary> =>
    call("ai_watch_cancel", { watchId, expectedRevision }),
  subscribeWatchToolData: (owner: string, onData: (data: WatchToolDataPayload) => void): (() => void) => {
    let off: (() => void) | null = null;
    let closed = false;
    void listenEvent<unknown>(EVENTS.aiWatchToolData, (payload) => {
      if (!isWatchToolDataEvent(payload)) return;
      if (payload.ownerId !== "" && payload.ownerId !== owner) return;
      onData(payload.data);
    }).then((unlisten) => {
      if (closed) {
        unlisten();
        return;
      }
      off = unlisten;
    });
    return () => {
      closed = true;
      off?.();
    };
  },
};

// watchPanelApi is the reviewed panel contract backed by the production
// adapter: backend enums are validated at the seam instead of cast, and
// owner scoping stays with the caller's identity on every operation.
export const watchPanelApi = {
  listWatches: (_owner: string): Promise<WatchSummaryView[]> => watchApi.listWatches().then((rows) => rows.map(toWatchSummaryView)),
  listTargets: (_owner: string): Promise<WatchTargetView[]> => watchApi.listTargets().then((rows) => rows.map(toWatchTargetView)),
  cancelWatch: (_owner: string, watchId: string, expectedRevision: number): Promise<void> =>
    watchApi.cancelWatch(watchId, expectedRevision).then(() => undefined),
  subscribeWatchToolData: (owner: string, onData: (data: WatchToolDataPayload) => void): (() => void) =>
    watchApi.subscribeWatchToolData(owner, onData),
};
