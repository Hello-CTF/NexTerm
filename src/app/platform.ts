import { useSyncExternalStore, type CSSProperties } from "react";

const COARSE_POINTER_QUERY = "(pointer: coarse)";

export function coarsePointerMedia(): MediaQueryList {
  return window.matchMedia(COARSE_POINTER_QUERY);
}

export function isCoarsePointer(): boolean {
  return coarsePointerMedia().matches;
}

function subscribeCoarsePointer(callback: () => void): () => void {
  const query = coarsePointerMedia();
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}

export function useCoarsePointer(): boolean {
  return useSyncExternalStore(subscribeCoarsePointer, isCoarsePointer);
}

let macPlatform =
  typeof navigator !== "undefined" &&
  (/Mac/i.test(navigator.platform ?? "") || /Macintosh/.test(navigator.userAgent));

export function isMac(): boolean {
  return macPlatform;
}

const mobileUA =
  typeof navigator !== "undefined" && /iPhone|iPad|Android/i.test(navigator.userAgent);

export function isMobile(): boolean {
  return mobileUA;
}

export function isLandscapeFullScreen(): boolean {
  if (!isCoarsePointer()) return false;
  const w = window.innerWidth;
  const h = window.innerHeight;
  return Math.min(w, h) < 500 && w > h;
}

function subscribeLandscapeFull(callback: () => void): () => void {
  window.addEventListener("resize", callback);
  window.addEventListener("orientationchange", callback);
  window.visualViewport?.addEventListener("resize", callback);
  return () => {
    window.removeEventListener("resize", callback);
    window.removeEventListener("orientationchange", callback);
    window.visualViewport?.removeEventListener("resize", callback);
  };
}

export function useLandscapeFullScreen(): boolean {
  return useSyncExternalStore(subscribeLandscapeFull, isLandscapeFullScreen);
}

export function modHint(): string {
  return macPlatform ? "⌘" : "Ctrl";
}

export function setMacPlatform(fromBackend: boolean): void {
  macPlatform = fromBackend;
}

export const wailsDragRegionStyle = {
  "--wails-draggable": "drag",
} as CSSProperties;

export const wailsNoDragRegionStyle = {
  "--wails-draggable": "no-drag",
} as CSSProperties;

export function isWailsDragRegionTarget(target: EventTarget | null): boolean {
  return target instanceof Element && target.matches("[data-wails-drag-region]");
}
