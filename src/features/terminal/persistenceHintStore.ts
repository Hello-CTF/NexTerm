import { useSyncExternalStore } from "react";

interface TabHintState {
  dismissed: ReadonlySet<string>;
  // The text of a failure hint the user dismissed while it still applies; the tab strip keeps a mark for it.
  failureMark?: string;
}

// Hint state per store tab, kept in memory for the life of the tab, so a pane that remounts the terminal (a split
// or a move) does not show a dismissed hint again.
const tabStates = new Map<string, TabHintState>();
const listeners = new Set<() => void>();
const NO_DISMISSALS: ReadonlySet<string> = new Set();

function update(tabId: string, next: TabHintState | undefined): void {
  if (next) tabStates.set(tabId, next);
  else tabStates.delete(tabId);
  for (const listener of listeners) listener();
}

export function dismissedPersistenceHints(tabId: string): ReadonlySet<string> {
  return tabStates.get(tabId)?.dismissed ?? NO_DISMISSALS;
}

export function dismissPersistenceHint(tabId: string, key: string): ReadonlySet<string> {
  const current = tabStates.get(tabId);
  const dismissed = new Set(current?.dismissed ?? NO_DISMISSALS).add(key);
  update(tabId, { ...current, dismissed });
  return dismissed;
}

export function setPersistenceFailureMark(tabId: string, text: string | undefined): void {
  const current = tabStates.get(tabId);
  if (current?.failureMark === text) return;
  if (!current && text === undefined) return;
  update(tabId, { dismissed: current?.dismissed ?? NO_DISMISSALS, failureMark: text });
}

export function forgetPersistenceHints(tabId: string): void {
  if (tabStates.has(tabId)) update(tabId, undefined);
}

// prunePersistenceHints drops the state of tabs that no longer exist.
export function prunePersistenceHints(liveTabIds: ReadonlySet<string>): void {
  for (const tabId of [...tabStates.keys()]) {
    if (!liveTabIds.has(tabId)) forgetPersistenceHints(tabId);
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function usePersistenceFailureMark(tabId: string): string | undefined {
  return useSyncExternalStore(subscribe, () => tabStates.get(tabId)?.failureMark);
}
