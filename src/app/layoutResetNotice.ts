import { useSyncExternalStore } from "react";

// Workspaces whose saved split layout predates split trees and was rebuilt as one pane. The notice
// is shown once per read of such a layout and stays until dismissed or the account changes.
let workspaces: readonly string[] | null = null;
// The format version of an account layout saved by a newer client; while set, layout sync is paused.
let newerVersion: number | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

export function noteLayoutReset(titles: readonly string[]): void {
  if (titles.length === 0) return;
  workspaces = [...new Set([...(workspaces ?? []), ...titles])];
  emit();
}

export function layoutResetNotice(): readonly string[] | null {
  return workspaces;
}

export function dismissLayoutResetNotice(): void {
  if (workspaces === null) return;
  workspaces = null;
  emit();
}

export function subscribeLayoutResetNotice(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useLayoutResetNotice(): readonly string[] | null {
  return useSyncExternalStore(subscribeLayoutResetNotice, layoutResetNotice);
}

/** Records (or with null, clears) that the account layout was saved in a newer, unreadable format. */
export function noteNewerLayout(version: number | null): void {
  if (newerVersion === version) return;
  newerVersion = version;
  emit();
}

export function newerLayoutNotice(): number | null {
  return newerVersion;
}

export function useNewerLayoutNotice(): number | null {
  return useSyncExternalStore(subscribeLayoutResetNotice, newerLayoutNotice);
}
