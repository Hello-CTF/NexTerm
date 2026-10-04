import type { CSSProperties } from "react";

let macPlatform =
  typeof navigator !== "undefined" &&
  (/Mac/i.test(navigator.platform ?? "") || /Macintosh/.test(navigator.userAgent));

export function isMac(): boolean {
  return macPlatform;
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
