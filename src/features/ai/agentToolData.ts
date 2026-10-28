// Pure builders joining the M110 tool-event handle stream and the M131 roster
// contract onto the reviewed AgentToolCall shape. The rules mirror the
// backend: a handle travels only with its own tool call (never re-derived or
// borrowed from another call), conflicting evidence for one call id fails
// closed to ambiguous, and unprovable identity fields stay unknown instead of
// being fabricated.

import type { ConfirmItem, ToolItem } from "./conversation";
import type {
  AgentApproval,
  AgentApprovalDecision,
  AgentEvidence,
  AgentRosterEntry,
  AgentRunSummary,
  AgentToolCall,
} from "./agentTypes";

export type ToolHandleLookup =
  | { kind: "handle"; handle: string }
  | { kind: "ambiguous" }
  | { kind: "unknown" };

// ToolHandleRegistry records the verified roster handle of each tool call as
// the event stream delivers it, scoped to the run that produced the call:
// the backend accepts model-provided call ids without a cross-run uniqueness
// guarantee, so records are keyed by (generation, callId) and a second run
// reusing an id never borrows the first run's handle. Within one run a
// handle-less toolResult never clears the toolCall's verified handle; a
// conflicting handle marks only that call permanently ambiguous.
export class ToolHandleRegistry {
  private records = new Map<string, { handle: string } | { ambiguous: true }>();

  private key(generation: number, callId: string): string {
    return JSON.stringify([generation, callId]);
  }

  // record ingests one raw stream event of the given run generation and
  // reports whether the registry changed (a new verified handle or a newly
  // ambiguous call).
  record(generation: number, ev: Record<string, unknown>): boolean {
    if (ev.type !== "toolCall" && ev.type !== "toolResult") return false;
    const callId = typeof ev.id === "string" ? ev.id : "";
    if (callId === "") return false;
    const handle = typeof ev.handle === "string" && ev.handle !== "" ? ev.handle : null;
    if (handle === null) return false;
    const key = this.key(generation, callId);
    const current = this.records.get(key);
    if (current === undefined) {
      this.records.set(key, { handle });
      return true;
    }
    if ("ambiguous" in current) return false;
    if (current.handle === handle) return false;
    this.records.set(key, { ambiguous: true });
    return true;
  }

  // lookup resolves a tool item's handle. History items rebuilt from
  // persisted messages carry no run generation (attempt null) and message
  // rows never persist handles, so they honestly resolve unknown.
  lookup(generation: number | null, callId: string): ToolHandleLookup {
    if (generation === null) return { kind: "unknown" };
    const current = this.records.get(this.key(generation, callId));
    if (current === undefined) return { kind: "unknown" };
    if ("ambiguous" in current) return { kind: "ambiguous" };
    return { kind: "handle", handle: current.handle };
  }

  handles(): string[] {
    const out = new Set<string>();
    for (const record of this.records.values()) {
      if (!("ambiguous" in record)) out.add(record.handle);
    }
    return [...out];
  }

  clear(): void {
    this.records.clear();
  }
}

// RESOLUTION_DECISION mirrors the CONFIRM_RESOLUTION labels the sidebar
// stamps on resolved interactions; an unrecognized resolution hides the
// approval section rather than presenting a resolved card as pending.
const RESOLUTION_DECISION: Record<string, AgentApprovalDecision> = {
  已允许一次: "approve_once",
  本会话已允许此类: "approve_session",
  "已永久允许（可随时在授权管理中撤销）": "approve_persistent",
  已拒绝: "deny",
};

// approvalFromConfirmCard joins the tool call's own confirm card: the card
// must carry its HITL request id for the approval delegate to route the
// decision, and a resolved card maps its stamped label back to the decision.
export function approvalFromConfirmCard(card: ConfirmItem | null | undefined): AgentApproval | null {
  if (!card || !card.requestId) return null;
  if (card.resolution !== undefined && RESOLUTION_DECISION[card.resolution] === undefined) {
    return null;
  }
  return {
    requestId: card.requestId,
    nonce: card.nonce,
    attempt: card.hitlAttempt ?? 1,
    decision: card.resolution === undefined ? null : RESOLUTION_DECISION[card.resolution],
    ...(card.sessionScope ? { sessionScope: card.sessionScope } : {}),
  };
}

export interface AgentRunRowView {
  answer?: string;
  error?: string;
  finishedAt?: number;
}

const RUN_SUMMARY_RUNES = 80;

function runExcerpt(text: string): string {
  const trimmed = text.trim();
  const runes = [...trimmed];
  return runes.length > RUN_SUMMARY_RUNES ? runes.slice(0, RUN_SUMMARY_RUNES).join("") + "…" : trimmed;
}

// runSummaryFromAttempt projects the conversation's own run tracking onto the
// reviewed run chip, mirroring the backend history contract: terminal states
// come only from the run row's own finish record, and an unfinished or
// missing row carries no evidence (the card degrades to unknown by itself).
export function runSummaryFromAttempt(
  outcome: "done" | "error" | "canceled" | null,
  row: AgentRunRowView | undefined,
): AgentRunSummary | null {
  if (outcome === null) return { state: "running", evidence: null };
  const finishedAt = row?.finishedAt;
  let evidence: AgentEvidence | null = null;
  if (typeof finishedAt === "number") {
    if (outcome === "done") {
      const excerpt = runExcerpt(row?.answer ?? "");
      evidence = { kind: "output", observedAt: finishedAt, summary: excerpt === "" ? "已完成" : excerpt };
    } else if (outcome === "error") {
      evidence = { kind: "exit", observedAt: finishedAt, summary: row?.error?.trim() || "运行失败" };
    } else {
      evidence = { kind: "exit", observedAt: finishedAt, summary: row?.error?.trim() || "已取消" };
    }
  }
  return { state: outcome, evidence };
}

// buildAgentToolCall assembles the reviewed card contract from the live tool
// item, its verified handle and the roster join. A handle whose roster row is
// not in the snapshot keeps every joined field honestly unknown — never an
// asset name or an invented presence.
export function buildAgentToolCall(
  item: ToolItem,
  handle: string,
  rosterEntry: AgentRosterEntry | undefined,
  run: AgentRunSummary | null,
  approval: AgentApproval | null,
): AgentToolCall {
  return {
    callId: item.callId,
    tool: item.name,
    handle: { handle, host: rosterEntry?.host ?? "", cwd: rosterEntry?.cwd ?? "" },
    ownerName: rosterEntry?.ownerName ?? "",
    isMine: rosterEntry?.isMine ?? true,
    production: rosterEntry?.production ?? false,
    persistent: rosterEntry !== undefined ? rosterEntry.persistent : null,
    presence: rosterEntry?.presence ?? "unverifiable",
    state:
      item.summary === undefined
        ? "running"
        : item.ok === true
          ? "ok"
          : item.ok === false
            ? "error"
            : "unknown",
    exitCode: item.exitCode ?? null,
    summary: item.summary ?? "",
    output: item.text ?? item.summary ?? "",
    run,
    approval,
  };
}
