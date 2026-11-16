import { useCallback, useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";

/**
 * Reports whether a one-row toolbar must collapse: true once its content is wider than the toolbar, false again when
 * the toolbar has grown back to the width its full content needed. The check runs after every render (before paint,
 * so a badge or a longer title that no longer fits collapses without a visible frame) and on every resize.
 */
export function useCollapseOnOverflow(ref: RefObject<HTMLElement | null>): boolean {
  const [collapsed, setCollapsed] = useState(false);
  const collapsedRef = useRef(false);
  const neededWidth = useRef(0);
  const check = useCallback(() => {
    const element = ref.current;
    if (!element) return;
    let next = collapsedRef.current;
    if (!next && element.scrollWidth > element.clientWidth + 1) {
      neededWidth.current = element.scrollWidth;
      next = true;
    } else if (next && element.clientWidth >= neededWidth.current) {
      next = false;
    }
    if (next !== collapsedRef.current) {
      collapsedRef.current = next;
      setCollapsed(next);
    }
  }, [ref]);
  useLayoutEffect(check);
  useEffect(() => {
    const element = ref.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(check);
    observer.observe(element);
    return () => observer.disconnect();
  }, [ref, check]);
  return collapsed;
}
