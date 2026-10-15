import { useCallback, useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

export const VIRTUAL_ITEM_ESTIMATE_PX = 88;
export const VIRTUAL_ITEM_GAP_PX = 10;
export const VIRTUAL_OVERSCAN = 6;
export const VIRTUAL_THRESHOLD = 60;

export interface VirtualRange {
  active: boolean;
  start: number;
  end: number;
  padTop: number;
  padBottom: number;
}

export interface VirtualWindowItem {
  index: number;
  start: number;
  end: number;
}

export function windowRange(
  count: number,
  items: VirtualWindowItem[],
  totalSize: number,
): VirtualRange {
  if (count <= VIRTUAL_THRESHOLD) {
    return { active: false, start: 0, end: count, padTop: 0, padBottom: 0 };
  }
  const first = items[0];
  const last = items[items.length - 1];
  if (!first || !last) {
    const end = Math.min(count, VIRTUAL_OVERSCAN * 2);
    return {
      active: true,
      start: 0,
      end,
      padTop: 0,
      padBottom: (count - end) * (VIRTUAL_ITEM_ESTIMATE_PX + VIRTUAL_ITEM_GAP_PX),
    };
  }
  return {
    active: true,
    start: first.index,
    end: last.index + 1,
    padTop: first.start,
    padBottom: Math.max(0, totalSize - last.end),
  };
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
  measureRef: (el: HTMLElement | null) => void;
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
  const maxScroll = Math.max(0, container.scrollHeight - container.clientHeight);
  const next = Math.min(
    centeredScrollTop(
      containerRect.top,
      container.scrollTop,
      container.clientHeight,
      targetRect.top,
      targetRect.height,
    ),
    maxScroll,
  );
  if (Math.abs(next - container.scrollTop) <= 1) {
    pending.current = null;
  }
  container.scrollTop = next;
}

export function useVirtualWindow(count: number): VirtualWindow {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const revealTargetRef = useRef<number | null>(null);

  const scrollRef = useCallback((node: HTMLElement | null) => {
    setEl(node);
  }, []);

  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => el,
    estimateSize: () => VIRTUAL_ITEM_ESTIMATE_PX,
    overscan: VIRTUAL_OVERSCAN,
    gap: VIRTUAL_ITEM_GAP_PX,
  });

  const range = windowRange(count, virtualizer.getVirtualItems(), virtualizer.getTotalSize());

  const settleReveal = useCallback((node: HTMLElement, target: Element) => {
    centerElement(node, target, revealTargetRef);
  }, []);

  const revealIndex = useCallback(
    (index: number) => {
      if (count > VIRTUAL_THRESHOLD) {
        revealTargetRef.current = null;
        virtualizer.scrollToIndex(index, { align: "center" });
        return;
      }
      const node = el;
      if (!node) return;
      revealTargetRef.current = index;
      const mounted = node.querySelector(`[data-conversation-index="${index}"]`);
      if (mounted) settleReveal(node, mounted);
    },
    [count, el, virtualizer, settleReveal],
  );

  useEffect(() => {
    const index = revealTargetRef.current;
    if (index === null || !el) return;
    if (index >= count || count > VIRTUAL_THRESHOLD) {
      revealTargetRef.current = null;
      return;
    }
    const target = el.querySelector(`[data-conversation-index="${index}"]`);
    if (target) settleReveal(el, target);
  });

  return {
    scrollRef,
    measureRef: virtualizer.measureElement,
    revealIndex,
    range,
  };
}
