import { useEffect, useRef } from "react";
import { isCoarsePointer, isMobile } from "./platform";

const EDGE_PX = 20;
const SWIPE_MIN_PX = 32;

interface EdgeGestureHandlers {
  overlayDock: "left" | "right" | null;
  openLeft: () => void;
  openRight: () => void;
  closeOverlay: () => void;
}

// 只在屏幕左右缘 20pt 内起手才识别为拉侧栏手势, 其余触摸原样留给终端与列表滚动;
// overlay 打开后从遮罩/面板内反向滑动(或点遮罩)收起。仅触屏(coarse/mobile), 桌面不受影响。
export function useEdgeGestures(handlers: EdgeGestureHandlers): void {
  const ref = useRef(handlers);
  ref.current = handlers;
  useEffect(() => {
    if (!isCoarsePointer() && !isMobile()) return;
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
      // 边缘水平滑动成立后拦截, 免得 xterm 同时当文本选择处理
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
