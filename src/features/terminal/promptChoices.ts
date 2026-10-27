import type { AttentionPrompt } from "../../ipc/generated/types";

// promptIdentity names one detected prompt instance: a tap is valid only for it, and a view's answered or stale
// mark belongs to it. A new attention event for the same prompt with another epoch is another instance.
export function promptIdentity(prompt: AttentionPrompt): string {
  return `${prompt.generation ?? ""}|${prompt.epoch}|${prompt.fingerprint}`;
}

const BLOCKED_HINTS: Record<string, string> = {
  question: "这个提问需要输入文字，请在终端里回答",
  shell_prompt: "终端已回到 shell 提示符，不能用按钮回答",
  shell_foreground: "终端前台是 shell，不能用按钮回答",
  foreground_unknown: "无法确认终端前台程序，请在终端里回答",
  weak_marker: "无法可靠识别选项，请在终端里回答",
  not_line_mode: "终端不在行输入模式，请在终端里回答",
};

// promptBlockedHint says why a prompt cannot be answered by tap; the question is still shown.
export function promptBlockedHint(prompt: AttentionPrompt): string | null {
  if (prompt.tappable && (prompt.options?.length ?? 0) > 0) return null;
  return BLOCKED_HINTS[prompt.blocked ?? ""] ?? (prompt.kind === "question" ? BLOCKED_HINTS.question : "请在终端里回答");
}

// The text shown next to the buttons. The question is a security decision as often as not (an SSH host key
// confirmation), so it is never left out; a prompt without one says that the screen asks for a choice.
export function promptQuestion(prompt: AttentionPrompt): string {
  return prompt.question?.trim() || "终端在等待你选择";
}

// separateEnterKey is the Enter a yes/no prompt outside line mode offers as its own action; null when the tap
// presses Enter itself or the prompt offers none.
export function separateEnterKey(prompt: AttentionPrompt): string | null {
  return prompt.submit === "separate" && prompt.enterKey ? prompt.enterKey : null;
}

// Why a session known only from the cross-host listing cannot be answered here. A tap needs this client attached
// to the terminal; taking the session over just to send a key would answer a security question the user has not
// seen on the live screen, from a listing that may be out of date.
export const OPEN_TO_ANSWER = "先打开这个终端，在终端画面里核对后再回答";
