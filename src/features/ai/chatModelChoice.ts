import { useSyncExternalStore } from "react";

// The model profile an AI chat tab uses instead of the default one. A choice made in the AI panel stays with its tab
// and never changes the default, which only the model settings set.
const choices = new Map<string, string>();
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function chatModelChoice(tabKey: string): string | undefined {
  return choices.get(tabKey);
}

// setChatModelChoice picks a profile for the tab; null goes back to the default profile.
export function setChatModelChoice(tabKey: string, profileId: string | null): void {
  if (profileId === null ? !choices.has(tabKey) : choices.get(tabKey) === profileId) return;
  if (profileId === null) choices.delete(tabKey);
  else choices.set(tabKey, profileId);
  listeners.forEach((listener) => listener());
}

export function useChatModelChoice(tabKey: string): string | undefined {
  return useSyncExternalStore(subscribe, () => choices.get(tabKey));
}
