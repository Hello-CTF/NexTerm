import { useCallback, useEffect, useRef, useState } from "react";

export const FOLLOW_BOTTOM_THRESHOLD_PX = 32;

export interface ScrollMetrics {
  scrollTop: number;
  clientHeight: number;
  scrollHeight: number;
}

export function isNearBottom(
  metrics: ScrollMetrics,
  threshold: number = FOLLOW_BOTTOM_THRESHOLD_PX,
): boolean {
  return metrics.scrollHeight - metrics.scrollTop - metrics.clientHeight <= threshold;
}

function scrollToBottom(el: HTMLElement) {
  const top = el.scrollHeight;
  if (typeof el.scrollTo === "function") {
    el.scrollTo({ top });
  } else {
    el.scrollTop = top;
  }
}

export interface ConversationFollow {
  scrollRef: (el: HTMLElement | null) => void;
  newOutput: boolean;
  following: boolean;
  jumpToLatest: () => void;
}

export function useConversationFollow(revision: unknown): ConversationFollow {
  const [el, setEl] = useState<HTMLElement | null>(null);
  const elRef = useRef<HTMLElement | null>(null);
  const followRef = useRef(true);
  const [following, setFollowing] = useState(true);
  const [newOutput, setNewOutput] = useState(false);
  const lastHeightRef = useRef(0);
  const jumpRef = useRef<() => void>(() => {});

  const scrollRef = useCallback((node: HTMLElement | null) => {
    elRef.current = node;
    setEl(node);
  }, []);

  const setFollow = (value: boolean) => {
    followRef.current = value;
    setFollowing(value);
    if (value) setNewOutput(false);
  };

  jumpRef.current = () => {
    const node = elRef.current;
    setFollow(true);
    if (node) {
      scrollToBottom(node);
      lastHeightRef.current = node.scrollHeight;
    }
  };

  useEffect(() => {
    if (!el) return;
    const onScroll = () => {
      const near = isNearBottom(el);
      if (near !== followRef.current) setFollow(near);
      if (near) setNewOutput(false);
      lastHeightRef.current = el.scrollHeight;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    lastHeightRef.current = el.scrollHeight;
    return () => el.removeEventListener("scroll", onScroll);
  }, [el]);

  useEffect(() => {
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (followRef.current) scrollToBottom(el);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [el]);

  useEffect(() => {
    if (!el) return;
    if (followRef.current) {
      scrollToBottom(el);
    } else if (el.scrollHeight > lastHeightRef.current) {
      setNewOutput(true);
    }
    lastHeightRef.current = el.scrollHeight;
  }, [el, revision]);

  return { scrollRef, newOutput, following, jumpToLatest: () => jumpRef.current() };
}
