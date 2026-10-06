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
