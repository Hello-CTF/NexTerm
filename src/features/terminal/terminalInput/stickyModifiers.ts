import { useSyncExternalStore } from "react";
import { ESC, ctrlByte, encodeWithModifiers, type KeyModifiers } from "./keyEncoding";

// The key bar's latched Ctrl/Alt. There is one system keyboard, so there is one latch: it applies to the next key
// from the bar or the next character typed into the focused terminal, whichever comes first. A double tap locks a
// modifier: it then applies to every key until it is tapped again.

const NONE: KeyModifiers = Object.freeze({ ctrl: false, alt: false });

/** Two taps on the same modifier within this window lock it. */
export const STICKY_MODIFIER_DOUBLE_TAP_MS = 300;

let latched: KeyModifiers = NONE;
let locked: KeyModifiers = NONE;
const lastTap: Record<keyof KeyModifiers, number | null> = { ctrl: null, alt: null };
const listeners = new Set<() => void>();

const freeze = (next: KeyModifiers): KeyModifiers => (next.ctrl || next.alt ? Object.freeze({ ...next }) : NONE);
const same = (a: KeyModifiers, b: KeyModifiers) => a.ctrl === b.ctrl && a.alt === b.alt;

function publish(nextLatched: KeyModifiers, nextLocked: KeyModifiers = locked): void {
  // A locked modifier is always latched.
  const merged = { ctrl: nextLatched.ctrl || nextLocked.ctrl, alt: nextLatched.alt || nextLocked.alt };
  if (same(merged, latched) && same(nextLocked, locked)) return;
  latched = freeze(merged);
  locked = freeze(nextLocked);
  for (const listener of listeners) listener();
}

function forgetTaps(): void {
  lastTap.ctrl = null;
  lastTap.alt = null;
}

export function getStickyModifiers(): KeyModifiers {
  return latched;
}

export function getLockedStickyModifiers(): KeyModifiers {
  return locked;
}

export function toggleStickyModifier(modifier: keyof KeyModifiers): void {
  lastTap[modifier] = null;
  publish({ ...latched, [modifier]: !latched[modifier] }, { ...locked, [modifier]: false });
}

/**
 * A tap on the bar's modifier key: off -> latched for one key; a second tap within the double-tap window -> locked;
 * a later second tap releases the latch; a tap on a locked modifier releases it.
 */
export function tapStickyModifier(modifier: keyof KeyModifiers, now: number = Date.now()): void {
  const previous = lastTap[modifier];
  lastTap[modifier] = null;
  if (locked[modifier]) {
    publish({ ...latched, [modifier]: false }, { ...locked, [modifier]: false });
  } else if (latched[modifier]) {
    if (previous !== null && now - previous <= STICKY_MODIFIER_DOUBLE_TAP_MS) {
      publish(latched, { ...locked, [modifier]: true });
    } else {
      publish({ ...latched, [modifier]: false });
    }
  } else {
    lastTap[modifier] = now;
    publish({ ...latched, [modifier]: true });
  }
}

/** Releases the one-shot latches after a key used them; locked modifiers stay. */
export function releaseStickyModifiers(): void {
  forgetTaps();
  publish(locked);
}

/** Releases every modifier, locked ones included. */
export function clearStickyModifiers(): void {
  forgetTaps();
  publish(NONE, NONE);
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

export function useLockedStickyModifiers(): KeyModifiers {
  return useSyncExternalStore(subscribeStickyModifiers, getLockedStickyModifiers);
}

/** Encodes one key's bytes with the latched modifiers and releases the one-shot latch. */
export function consumeStickyModifiers(data: string): string {
  const modifiers = latched;
  releaseStickyModifiers();
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
 * dictation, CJK) is sent unchanged and releases a one-shot latch, so it cannot surprise a later key. Escape
 * sequences (terminal reports, arrow keys) pass through and leave the latch alone.
 */
export function applyStickyModifiersToTyped(data: string): string {
  if (!latched.ctrl && !latched.alt) return data;
  if (data.startsWith(ESC)) return data;
  if (takesModifiers(data, latched)) return consumeStickyModifiers(data);
  releaseStickyModifiers();
  return data;
}
