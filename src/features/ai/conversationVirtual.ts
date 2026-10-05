import { useCallback, useEffect, useRef, useState } from "react";

export const VIRTUAL_ITEM_ESTIMATE_PX = 88;
export const VIRTUAL_OVERSCAN = 6;
export const VIRTUAL_THRESHOLD = 60;

export interface VirtualRange {
  active: boolean;
  start: number;
  end: number;
  padTop: number;
  padBottom: number;
}

export function virtualRange(
  count: number,
  scrollTop: number,
  viewportHeight: number,
  estimate: number = VIRTUAL_ITEM_ESTIMATE_PX,
  overscan: number = VIRTUAL_OVERSCAN,
): VirtualRange {
  if (count <= VIRTUAL_THRESHOLD) {
    return { active: false, start: 0, end: count, padTop: 0, padBottom: 0 };
  }
  const start = Math.max(0, Math.floor(Math.max(0, scrollTop) / estimate) - overscan);
  const visible = Math.ceil(Math.max(0, viewportHeight) / estimate) + overscan * 2;
  const end = Math.min(count, start + visible);
  return {
    active: true,
    start,
    end,
    padTop: start * estimate,
    padBottom: (count - end) * estimate,
  };
}

export function virtualOffsetForIndex(
  index: number,
  estimate: number = VIRTUAL_ITEM_ESTIMATE_PX,
): number {
  return Math.max(0, index) * estimate;
}

export function centeredScrollTop(
  containerViewportTop: number,
  currentScrollTop: number,
  containerHeight: number,
  itemViewportTop: number,
  itemHeight: number,
): number {
  const centered =
    currentScrollTop + (itemViewportTop - containerViewportTop) - (containerHeight - itemHeight) / 2;
  return Math.max(0, centered);
}

export interface VirtualWindow {
  scrollRef: (el: HTMLElement | null) => void;
  revealIndex: (index: number) => void;
  range: VirtualRange;
}

function centerElement(
  container: HTMLElement,
  target: Element,
  pending: { current: number | null },
): void {
  const containerRect = container.getBoundingClientRect();
  const targetRect = target.getBoundingClientRect();
  const next = centeredScrollTop(
    containerRect.top,
    container.scrollTop,
    container.clientHeight,
    targetRect.top,
    targetRect.height,
  );
  if (Math.abs(next - container.scrollTop) <= 1) {
    pending.current = null;
  }
  container.scrollTop = next;
}

export function useVirtualWindow(count: number): VirtualWindow {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const [metrics, setMetrics] = useState({ scrollTop: 0, viewportHeight: 0 });
  const revealTargetRef = useRef<number | null>(null);
  const frozenWindowRef = useRef<{ start: number; end: number } | null>(null);

  const scrollRef = useCallback((node: HTMLElement | null) => {
    setEl(node);
  }, []);

  useEffect(() => {
    if (!el) return;
    const measure = () => {
      setMetrics((prev) =>
        prev.scrollTop === el.scrollTop && prev.viewportHeight === el.clientHeight
          ? prev
          : { scrollTop: el.scrollTop, viewportHeight: el.clientHeight },
      );
    };
    measure();
    el.addEventListener("scroll", measure, { passive: true });
    return () => el.removeEventListener("scroll", measure);
  }, [el]);

  const derivedRange = virtualRange(count, metrics.scrollTop, metrics.viewportHeight);
  const frozen = frozenWindowRef.current;
  const range =
    frozen && derivedRange.active && frozen.start < count
      ? {
          active: true as const,
          start: frozen.start,
          end: Math.min(frozen.end, count),
          padTop: frozen.start * VIRTUAL_ITEM_ESTIMATE_PX,
          padBottom: (count - Math.min(frozen.end, count)) * VIRTUAL_ITEM_ESTIMATE_PX,
        }
      : derivedRange;
  const rangeRef = useRef(range);
  rangeRef.current = range;

  const settleReveal = useCallback((node: HTMLElement, target: Element) => {
    if (frozenWindowRef.current === null) {
      frozenWindowRef.current = { start: rangeRef.current.start, end: rangeRef.current.end };
    }
    centerElement(node, target, revealTargetRef);
    if (revealTargetRef.current === null) {
      frozenWindowRef.current = null;
    }
  }, []);

  const revealIndex = useCallback(
    (index: number) => {
      const node = el;
      if (!node) return;
      revealTargetRef.current = index;
      const mounted = node.querySelector(`[data-conversation-index="${index}"]`);
      if (mounted) {
        settleReveal(node, mounted);
        return;
      }
      node.scrollTop = virtualOffsetForIndex(index);
    },
    [el, settleReveal],
  );

  useEffect(() => {
    const index = revealTargetRef.current;
    if (index === null || !el) return;
    if (index >= count) {
      revealTargetRef.current = null;
      frozenWindowRef.current = null;
      return;
    }
    const target = el.querySelector(`[data-conversation-index="${index}"]`);
    if (target) settleReveal(el, target);
  });

  return {
    scrollRef,
    revealIndex,
    range,
  };
}
