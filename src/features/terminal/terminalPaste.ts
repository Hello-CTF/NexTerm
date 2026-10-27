import { getTerminalPrefs, setTerminalConfirmMultilinePaste } from "../../app/preferences";
import { ask } from "../../ui/dialogs";
import {
  multilinePasteHostMounted,
  multilinePasteSkippedForTab,
  multilinePasteTabSkippable,
  requestMultilinePastePrompt,
  skipMultilinePasteForTab,
} from "./multilinePastePrompt";

export const BRACKETED_PASTE_START = "\x1b[200~";
export const BRACKETED_PASTE_END = "\x1b[201~";

// Inside a bracketed paste every ESC is shown as U+241B, as xterm's own paste does: pasted text can then neither
// close the bracket early with its own end sequence nor smuggle other control sequences.
export function wrapBracketedPaste(text: string, enabled: boolean): string {
  return enabled ? `${BRACKETED_PASTE_START}${text.replace(/\x1b/g, "␛")}${BRACKETED_PASTE_END}` : text;
}

/**
 * The bytes a quick command sends: the text as a bracketed paste when the terminal enabled bracketed paste (so a
 * shell or TUI takes it as one paste and a trailing Enter submits it), then Enter when the command asks for it.
 */
export function quickCommandBytes(text: string, appendEnter: boolean, bracketedPaste: boolean): string {
  return `${wrapBracketedPaste(text, bracketedPaste)}${appendEnter ? "\r" : ""}`;
}

export function multilinePasteLines(text: string): string[] | null {
  const body = text.replace(/[\r\n]+$/, "");
  if (!/[\r\n]/.test(body)) return null;
  return body.split(/\r\n|\r|\n/);
}

export const MULTILINE_PASTE_PREVIEW_LINES = 5;
const PREVIEW_LINE_CHARS = 120;

export function multilinePastePreview(lines: string[]): string[] {
  return lines
    .slice(0, MULTILINE_PASTE_PREVIEW_LINES)
    .map((line) => (line.length > PREVIEW_LINE_CHARS ? `${line.slice(0, PREVIEW_LINE_CHARS)}…` : line));
}

export const MULTILINE_PASTE_WARNING = "多行内容可能包含多条命令，部分程序会立即逐行执行，请确认来源和内容。";

/**
 * Asks before more than one line is pasted into a terminal. tabId is the workspace tab the paste goes to; the
 * prompt then also offers to stop asking in that tab. bracketedPaste is the terminal's live bracketed paste mode
 * (DECSET 2004) at paste time. Single lines, a tab the user exempted, the "never" setting, and, under the default
 * "unbracketed" setting, a terminal in bracketed paste mode paste without asking: there the text arrives wrapped and
 * sanitised as one paste rather than running line by line. This only decides whether to paste: the caller still
 * sends the text through its usual bracketed-paste path.
 */
export async function confirmMultilinePaste(text: string, tabId?: string, bracketedPaste = false): Promise<boolean> {
  const lines = multilinePasteLines(text);
  if (!lines) return true;
  const mode = getTerminalPrefs().confirmMultilinePaste;
  if (mode === "never" || (mode === "unbracketed" && bracketedPaste)) return true;
  if (tabId && multilinePasteSkippedForTab(tabId)) return true;
  const preview = multilinePastePreview(lines);
  if (!multilinePasteHostMounted()) {
    const suffix = lines.length > preview.length ? `\n…（共 ${lines.length} 行）` : "";
    return ask(`即将向终端粘贴 ${lines.length} 行文本。${MULTILINE_PASTE_WARNING}\n\n${preview.join("\n")}${suffix}`, {
      title: "粘贴多行文本",
      kind: "warning",
    });
  }
  const targetTab = multilinePasteTabSkippable(tabId) ? tabId : null;
  const decision = await requestMultilinePastePrompt({
    lineCount: lines.length,
    preview,
    tabScoped: targetTab !== null,
    tabId: targetTab,
  });
  if (!decision.paste) return false;
  if (decision.skipAlways) setTerminalConfirmMultilinePaste("never");
  if (decision.skipTab && tabId) skipMultilinePasteForTab(tabId);
  return true;
}
