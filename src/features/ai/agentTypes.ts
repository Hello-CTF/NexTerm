// Contract types for the agent roster UI (P5). These mirror the P1.1 roster
// contract shapes (durable handles, exact incarnation identity, distinct
// presence states) without importing generated IPC, so the components can be
// exercised against explicit mock contracts until the shared IPC owner lands
// the real bindings.

export type AgentPresence =
  | "attached"
  | "detached"
  | "disconnected"
  | "gone"
  | "replaced"
  | "exited"
  | "unverifiable"
  | "nonpersistent";

export interface AgentEvidence {
  kind: "output" | "exit" | "attach" | "release" | "reconcile" | "watch" | "approval";
  observedAt: number;
  summary: string;
}

export interface AgentHandleRef {
  handle: string;
  host: string;
  cwd: string;
}

export interface AgentRunSummary {
  state: AgentRunState;
  evidence: AgentEvidence | null;
}

export interface AgentRosterEntry {
  handle: string;
  host: string;
  cwd: string;
  ownerId: string;
  ownerName: string;
  isMine: boolean;
  production: boolean;
  persistent: boolean;
  // The backend incarnation is an opaque durable-identity string, never a
  // sequence number; it is carried for exact-identity operations, not display.
  incarnation: string;
  presence: AgentPresence;
  evidence: AgentEvidence | null;
  releasePending: boolean;
  tabId: string | null;
  lastRun: AgentRunSummary | null;
}

export type AgentRunState = "queued" | "running" | "idle" | "done" | "error" | "canceled";

export interface AgentRunHistoryEntry {
  runId: string;
  state: AgentRunState;
  evidence: AgentEvidence | null;
  startedAt: number;
  finishedAt: number | null;
  summary: string;
}

// One backend page of run history. nextCursor is opaque to the UI: it is
// passed back verbatim to fetch the next older page, and null means the
// backend has no more history. The UI never slices pages in memory.
export interface AgentRunHistoryPage {
  entries: AgentRunHistoryEntry[];
  nextCursor: string | null;
}

export type AgentRunStatusFilter = "all" | AgentRunState;

// Recovery summary reported by the backend after restoring a conversation's
// runs: how many were restored, which run can resume, which were interrupted,
// and whether restoration was truncated (more runs exist than were restored).
export interface AgentRecoverySummary {
  conversationId: string;
  restoredRuns: number;
  resumableRunId: string | null;
  interruptedRunIds: string[];
  truncated: boolean;
}

export type AgentApprovalDecision = "approve_once" | "approve_session" | "deny";

export interface AgentApproval {
  requestId: string;
  nonce: string;
  attempt: number;
  decision: AgentApprovalDecision | null;
}

export interface AgentToolCall {
  callId: string;
  tool: string;
  handle: AgentHandleRef;
  ownerName: string;
  isMine: boolean;
  production: boolean;
  persistent: boolean;
  presence: AgentPresence;
  state: "running" | "ok" | "error" | "unknown";
  exitCode: number | null;
  summary: string;
  output: string;
  run: AgentRunSummary | null;
  approval: AgentApproval | null;
}
