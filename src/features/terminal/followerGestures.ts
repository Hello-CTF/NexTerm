import { pinchDirection } from "./followerScale";

// Gestures of a terminal that shows another device's PTY size. A pinch switches between the whole grid and a larger
// font that fits the rows; while the columns are wider than the pane, a horizontal swipe or wheel pans them. One-finger
// vertical swipes stay with the terminal's own scrolling (touchScroll), which ignores horizontal swipes and pinches.

export interface FollowerGestureOptions {
  // The terminal follows another device's size now.
  following: () => boolean;
  // The columns are wider than the pane.
  panX: () => boolean;
  onPinch: (direction: "in" | "out") => void;
}

const PAN_SLOP_PX = 10;

function distance(touches: TouchList): number {
  const a = touches[0];
  const b = touches[1];
  if (!a || !b) return 0;
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
}

export function attachFollowerGestures(host: HTMLElement, options: FollowerGestureOptions): () => void {
  let pinchStart = 0;
  let pinchLast = 0;
  let pan: { x: number; y: number; left: number; active: boolean } | null = null;

  const onStart = (event: TouchEvent) => {
    if (!options.following()) return;
    if (event.touches.length === 2) {
      pan = null;
      pinchStart = pinchLast = distance(event.touches);
      return;
    }
    if (event.touches.length === 1 && options.panX()) {
      const touch = event.touches[0];
      pan = { x: touch.clientX, y: touch.clientY, left: host.scrollLeft, active: false };
    }
  };
  const onMove = (event: TouchEvent) => {
    if (pinchStart > 0 && event.touches.length === 2) {
      pinchLast = distance(event.touches);
      return;
    }
    if (!pan || event.touches.length !== 1) return;
    const touch = event.touches[0];
    const dx = touch.clientX - pan.x;
    const dy = touch.clientY - pan.y;
    if (!pan.active) {
      if (Math.hypot(dx, dy) <= PAN_SLOP_PX) return;
      if (Math.abs(dx) < Math.abs(dy)) {
        pan = null;
        return;
      }
      pan.active = true;
    }
    event.preventDefault();
    host.scrollLeft = pan.left - dx;
  };
  const onEnd = (event: TouchEvent) => {
    if (pinchStart > 0 && event.touches.length < 2) {
      const direction = pinchDirection(pinchStart, pinchLast);
      pinchStart = pinchLast = 0;
      if (direction && options.following()) options.onPinch(direction);
    }
    if (event.touches.length === 0) pan = null;
  };
  const onWheel = (event: WheelEvent) => {
    if (!options.following() || !options.panX() || Math.abs(event.deltaX) <= Math.abs(event.deltaY)) return;
    event.preventDefault();
    host.scrollLeft += event.deltaX;
  };

  host.addEventListener("touchstart", onStart, { capture: true, passive: true });
  host.addEventListener("touchmove", onMove, { capture: true, passive: false });
  host.addEventListener("touchend", onEnd, { capture: true, passive: true });
  host.addEventListener("touchcancel", onEnd, { capture: true, passive: true });
  host.addEventListener("wheel", onWheel, { capture: true, passive: false });
  return () => {
    host.removeEventListener("touchstart", onStart, true);
    host.removeEventListener("touchmove", onMove, true);
    host.removeEventListener("touchend", onEnd, true);
    host.removeEventListener("touchcancel", onEnd, true);
    host.removeEventListener("wheel", onWheel, true);
  };
}
