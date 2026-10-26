import { useSyncExternalStore } from "react";

// How a touch shell takes text: "direct" types into the terminal as keys are pressed; "buffered" edits a whole line
// in a local input (system dictation and autocorrect work there) and sends it at once. The choice belongs to the
// device, since it depends on its keyboard, so it is stored locally and never synced.
export type TerminalInputMode = "direct" | "buffered";

const STORAGE_KEY = "nexterm.terminalInputMode.v1";
const listeners = new Set<() => void>();

function read(): TerminalInputMode {
  try {
    return localStorage.getItem(STORAGE_KEY) === "buffered" ? "buffered" : "direct";
  } catch {
    return "direct";
  }
}

let mode: TerminalInputMode = read();

export function getTerminalInputMode(): TerminalInputMode {
  return mode;
}

export function setTerminalInputMode(next: TerminalInputMode): void {
  try {
    if (next === "direct") localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, next);
  } catch {
  }
  if (mode === next) return;
  mode = next;
  for (const listener of listeners) listener();
}

/** Re-reads the stored choice, for tests that change storage between renders. */
export function reloadTerminalInputMode(): void {
  const next = read();
  if (next === mode) return;
  mode = next;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useTerminalInputMode(): TerminalInputMode {
  return useSyncExternalStore(subscribe, getTerminalInputMode);
}

/**
 * Lets a tap on the terminal focus the buffered input field instead. The field only takes the tap when buffered
 * mode is on and the field is rendered (a wide window hides the key bar and the field); otherwise the tap falls back
 * to focusing the terminal. Returns true when the field took the tap.
 */
export function takeTapForComposer(composer: HTMLInputElement | null): boolean {
  if (mode !== "buffered" || !composer?.isConnected || composer.getClientRects().length === 0) return false;
  composer.focus({ preventScroll: true });
  return document.activeElement === composer;
}
