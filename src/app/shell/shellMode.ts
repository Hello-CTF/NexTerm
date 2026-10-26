import { isCoarsePointer, isLandscapeFullScreen, isMobile } from "../platform";
import { workspaceViewport } from "../../features/terminal/workspaceLayout";

// ShellMode is the single answer to "which app shell is this":
// - "mobile": a phone or tablet build/browser (iPhone, iPad or Android user agent). It never changes at runtime.
// - "compact": any other device whose viewport puts the sidebars into overlays (width <= 820px,
//   or a coarse-pointer landscape screen shorter than 500px).
// - "desktop": everything else.
// Layout details that are not a mode (split height, the <=560px is-compact class, the terminal key bar)
// stay on ShellLayout so each consumer keeps reading the exact flag it used before.
export type ShellMode = "desktop" | "compact" | "mobile";

export function shellModeFor(mobileDevice: boolean, overlaySidebars: boolean): ShellMode {
  if (mobileDevice) return "mobile";
  return overlaySidebars ? "compact" : "desktop";
}

// overlaySidebarsFor is the app layout's overlay rule: the viewport width threshold, or landscape fullscreen,
// where the width often exceeds the threshold but the sidebars must not squeeze the fullscreen terminal.
export function overlaySidebarsFor(widthOverlay: boolean, landscapeFull: boolean): boolean {
  return widthOverlay || landscapeFull;
}

export function readShellMode(): ShellMode {
  const widthOverlay = workspaceViewport(window.innerWidth, false).overlaySidebars;
  return shellModeFor(isMobile(), overlaySidebarsFor(widthOverlay, isLandscapeFullScreen()));
}

// isMobileShell is the non-React form of useShell() === "mobile" for event handlers and memoized lists.
export function isMobileShell(): boolean {
  return isMobile();
}

// isTouchShell is true on mobile devices and on any coarse-pointer screen; edge swipe gestures use it.
export function isTouchShell(): boolean {
  return isCoarsePointer() || isMobile();
}

// isWidthOverlayViewport is the width-only overlay rule (<= 820px). Unlike the app layout it ignores
// landscape fullscreen; sidebar navigation that closes the overlay after picking an item uses it.
export function isWidthOverlayViewport(): boolean {
  return workspaceViewport(window.innerWidth, false).overlaySidebars;
}
