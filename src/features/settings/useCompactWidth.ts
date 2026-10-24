import { useCallback, useEffect, useRef, useState } from "react";

// Returns [attachRef, compact]: compact is true while the element is narrower than `max` px.
// A zero width (jsdom, hidden panes) keeps the wide layout. The ref is a callback so the observer follows remounts.
export function useCompactWidth(max: number): [(el: HTMLElement | null) => void, boolean] {
  const [compact, setCompact] = useState(false);
  const observer = useRef<ResizeObserver | null>(null);
  useEffect(() => () => observer.current?.disconnect(), []);
  const attach = useCallback((el: HTMLElement | null) => {
    observer.current?.disconnect();
    observer.current = null;
    if (!el) return;
    const update = (width: number) => setCompact(width > 0 && width < max);
    update(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    observer.current = new ResizeObserver((entries) => {
      const entry = entries[entries.length - 1];
      if (entry) update(entry.contentRect.width);
    });
    observer.current.observe(el);
  }, [max]);
  return [attach, compact];
}
