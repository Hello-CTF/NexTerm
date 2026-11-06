import { useCallback, useEffect, useRef, useState } from "react";
import { IconCheckCircle, IconClose, IconInfo, IconXCircle } from "../../../src/ui/icons";
import "./demo.css";

export type Language = "zh" | "en";

export interface DemoProps {
  language: Language;
}

// pick returns the copy for the current language from a { zh, en } table.
export function pick<T>(language: Language, table: { zh: T; en: T }): T {
  return table[language];
}

export function usePrefersReducedMotion(): boolean {
  const query = "(prefers-reduced-motion: reduce)";
  const [reduced, setReduced] = useState(() => typeof window !== "undefined" && window.matchMedia?.(query).matches === true);
  useEffect(() => {
    const media = window.matchMedia?.(query);
    if (!media) return;
    const update = () => setReduced(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);
  return reduced;
}

export type ToastKind = "info" | "success" | "error";
interface ToastItem {
  id: number;
  kind: ToastKind;
  text: string;
}

// useToasts mirrors the app's toast stack inside the demo window; each toast dismisses itself after a few seconds.
export function useToasts(language: Language) {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const next = useRef(0);
  const timers = useRef(new Set<number>());
  useEffect(() => {
    const pending = timers.current;
    return () => pending.forEach((timer) => window.clearTimeout(timer));
  }, []);
  const dismiss = useCallback((id: number) => setToasts((list) => list.filter((toast) => toast.id !== id)), []);
  const push = useCallback((kind: ToastKind, text: string) => {
    const id = ++next.current;
    setToasts((list) => [...list.slice(-2), { id, kind, text }]);
    const timer = window.setTimeout(() => {
      timers.current.delete(timer);
      dismiss(id);
    }, 3200);
    timers.current.add(timer);
  }, [dismiss]);
  const view = (
    <div className="demo-toasts" role="status" aria-live="polite">
      {toasts.map((toast) => {
        const Icon = toast.kind === "error" ? IconXCircle : toast.kind === "success" ? IconCheckCircle : IconInfo;
        return <div key={toast.id} className={`demo-toast is-${toast.kind}`}>
          <Icon size={14} />
          <span>{toast.text}</span>
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label={language === "zh" ? "关闭" : "Dismiss"} onClick={() => dismiss(toast.id)}><IconClose size={11} /></button>
        </div>;
      })}
    </div>
  );
  return { push, view };
}

// ConfirmDialog mirrors the app's confirmation dialog: a title, the message, then Cancel and OK.
export function ConfirmDialog({ title, message, cancel, ok, danger = false, onClose }: {
  title: string;
  message: string;
  cancel: string;
  ok: string;
  danger?: boolean;
  onClose: (confirmed: boolean) => void;
}) {
  const okRef = useRef<HTMLButtonElement>(null);
  useEffect(() => okRef.current?.focus(), []);
  return <div className="demo-overlay" onClick={() => onClose(false)}>
    <div className="nx-modal" role="dialog" aria-modal="true" aria-label={title} onClick={(event) => event.stopPropagation()} onKeyDown={(event) => { if (event.key === "Escape") onClose(false); }}>
      <div className="nx-modal-header">{title}</div>
      <div className="nx-modal-body">{message}</div>
      <div className="nx-modal-footer">
        <button type="button" className="nx-btn nx-btn-ghost" onClick={() => onClose(false)}>{cancel}</button>
        <button ref={okRef} type="button" className={`nx-btn ${danger ? "nx-btn-danger-solid" : "nx-btn-primary"}`} onClick={() => onClose(true)}>{ok}</button>
      </div>
    </div>
  </div>;
}

// copyToClipboard copies text for a demo action and reports whether the browser allowed it.
export async function copyToClipboard(text: string): Promise<void> {
  if (!navigator.clipboard) throw new Error("Clipboard API unavailable");
  await navigator.clipboard.writeText(text);
}

// errorText turns a thrown value into a short message.
export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
