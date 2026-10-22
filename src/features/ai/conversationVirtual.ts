import { useCallback, useEffect, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

export const VIRTUAL_ITEM_ESTIMATE_PX = 88;
export const VIRTUAL_ITEM_GAP_PX = 10;
export const VIRTUAL_OVERSCAN = 6;
export const VIRTUAL_THRESHOLD = 60;
export const CONVERSATION_SCROLL_SETTLE_MS = 150;

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
  clearReveal: () => void;
  range: VirtualRange;
}

function centerElement(
  container: HTMLElement,
  target: Element,
  scrollTo: ((top: number) => void) | undefined,
  force: boolean,
): number {
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
  if (force || Math.abs(next - container.scrollTop) > 1) {
    if (scrollTo) scrollTo(next);
    else container.scrollTop = next;
  }
  return next;
}

export function useVirtualWindow(count: number): VirtualWindow {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const [boundaryIndex, setBoundaryIndex] = useState<number | null>(null);
  const revealTargetRef = useRef<number | null>(null);
  const revealOffsetRef = useRef<number | null>(null);
  const followEndRef = useRef(true);

  const scrollRef = useCallback((node: HTMLElement | null) => {
    setEl(node);
  }, []);

  const virtualizer = useVirtualizer({
    count,
    getScrollElement: () => el,
    estimateSize: () => VIRTUAL_ITEM_ESTIMATE_PX,
    overscan: VIRTUAL_OVERSCAN,
    gap: VIRTUAL_ITEM_GAP_PX,
    isScrollingResetDelay: CONVERSATION_SCROLL_SETTLE_MS,
    paddingStart: count > VIRTUAL_THRESHOLD && boundaryIndex !== null ? (el?.clientHeight ?? 0) : 0,
    paddingEnd: count > VIRTUAL_THRESHOLD && boundaryIndex !== null ? (el?.clientHeight ?? 0) : 0,
    onChange: () => {
      if (!el || count <= VIRTUAL_THRESHOLD || !followEndRef.current) return;
      if (typeof el.scrollTo === "function") el.scrollTo({ top: el.scrollHeight });
      else el.scrollTop = el.scrollHeight;
    },
  });

  const totalSize = virtualizer.getTotalSize();
  const range = windowRange(count, virtualizer.getVirtualItems(), totalSize);

  useEffect(() => {
    if (!el) return;
    const onWheel = (event: WheelEvent) => {
      revealTargetRef.current = null;
      revealOffsetRef.current = null;
      if (event.deltaY < 0) followEndRef.current = false;
    };
    const onScroll = () => {
      const expected = revealOffsetRef.current;
      if (revealTargetRef.current !== null && expected !== null && Math.abs(el.scrollTop - expected) > 1) {
        revealTargetRef.current = null;
        revealOffsetRef.current = null;
      }
      if (el.scrollHeight - el.scrollTop - el.clientHeight <= 32) followEndRef.current = true;
    };
    el.addEventListener("wheel", onWheel, { passive: true });
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("scroll", onScroll);
    };
  }, [el]);

  const settleReveal = useCallback((node: HTMLElement, target: Element) => {
    const force = revealOffsetRef.current === null;
    revealOffsetRef.current = centerElement(
      node,
      target,
      count > VIRTUAL_THRESHOLD
        ? (top) => virtualizer.scrollToOffset(top, { align: "start", behavior: "auto" })
        : undefined,
      force,
    );
  }, [count, virtualizer]);

  const clearReveal = useCallback(() => {
    revealTargetRef.current = null;
    revealOffsetRef.current = null;
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = undefined;
    setBoundaryIndex(null);
  }, [virtualizer]);

  const revealIndex = useCallback(
    (index: number) => {
      followEndRef.current = false;
      revealTargetRef.current = index;
      revealOffsetRef.current = null;
      if (count > VIRTUAL_THRESHOLD) {
        virtualizer.shouldAdjustScrollPositionOnItemSizeChange = () => false;
      }
      setBoundaryIndex(index);
      const node = el;
      if (!node) return;
      const mounted = node.querySelector(`[data-conversation-index="${index}"]`);
      if (count > VIRTUAL_THRESHOLD) {
        if (!mounted && index === 0) {
          const offset = virtualizer.getOffsetForIndex(index, "center");
          if (offset) node.scrollTop = offset[0];
        } else if (!mounted) {
          virtualizer.scrollToIndex(index, { align: "center" });
        }
        return;
      }
      if (mounted) settleReveal(node, mounted);
    },
    [count, el, virtualizer, settleReveal],
  );

  useEffect(() => {
    const index = revealTargetRef.current;
    if (index === null || !el) return;
    if (index >= count) {
      clearReveal();
      return;
    }
    const expected = revealOffsetRef.current;
    if (expected !== null && Math.abs(el.scrollTop - expected) > 1) {
      revealTargetRef.current = null;
      revealOffsetRef.current = null;
      return;
    }
    const target = el.querySelector(`[data-conversation-index="${index}"]`);
    if (target) {
      settleReveal(el, target);
    } else if (count > VIRTUAL_THRESHOLD && index === 0) {
      const offset = virtualizer.getOffsetForIndex(index, "center");
      if (offset) el.scrollTop = offset[0];
    } else if (count > VIRTUAL_THRESHOLD) {
      virtualizer.scrollToIndex(index, { align: "center" });
    }
  });

  return {
    scrollRef,
    measureRef: virtualizer.measureElement,
    revealIndex,
    clearReveal,
    range,
  };
}
