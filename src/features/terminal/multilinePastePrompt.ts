import { useSyncExternalStore } from "react";
import { findTab, useUi } from "../../app/store";

// The multi-line paste prompt: a queue of open requests shown one at a time by MultilinePasteDialogHost, and the
// in-memory set of terminal tabs whose user chose 本终端不再提示.

export interface MultilinePasteDecision {
  paste: boolean;
  // Only meaningful when paste is true: the user also asked not to be prompted again in this tab, or anywhere.
  skipTab: boolean;
  skipAlways: boolean;
}

export interface MultilinePastePrompt {
  id: number;
  lineCount: number;
  preview: string[];
  // tabScoped offers 本终端不再提示; a paste without a known tab cannot remember it.
  tabScoped: boolean;
  // The workspace tab the paste goes to; the prompt is cancelled if that tab closes while it is open.
  tabId: string | null;
  settle: (decision: MultilinePasteDecision) => void;
}

const CANCELLED: MultilinePasteDecision = { paste: false, skipTab: false, skipAlways: false };

let queue: MultilinePastePrompt[] = [];
let hosts = 0;
let nextId = 0;
const listeners = new Set<() => void>();

function publish(next: MultilinePastePrompt[]): void {
  queue = next;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function currentPrompt(): MultilinePastePrompt | null {
  return queue[0] ?? null;
}

export function useMultilinePastePrompt(): MultilinePastePrompt | null {
  return useSyncExternalStore(subscribe, currentPrompt);
}

// registerMultilinePasteHost marks a mounted dialog host; without one, callers fall back to the generic dialog.
export function registerMultilinePasteHost(): () => void {
  hosts += 1;
  return () => {
    hosts -= 1;
    if (hosts === 0) cancelMultilinePastePrompts();
  };
}

export function multilinePasteHostMounted(): boolean {
  return hosts > 0;
}

export function requestMultilinePastePrompt(
  input: Omit<MultilinePastePrompt, "id" | "settle">,
): Promise<MultilinePasteDecision> {
  return new Promise((resolve) => {
    nextId += 1;
    const prompt: MultilinePastePrompt = {
      ...input,
      id: nextId,
      settle: (decision) => {
        if (!queue.includes(prompt)) return;
        publish(queue.filter((entry) => entry !== prompt));
        syncTabWatcher();
        resolve(decision);
      },
    };
    publish([...queue, prompt]);
    syncTabWatcher();
  });
}

// cancelMultilinePastePrompts settles every open prompt as cancelled, e.g. when the account changes.
export function cancelMultilinePastePrompts(): void {
  for (const prompt of [...queue]) prompt.settle(CANCELLED);
}

const skippedTabs = new Set<string>();
let stopWatchingTabs: (() => void) | null = null;

// A closed tab takes its skip choice and any prompt still asking about it along, and an account switch forgets every
// skip choice, so no choice outlives the tab or the account it was made in.
function onTabsChanged(accountChanged: boolean): void {
  if (accountChanged) skippedTabs.clear();
  for (const tabId of skippedTabs) {
    if (!findTab(tabId)) skippedTabs.delete(tabId);
  }
  for (const prompt of [...queue]) {
    if (prompt.tabId !== null && !findTab(prompt.tabId)) prompt.settle(CANCELLED);
  }
  syncTabWatcher();
}

// The store is watched only while a skip choice or a tab-bound prompt exists.
function syncTabWatcher(): void {
  const needed = skippedTabs.size > 0 || queue.some((prompt) => prompt.tabId !== null);
  if (needed && !stopWatchingTabs) {
    stopWatchingTabs = useUi.subscribe((state, previous) => {
      const accountChanged = state.accountEpoch !== previous.accountEpoch;
      if (accountChanged || state.workspaces !== previous.workspaces) onTabsChanged(accountChanged);
    });
  } else if (!needed && stopWatchingTabs) {
    stopWatchingTabs();
    stopWatchingTabs = null;
  }
}

// Only a workspace tab can be exempted: its id is stable across terminal remounts and its closing is observable.
export function multilinePasteTabSkippable(tabId: string | undefined): tabId is string {
  return tabId !== undefined && findTab(tabId) !== null;
}

export function skipMultilinePasteForTab(tabId: string): void {
  if (!multilinePasteTabSkippable(tabId)) return;
  skippedTabs.add(tabId);
  syncTabWatcher();
}

export function multilinePasteSkippedForTab(tabId: string): boolean {
  return skippedTabs.has(tabId);
}

export function resetMultilinePastePromptForTest(): void {
  cancelMultilinePastePrompts();
  skippedTabs.clear();
  stopWatchingTabs?.();
  stopWatchingTabs = null;
}
