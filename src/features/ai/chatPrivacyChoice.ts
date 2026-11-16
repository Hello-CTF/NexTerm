import { useSyncExternalStore } from "react";

// The temporary redaction choice of an AI chat tab. It applies to the messages that tab sends from now on and never
// changes the model profile or the global setting; without one the tab follows them.
export type ChatPrivacyChoice = "on" | "off";

const choices = new Map<string, ChatPrivacyChoice>();
const listeners = new Set<() => void>();

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function chatPrivacyChoice(tabKey: string): ChatPrivacyChoice | undefined {
  return choices.get(tabKey);
}

// setChatPrivacyChoice sets the tab's choice; null goes back to following the profile and the global setting.
export function setChatPrivacyChoice(tabKey: string, choice: ChatPrivacyChoice | null): void {
  if (choice === null ? !choices.has(tabKey) : choices.get(tabKey) === choice) return;
  if (choice === null) choices.delete(tabKey);
  else choices.set(tabKey, choice);
  listeners.forEach((listener) => listener());
}

export function useChatPrivacyChoice(tabKey: string): ChatPrivacyChoice | undefined {
  return useSyncExternalStore(subscribe, () => choices.get(tabKey));
}
