import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

// Scrolling within this distance of the top loads the previous history page.
export const HISTORY_AUTOLOAD_TOP_PX = 48;

interface PendingLoad {
  anchor: { index: number; offset: number } | null;
  count: number;
  windowStart: number;
}

export interface HistoryAutoloadOptions {
  // windowStart is the first loaded history message; 0 means nothing older remains.
  windowStart: number;
  itemCount: number;
  // loadOlder returns false when it loaded nothing.
  loadOlder: () => boolean;
  // holdPosition stops following the end before older items are prepended.
  holdPosition: () => void;
  // anchorIndex scrolls so the item at index sits offset pixels below the top, for an item that is not mounted.
  anchorIndex: (index: number, offset: number) => void;
  // acknowledgeGrowth records the grown height so prepended history is not reported as new output.
  acknowledgeGrowth: () => void;
}

export interface HistoryAutoload {
  scrollRef: (el: HTMLElement | null) => void;
  // loadOlder loads the previous page and keeps the item at the top of the viewport where it was.
  loadOlder: () => void;
}

function firstVisibleItem(el: HTMLElement): { index: number; offset: number } | null {
  const top = el.getBoundingClientRect().top;
  for (const node of el.querySelectorAll<HTMLElement>("[data-conversation-index]")) {
    const rect = node.getBoundingClientRect();
    if (rect.height <= 0 || rect.bottom <= top) continue;
    const index = Number(node.dataset.conversationIndex);
    if (Number.isInteger(index)) return { index, offset: rect.top - top };
  }
  return null;
}

export function useHistoryAutoload(options: HistoryAutoloadOptions): HistoryAutoload {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const pendingRef = useRef<PendingLoad | null>(null);

  const scrollRef = useCallback((node: HTMLElement | null) => {
    setEl(node);
  }, []);

  const loadOlder = useCallback(() => {
    const current = optionsRef.current;
    // One page at a time: the next load waits until the previous page has rendered and the position is restored.
    if (pendingRef.current || current.windowStart <= 0) return;
    const pending: PendingLoad = {
      anchor: el ? firstVisibleItem(el) : null,
      count: current.itemCount,
      windowStart: current.windowStart,
    };
    current.holdPosition();
    pendingRef.current = pending;
    if (!current.loadOlder()) pendingRef.current = null;
  }, [el]);

  useEffect(() => {
    if (!el) return;
    const onScroll = () => {
      if (el.scrollTop <= HISTORY_AUTOLOAD_TOP_PX && el.scrollHeight > el.clientHeight) loadOlder();
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [el, loadOlder]);

  const { windowStart, itemCount } = options;
  useLayoutEffect(() => {
    const pending = pendingRef.current;
    if (!pending || pending.windowStart === windowStart) return;
    pendingRef.current = null;
    const current = optionsRef.current;
    const anchor = pending.anchor;
    if (!anchor || !el) {
      current.acknowledgeGrowth();
      return;
    }
    // Older items are prepended, so the anchored item moved down by the number of new items.
    const index = anchor.index + (itemCount - pending.count);
    const node = el.querySelector<HTMLElement>(`[data-conversation-index="${index}"]`);
    if (node) {
      const shift = node.getBoundingClientRect().top - el.getBoundingClientRect().top - anchor.offset;
      if (shift !== 0) el.scrollTop += shift;
    } else {
      current.anchorIndex(index, anchor.offset);
    }
    current.acknowledgeGrowth();
  }, [el, windowStart, itemCount]);

  return { scrollRef, loadOlder };
}
