// Pure state presentation helpers for the agent UI (P5). The central rule:
// states are only rendered as what their evidence supports. Terminal claims
// (done/error/canceled) and authoritative absence (gone/replaced/exited)
// without evidence degrade to "unknown"; idle is a state of its own and must
// never be presented as done.

import type {
  AgentEvidence,
  AgentHandleRef,
  AgentPresence,
  AgentRunState,
} from "./agentTypes";

export type AgentStateTone = "ok" | "warn" | "error" | "muted" | "accent";

export interface AgentStateView {
  key: string;
  label: string;
  tone: AgentStateTone;
  evidence: AgentEvidence | null;
  unknown: boolean;
}

const UNKNOWN_VIEW: AgentStateView = {
  key: "unknown",
  label: "未知",
  tone: "muted",
  evidence: null,
  unknown: true,
};

function unknownBecause(missing: string): AgentStateView {
  return { ...UNKNOWN_VIEW, label: `未知（缺少${missing}）` };
}

export function presenceView(
  presence: AgentPresence,
  evidence: AgentEvidence | null,
): AgentStateView {
  switch (presence) {
    case "attached":
      return { key: "attached", label: "已附加", tone: "ok", evidence, unknown: false };
    case "detached":
      return { key: "detached", label: "已分离", tone: "muted", evidence, unknown: false };
    case "disconnected":
      return { key: "disconnected", label: "已断开", tone: "warn", evidence, unknown: false };
    case "unverifiable":
      // Unverifiable stays visible as its own state; it never collapses into
      // gone, and it always forbids automatic writes (see autoWriteAllowed).
      return {
        key: "unverifiable",
        label: evidence ? "无法验证" : "无法验证（无证据）",
        tone: "warn",
        evidence,
        unknown: false,
      };
    case "nonpersistent":
      return {
        key: "nonpersistent",
        label: "非持久目标",
        tone: "muted",
        evidence,
        unknown: false,
      };
    case "gone":
      // Gone requires authoritative absence; without evidence we cannot claim it.
      return evidence
        ? { key: "gone", label: "已消失", tone: "error", evidence, unknown: false }
        : unknownBecause("权威缺席证据");
    case "replaced":
      return evidence
        ? { key: "replaced", label: "已被替换", tone: "warn", evidence, unknown: false }
        : unknownBecause("替换证据");
    case "exited":
      return evidence
        ? { key: "exited", label: "已退出", tone: "muted", evidence, unknown: false }
        : unknownBecause("退出证据");
  }
}

export function runStateView(
  state: AgentRunState,
  evidence: AgentEvidence | null,
): AgentStateView {
  switch (state) {
    case "queued":
      return { key: "queued", label: "排队中", tone: "muted", evidence, unknown: false };
    case "running":
      return { key: "running", label: "运行中", tone: "accent", evidence, unknown: false };
    case "idle":
      // Idle is deliberately its own label: an idle agent is not a completed one.
      return { key: "idle", label: "空闲", tone: "muted", evidence, unknown: false };
    case "done":
      return evidence
        ? { key: "done", label: "已完成", tone: "ok", evidence, unknown: false }
        : unknownBecause("完成证据");
    case "error":
      return evidence
        ? { key: "error", label: "失败", tone: "error", evidence, unknown: false }
        : unknownBecause("失败证据");
    case "canceled":
      return evidence
        ? { key: "canceled", label: "已取消", tone: "warn", evidence, unknown: false }
        : unknownBecause("取消证据");
  }
}

// Automatic writes are only allowed when the handle identity is live and
// verified. Everything else (disconnected, gone, replaced, exited,
// unverifiable, nonpersistent) forbids them.
export function autoWriteAllowed(presence: AgentPresence): boolean {
  return presence === "attached" || presence === "detached";
}

// Handles are durable, human-chosen names referenced as @handle in prompts and
// rendered as handle@host:cwd on tool cards, so they must stay URL- and
// mention-safe: no whitespace, "@", or ":".
const HANDLE_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_-]*$/;

export function isValidHandleName(handle: string): boolean {
  return HANDLE_PATTERN.test(handle);
}

// handleAtHostCwd renders the target as handle@host:cwd. Host or cwd can be
// honestly unknown (the roster contract does not evidence them yet): a
// missing cwd drops the :cwd suffix and a missing host drops the @host part
// entirely, so the row never claims a target identity it does not have.
export function handleAtHostCwd(ref: AgentHandleRef): string {
  if (ref.host === "") return ref.handle;
  if (ref.cwd === "") return `${ref.handle}@${ref.host}`;
  return `${ref.handle}@${ref.host}:${ref.cwd}`;
}

export function mentionToken(ref: AgentHandleRef): string {
  return `@${ref.handle}`;
}

export function formatEvidence(evidence: AgentEvidence): string {
  return `${evidence.summary} · ${new Date(evidence.observedAt).toLocaleString()}`;
}
