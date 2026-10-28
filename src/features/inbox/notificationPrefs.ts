import { useSyncExternalStore } from "react";

// Notification preferences belong to the device: whether this phone, browser or desktop pops system notifications
// is decided where the user sits, so they are kept in local storage and never synced.
export interface NotificationPrefs {
  // system lets this device show operating system notifications at all.
  system: boolean;
  // needsMe: a session rang the bell, sent a notification or matched a state rule that asks for the user.
  needsMe: boolean;
  // finished: a long command (the helper's notable threshold) finished.
  finished: boolean;
  // ai: the AI assistant's notify tool.
  ai: boolean;
}

export const DEFAULT_NOTIFICATION_PREFS: NotificationPrefs = { system: true, needsMe: true, finished: true, ai: true };

const STORAGE_KEY = "nexterm.notifications.v1";

let cached: NotificationPrefs | null = null;
const listeners = new Set<() => void>();

function read(): NotificationPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULT_NOTIFICATION_PREFS;
    const parsed = JSON.parse(raw) as Partial<Record<keyof NotificationPrefs, unknown>>;
    const pick = (key: keyof NotificationPrefs) =>
      typeof parsed[key] === "boolean" ? (parsed[key] as boolean) : DEFAULT_NOTIFICATION_PREFS[key];
    return {
      system: pick("system"),
      needsMe: pick("needsMe"),
      finished: pick("finished"),
      ai: pick("ai"),
    };
  } catch {
    return DEFAULT_NOTIFICATION_PREFS;
  }
}

export function getNotificationPrefs(): NotificationPrefs {
  cached ??= read();
  return cached;
}

export function setNotificationPrefs(patch: Partial<NotificationPrefs>): void {
  cached = { ...getNotificationPrefs(), ...patch };
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(cached));
  } catch {
    // Storage may be unavailable (private mode); the choice still applies until reload.
  }
  for (const listener of listeners) listener();
}

export function subscribeNotificationPrefs(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useNotificationPrefs(): NotificationPrefs {
  return useSyncExternalStore(subscribeNotificationPrefs, getNotificationPrefs, getNotificationPrefs);
}

export function resetNotificationPrefsForTest(): void {
  cached = null;
}
