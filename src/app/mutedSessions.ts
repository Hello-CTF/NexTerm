import { useSyncExternalStore } from "react";
import { createSyncedPreference } from "./syncedPreference";

// A muted session's needs_me and just_finished states are hidden everywhere: no notification, no inbox entry, no
// badge. The list follows the account, so muting on one device mutes on all of them; a durable terminal keeps its
// tab id on every device that takes it over.
export const MUTED_SESSIONS_PREFERENCE_KEY = "terminal.mutedSessions";
const STORAGE_KEY = "nexterm.mutedSessions.v1";

// The limit internal/account/preferences.go enforces; muting past it forgets the oldest entries, which mostly name
// sessions that ended long ago.
export const MUTED_SESSIONS_LIMIT = 200;

// A temporary terminal's tab id is 32 lowercase hex characters; a durable terminal's is the 26-character id the backend
// generates with ids.New, the same on every device. internal/account/preferences.go accepts exactly these two forms.
const TAB_ID = /^(?:[0-9a-f]{32}|[0-9A-Za-z]{26})$/;

export function parseMutedSessions(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null;
  const ids: string[] = [];
  for (const entry of value) {
    if (typeof entry === "string" && TAB_ID.test(entry) && !ids.includes(entry)) ids.push(entry);
  }
  return ids.slice(-MUTED_SESSIONS_LIMIT);
}

const mutedSessions = createSyncedPreference<string[]>({
  key: MUTED_SESSIONS_PREFERENCE_KEY,
  localKey: STORAGE_KEY,
  parse: parseMutedSessions,
  fallback: () => [],
});

let cachedList: readonly string[] | null = null;
let cachedSet: ReadonlySet<string> = new Set();

function mutedSet(): ReadonlySet<string> {
  const list = mutedSessions.get();
  if (list !== cachedList) {
    cachedList = list;
    cachedSet = new Set(list);
  }
  return cachedSet;
}

export function isSessionMuted(tabId: string | undefined): boolean {
  return Boolean(tabId) && mutedSet().has(tabId as string);
}

export function subscribeMutedSessions(listener: () => void): () => void {
  return mutedSessions.subscribe(listener);
}

export function useSessionMuted(tabId: string | undefined): boolean {
  return useSyncExternalStore(mutedSessions.subscribe, () => isSessionMuted(tabId));
}

// setSessionMuted rejects when the tab id cannot be stored or the account write fails; the previous list is shown
// again then.
export async function setSessionMuted(tabId: string, muted: boolean): Promise<void> {
  if (!TAB_ID.test(tabId)) throw new Error(`unsupported terminal tab id: ${tabId}`);
  const current = mutedSessions.get().filter((id) => id !== tabId);
  if (muted) current.push(tabId);
  await mutedSessions.set(current.slice(-MUTED_SESSIONS_LIMIT));
}

export function useMutedSessionSet(): ReadonlySet<string> {
  return useSyncExternalStore(mutedSessions.subscribe, mutedSet);
}
