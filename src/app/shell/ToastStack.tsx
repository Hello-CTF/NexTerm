import { useUi } from "../store";
import { IconCheckCircle, IconClose, IconInfo, IconXCircle } from "../../ui/icons";

export function ToastStack() {
  const toasts = useUi((s) => s.toasts);
  const dismissToast = useUi((s) => s.dismissToast);
  const holdToast = useUi((s) => s.holdToast);
  const releaseToast = useUi((s) => s.releaseToast);
  return (
    <div className="nx-toasts pointer-events-none fixed right-4 flex flex-col gap-2">
      {toasts.map((t) => {
        const Icon = t.kind === "error" ? IconXCircle : t.kind === "success" ? IconCheckCircle : IconInfo;
        const tone =
          t.kind === "error"
            ? "border-red-500/40 bg-red-950/90 text-red-100"
            : t.kind === "success"
              ? "nx-alert-success"
              : "border-neutral-700 bg-neutral-800/95 text-neutral-100";
        return (
          <div
            key={t.id}
            onMouseEnter={() => holdToast(t.id)}
            onMouseLeave={() => releaseToast(t.id)}
            onFocus={() => holdToast(t.id)}
            onBlur={() => releaseToast(t.id)}
            className={`pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[var(--shadow-pop)] backdrop-blur ${tone}`}
          >
            <div className="flex min-w-0 flex-1 items-start gap-2.5 text-left">
              <Icon
                size={14}
                className={
                  t.kind === "error"
                    ? "mt-px shrink-0 text-red-300"
                    : t.kind === "success"
                      ? "mt-px shrink-0 text-green-300"
                      : "mt-px shrink-0 text-blue-300"
                }
              />
              <span className="min-w-0 flex-1 select-text">{t.text}</span>
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
              aria-label="关闭通知"
              title="关闭"
              onClick={() => dismissToast(t.id)}
            >
              <IconClose size={12} />
            </button>
          </div>
        );
      })}
    </div>
  );
}
