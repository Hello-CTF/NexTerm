import { useSyncExternalStore } from "react";
import { createSyncedPreference } from "../../app/syncedPreference";
import { KEY_BYTES, ctrlByte, encodeWithModifiers, type KeyModifiers } from "./terminalInput/keyEncoding";

export { SHIFT_ENTER_SEQUENCE, isShiftEnter, terminalKeyOverride } from "./terminalInput/keyEncoding";

export interface TerminalKeySpec {
  id: string;
  label: string;
  title: string;
  data: string;
}

export type TerminalKeyModifiers = KeyModifiers;

const ctrl = (char: string): string => ctrlByte(char) ?? char;

export const TERMINAL_KEY_CATALOG: TerminalKeySpec[] = [
  { id: "escape", label: "Esc", title: "Escape", data: KEY_BYTES.Escape },
  { id: "tab", label: "Tab", title: "制表符", data: KEY_BYTES.Tab },
  { id: "up", label: "↑", title: "上方向键", data: KEY_BYTES.ArrowUp },
  { id: "down", label: "↓", title: "下方向键", data: KEY_BYTES.ArrowDown },
  { id: "right", label: "→", title: "右方向键", data: KEY_BYTES.ArrowRight },
  { id: "left", label: "←", title: "左方向键", data: KEY_BYTES.ArrowLeft },
  { id: "home", label: "Home", title: "行首", data: KEY_BYTES.Home },
  { id: "end", label: "End", title: "行尾", data: KEY_BYTES.End },
  { id: "pageup", label: "PgUp", title: "向上翻页", data: KEY_BYTES.PageUp },
  { id: "pagedown", label: "PgDn", title: "向下翻页", data: KEY_BYTES.PageDown },
  { id: "c", label: "C", title: "字母 C；Ctrl 激活时为 Ctrl+C", data: "c" },
  { id: "d", label: "D", title: "字母 D；Ctrl 激活时为 Ctrl+D", data: "d" },
  { id: "z", label: "Z", title: "字母 Z；Ctrl 激活时为 Ctrl+Z", data: "z" },
  { id: "ctrl-a", label: "^A", title: "Ctrl+A（移到行首）", data: ctrl("a") },
  { id: "ctrl-e", label: "^E", title: "Ctrl+E（移到行尾）", data: ctrl("e") },
  { id: "ctrl-k", label: "^K", title: "Ctrl+K（删除到行尾）", data: ctrl("k") },
  { id: "ctrl-l", label: "^L", title: "Ctrl+L（清屏）", data: ctrl("l") },
  { id: "ctrl-r", label: "^R", title: "Ctrl+R（历史搜索）", data: ctrl("r") },
  { id: "ctrl-u", label: "^U", title: "Ctrl+U（删除到行首）", data: ctrl("u") },
  { id: "ctrl-w", label: "^W", title: "Ctrl+W（删除前一个词）", data: ctrl("w") },
  // Answer keys for prompts and numbered menus of any program; they send plain characters, no per-program rules.
  { id: "digit-1", label: "1", title: "数字 1（选择第 1 项）", data: "1" },
  { id: "digit-2", label: "2", title: "数字 2（选择第 2 项）", data: "2" },
  { id: "digit-3", label: "3", title: "数字 3（选择第 3 项）", data: "3" },
  { id: "y", label: "y", title: "字母 y（确认）", data: "y" },
  { id: "n", label: "n", title: "字母 n（拒绝）", data: "n" },
  { id: "enter", label: "⏎", title: "回车", data: KEY_BYTES.Enter },
];

export const DEFAULT_TERMINAL_KEY_IDS: string[] = [
  "escape",
  "tab",
  "up",
  "down",
  "right",
  "left",
  "home",
  "end",
  "pageup",
  "pagedown",
  "c",
  "d",
  "z",
  "ctrl-l",
  "ctrl-r",
];

// The key bar layout is the account preference terminal.keyBar; the device key below holds it without an
// account and is imported into the account once (see createSyncedPreference).
export const TERMINAL_KEY_BAR_PREFERENCE_KEY = "terminal.keyBar";
const STORAGE_KEY = "nexterm.terminalKeys.v1";

// parseTerminalKeyIds keeps the known catalog ids of a stored layout in order, without duplicates. Ids saved by a
// newer client that this one does not know are skipped.
export function parseTerminalKeyIds(value: unknown): string[] | null {
  if (!Array.isArray(value)) return null;
  const known = new Set(TERMINAL_KEY_CATALOG.map((key) => key.id));
  const ids: string[] = [];
  const seen = new Set<string>();
  for (const id of value) {
    if (typeof id !== "string" || seen.has(id) || !known.has(id)) continue;
    seen.add(id);
    ids.push(id);
  }
  return ids;
}

const terminalKeyBar = createSyncedPreference<string[]>({
  key: TERMINAL_KEY_BAR_PREFERENCE_KEY,
  localKey: STORAGE_KEY,
  parse: parseTerminalKeyIds,
  fallback: () => [...DEFAULT_TERMINAL_KEY_IDS],
});

export function resolveTerminalKeys(ids: string[]): TerminalKeySpec[] {
  const byId = new Map(TERMINAL_KEY_CATALOG.map((key) => [key.id, key]));
  const keys: TerminalKeySpec[] = [];
  for (const id of ids) {
    const key = byId.get(id);
    if (key) keys.push(key);
  }
  return keys;
}

export function loadTerminalKeyConfig(): string[] {
  return terminalKeyBar.get();
}

/** Saves the layout to the account (or the device without one); rejects when the account write fails. */
export function saveTerminalKeyConfig(ids: string[]): Promise<void> {
  return terminalKeyBar.set(ids);
}

export function resetTerminalKeyConfig(): Promise<void> {
  return terminalKeyBar.reset();
}

export function useTerminalKeyConfig(): string[] {
  return useSyncExternalStore(terminalKeyBar.subscribe, terminalKeyBar.get);
}

export function formatKeySequence(data: string): string {
  let out = "";
  for (const ch of data) {
    const code = ch.codePointAt(0) ?? 0;
    if (ch === "\x1b") out += "^[";
    else if (ch === "\t") out += "Tab";
    else if (ch === "\r") out += "^M";
    else if (ch === "\n") out += "^J";
    else if (code === 0x7f) out += "^?";
    else if (code < 0x20) out += `^${String.fromCharCode(code + 64)}`;
    else out += ch;
  }
  return out;
}

// terminalKeySequence encodes a key-bar key with the latched Ctrl/Alt modifiers through the shared encoder.
export function terminalKeySequence(key: TerminalKeySpec, modifiers: TerminalKeyModifiers): string {
  return encodeWithModifiers(key.data, modifiers);
}
