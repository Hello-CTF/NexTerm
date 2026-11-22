import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { isImeKeyEvent } from "./imeKeyEvent";

// WebKit (Safari, WKWebView, WebKitGTK) commits a composition on Enter and then delivers that Enter's keydown after
// compositionend, with isComposing false and sometimes a real keyCode 13. Chromium-based engines report the
// committing Enter as isComposing / keyCode 229 and need no rule; there a real Enter after compositionend is a new
// Enter (Android keyboards commit the word and send Enter in the same frame).
export const COMPOSITION_COMMIT_WINDOW_MS = 50;

/**
 * WebKit engines: desktop Safari, WKWebView (macOS desktop, iOS and iPadOS, where every browser is WebKit) and
 * WebKitGTK. Chromium-based engines (Chrome, Edge, WebView2, Android WebView) also carry "AppleWebKit" in their user
 * agent but name Chrome or Chromium, and Android browsers name Android.
 */
export function isWebKitEngine(userAgent: string): boolean {
  return /AppleWebKit\//.test(userAgent) && !/Chrome|Chromium|Android/.test(userAgent);
}

interface CommitEnterConfig {
  webkit: boolean | null;
  now: () => number;
}

const config: CommitEnterConfig = { webkit: null, now: () => performance.now() };
let pending: { target: EventTarget | null; at: number } | null = null;
// The keydown WebKit delivered for the Enter that committed the last composition, recorded before React sees it.
let commitKeydown: Event | null = null;

function deliversCommitEnter(): boolean {
  if (config.webkit === null) config.webkit = typeof navigator !== "undefined" && isWebKitEngine(navigator.userAgent);
  return config.webkit;
}

function onCompositionEnd(event: Event) {
  pending = deliversCommitEnter() ? { target: event.target, at: config.now() } : null;
}

// At most one keydown, the first after compositionend, can be the committing Enter.
function onKeyDown(event: KeyboardEvent) {
  const last = pending;
  pending = null;
  const commit =
    last !== null &&
    event.key === "Enter" &&
    event.target === last.target &&
    config.now() - last.at <= COMPOSITION_COMMIT_WINDOW_MS;
  commitKeydown = commit ? event : null;
}

if (typeof document !== "undefined") {
  document.addEventListener("compositionend", onCompositionEnd, true);
  document.addEventListener("keydown", onKeyDown, true);
}

/** Test hook: the engine rule (null detects it from the user agent) and the clock. */
export function configureImeCommitEnter(next: Partial<CommitEnterConfig>): void {
  if ("webkit" in next) config.webkit = next.webkit ?? null;
  if (next.now) config.now = next.now;
  pending = null;
  commitKeydown = null;
}

/**
 * Whether an Enter keydown in a text field belongs to the input method and must not submit: it arrived
 * mid-composition, carries keyCode 229, or is the Enter WebKit delivers right after compositionend to commit the text.
 * Fields that submit on Enter check this instead of isImeKeyEvent.
 */
export function isImeEnter(event: KeyboardEvent | ReactKeyboardEvent): boolean {
  if (isImeKeyEvent(event)) return true;
  const native = "nativeEvent" in event ? event.nativeEvent : event;
  return commitKeydown !== null && native === commitKeydown;
}
