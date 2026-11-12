import { useCallback, useEffect, useRef, type MouseEvent, type PointerEvent } from "react";

export const KEY_REPEAT_DELAY_MS = 400;
export const KEY_REPEAT_INTERVAL_MS = 50;
// The key bar scrolls horizontally: a finger that moves farther than this is panning, not holding a key.
export const KEY_REPEAT_SLOP_PX = 10;

interface Hold {
  pointerId: number;
  x: number;
  y: number;
  delay: ReturnType<typeof setTimeout> | null;
  interval: ReturnType<typeof setInterval> | null;
}

export interface KeyRepeatHandlers {
  onPointerDown: (event: PointerEvent<HTMLElement>) => void;
  onPointerMove: (event: PointerEvent<HTMLElement>) => void;
  onPointerUp: () => void;
  onPointerCancel: () => void;
  onPointerLeave: () => void;
  onContextMenu: (event: MouseEvent<HTMLElement>) => void;
}

/**
 * Long-press auto-repeat for key bar buttons. Holding a key sends it once after KEY_REPEAT_DELAY_MS and then every
 * KEY_REPEAT_INTERVAL_MS until the pointer lifts, leaves, is cancelled or moves past the slop. `repeat(true)` is the
 * first send of a hold. A click that ends a hold that already repeated is reported by `consumeClick` so the caller
 * does not send one extra key.
 */
export function useKeyAutoRepeat() {
  const hold = useRef<Hold | null>(null);
  const repeated = useRef(false);

  const stop = useCallback(() => {
    const current = hold.current;
    if (!current) return;
    if (current.delay !== null) clearTimeout(current.delay);
    if (current.interval !== null) clearInterval(current.interval);
    hold.current = null;
  }, []);

  useEffect(() => stop, [stop]);

  const bind = (repeat: (first: boolean) => void): KeyRepeatHandlers => ({
    onPointerDown: (event) => {
      stop();
      repeated.current = false;
      if (event.button !== 0) return;
      const current: Hold = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, delay: null, interval: null };
      current.delay = setTimeout(() => {
        current.delay = null;
        repeated.current = true;
        repeat(true);
        current.interval = setInterval(() => repeat(false), KEY_REPEAT_INTERVAL_MS);
      }, KEY_REPEAT_DELAY_MS);
      hold.current = current;
    },
    onPointerMove: (event) => {
      const current = hold.current;
      if (!current || event.pointerId !== current.pointerId) return;
      if (Math.hypot(event.clientX - current.x, event.clientY - current.y) > KEY_REPEAT_SLOP_PX) stop();
    },
    onPointerUp: stop,
    onPointerCancel: stop,
    onPointerLeave: stop,
    // A long press would otherwise open the system context menu or callout on touch screens.
    onContextMenu: (event) => event.preventDefault(),
  });

  /** True when this click ends a hold that already sent keys; the click must then send nothing. */
  const consumeClick = (): boolean => {
    const skip = repeated.current;
    repeated.current = false;
    return skip;
  };

  return { bind, consumeClick };
}
