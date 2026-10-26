import { useEffect } from "react";
import { isEditableTarget } from "../../ui/DialogHost";

export function visualViewportInset(
  innerHeight: number,
  vvHeight: number,
  vvOffsetTop: number,
): number {
  if (!Number.isFinite(innerHeight) || !Number.isFinite(vvHeight)) return 0;
  const offset = Number.isFinite(vvOffsetTop) ? vvOffsetTop : 0;
  return Math.max(0, innerHeight - vvHeight - offset);
}

// keyboardViewportBox is the area of the layout viewport that stays visible above an overlaying
// keyboard. The app root is sized to it, so nothing depends on the keyboard accessory bar height.
export function keyboardViewportBox(
  innerHeight: number,
  inset: number,
  vvOffsetTop: number,
  fromVirtualKeyboard: boolean,
): { top: number; height: number } | null {
  if (!Number.isFinite(innerHeight) || !Number.isFinite(inset) || inset <= 0) return null;
  const rawTop = fromVirtualKeyboard || !Number.isFinite(vvOffsetTop) ? 0 : vvOffsetTop;
  const top = Math.min(Math.max(0, rawTop), innerHeight);
  const height = Math.max(0, innerHeight - inset - top);
  return height > 0 ? { top: Math.round(top), height: Math.round(height) } : null;
}

// Below this visible height (landscape phone with keyboard) secondary chrome is hidden so the terminal keeps room.
export const TIGHT_KEYBOARD_VIEWPORT_HEIGHT = 340;

export function isTightKeyboardViewport(height: number): boolean {
  return Number.isFinite(height) && height < TIGHT_KEYBOARD_VIEWPORT_HEIGHT;
}

export function virtualKeyboardInset(
  innerHeight: number,
  keyboardTop: number,
  keyboardHeight: number,
): number {
  if (!Number.isFinite(innerHeight) || !Number.isFinite(keyboardTop)) return 0;
  if (!Number.isFinite(keyboardHeight) || keyboardHeight <= 0) return 0;
  return Math.max(0, Math.min(innerHeight, innerHeight - keyboardTop));
}

interface VirtualKeyboardLike extends EventTarget {
  overlaysContent: boolean;
  readonly boundingRect: DOMRect;
}

export function useKeyboardInset(): void {
  useEffect(() => {
    const root = document.documentElement;
    const vv = window.visualViewport ?? null;
    let frame: number | null = null;
    let vk: VirtualKeyboardLike | undefined;

    let fromVk = false;
    const readInset = (): number => {
      fromVk = false;
      if (vk) {
        try {
          fromVk = true;
          return virtualKeyboardInset(
            window.innerHeight,
            vk.boundingRect.top,
            vk.boundingRect.height,
          );
        } catch {
          disableVk();
        }
      }
      if (!vv) return 0;
      return visualViewportInset(window.innerHeight, vv.height, vv.offsetTop);
    };

    const keepFocusedControlVisible = (inset: number) => {
      if (inset <= 0) return;
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !isEditableTarget(active)) return;
      const visibleBottom = window.innerHeight - inset;
      for (let node = active.parentElement; node; node = node.parentElement) {
        const overflow = active.getBoundingClientRect().bottom - visibleBottom;
        if (overflow <= 0) return;
        const style = getComputedStyle(node);
        if (/(auto|scroll|overlay)/.test(style.overflowY) && node.scrollHeight > node.clientHeight + 1) {
          node.scrollTop += overflow + 8;
        }
      }
      if (active.getBoundingClientRect().bottom > visibleBottom) {
        active.scrollIntoView({ block: "nearest" });
      }
    };

    const publish = () => {
      frame = null;
      const inset = readInset();
      root.style.setProperty("--nx-kb-inset", `${Math.round(inset)}px`);
      const box = keyboardViewportBox(window.innerHeight, inset, vv?.offsetTop ?? 0, fromVk);
      if (box) {
        root.style.setProperty("--nx-vv-top", `${box.top}px`);
        root.style.setProperty("--nx-vv-height", `${box.height}px`);
        root.dataset.nxKeyboard = "open";
        if (isTightKeyboardViewport(box.height)) root.dataset.nxKeyboardTight = "true";
        else delete root.dataset.nxKeyboardTight;
        // iOS scrolls the layout viewport to reveal the focused input; the fixed root follows
        // visualViewport.offsetTop, and the page itself is kept at the top.
        if (window.scrollY !== 0 || window.scrollX !== 0) window.scrollTo(0, 0);
      } else {
        delete root.dataset.nxKeyboardTight;
        root.style.removeProperty("--nx-vv-top");
        root.style.removeProperty("--nx-vv-height");
        delete root.dataset.nxKeyboard;
      }
      keepFocusedControlVisible(inset);
    };
    const schedule = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(publish);
    };

    function disableVk() {
      if (!vk) return;
      try {
        vk.overlaysContent = false;
      } catch {}
      try {
        vk.removeEventListener("geometrychanged", schedule);
      } catch {}
      vk = undefined;
    }

    try {
      const candidate = (navigator as Navigator & { virtualKeyboard?: VirtualKeyboardLike })
        .virtualKeyboard;
      if (candidate) {
        candidate.overlaysContent = true;
        candidate.addEventListener("geometrychanged", schedule);
        vk = candidate;
      }
    } catch {
      vk = undefined;
    }
    vv?.addEventListener("resize", schedule);
    vv?.addEventListener("scroll", schedule);
    window.addEventListener("resize", schedule);
    window.addEventListener("scroll", schedule, { passive: true });
    document.addEventListener("focusin", schedule, true);
    schedule();
    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      disableVk();
      vv?.removeEventListener("resize", schedule);
      vv?.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
      window.removeEventListener("scroll", schedule);
      document.removeEventListener("focusin", schedule, true);
      root.style.removeProperty("--nx-kb-inset");
      root.style.removeProperty("--nx-vv-top");
      root.style.removeProperty("--nx-vv-height");
      delete root.dataset.nxKeyboard;
      delete root.dataset.nxKeyboardTight;
    };
  }, []);
}
