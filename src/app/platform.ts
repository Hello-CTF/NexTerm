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

let windowsPlatform =
  typeof navigator !== "undefined" && /Win/i.test(navigator.platform ?? "");

export function isWindows(): boolean {
  return windowsPlatform;
}

export function setWindowsPlatform(fromBackend: boolean): void {
  windowsPlatform = fromBackend;
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

// The OS of the machine that hosts local sessions: the desktop itself, or the server in browser mode.
let backendOS: string | null = null;

export function setBackendOS(os: string | null): void {
  backendOS = os;
}

// Local sessions run on the backend, so their filesystem follows the backend OS, not the browser's.
export function localSessionIsWindows(): boolean {
  return backendOS === null ? windowsPlatform : backendOS === "windows";
}

export function localSessionIsMac(): boolean {
  return backendOS === null ? macPlatform : backendOS === "macos" || backendOS === "darwin";
}

/**
 * The backend OS describes the machine the UI runs on only for the desktop transport. In the browser the backend is a
 * server, so the UA-based detection of the user's own machine stays.
 */
export function applyBackendPlatform(os: unknown, desktop: boolean): void {
  if (typeof os !== "string" || os.length === 0) return;
  backendOS = os;
  if (!desktop) return;
  setMacPlatform(os === "macos" || os === "darwin");
  setWindowsPlatform(os === "windows");
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
