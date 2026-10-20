export type WatchConditionType = "contains" | "regex";

export interface WatchCondition {
  type: WatchConditionType;
  pattern: string;
}

export type WatchStatus = "armed" | "matched" | "cancelled" | "expired" | "unverifiable";

// WatchTargetStateKind is the durable lifecycle state of a watched target.
// "gone", "replaced", "exited", "unverifiable" and "nonpersistent" are
// evidence-bearing states: without evidence they render as unknown, never as
// the definitive state.
export type WatchTargetStateKind =
  | "attached"
  | "disconnected"
  | "gone"
  | "replaced"
  | "exited"
  | "unverifiable"
  | "nonpersistent";

export interface WatchSummary {
  id: string;
  targetId: string;
  name: string;
  condition: WatchCondition;
  status: WatchStatus;
  repeat: boolean;
  anchorSeq: number;
  generation: number;
  // revision is the watch row's durable CAS revision; cancellation must
  // present it as expectedRevision, never the target-incarnation generation.
  revision: number;
  lastError?: string;
  updatedAt: number;
}

export interface WatchTargetSummary {
  targetId: string;
  identity: string;
  state: WatchTargetStateKind;
  evidence?: string;
}

export interface WatchEvent {
  kind: "watchEvent";
  deliveryId: string;
  runId: string;
  watchId: string;
  targetId: string;
  matchedFrom: number;
  matchedTo: number;
  excerpt: string;
  firedAt: number;
}

// WatchToolData is the delivery envelope for watch events: they travel as
// untrusted tool data, never as user messages.
export interface WatchToolData {
  channel: "tool";
  untrusted: true;
  event: WatchEvent;
}

// WatchApi is the injected contract the Watch panel runs against. The real
// IPC/runtime adapter lands with the pending contract missions; tests inject
// synthetic implementations.
export interface WatchApi {
  listWatches(owner: string): Promise<WatchSummary[]>;
  listTargets(owner: string): Promise<WatchTargetSummary[]>;
  cancelWatch(owner: string, watchId: string, expectedRevision: number): Promise<void>;
  subscribeWatchToolData(owner: string, onData: (data: WatchToolData) => void): () => void;
}
