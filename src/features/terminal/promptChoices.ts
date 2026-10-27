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
  // A tap must name the helper generation; a prompt without one cannot be answered safely.
  if (!prompt.generation) return "无法确认这个提示的来源，请在终端里回答";
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

// A session known only from the cross-host listing is answered after opening it: a tap needs this client attached
// to the terminal, and the user then checks the live screen before answering a question that may be a security
// decision, instead of trusting a listing that may be out of date.
export const OPEN_TO_ANSWER_LABEL = "打开并回答";
export const OPEN_TO_ANSWER_NOTE = "打开终端后在画面里核对，再用按钮回答";
// A tap is this device's input: the PTY then follows this device's terminal size.
export const ANSWER_TAKES_SIZE_NOTE = "在这里回答后，终端尺寸改为跟随本设备";

// A button is armed only after its prompt instance and position held still this long, so a list that shifts under
// a finger cannot turn a tap into an answer to another session's prompt.
export const CHOICE_ARM_MS = 450;
