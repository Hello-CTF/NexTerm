// Byte encoding shared by every terminal input source: hardware keys the pipeline overrides, the mobile key bar,
// and system-keyboard characters typed while a key-bar modifier is latched.

export type KeyLike = Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "altKey" | "metaKey">;

export interface KeyModifiers {
  ctrl: boolean;
  alt: boolean;
}

export const ESC = "\x1b";

/** Bytes of the named keys a VT-style terminal receives without modifiers (normal cursor-key mode). */
export const KEY_BYTES = {
  Escape: ESC,
  Tab: "\t",
  Enter: "\r",
  Backspace: "\x7f",
  ArrowUp: `${ESC}[A`,
  ArrowDown: `${ESC}[B`,
  ArrowRight: `${ESC}[C`,
  ArrowLeft: `${ESC}[D`,
  Home: `${ESC}[H`,
  End: `${ESC}[F`,
  PageUp: `${ESC}[5~`,
  PageDown: `${ESC}[6~`,
} as const;

const CTRL_SYMBOLS: Record<string, number> = {
  " ": 0x00,
  "@": 0x00,
  "2": 0x00,
  "[": 0x1b,
  "3": 0x1b,
  "\\": 0x1c,
  "4": 0x1c,
  "]": 0x1d,
  "5": 0x1d,
  "^": 0x1e,
  "6": 0x1e,
  "_": 0x1f,
  "7": 0x1f,
  "/": 0x1f,
  "?": 0x7f,
  "8": 0x7f,
};

/** The C0 control byte Ctrl+<char> produces, or null when the character has no control form. */
export function ctrlByte(char: string): string | null {
  if (char.length !== 1) return null;
  const lower = char.toLowerCase();
  if (lower >= "a" && lower <= "z") return String.fromCharCode(lower.charCodeAt(0) - 0x60);
  const code = CTRL_SYMBOLS[char];
  return code === undefined ? null : String.fromCharCode(code);
}

/**
 * Applies Ctrl and Alt to the bytes of one key: Ctrl maps a single character to its control byte (keys without
 * a control form, such as arrows, are unchanged) and Alt prefixes ESC, as xterm does with Alt sent as Meta.
 */
export function encodeWithModifiers(data: string, modifiers: KeyModifiers): string {
  const base = modifiers.ctrl ? (ctrlByte(data) ?? data) : data;
  return modifiers.alt ? `${ESC}${base}` : base;
}

// ESC+CR: TUIs such as Claude Code read it as "insert newline" rather than submit.
export const SHIFT_ENTER_SEQUENCE = `${ESC}\r`;

export function isShiftEnter(event: KeyLike): boolean {
  return event.key === "Enter" && event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey;
}

const DELETE_WORD = encodeWithModifiers("w", { ctrl: true, alt: false });
const LINE_START = encodeWithModifiers("a", { ctrl: true, alt: false });
const LINE_END = encodeWithModifiers("e", { ctrl: true, alt: false });
const KILL_LINE = encodeWithModifiers("u", { ctrl: true, alt: false });
const WORD_BACK = encodeWithModifiers("b", { ctrl: false, alt: true });
const WORD_FORWARD = encodeWithModifiers("f", { ctrl: false, alt: true });

// terminalKeyOverride returns the bytes for key chords whose xterm.js default is ambiguous or not what
// native terminals send, or null to keep the default encoding.
//  - Shift+Enter: xterm sends a bare CR (same as Enter); ESC+CR lets TUIs insert a newline.
//  - Ctrl+Backspace: xterm sends BS (same as Backspace); ^W deletes the previous word.
//  - macOS Option+Left/Right: word jump (ESC b / ESC f) as in Terminal.app and iTerm2.
//  - macOS Cmd+Left/Right/Backspace: line start / line end / kill line (^A / ^E / ^U).
export function terminalKeyOverride(event: KeyLike, mac: boolean): string | null {
  const { key, shiftKey, ctrlKey, altKey, metaKey } = event;
  if (isShiftEnter(event)) return SHIFT_ENTER_SEQUENCE;
  if (key === "Backspace" && ctrlKey && !shiftKey && !altKey && !metaKey) return DELETE_WORD;
  if (!mac || shiftKey || ctrlKey) return null;
  if (altKey && !metaKey) {
    if (key === "ArrowLeft") return WORD_BACK;
    if (key === "ArrowRight") return WORD_FORWARD;
  }
  if (metaKey && !altKey) {
    if (key === "ArrowLeft") return LINE_START;
    if (key === "ArrowRight") return LINE_END;
    if (key === "Backspace") return KILL_LINE;
  }
  return null;
}
