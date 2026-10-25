import { useEffect, useState } from "react";
import "./skeleton.css";

// The fallback stays invisible for a short grace period so chunks that arrive from cache never flash a placeholder.
const REVEAL_DELAY_MS = 150;

export function PanelFallback({ label = "正在加载" }: { label?: string }) {
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const timer = window.setTimeout(() => setVisible(true), REVEAL_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, []);
  return (
    <div
      role="status"
      aria-live="polite"
      aria-label={label}
      className="flex h-full min-h-0 w-full flex-col gap-2 p-4"
      style={{ opacity: visible ? 1 : 0 }}
    >
      <span className="nx-skeleton" style={{ width: "38%" }} />
      <span className="nx-skeleton" style={{ width: "72%" }} />
      <span className="nx-skeleton" style={{ width: "56%" }} />
      <span className="sr-only">{label}</span>
    </div>
  );
}
