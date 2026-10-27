import { useSyncExternalStore } from "react";

// Whether this device already showed and dismissed the inbox guidance. It is a per-device note, never synced.
const STORAGE_KEY = "nexterm.inbox-guidance.v1";

let dismissed: boolean | null = null;
const listeners = new Set<() => void>();

function read(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "dismissed";
  } catch {
    return false;
  }
}

export function isInboxGuidanceDismissed(): boolean {
  dismissed ??= read();
  return dismissed;
}

export function dismissInboxGuidance(): void {
  dismissed = true;
  try {
    localStorage.setItem(STORAGE_KEY, "dismissed");
  } catch {
    // Storage may be unavailable (private mode); the guidance stays hidden until reload.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useInboxGuidanceDismissed(): boolean {
  return useSyncExternalStore(subscribe, isInboxGuidanceDismissed, isInboxGuidanceDismissed);
}

export function resetInboxGuidanceForTest(): void {
  dismissed = null;
}
