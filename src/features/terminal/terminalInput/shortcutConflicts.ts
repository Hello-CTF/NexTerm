import {
  KEYBINDING_ACTIONS,
  TERMINAL_WITHHELD_ACTIONS,
  parseBinding,
  type KeybindingAction,
  type KeybindingSnapshot,
} from "../../../app/keybindings";
import { ESC, KEY_BYTES, ctrlByte } from "./keyEncoding";

export interface TerminalShortcutConflict {
  action: KeybindingAction;
  /** The chord as the terminal would see it, e.g. "Ctrl+Shift+W" or "Ctrl+1…9". */
  chord: string;
  keys: TerminalChord;
  /** What xterm would otherwise have sent to the program, or null when it sends nothing for the chord. */
  bytes: string | null;
  /** What shells and common TUIs use those bytes for, when well known. */
  meaning: string | null;
}

const CONTROL_MEANINGS: Record<string, string> = {
  "\x01": "行首；screen 前缀",
  "\x02": "后退一个字符；tmux 前缀",
  "\x03": "中断 SIGINT",
  "\x04": "EOF / 退出",
  "\x05": "行尾",
  "\x06": "前进一个字符",
  "\x07": "取消",
  "\x08": "退格",
  "\t": "补全",
  "\n": "换行",
  "\x0b": "删除到行尾",
  "\x0c": "清屏",
  "\r": "回车",
  "\x0e": "下一条历史",
  "\x0f": "执行并取下一条历史",
  "\x10": "上一条历史",
  "\x11": "恢复输出 XON",
  "\x12": "反向搜索历史",
  "\x13": "暂停输出 XOFF",
  "\x14": "交换字符",
  "\x15": "删除到行首",
  "\x16": "按字面输入下一个键",
  "\x17": "删除前一个词",
  "\x18": "Emacs 前缀",
  "\x19": "粘贴已删除文本",
  "\x1a": "挂起 SIGTSTP",
  "\x1b": "Esc",
  "\x1c": "退出并转储 SIGQUIT",
  "\x1d": "telnet 转义",
  "\x7f": "退格",
};

const ARROW_FINAL: Record<string, string> = { ArrowUp: "A", ArrowDown: "B", ArrowRight: "C", ArrowLeft: "D" };

const DISPLAY: Record<string, string> = {
  ArrowUp: "↑",
  ArrowDown: "↓",
  ArrowLeft: "←",
  ArrowRight: "→",
  Escape: "Esc",
  "1-9": "1…9",
};

export interface TerminalChord {
  ctrl: boolean;
  shift: boolean;
  alt: boolean;
  key: string;
}

/**
 * The bytes xterm's default encoder produces for a chord. Kept to the chords app shortcuts can be bound to;
 * anything else reports null ("sends nothing") rather than a guess. On macOS, Option+character types a
 * third-level character instead of a Meta sequence.
 */
export function xtermChordBytes(chord: TerminalChord, mac: boolean): string | null {
  const { ctrl, shift, alt, key } = chord;
  const arrow = ARROW_FINAL[key];
  if (arrow) {
    const modifier = 1 + (shift ? 1 : 0) + (alt ? 2 : 0) + (ctrl ? 4 : 0);
    return modifier === 1 ? `${ESC}[${arrow}` : `${ESC}[1;${modifier}${arrow}`;
  }
  if (key === "Tab") return shift ? `${ESC}[Z` : KEY_BYTES.Tab;
  if (key === "Escape" || key === "Enter" || key === "Backspace") {
    if (ctrl || shift) return null;
    const base = KEY_BYTES[key];
    return alt ? `${ESC}${base}` : base;
  }
  if (key.length !== 1) return null;
  if (alt && mac) return null;
  if (alt) {
    if (key >= "a" && key <= "z") {
      const base = ctrl ? ctrlByte(key) : shift ? key.toUpperCase() : key;
      return `${ESC}${base}`;
    }
    return shift ? null : `${ESC}${key}`;
  }
  if (ctrl) {
    if (shift) return null;
    if (key === "2" || key === "@") return null;
    return ctrlByte(key);
  }
  return shift ? key.toUpperCase() : key;
}

function chordLabel(chord: TerminalChord): string {
  const parts: string[] = [];
  if (chord.ctrl) parts.push("Ctrl");
  if (chord.shift) parts.push("Shift");
  if (chord.alt) parts.push("Alt");
  parts.push(DISPLAY[chord.key] ?? (/^[a-z]$/.test(chord.key) ? chord.key.toUpperCase() : chord.key));
  return parts.join("+");
}

function terminalChord(binding: string, mac: boolean): TerminalChord | null {
  const parsed = parseBinding(binding);
  if (!parsed) return null;
  // Cmd (macOS) and Meta/Super chords produce no terminal input; Mod also matches Ctrl, which does.
  const ctrl = parsed.mod || parsed.ctrl || (parsed.primary && !mac);
  if (!ctrl && (parsed.meta || parsed.primary)) return null;
  return { ctrl, shift: parsed.shift, alt: parsed.alt, key: parsed.key };
}

function describeDigitRange(chord: TerminalChord): Pick<TerminalShortcutConflict, "bytes" | "meaning"> {
  if (chord.ctrl && !chord.shift && !chord.alt) {
    return { bytes: null, meaning: "Ctrl+3…7 原为 ^[…^_（含 Ctrl+4 = SIGQUIT），Ctrl+8 原为退格" };
  }
  return { bytes: null, meaning: null };
}

/**
 * Lists the chords the terminal withholds from programs because an app shortcut owns them, under the given keymap.
 * When two actions share a chord the first in match order owns it, as at runtime.
 */
export function terminalShortcutConflicts(
  bindings: KeybindingSnapshot,
  mac: boolean,
  web: boolean,
): TerminalShortcutConflict[] {
  const seen = new Set<string>();
  const out: TerminalShortcutConflict[] = [];
  for (const id of TERMINAL_WITHHELD_ACTIONS) {
    const action = KEYBINDING_ACTIONS.find((candidate) => candidate.id === id);
    const binding = bindings[id];
    if (!action || binding === null) continue;
    const candidates = [binding];
    if (!web && mac && action.macDefaultBinding && binding === action.defaultBinding) {
      candidates.push(action.macDefaultBinding);
    }
    for (const candidate of candidates) {
      const chord = terminalChord(candidate, mac);
      if (!chord) continue;
      const label = chordLabel(chord);
      if (seen.has(label)) continue;
      seen.add(label);
      if (chord.key === "1-9") {
        out.push({ action, chord: label, keys: chord, ...describeDigitRange(chord) });
        continue;
      }
      const bytes = xtermChordBytes(chord, mac);
      out.push({ action, chord: label, keys: chord, bytes, meaning: bytes === null ? null : (CONTROL_MEANINGS[bytes] ?? null) });
    }
  }
  return out;
}
