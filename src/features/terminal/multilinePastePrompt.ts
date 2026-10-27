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
        resolve(decision);
      },
    };
    publish([...queue, prompt]);
  });
}

// cancelMultilinePastePrompts settles every open prompt as cancelled, e.g. when the account changes.
export function cancelMultilinePastePrompts(): void {
  for (const prompt of [...queue]) prompt.settle(CANCELLED);
}

const skippedTabs = new Set<string>();
let stopWatchingTabs: (() => void) | null = null;

// A skipped tab is forgotten as soon as it leaves every workspace, so a closed tab's choice never outlives it.
function pruneClosedTabs(): void {
  for (const tabId of skippedTabs) {
    if (!findTab(tabId)) skippedTabs.delete(tabId);
  }
  if (skippedTabs.size === 0 && stopWatchingTabs) {
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
  if (!stopWatchingTabs) {
    stopWatchingTabs = useUi.subscribe((state, previous) => {
      if (state.workspaces !== previous.workspaces) pruneClosedTabs();
    });
  }
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
