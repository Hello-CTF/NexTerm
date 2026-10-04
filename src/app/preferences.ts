import { useSyncExternalStore } from "react";

export interface InputPrefs {
  selectionAutoCopy: boolean;
}

const STORAGE_KEY = "nexterm.inputPrefs.v1";

function load(): InputPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { selectionAutoCopy: false };
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return { selectionAutoCopy: false };
    return { selectionAutoCopy: (parsed as { selectionAutoCopy?: unknown }).selectionAutoCopy === true };
  } catch {
    return { selectionAutoCopy: false };
  }
}

let prefs: InputPrefs = load();
const listeners = new Set<() => void>();

export function getInputPrefs(): InputPrefs {
  return prefs;
}

export function setSelectionAutoCopy(value: boolean): void {
  if (prefs.selectionAutoCopy === value) return;
  prefs = { ...prefs, selectionAutoCopy: value };
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
  }
  for (const listener of listeners) listener();
}

export function subscribeInputPrefs(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useInputPrefs(): InputPrefs {
  return useSyncExternalStore(subscribeInputPrefs, getInputPrefs);
}
