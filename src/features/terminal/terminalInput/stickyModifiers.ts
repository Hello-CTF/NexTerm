import { useSyncExternalStore } from "react";
import { encodeWithModifiers, type KeyModifiers } from "./keyEncoding";

// The key bar's latched Ctrl/Alt. There is one system keyboard, so there is one latch: it applies to the next key
// from the bar or the next character typed into the focused terminal, whichever comes first.

const NONE: KeyModifiers = Object.freeze({ ctrl: false, alt: false });

let latched: KeyModifiers = NONE;
const listeners = new Set<() => void>();

function publish(next: KeyModifiers): void {
  if (next.ctrl === latched.ctrl && next.alt === latched.alt) return;
  latched = next.ctrl || next.alt ? Object.freeze({ ...next }) : NONE;
  for (const listener of listeners) listener();
}

export function getStickyModifiers(): KeyModifiers {
  return latched;
}

export function toggleStickyModifier(modifier: keyof KeyModifiers): void {
  publish({ ...latched, [modifier]: !latched[modifier] });
}

export function clearStickyModifiers(): void {
  publish(NONE);
}

export function subscribeStickyModifiers(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useStickyModifiers(): KeyModifiers {
  return useSyncExternalStore(subscribeStickyModifiers, getStickyModifiers);
}

/** Encodes one key's bytes with the latched modifiers and releases the latch. */
export function consumeStickyModifiers(data: string): string {
  const modifiers = latched;
  clearStickyModifiers();
  return encodeWithModifiers(data, modifiers);
}

/**
 * Applies the latch to input typed on the system keyboard. Only a single typed character takes the modifiers
 * (Ctrl latched + "c" sends ^C); anything longer, such as a paste or a terminal report, passes through unchanged
 * and leaves the latch for the next key.
 */
export function applyStickyModifiersToTyped(data: string): string {
  if (!latched.ctrl && !latched.alt) return data;
  if ([...data].length !== 1) return data;
  return consumeStickyModifiers(data);
}
