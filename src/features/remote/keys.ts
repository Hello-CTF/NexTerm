import type { ModifierKey } from "./remoteTypes";

// Keys are named by KeyboardEvent.code. The VNC client maps them to X11 keysyms; the RDP client replays them as
// keyboard events so the IronRDP component turns them into scancodes with its own table.

const FIXED_KEYSYMS: Record<string, number> = {
  ControlLeft: 0xffe3,
  ControlRight: 0xffe4,
  ShiftLeft: 0xffe1,
  ShiftRight: 0xffe2,
  AltLeft: 0xffe9,
  AltRight: 0xffea,
  MetaLeft: 0xffeb,
  MetaRight: 0xffec,
  Escape: 0xff1b,
  Tab: 0xff09,
  Enter: 0xff0d,
  Backspace: 0xff08,
  Delete: 0xffff,
  Insert: 0xff63,
  Home: 0xff50,
  End: 0xff57,
  PageUp: 0xff55,
  PageDown: 0xff56,
  ArrowLeft: 0xff51,
  ArrowUp: 0xff52,
  ArrowRight: 0xff53,
  ArrowDown: 0xff54,
  PrintScreen: 0xff61,
  Space: 0x20,
  ContextMenu: 0xff67,
};

export function keysymOf(code: string): number | null {
  const fixed = FIXED_KEYSYMS[code];
  if (fixed !== undefined) return fixed;
  const fn = /^F([1-9]|1[0-2])$/.exec(code);
  if (fn) return 0xffbe + Number(fn[1]) - 1;
  const letter = /^Key([A-Z])$/.exec(code);
  if (letter) return letter[1].toLowerCase().charCodeAt(0);
  const digit = /^Digit([0-9])$/.exec(code);
  if (digit) return digit[1].charCodeAt(0);
  return null;
}

// The character a typed key produces, for the keys the bar sends as text.
export function keyLabel(code: string): string {
  if (code === "ControlLeft") return "Ctrl";
  if (code === "AltLeft") return "Alt";
  if (code === "ShiftLeft") return "Shift";
  if (code === "MetaLeft") return "Win";
  if (code === "Escape") return "Esc";
  if (code === "Delete") return "Del";
  if (code.startsWith("Key")) return code.slice(3);
  if (code.startsWith("Digit")) return code.slice(5);
  return code;
}

export const MODIFIER_LABEL: Record<ModifierKey, string> = {
  ControlLeft: "Ctrl",
  AltLeft: "Alt",
  ShiftLeft: "Shift",
  MetaLeft: "Win",
};

export interface KeyCombo {
  id: string;
  label: string;
  group: string;
  keys: readonly string[];
  hint?: string;
}

export const KEY_COMBOS: readonly KeyCombo[] = [
  { id: "cad", label: "Ctrl+Alt+Del", group: "系统", keys: ["ControlLeft", "AltLeft", "Delete"], hint: "安全选项或登录" },
  { id: "taskmgr", label: "Ctrl+Shift+Esc", group: "系统", keys: ["ControlLeft", "ShiftLeft", "Escape"], hint: "任务管理器" },
  { id: "win", label: "Win", group: "系统", keys: ["MetaLeft"], hint: "开始菜单" },
  { id: "winl", label: "Win+L", group: "系统", keys: ["MetaLeft", "KeyL"], hint: "锁定远程会话" },
  { id: "winr", label: "Win+R", group: "系统", keys: ["MetaLeft", "KeyR"], hint: "运行" },
  { id: "wine", label: "Win+E", group: "系统", keys: ["MetaLeft", "KeyE"], hint: "资源管理器" },
  { id: "wind", label: "Win+D", group: "窗口", keys: ["MetaLeft", "KeyD"], hint: "显示桌面" },
  { id: "alttab", label: "Alt+Tab", group: "窗口", keys: ["AltLeft", "Tab"], hint: "切换窗口" },
  { id: "altf4", label: "Alt+F4", group: "窗口", keys: ["AltLeft", "F4"], hint: "关闭窗口" },
  { id: "altesc", label: "Alt+Esc", group: "窗口", keys: ["AltLeft", "Escape"] },
  { id: "ctrlesc", label: "Ctrl+Esc", group: "窗口", keys: ["ControlLeft", "Escape"], hint: "开始菜单" },
  { id: "prtsc", label: "PrintScreen", group: "其他", keys: ["PrintScreen"] },
];

export function filterCombos(query: string): KeyCombo[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [...KEY_COMBOS];
  return KEY_COMBOS.filter((combo) => `${combo.label} ${combo.hint ?? ""} ${combo.group}`.toLowerCase().includes(needle));
}
