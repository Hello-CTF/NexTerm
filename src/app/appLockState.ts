import { useSyncExternalStore } from "react";
import { keyboardCaptureOwner, releaseKeyboard } from "./inputCapture";
import { t } from "../i18n";

// The app lock covers the whole interface until the user proves who they are again. Sessions, persistent
// terminals and transfers keep running underneath; only the view and the input are withheld.
//
// The locked flag lives in localStorage so a reload, a second browser tab or a restart of the desktop app opens
// locked again. The password is never stored: unlocking verifies it against the credential vault (desktop and
// mobile) or the account's data key envelope (browser server mode) and then forgets it.

const LOCK_KEY = "nexterm.appLock.v1";
const IDLE_KEY = "nexterm.appLock.idleMinutes.v1";
export const APP_LOCK_ROOT_ID = "nx-app-lock-root";
export const MAX_APP_LOCK_IDLE_MINUTES = 1440;

export type AppLockMethod = "vault" | "account";

function readFlag(): boolean {
  try {
    return localStorage.getItem(LOCK_KEY) === "1";
  } catch {
    return false;
  }
}

function writeFlag(value: boolean): void {
  try {
    if (value) localStorage.setItem(LOCK_KEY, "1");
    else localStorage.removeItem(LOCK_KEY);
  } catch {
    // The lock then lasts for this page only.
  }
}

let locked = readFlag();
let redactedCount = 0;
const listeners = new Set<() => void>();

function publish(): void {
  for (const listener of [...listeners]) listener();
}

export function isAppLocked(): boolean {
  return locked;
}

export function subscribeAppLock(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useAppLocked(): boolean {
  return useSyncExternalStore(subscribeAppLock, isAppLocked);
}

function setLocked(next: boolean, persist = true): void {
  if (persist) writeFlag(next);
  if (locked === next) return;
  locked = next;
  redactedCount = 0;
  if (next) withdrawFocus();
  publish();
}

// withdrawFocus takes the keyboard away from whatever had it: a terminal's hidden textarea, a remote desktop that
// captured the keyboard, or a fullscreen pane, which would otherwise stay above the lock screen in the top layer.
function withdrawFocus(): void {
  if (typeof document === "undefined") return;
  const owner = keyboardCaptureOwner();
  if (owner) releaseKeyboard(owner);
  const active = document.activeElement;
  if (active instanceof HTMLElement && !active.closest(`#${APP_LOCK_ROOT_ID}`)) active.blur();
  if (document.fullscreenElement && typeof document.exitFullscreen === "function") {
    void document.exitFullscreen().catch(() => undefined);
  }
}

/** Locks the interface without checking for an unlock method; requestAppLock is the user-facing entry. */
export function lockApp(): void {
  setLocked(true);
}

/** Clears the lock; call only after the unlock credential was verified. */
export function unlockApp(): void {
  setLocked(false);
}

// Notifications raised while locked carry no content: one replaced notification counts how many arrived.
export const LOCKED_NOTIFICATION_ID = "nexterm-app-locked";

export function redactLockedNotification<T extends { id: string; title: string; body?: string; tabId?: string }>(
  notification: T,
): T | { id: string; title: string; body: string } {
  if (!locked) return notification;
  redactedCount += 1;
  return {
    id: LOCKED_NOTIFICATION_ID,
    title: t("NexTerm 已锁定"),
    body: t("{count} 条新通知，解锁后查看", { count: redactedCount }),
  };
}

export function readAppLockIdleMinutes(): number {
  try {
    const raw = Number(localStorage.getItem(IDLE_KEY) ?? "0");
    return Number.isInteger(raw) && raw > 0 && raw <= MAX_APP_LOCK_IDLE_MINUTES ? raw : 0;
  } catch {
    return 0;
  }
}

const idleListeners = new Set<() => void>();
let idleMinutes = readAppLockIdleMinutes();

export function appLockIdleMinutes(): number {
  return idleMinutes;
}

export function setAppLockIdleMinutes(minutes: number): void {
  const next = Number.isInteger(minutes) && minutes > 0 && minutes <= MAX_APP_LOCK_IDLE_MINUTES ? minutes : 0;
  try {
    if (next === 0) localStorage.removeItem(IDLE_KEY);
    else localStorage.setItem(IDLE_KEY, String(next));
  } catch {
    // Applies to this page only.
  }
  if (next === idleMinutes) return;
  idleMinutes = next;
  for (const listener of [...idleListeners]) listener();
}

export function subscribeAppLockIdle(listener: () => void): () => void {
  idleListeners.add(listener);
  return () => {
    idleListeners.delete(listener);
  };
}

export function useAppLockIdleMinutes(): number {
  return useSyncExternalStore(subscribeAppLockIdle, appLockIdleMinutes);
}

// The guard runs in the capture phase on window and is installed when this module loads, before any component
// registers its own window listeners, so a locked app delivers no key, pointer, clipboard or drop event to a
// terminal, an editor or a remote desktop canvas. Only the lock screen itself receives input.
const GUARDED_EVENTS = [
  "keydown",
  "keyup",
  "keypress",
  "beforeinput",
  "input",
  "compositionstart",
  "paste",
  "cut",
  "copy",
  "drop",
  "dragover",
  "pointerdown",
  "pointerup",
  "mousedown",
  "mouseup",
  "click",
  "dblclick",
  "auxclick",
  "contextmenu",
  "wheel",
  "touchstart",
  "touchmove",
  "touchend",
] as const;

function insideLockScreen(target: EventTarget | null): boolean {
  return target instanceof Node && !!document.getElementById(APP_LOCK_ROOT_ID)?.contains(target);
}

function guard(event: Event): void {
  if (!locked || insideLockScreen(event.target)) return;
  event.stopImmediatePropagation();
  if (event.cancelable) event.preventDefault();
}

function guardFocus(event: FocusEvent): void {
  if (!locked || insideLockScreen(event.target)) return;
  event.stopImmediatePropagation();
  const target = event.target;
  if (target instanceof HTMLElement) target.blur();
  document.getElementById(APP_LOCK_ROOT_ID)?.querySelector<HTMLElement>("input, button")?.focus();
}

function onStorage(event: StorageEvent): void {
  if (event.key !== LOCK_KEY && event.key !== null) return;
  setLocked(readFlag(), false);
}

let guardInstalled = false;

export function installAppLockGuard(): void {
  if (guardInstalled || typeof window === "undefined") return;
  guardInstalled = true;
  for (const type of GUARDED_EVENTS) window.addEventListener(type, guard, { capture: true, passive: false });
  window.addEventListener("focusin", guardFocus, true);
  // Another tab locking or unlocking applies here as well.
  window.addEventListener("storage", onStorage);
}

installAppLockGuard();

export function resetAppLockForTest(): void {
  writeFlag(false);
  try {
    localStorage.removeItem(IDLE_KEY);
  } catch {
    // Ignore
  }
  locked = false;
  redactedCount = 0;
  idleMinutes = 0;
  publish();
}
