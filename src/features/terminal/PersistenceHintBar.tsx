import { IconClose } from "../../ui/icons";
import { persistenceHintFailure, persistenceHintMessage, type PersistenceHint } from "./persistenceHint";

export function PersistenceHintBar({
  hint,
  local,
  onOpenDurable,
  onManageHelper,
  onDismiss,
}: {
  hint: PersistenceHint;
  local: boolean;
  // Absent when no durable helper serves this session.
  onOpenDurable?: () => void;
  onManageHelper?: () => void;
  onDismiss: () => void;
}) {
  const tone = hint.kind === "ready" ? "accent" : persistenceHintFailure(hint) ? "danger" : "neutral";
  let action: { label: string; onSelect: () => void; primary?: boolean } | null = null;
  if (hint.kind === "ready") {
    if (onOpenDurable) action = { label: "新建持久终端", onSelect: onOpenDurable, primary: true };
  } else if (onManageHelper && (hint.kind === "attention" || hint.kind === "restore")) {
    action = { label: "处理", onSelect: onManageHelper };
  } else if (onManageHelper && (hint.kind !== "preparing" || !local)) {
    action = { label: local ? "本机持久终端组件设置…" : "持久终端组件…", onSelect: onManageHelper };
  }
  const dot = tone === "accent" ? "bg-[var(--color-accent)]" : tone === "danger" ? "bg-red-500" : "bg-[var(--nx-fg-tertiary)]";
  return (
    <div
      role="status"
      data-testid="persistence-hint"
      data-tone={tone}
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--nx-border)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_55%,var(--nx-bg-pane))] py-1 pr-1 pl-2.5 text-xs text-[var(--nx-fg)] pointer-coarse:py-0.5"
    >
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} aria-hidden="true" />
      <span className="min-w-[min(12rem,70%)] flex-1 break-words">{persistenceHintMessage(hint, local)}</span>
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
