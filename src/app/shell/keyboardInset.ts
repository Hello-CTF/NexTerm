import { useEffect } from "react";
import { DESKTOP } from "../../ipc/env";
import { listenWailsEvent } from "../../ipc/wails";
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

// The Android app draws edge to edge, so the WebView keeps its height under the soft keyboard and neither
// visualViewport nor the VirtualKeyboard API sees it. Its bridge reports the keyboard as "common:keyboard"
// {visible, height}, height being the covered part of the WebView in CSS px.
export const NATIVE_KEYBOARD_EVENT = "common:keyboard";

// A keyboard reported taller than this share of the page is not plausible (a unit mix-up); it is ignored.
const NATIVE_KEYBOARD_MAX_SHARE = 0.8;

// nativeKeyboardInset reads a "common:keyboard" payload (an object, or its JSON text) as the inset to use, 0 when the
// keyboard is hidden. It returns null for a payload it cannot trust, which leaves the browser's own readings in use.
export function nativeKeyboardInset(payload: unknown, innerHeight: number): number | null {
  let value = payload;
  if (typeof value === "string") {
    try {
      value = JSON.parse(value) as unknown;
    } catch {
      return null;
    }
  }
  if (!value || typeof value !== "object") return null;
  const { visible, height } = value as { visible?: unknown; height?: unknown };
  if (visible === false) return 0;
  if (typeof height !== "number" || !Number.isFinite(height) || height < 0) return null;
  if (height === 0) return 0;
  if (!Number.isFinite(innerHeight) || innerHeight <= 0 || height > innerHeight * NATIVE_KEYBOARD_MAX_SHARE) return null;
  return height;
}

export function nativeKeyboardAvailable(desktop: boolean, userAgent: string): boolean {
  return desktop && /Android/i.test(userAgent);
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

    // The last native keyboard inset; null until the Android bridge reported one or after one it could not trust.
    // Only an open keyboard (above 0) overrides the browser's readings, so they stay the fallback.
    let nativeInset: number | null = null;
    // The inset overlays the layout viewport (native or VirtualKeyboard), so the visual viewport offset does not apply.
    let fromVk = false;
    const readInset = (): number => {
      fromVk = false;
      if (nativeInset !== null && nativeInset > 0) {
        fromVk = true;
        return Math.min(nativeInset, window.innerHeight);
      }
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
    let disposed = false;
    let unlistenNative: (() => void) | null = null;
    if (nativeKeyboardAvailable(DESKTOP, navigator.userAgent)) {
      listenWailsEvent<unknown>(NATIVE_KEYBOARD_EVENT, (payload) => {
        nativeInset = nativeKeyboardInset(payload, window.innerHeight);
        schedule();
      }).then(
        (off) => {
          if (disposed) off();
          else unlistenNative = off;
        },
        () => {},
      );
    }
    vv?.addEventListener("resize", schedule);
    vv?.addEventListener("scroll", schedule);
    window.addEventListener("resize", schedule);
    window.addEventListener("scroll", schedule, { passive: true });
    document.addEventListener("focusin", schedule, true);
    schedule();
    return () => {
      disposed = true;
      unlistenNative?.();
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
