export type WatchConditionType = "contains" | "regex";

export interface WatchCondition {
  type: WatchConditionType;
  pattern: string;
}

export type WatchStatus = "armed" | "matched" | "cancelled" | "expired" | "unverifiable";

// WatchTargetStateKind is the durable lifecycle state of a watched target.
// "unverifiable" and "nonpersistent" are evidence-bearing states: they are
// only shown when the runtime supplies a reason, never inferred.
export type WatchTargetStateKind =
  | "attached"
  | "disconnected"
  | "gone"
  | "replaced"
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
