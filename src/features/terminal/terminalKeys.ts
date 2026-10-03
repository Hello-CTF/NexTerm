export interface TerminalKeySpec {
  id: string;
  label: string;
  title: string;
  data: string;
  ctrlData?: string;
}

export const TERMINAL_KEYS: TerminalKeySpec[] = [
  { id: "escape", label: "Esc", title: "Escape", data: "\x1b" },
  { id: "tab", label: "Tab", title: "制表符", data: "\t" },
  { id: "up", label: "↑", title: "上方向键", data: "\x1b[A" },
  { id: "down", label: "↓", title: "下方向键", data: "\x1b[B" },
  { id: "right", label: "→", title: "右方向键", data: "\x1b[C" },
  { id: "left", label: "←", title: "左方向键", data: "\x1b[D" },
  { id: "home", label: "Home", title: "行首", data: "\x1b[H" },
  { id: "end", label: "End", title: "行尾", data: "\x1b[F" },
  { id: "c", label: "C", title: "字母 C；Ctrl 激活时为 Ctrl+C", data: "c", ctrlData: "\x03" },
  { id: "d", label: "D", title: "字母 D；Ctrl 激活时为 Ctrl+D", data: "d", ctrlData: "\x04" },
  { id: "z", label: "Z", title: "字母 Z；Ctrl 激活时为 Ctrl+Z", data: "z", ctrlData: "\x1a" },
];

export function terminalKeySequence(key: TerminalKeySpec, ctrlActive: boolean): string {
  return ctrlActive ? (key.ctrlData ?? key.data) : key.data;
}
