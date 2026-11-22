import { useSyncExternalStore } from "react";

// The AI tabs waiting for the user: an approval to grant or a question to answer. The chat view sets it from its
// pending card; runOwnership clears it when the run ends or the tab is closed, so a tab whose board is not mounted
// keeps its flag. Kept apart from the AI panel code so the app header can show it without loading the panel.
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

export function resetAiNeedsInputForTest(): void {
  waiting.clear();
  for (const listener of [...listeners]) listener();
}

export function aiNeedsInputCount(): number {
  return waiting.size;
}

export function useAiNeedsInputCount(): number {
  return useSyncExternalStore(subscribe, aiNeedsInputCount);
}
