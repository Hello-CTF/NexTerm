import { useSyncExternalStore } from "react";
import { ESC, ctrlByte, encodeWithModifiers, type KeyModifiers } from "./keyEncoding";

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

function takesModifiers(data: string, modifiers: KeyModifiers): boolean {
  if (modifiers.ctrl) return ctrlByte(data) !== null;
  // Alt as Meta: ESC + one printable ASCII character.
  return data.length === 1 && data >= " " && data <= "~";
}

/**
 * Applies the latch to input typed on the system keyboard: Ctrl to a character with a control form (Ctrl + "c"
 * sends ^C), Alt to one printable ASCII character. Other committed text (a multi-character IME commit, emoji,
 * dictation, CJK) is sent unchanged and releases the latch, so it cannot surprise a later key. Escape sequences
 * (terminal reports, arrow keys) pass through and leave the latch alone.
 */
export function applyStickyModifiersToTyped(data: string): string {
  if (!latched.ctrl && !latched.alt) return data;
  if (data.startsWith(ESC)) return data;
  if (takesModifiers(data, latched)) return consumeStickyModifiers(data);
  clearStickyModifiers();
  return data;
}
