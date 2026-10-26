import { useSyncExternalStore } from "react";
import { createSyncedPreference } from "../../app/syncedPreference";
import { ask } from "../../ui/dialogs";
import { multilinePasteLines } from "./terminalPaste";

export { quickCommandBytes } from "./terminalPaste";

export interface QuickCommand {
  label: string;
  text: string;
  /** Send Enter after the text. */
  appendEnter: boolean;
}

// The same limits internal/account/preferences.go enforces for terminal.quickCommands.
export const QUICK_COMMAND_LIMITS = {
  count: 50,
  labelChars: 32,
  textBytes: 4 * 1024,
  totalBytes: 48 * 1024,
} as const;

export const QUICK_COMMANDS_PREFERENCE_KEY = "terminal.quickCommands";
const STORAGE_KEY = "nexterm.quickCommands.v1";

const encoder = new TextEncoder();
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/;

/** Returns why a command cannot be saved, or null when it is valid. */
export function quickCommandError(command: QuickCommand): string | null {
  if (!command.label.trim()) return "请填写名称";
  if ([...command.label].length > QUICK_COMMAND_LIMITS.labelChars) {
    return `名称最多 ${QUICK_COMMAND_LIMITS.labelChars} 个字符`;
  }
  if (CONTROL_CHARS.test(command.label)) return "名称不能包含换行或控制字符";
  if (!command.text) return "请填写要发送的内容";
  if (command.text.includes("\0")) return "内容不能包含 NUL 字符";
  if (encoder.encode(command.text).length > QUICK_COMMAND_LIMITS.textBytes) return "内容不能超过 4 KiB";
  return null;
}

/** Returns why the whole list cannot be saved, or null when it is valid. */
export function quickCommandListError(commands: readonly QuickCommand[]): string | null {
  if (commands.length > QUICK_COMMAND_LIMITS.count) return `最多保存 ${QUICK_COMMAND_LIMITS.count} 条快捷命令`;
  for (const command of commands) {
    const error = quickCommandError(command);
    if (error) return `「${command.label}」：${error}`;
  }
  if (encoder.encode(JSON.stringify(commands)).length > QUICK_COMMAND_LIMITS.totalBytes) {
    return "快捷命令总大小超过 48 KiB，请删减内容";
  }
  return null;
}

/** Keeps the valid commands of a stored list; null when the value is not a list. */
export function parseQuickCommands(value: unknown): QuickCommand[] | null {
  if (!Array.isArray(value)) return null;
  const commands: QuickCommand[] = [];
  for (const entry of value) {
    if (!entry || typeof entry !== "object") continue;
    const { label, text, appendEnter } = entry as Record<string, unknown>;
    if (typeof label !== "string" || typeof text !== "string" || typeof appendEnter !== "boolean") continue;
    const command = { label, text, appendEnter };
    if (quickCommandError(command) === null) commands.push(command);
    if (commands.length === QUICK_COMMAND_LIMITS.count) break;
  }
  return commands;
}

const quickCommands = createSyncedPreference<QuickCommand[]>({
  key: QUICK_COMMANDS_PREFERENCE_KEY,
  localKey: STORAGE_KEY,
  parse: parseQuickCommands,
  fallback: () => [],
});

export function getQuickCommands(): QuickCommand[] {
  return quickCommands.get();
}

/** Saves the list to the account (or the device without one); rejects an invalid list or a failed account write. */
export async function saveQuickCommands(commands: QuickCommand[]): Promise<void> {
  const error = quickCommandListError(commands);
  if (error) throw new Error(error);
  await quickCommands.set(commands.map(({ label, text, appendEnter }) => ({ label, text, appendEnter })));
}

export function useQuickCommands(): QuickCommand[] {
  return useSyncExternalStore(quickCommands.subscribe, quickCommands.get);
}

/** A command with line breaks can run several commands at once, so it is confirmed like a multi-line paste. */
export async function confirmQuickCommand(command: QuickCommand): Promise<boolean> {
  const lines = multilinePasteLines(command.text);
  if (!lines) return true;
  const preview = lines
    .slice(0, 5)
    .map((line) => (line.length > 120 ? `${line.slice(0, 120)}…` : line));
  const suffix = lines.length > preview.length ? `\n…（共 ${lines.length} 行）` : "";
  return ask(
    `快捷命令「${command.label}」包含 ${lines.length} 行文本。多行内容可能包含多条命令，部分程序会立即逐行执行，请确认内容。\n\n${preview.join("\n")}${suffix}`,
    { title: "发送多行快捷命令", kind: "warning" },
  );
}
