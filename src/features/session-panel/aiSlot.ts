import { useSyncExternalStore } from "react";

// The AI assistant is mounted once for the whole app so running conversations survive moving it. The session panel's
// AI tab publishes an empty element here while it is shown; the shell then moves the one AI sidebar into it, and back
// into the right dock when the tab closes.
let slot: HTMLElement | null = null;
const listeners = new Set<() => void>();

export function setAiPanelSlot(element: HTMLElement | null): void {
  if (slot === element) return;
  slot = element;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function snapshot(): HTMLElement | null {
  return slot;
}

export function useAiPanelSlot(): HTMLElement | null {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}
