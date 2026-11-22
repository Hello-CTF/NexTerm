import { useSyncExternalStore } from "react";

// The AI tabs waiting for the user: an approval to grant or a question to answer. Kept apart from the AI panel code
// so the app header can show the flag without loading the panel.
const waiting = new Set<string>();
const listeners = new Set<() => void>();

export function setAiTabNeedsInput(tabKey: string, needsInput: boolean): void {
  if (waiting.has(tabKey) === needsInput) return;
  if (needsInput) waiting.add(tabKey);
  else waiting.delete(tabKey);
  for (const listener of [...listeners]) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function aiNeedsInputCount(): number {
  return waiting.size;
}

export function useAiNeedsInputCount(): number {
  return useSyncExternalStore(subscribe, aiNeedsInputCount);
}
