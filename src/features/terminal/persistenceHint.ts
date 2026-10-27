import {
  DURABLE_FALLBACK_REASON,
  DURABLE_NOT_READY_AT_OPEN_REASON,
  DURABLE_PREPARING_REASON,
  DURABLE_UNAVAILABLE_PREFIX,
} from "../../app/store";
import type { DurableReadiness, LocalHelperStatus } from "../../ipc/commands";

export type PersistenceHint =
  | { kind: "ready" }
  | { kind: "preparing" }
  | { kind: "regular"; reason: string; failure: boolean }
  | { kind: "attention"; message?: string }
  | { kind: "restore"; message?: string };

export interface PersistenceHintInput {
  durable: boolean;
  // Line-mode and container exec terminals are never durable, so they carry no persistence hint.
  eligible: boolean;
  local: boolean;
  // The helper operation this tab owns, if any.
  operation?: { status: string; kind: string; message?: string } | null;
  // The helper deployment this tab owned has finished.
  ownedReady: boolean;
  // The latest readiness known for the session, shared by every tab on it.
  readiness?: DurableReadiness;
  localHelper?: LocalHelperStatus | null;
  regularReason?: string;
}

function localReadiness(status: LocalHelperStatus | null | undefined): DurableReadiness | undefined {
  if (!status) return undefined;
  switch (status.state) {
    case "ready":
      return { state: "ready" };
    case "starting":
    case "idle":
      return { state: "preparing" };
    case "disabled":
      return { state: "disabled" };
    default:
      return status.reason ? { state: "unavailable", reason: status.reason } : { state: "unavailable" };
  }
}

// persistenceHint derives the hint shown above a terminal from the live helper state, so every tab on a session
// agrees with it and a remounted pane shows the same hint. A regular tab without a recorded reason is regular by
// choice and gets no hint.
export function persistenceHint(input: PersistenceHintInput): PersistenceHint | null {
  if (!input.eligible) return null;
  const operation = input.operation;
  if (operation?.status === "awaiting_choice") {
    if (operation.kind === "reattach") return { kind: "restore", message: operation.message };
    if (!input.durable) return { kind: "attention", message: operation.message };
  }
  if (input.durable) return null;
  // The deployment this tab started is still running; it reports its own outcome.
  if (operation?.status === "running") return null;
  // This regular tab waited for the deployment it started, which has finished.
  if (input.ownedReady) return { kind: "ready" };
  const reason = input.regularReason;
  if (!reason) return null;
  if (reason === DURABLE_FALLBACK_REASON) return { kind: "regular", reason, failure: true };
  const live = (input.local ? localReadiness(input.localHelper) : undefined) ?? input.readiness;
  switch (live?.state) {
    case "ready":
      return { kind: "ready" };
    case "disabled":
      return null;
    case "preparing":
      return { kind: "preparing" };
    case "unavailable":
      return { kind: "regular", reason: `${DURABLE_UNAVAILABLE_PREFIX}：${live.reason || "原因未知"}`, failure: true };
  }
  if (reason === DURABLE_PREPARING_REASON) return { kind: "preparing" };
  return { kind: "regular", reason, failure: reason !== DURABLE_NOT_READY_AT_OPEN_REASON };
}

export function persistenceHintFailure(hint: PersistenceHint): boolean {
  return hint.kind === "attention" || hint.kind === "restore" || (hint.kind === "regular" && hint.failure);
}

// persistenceHintKey identifies a hint for dismissal: dismissing one hides it until a different one appears.
export function persistenceHintKey(hint: PersistenceHint): string {
  switch (hint.kind) {
    case "ready":
    case "preparing":
      return hint.kind;
    case "attention":
    case "restore":
      return `${hint.kind}:${hint.message ?? ""}`;
    default:
      return `regular:${hint.reason}`;
  }
}

function clause(text: string): string {
  return text.replace(/[。.]+$/, "");
}

const REGULAR_TAIL = "本标签是普通终端，断开后进程会结束。";

export function persistenceHintMessage(hint: PersistenceHint, local: boolean): string {
  switch (hint.kind) {
    case "ready":
      return `${local ? "本机" : "这台主机"}的持久终端已就绪。${REGULAR_TAIL}`;
    case "preparing":
      return `持久组件准备中。${REGULAR_TAIL}`;
    case "attention":
      return hint.message ? `持久终端暂时不可用：${clause(hint.message)}。${REGULAR_TAIL}` : `持久终端暂时不可用。${REGULAR_TAIL}`;
    case "restore":
      return hint.message
        ? `原持久终端恢复失败：${clause(hint.message)}。可以重试恢复，或改用新的普通终端。`
        : "原持久终端恢复失败。可以重试恢复，或改用新的普通终端。";
    default:
      return `本标签是普通终端（${clause(hint.reason)}），断开后进程会结束。`;
  }
}
