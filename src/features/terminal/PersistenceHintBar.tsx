import {
  DURABLE_NOT_READY_AT_OPEN_REASON,
  DURABLE_PREPARING_REASON,
  LOCAL_DURABLE_UNAVAILABLE_REASON,
} from "../../app/store";
import { IconClose } from "../../ui/icons";

export type PersistenceHint =
  | { kind: "ready" }
  | { kind: "regular"; reason: string; failure: boolean }
  | { kind: "attention"; message?: string };

export interface PersistenceHintInput {
  durable: boolean;
  // Line-mode and container exec terminals are never durable, so they carry no persistence hint.
  eligible: boolean;
  helper: "running" | "attention" | "ready" | null;
  helperMessage?: string;
  regularReason?: string;
}

// Expected limitations get a neutral dot; anything else that left the tab regular is a failure.
const EXPECTED_REASONS = new Set([LOCAL_DURABLE_UNAVAILABLE_REASON, DURABLE_NOT_READY_AT_OPEN_REASON]);

export function persistenceHint(input: PersistenceHintInput): PersistenceHint | null {
  if (input.durable || !input.eligible) return null;
  if (input.helper === "running") return null;
  if (input.helper === "attention") return { kind: "attention", message: input.helperMessage };
  if (input.helper === "ready") return { kind: "ready" };
  const reason = input.regularReason;
  if (!reason || reason === DURABLE_PREPARING_REASON) return null;
  return { kind: "regular", reason, failure: !EXPECTED_REASONS.has(reason) };
}

// persistenceHintKey identifies a hint for dismissal: dismissing one hides it until a different one appears.
export function persistenceHintKey(hint: PersistenceHint): string {
  switch (hint.kind) {
    case "ready":
      return "ready";
    case "attention":
      return `attention:${hint.message ?? ""}`;
    default:
      return `regular:${hint.reason}`;
  }
}

// Dismissed hint keys per store tab. They live in memory for the life of the tab, so a pane that remounts the
// terminal (a split or a move) does not show a dismissed hint again.
const dismissedHints = new Map<string, ReadonlySet<string>>();

export function dismissedPersistenceHints(tabId: string): ReadonlySet<string> {
  return dismissedHints.get(tabId) ?? new Set();
}

export function dismissPersistenceHint(tabId: string, key: string): ReadonlySet<string> {
  const next = new Set(dismissedPersistenceHints(tabId)).add(key);
  dismissedHints.set(tabId, next);
  return next;
}

export function forgetPersistenceHints(tabId: string): void {
  dismissedHints.delete(tabId);
}

function sentence(text: string): string {
  return text.replace(/[。.]+$/, "");
}

export function PersistenceHintBar({
  hint,
  onReopenDurable,
  onOpenHelper,
  onDismiss,
}: {
  hint: PersistenceHint;
  onReopenDurable?: () => void;
  // Absent when the session has no helper to manage (local terminals).
  onOpenHelper?: () => void;
  onDismiss: () => void;
}) {
  let tone: "accent" | "neutral" | "danger";
  let message: string;
  let action: { label: string; onSelect: () => void; primary?: boolean } | null = null;
  if (hint.kind === "ready") {
    tone = "accent";
    message = "这台主机的持久终端已就绪。本标签是普通终端，断开后进程会结束。";
    if (onReopenDurable) action = { label: "在持久终端中重开", onSelect: onReopenDurable, primary: true };
  } else if (hint.kind === "attention") {
    tone = "danger";
    message = hint.message
      ? `持久终端暂时不可用：${sentence(hint.message)}。本标签是普通终端，断开后进程会结束。`
      : "持久终端暂时不可用。本标签是普通终端，断开后进程会结束。";
    if (onOpenHelper) action = { label: "处理", onSelect: onOpenHelper };
  } else {
    tone = hint.failure ? "danger" : "neutral";
    message = `本标签是普通终端（${sentence(hint.reason)}），断开后进程会结束。`;
    if (onOpenHelper) action = { label: "持久终端组件…", onSelect: onOpenHelper };
  }
  const dot = tone === "accent" ? "bg-[var(--color-accent)]" : tone === "danger" ? "bg-red-500" : "bg-[var(--nx-fg-tertiary)]";
  return (
    <div
      role="status"
      data-testid="persistence-hint"
      data-tone={tone}
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--nx-border)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_55%,var(--nx-bg-pane))] py-1 pr-1 pl-2.5 text-xs text-[var(--nx-fg)]"
    >
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} aria-hidden="true" />
      <span className="min-w-[min(12rem,70%)] flex-1 break-words">{message}</span>
      {action && (
        <button
          type="button"
          className={`nx-btn nx-btn-sm shrink-0 ${action.primary ? "nx-btn-primary" : "nx-btn-ghost"}`}
          onClick={action.onSelect}
        >
          {action.label}
        </button>
      )}
      <button
        type="button"
        className="nx-icon-btn h-8 w-8 shrink-0"
        aria-label="关闭提示"
        title="关闭提示"
        onClick={onDismiss}
      >
        <IconClose size={12} />
      </button>
    </div>
  );
}
