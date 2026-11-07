import type { QueryClient } from "@tanstack/react-query";
import { assetApi, terminalApi } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask, promptText } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { insertRisk } from "../explorer/snippetInsert";
import type { CommandBlock } from "./commandBlocks";
import { t } from "../../i18n";

const EXPLAIN_TEXT_LIMIT = 16000;
const COMMAND_LABEL_LIMIT = 32;
const SNIPPET_NAME_LIMIT = 40;

// singleLine turns line breaks and control characters into spaces and collapses whitespace so a reference label stays on one line.
function singleLine(text: string): string {
  return text.replace(/[\u0000-\u001f\u007f-\u009f\u2028\u2029]/g, " ").replace(/\s+/g, " ").trim();
}

function toast(kind: "info" | "success" | "error", text: string) {
  useUi.getState().pushToast(kind, text);
}

// rerunCommandBlock types the block's command into its own terminal again.
export async function rerunCommandBlock(tabId: string, block: CommandBlock): Promise<boolean> {
  const command = block.command;
  if (!command.trim()) {
    toast("info", t("该块没有可重新运行的命令"));
    return false;
  }
  const risk = insertRisk(command);
  if (risk !== "none") {
    const ok = await ask(
      risk === "execute"
        ? t("命令包含回车或换行，重新运行时会逐行提交执行：\n{command}\n\n仍要重新运行吗？", { command })
        : t("命令包含终端控制字符，可能执行 shell 代码、中断前台进程或改变终端状态：\n{command}\n\n仍要重新运行吗？", { command }),
      { kind: "warning" },
    );
    if (!ok) return false;
  }
  try {
    await terminalApi.write(tabId, new TextEncoder().encode(command + "\r"));
    toast("success", t("已重新运行块 #{index}", { index: block.index }));
    return true;
  } catch (e) {
    toast("error", t("重新运行失败：{value}", { value: describeError(e) }));
    return false;
  }
}

// saveCommandBlockAsSnippet stores the block's command as a single-command snippet after asking for a name.
export async function saveCommandBlockAsSnippet(block: CommandBlock, queryClient?: QueryClient): Promise<boolean> {
  if (!block.command.trim()) {
    toast("info", t("该块没有可保存的命令"));
    return false;
  }
  const name = await promptText(t("片段名称"), Array.from(block.command.trim()).slice(0, SNIPPET_NAME_LIMIT).join(""));
  const trimmed = name?.trim();
  if (!trimmed) return false;
  try {
    await assetApi.snippetCreate(trimmed, block.command);
    void queryClient?.invalidateQueries({ queryKey: ["snippets"] });
    toast("success", t("已保存片段「{trimmed}」", { trimmed }));
    return true;
  } catch (e) {
    toast("error", t("保存片段失败：{value}", { value: describeError(e) }));
    return false;
  }
}

// explainCommandBlock queues the block output as a text reference for the AI panel and opens the panel.
// The output travels as a data reference, never as part of the user's message.
export function explainCommandBlock(block: CommandBlock, text: string, sourceLabel: string): boolean {
  if (!text) {
    toast("info", t("块 #{index} 的输出已滚出缓冲，无法解释", { index: block.index }));
    return false;
  }
  const id = `block:${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`;
  const clipped = text.length > EXPLAIN_TEXT_LIMIT ? text.slice(-EXPLAIN_TEXT_LIMIT) : text;
  const command = Array.from(singleLine(block.command)).slice(0, COMMAND_LABEL_LIMIT).join("");
  const ui = useUi.getState();
  ui.queueAiRef({
    kind: "text",
    id,
    label: t("命令块 #{index} · {command}", { index: block.index, command }),
    detail: singleLine(sourceLabel),
    refId: id,
    text: clipped,
  });
  ui.setRightOpen(true);
  return true;
}
