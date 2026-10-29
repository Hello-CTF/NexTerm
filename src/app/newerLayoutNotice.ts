import { useSyncExternalStore } from "react";

// The format version of an account layout saved by a newer client; while set, layout sync is paused.
let newerVersion: number | null = null;
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
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
  return useSyncExternalStore(subscribe, newerLayoutNotice);
}
