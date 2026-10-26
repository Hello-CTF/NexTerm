import { useUi } from "../store";
import { IconCheckCircle, IconInfo, IconXCircle } from "../../ui/icons";

export function ToastStack() {
  const toasts = useUi((s) => s.toasts);
  const dismissToast = useUi((s) => s.dismissToast);
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
            className={`pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[var(--shadow-pop)] backdrop-blur ${tone}`}
          >
            <button
              type="button"
              className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
              onClick={() => dismissToast(t.id)}
            >
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
              <span className="min-w-0 flex-1">{t.text}</span>
            </button>
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
          </div>
        );
      })}
    </div>
  );
}
