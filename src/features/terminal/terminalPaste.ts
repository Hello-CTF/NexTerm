import { ask } from "../../ui/dialogs";

export const BRACKETED_PASTE_START = "\x1b[200~";
export const BRACKETED_PASTE_END = "\x1b[201~";

export function wrapBracketedPaste(text: string, enabled: boolean): string {
  return enabled ? `${BRACKETED_PASTE_START}${text}${BRACKETED_PASTE_END}` : text;
}

export function multilinePasteLines(text: string): string[] | null {
  const body = text.replace(/[\r\n]+$/, "");
  if (!/[\r\n]/.test(body)) return null;
  return body.split(/\r\n|\r|\n/);
}

export async function confirmMultilinePaste(text: string): Promise<boolean> {
  const lines = multilinePasteLines(text);
  if (!lines) return true;
  const preview = lines
    .slice(0, 5)
    .map((line) => (line.length > 120 ? `${line.slice(0, 120)}…` : line));
  const suffix = lines.length > preview.length ? `\n…（共 ${lines.length} 行）` : "";
  return ask(`即将向终端粘贴 ${lines.length} 行文本：\n\n${preview.join("\n")}${suffix}`, {
    title: "粘贴多行文本",
    kind: "warning",
  });
}
