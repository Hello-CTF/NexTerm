import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { useUi, type ToastItem } from "../store";
import { IconAlert, IconCheckCircle, IconClose, IconInfo, IconXCircle } from "../../ui/icons";
import { t as tr } from "../../i18n";

// A dismissed toast stays this long with the leaving animation before it leaves the DOM. The CSS exit is shorter,
// so removal never waits on animationend (which jsdom and reduced motion never fire).
export const TOAST_EXIT_MS = 150;

interface ShownToast {
  toast: ToastItem;
  leaving: boolean;
}

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

// mergeShown keeps a toast the store removed in its place, marked leaving, and appends new ones in store order.
function mergeShown(prev: ShownToast[], toasts: ToastItem[], keepLeaving: boolean): ShownToast[] {
  const current = new Map(toasts.map((t) => [t.id, t]));
  const next: ShownToast[] = [];
  for (const entry of prev) {
    const live = current.get(entry.toast.id);
    if (live) next.push({ toast: live, leaving: false });
    else if (keepLeaving) next.push({ toast: entry.toast, leaving: true });
  }
  const known = new Set(prev.map((entry) => entry.toast.id));
  for (const t of toasts) if (!known.has(t.id)) next.push({ toast: t, leaving: false });
  return next;
}

const ICONS = { error: IconXCircle, warning: IconAlert, success: IconCheckCircle, info: IconInfo } as const;
const TONES = {
  error: "border-red-500/40 bg-red-950/90 text-red-100",
  warning: "nx-toast-warning",
  success: "nx-alert-success",
  info: "border-neutral-700 bg-neutral-800/95 text-neutral-100",
} as const;
const ICON_TONES = {
  error: "text-red-300",
  warning: "text-amber-300",
  success: "text-green-300",
  info: "text-blue-300",
} as const;

// children render above the toasts inside the same stack, for indicators that share its position.
export function ToastStack({ children }: { children?: ReactNode }) {
  const toasts = useUi((s) => s.toasts);
  const [seen, setSeen] = useState(toasts);
  const [shown, setShown] = useState<ShownToast[]>(() => mergeShown([], toasts, false));
  if (seen !== toasts) {
    setSeen(toasts);
    setShown((prev) => mergeShown(prev, toasts, !prefersReducedMotion()));
  }

  const exitTimers = useRef(new Map<number, number>());
  useEffect(() => {
    const timers = exitTimers.current;
    for (const entry of shown) {
      if (!entry.leaving || timers.has(entry.toast.id)) continue;
      const id = entry.toast.id;
      timers.set(
        id,
        window.setTimeout(() => {
          timers.delete(id);
          setShown((prev) => prev.filter((e) => !(e.leaving && e.toast.id === id)));
        }, TOAST_EXIT_MS),
      );
    }
  }, [shown]);
  useEffect(() => {
    const timers = exitTimers.current;
    return () => {
      for (const handle of timers.values()) window.clearTimeout(handle);
      timers.clear();
    };
  }, []);

  const alerts = shown.filter((e) => e.toast.kind === "error");
  const statuses = shown.filter((e) => e.toast.kind !== "error");
  return (
    <div className="nx-toasts pointer-events-none fixed right-4 flex flex-col">
      {children}
      <div className="nx-toast-region" role="status" aria-live="polite" aria-label={tr("通知")}>
        {statuses.map((e) => (
          <Toast key={e.toast.id} toast={e.toast} leaving={e.leaving} />
        ))}
      </div>
      <div className="nx-toast-region" role="alert" aria-live="assertive" aria-label={tr("错误通知")}>
        {alerts.map((e) => (
          <Toast key={e.toast.id} toast={e.toast} leaving={e.leaving} />
        ))}
      </div>
    </div>
  );
}

function Toast({ toast: t, leaving }: { toast: ToastItem; leaving: boolean }) {
  const dismissToast = useUi((s) => s.dismissToast);
  const holdToast = useUi((s) => s.holdToast);
  const releaseToast = useUi((s) => s.releaseToast);
  const Icon = ICONS[t.kind];
  const remaining = t.remaining ?? t.duration;
  const progress =
    t.duration && remaining !== undefined
      ? ({ "--nx-toast-ms": `${remaining}ms`, "--nx-toast-left": String(Math.min(1, remaining / t.duration)) } as CSSProperties)
      : null;
  return (
    <div
      data-toast-kind={t.kind}
      data-held={t.held ? "" : undefined}
      aria-hidden={leaving || undefined}
      inert={leaving || undefined}
      onMouseEnter={() => holdToast(t.id)}
      onMouseLeave={() => releaseToast(t.id)}
      onFocus={() => holdToast(t.id)}
      onBlur={() => releaseToast(t.id)}
      className={`nx-toast${leaving ? " is-leaving" : ""} pointer-events-auto relative flex items-start gap-2.5 overflow-hidden rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[var(--shadow-pop)] backdrop-blur ${TONES[t.kind]}`}
    >
      <div className="flex min-w-0 flex-1 items-start gap-2.5 text-left">
        <Icon size={14} className={`mt-px shrink-0 ${ICON_TONES[t.kind]}`} />
        <div className="min-w-0 flex-1 select-text">
          <span>{t.text}</span>
          {t.detail ? (
            <details className="mt-1" onToggle={(event) => (event.currentTarget.open ? holdToast(t.id) : releaseToast(t.id))}>
              <summary className="cursor-pointer text-[11px] opacity-80">{tr("详情")}</summary>
              <pre className="mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-words text-[11px] opacity-80">{t.detail}</pre>
            </details>
          ) : null}
        </div>
      </div>
      {t.action ? (
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
          onClick={() => {
            dismissToast(t.id);
            t.action?.onSelect();
          }}
        >
          {t.action.label}
        </button>
      ) : null}
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
        aria-label={tr("关闭通知")}
        title={tr("关闭")}
        onClick={() => dismissToast(t.id)}
      >
        <IconClose size={12} />
      </button>
      {progress && !leaving ? (
        // Keyed by the remaining time so a hold restarts the bar from where the timer stopped.
        <div key={remaining} className="nx-toast-progress" style={progress} aria-hidden="true" />
      ) : null}
    </div>
  );
}
