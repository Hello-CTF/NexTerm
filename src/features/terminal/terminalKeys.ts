export interface TerminalKeySpec {
  id: string;
  label: string;
  title: string;
  data: string;
  ctrlData?: string;
  altData?: string;
}

export interface TerminalKeyModifiers {
  ctrl: boolean;
  alt: boolean;
}

export const TERMINAL_KEY_CATALOG: TerminalKeySpec[] = [
  { id: "escape", label: "Esc", title: "Escape", data: "\x1b" },
  { id: "tab", label: "Tab", title: "制表符", data: "\t" },
  { id: "up", label: "↑", title: "上方向键", data: "\x1b[A" },
  { id: "down", label: "↓", title: "下方向键", data: "\x1b[B" },
  { id: "right", label: "→", title: "右方向键", data: "\x1b[C" },
  { id: "left", label: "←", title: "左方向键", data: "\x1b[D" },
  { id: "home", label: "Home", title: "行首", data: "\x1b[H" },
  { id: "end", label: "End", title: "行尾", data: "\x1b[F" },
  { id: "pageup", label: "PgUp", title: "向上翻页", data: "\x1b[5~" },
  { id: "pagedown", label: "PgDn", title: "向下翻页", data: "\x1b[6~" },
  { id: "c", label: "C", title: "字母 C；Ctrl 激活时为 Ctrl+C", data: "c", ctrlData: "\x03" },
  { id: "d", label: "D", title: "字母 D；Ctrl 激活时为 Ctrl+D", data: "d", ctrlData: "\x04" },
  { id: "z", label: "Z", title: "字母 Z；Ctrl 激活时为 Ctrl+Z", data: "z", ctrlData: "\x1a" },
  { id: "ctrl-a", label: "^A", title: "Ctrl+A（移到行首）", data: "\x01" },
  { id: "ctrl-e", label: "^E", title: "Ctrl+E（移到行尾）", data: "\x05" },
  { id: "ctrl-k", label: "^K", title: "Ctrl+K（删除到行尾）", data: "\x0b" },
  { id: "ctrl-l", label: "^L", title: "Ctrl+L（清屏）", data: "\x0c" },
  { id: "ctrl-r", label: "^R", title: "Ctrl+R（历史搜索）", data: "\x12" },
  { id: "ctrl-u", label: "^U", title: "Ctrl+U（删除整行）", data: "\x15" },
  { id: "ctrl-w", label: "^W", title: "Ctrl+W（删除前一个词）", data: "\x17" },
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

const STORAGE_KEY = "nexterm.terminalKeys.v1";

export function terminalKeySequence(key: TerminalKeySpec, modifiers: TerminalKeyModifiers): string {
  const base = modifiers.ctrl ? (key.ctrlData ?? key.data) : key.data;
  if (!modifiers.alt) return base;
  return key.altData ?? `\x1b${base}`;
}

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
  let raw: string | null = null;
  try {
    raw = localStorage.getItem(STORAGE_KEY);
  } catch {
    return [...DEFAULT_TERMINAL_KEY_IDS];
  }
  if (raw === null) return [...DEFAULT_TERMINAL_KEY_IDS];
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [...DEFAULT_TERMINAL_KEY_IDS];
    const ids: string[] = [];
    const seen = new Set<string>();
    for (const id of parsed) {
      if (typeof id !== "string" || seen.has(id)) continue;
      if (!TERMINAL_KEY_CATALOG.some((key) => key.id === id)) continue;
      seen.add(id);
      ids.push(id);
    }
    return ids;
  } catch {
    return [...DEFAULT_TERMINAL_KEY_IDS];
  }
}

export function saveTerminalKeyConfig(ids: string[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(ids));
  } catch {
  }
}

export function resetTerminalKeyConfig(): string[] {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
  }
  return [...DEFAULT_TERMINAL_KEY_IDS];
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
