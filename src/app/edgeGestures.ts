import { useEffect, useRef } from "react";
import { isTouchShell } from "./shell/shellMode";

const EDGE_PX = 20;
const SWIPE_MIN_PX = 32;

interface EdgeGestureHandlers {
  // The session sheet ("session") is closed with its own button or Escape, not a swipe.
  overlayDock: "left" | "right" | "session" | null;
  openLeft: () => void;
  openRight: () => void;
  closeOverlay: () => void;
}

// Recognize a sidebar-pull gesture only when the touch starts within 20pt of the left/right screen edge; other touches pass through to terminal and list scrolling;
// once the overlay is open, swipe back from the scrim/panel (or tap the scrim) to collapse. Touch screens only (coarse/mobile); desktop is unaffected.
export function useEdgeGestures(handlers: EdgeGestureHandlers): void {
  const ref = useRef(handlers);
  ref.current = handlers;
  useEffect(() => {
    if (!isTouchShell()) return;
    let startX = 0;
    let startY = 0;
    let edge: "left" | "right" | null = null;
    let startTarget: EventTarget | null = null;

    const onTouchStart = (e: TouchEvent) => {
      const t = e.touches[0];
      if (!t) return;
      startX = t.clientX;
      startY = t.clientY;
      startTarget = e.target;
      edge =
        startX <= EDGE_PX ? "left" : startX >= window.innerWidth - EDGE_PX ? "right" : null;
    };
    const onTouchMove = (e: TouchEvent) => {
      if (!edge) return;
      const t = e.touches[0];
      if (!t) return;
      const dx = t.clientX - startX;
      const dy = t.clientY - startY;
      // Intercept once the edge horizontal swipe is established, so xterm does not also treat it as text selection
      if (Math.abs(dx) > 8 && Math.abs(dx) > Math.abs(dy)) e.preventDefault();
    };
    const onTouchEnd = (e: TouchEvent) => {
      const t = e.changedTouches[0];
      const from = edge;
      const target = startTarget;
      edge = null;
      startTarget = null;
      if (!t) return;
      const dx = t.clientX - startX;
      const dy = t.clientY - startY;
      if (Math.abs(dx) < SWIPE_MIN_PX || Math.abs(dx) <= Math.abs(dy)) return;
      const { overlayDock, openLeft, openRight, closeOverlay } = ref.current;
      const inOverlayChrome =
        target instanceof Element &&
        target.closest(".nx-dock-backdrop, .nx-left-dock, .nx-right-dock") !== null;
      if (inOverlayChrome && overlayDock === "left" && dx < 0) closeOverlay();
      else if (inOverlayChrome && overlayDock === "right" && dx > 0) closeOverlay();
      else if (from === "left" && dx > 0) openLeft();
      else if (from === "right" && dx < 0) openRight();
    };
    window.addEventListener("touchstart", onTouchStart, { passive: true });
    window.addEventListener("touchmove", onTouchMove, { passive: false });
    window.addEventListener("touchend", onTouchEnd, { passive: true });
    return () => {
      window.removeEventListener("touchstart", onTouchStart);
      window.removeEventListener("touchmove", onTouchMove);
      window.removeEventListener("touchend", onTouchEnd);
    };
  }, []);
}
