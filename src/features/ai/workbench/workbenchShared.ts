import { useUi } from "../../../app/store";
import { aiApi } from "../../../ipc/commands";
import type { AiConversationStatus } from "../../../ipc/types";
import { ask, pickSavePath, finishSave } from "../../../ui/dialogs";
import { describeError } from "../../../ui/errorText";
import { isoDate, localDayStart, sanitizeFileName, viewerTimeZone } from "../../terminal/transcriptExport";
import { aiTabBusyIds } from "../runOwnership";
import { currentAiBoardKey, runStatusOf } from "../panel/aiSidebarShared";
import { t } from "../../../i18n";

export const WORKBENCH_STATUSES: AiConversationStatus[] = ["running", "interrupted", "completed", "failed", "idle"];

export function conversationStatusLabel(status: AiConversationStatus): string {
  switch (status) {
    case "running":
      return t("运行中");
    case "interrupted":
      return t("已中断");
    case "completed":
      return t("已完成");
    case "failed":
      return t("失败");
    default:
      return t("未运行");
  }
}

export function conversationStatusBadge(status: AiConversationStatus): string {
  switch (status) {
    case "running":
      return "nx-badge-amber";
    case "interrupted":
      return "nx-badge-purple";
    case "failed":
      return "nx-badge-red";
    case "completed":
      return "nx-badge-green";
    default:
      return "";
  }
}

export function conversationTitle(title: string): string {
  return title.trim() || t("（未命名对话）");
}

// Quick time ranges shared by the workbench filter and the export dialog; "custom" uses the day inputs.
export type RangePreset = "1" | "7" | "30" | "90" | "all" | "custom";

export const RANGE_PRESETS: { value: Exclude<RangePreset, "custom">; label: () => string }[] = [
  { value: "1", label: () => t("最近 1 天") },
  { value: "7", label: () => t("最近 7 天") },
  { value: "30", label: () => t("最近 30 天") },
  { value: "90", label: () => t("最近 90 天") },
  { value: "all", label: () => t("全部") },
];

// presetDates returns the inclusive local day range of a quick pick: the last N days end today.
export function presetDates(preset: Exclude<RangePreset, "custom">, now: Date): { from: string; to: string } {
  if (preset === "all") return { from: "", to: "" };
  const days = Number(preset);
  const from = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1));
  return { from: isoDate(from), to: isoDate(now) };
}

// rangeBounds turns the inclusive day inputs into [updatedFrom, updatedBefore) in Unix ms; 0 is unbounded.
export function rangeBounds(from: string, to: string): { updatedFrom: number; updatedBefore: number; valid: boolean } {
  const start = from ? localDayStart(from) : null;
  const end = to ? localDayStart(to) : null;
  const updatedFrom = start ? start.getTime() : 0;
  const updatedBefore = end ? new Date(end.getFullYear(), end.getMonth(), end.getDate() + 1).getTime() : 0;
  const valid = (!from || !!start) && (!to || !!end) && (!updatedBefore || updatedBefore > updatedFrom);
  return { updatedFrom, updatedBefore, valid };
}

// The Markdown wording of exported conversations in the interface language; the backend keeps the source wording
// for any key missing here.
export function conversationExportLabels(): Record<string, string> {
  return {
    host: t("主机"),
    unlinked: t("未关联主机"),
    status: t("状态"),
    created: t("创建时间"),
    updated: t("最后活动"),
    runs: t("运行次数"),
    user: t("用户"),
    assistant: t("AI"),
    interrupted: t("（回答已中断）"),
    images: t("附带 {count} 张图片", { count: "{count}" }),
    tool: t("工具调用"),
    result: t("结果"),
    failed: t("失败"),
    exitCode: t("退出码"),
    fileChange: t("文件变更"),
    plan: t("提交的方案"),
    summary: t("上下文摘要"),
    approvals: t("确认记录"),
    pending: t("未处理"),
    question: t("提问"),
    "status.running": t("运行中"),
    "status.interrupted": t("已中断"),
    "status.completed": t("已完成"),
    "status.failed": t("失败"),
    "status.idle": t("未运行"),
    "decision.allow": t("已允许一次"),
    "decision.allow_session": t("本对话已允许此类"),
    "decision.allow_persistent": t("已永久允许"),
    "decision.deny": t("已拒绝"),
    "decision.answered": t("已回答"),
    "decision.expired": t("已过期"),
  };
}

export function approvalDecisionLabel(decision: string): string {
  if (!decision) return t("未处理");
  return conversationExportLabels()[`decision.${decision}`] ?? decision;
}

// openConversationInDock shows the conversation in the right AI dock of the current workspace: a dock tab already
// bound to it is focused, otherwise an unused blank tab or a new tab is bound to it. The dock restores the
// conversation from the binding, including a run that is still going.
export function openConversationInDock(conversationId: string, title: string): void {
  const state = useUi.getState();
  const key = currentAiBoardKey();
  const board = state.ensureAiBoard(key);
  const existing = board.tabs.find((tab) => tab.conversationId === conversationId);
  if (existing) {
    state.setActiveAiTab(key, existing.id);
  } else {
    const blank = board.tabs.find((tab) => !tab.conversationId && !(tab.draft ?? "").trim() && !aiTabBusyIds.has(tab.id));
    const tabId = blank?.id ?? state.addAiTab(key);
    state.updateAiTab(key, tabId, { conversationId, title: conversationTitle(title) }, state.aiAccountEpoch);
    state.setActiveAiTab(key, tabId);
  }
  state.setRightOpen(true);
}

// deleteConversationGuarded is the delete flow of the dock's history list: it refuses while one of the
// conversation's runs is going or interrupted, asks for confirmation and deletes through the backend run guard.
// canProceed runs again after the confirmation, so a caller can refuse a run that started meanwhile.
export async function deleteConversationGuarded(
  conversation: { id: string; title: string },
  canProceed: () => boolean = () => true,
): Promise<boolean> {
  const pushToast = useUi.getState().pushToast;
  try {
    const runs = await aiApi.runs(conversation.id, 20);
    const inFlight = runStatusOf(Array.isArray(runs) ? runs : []);
    if (inFlight) {
      pushToast(
        "info",
        inFlight === "running"
          ? t("此对话中 AI 正在工作，请等待结束或先停止，再删除")
          : t("此对话中有被中断的 AI 任务，请先打开对话处理，再删除"),
      );
      return false;
    }
  } catch (e) {
    // Deleting removes the run records too, so an unchecked conversation is kept.
    pushToast("error", t("无法确认此对话中是否有 AI 任务正在运行，暂未删除，请稍后重试：{value}", { value: describeError(e) }));
    return false;
  }
  const ok = await ask(t("确定要删除对话「{value}」吗？消息记录、运行记录和定时任务将一并清除，不可恢复。", { value: conversation.title || t("（未命名对话）") }), {
    kind: "warning",
  });
  if (!ok || !canProceed()) return false;
  try {
    await aiApi.conversationDelete(conversation.id);
  } catch (e) {
    pushToast("error", t("未能删除对话：{value}", { value: describeError(e) }));
    return false;
  }
  return true;
}

// releaseDeletedConversation unbinds dock tabs from a conversation deleted outside them, so none keeps showing it.
export function releaseDeletedConversation(conversationId: string): void {
  const state = useUi.getState();
  for (const [key, board] of Object.entries(state.aiBoards)) {
    for (const tab of board.tabs) {
      if (tab.conversationId === conversationId) {
        state.updateAiTab(key, tab.id, { conversationId: undefined, title: t("新对话") }, state.aiAccountEpoch);
      }
    }
  }
}

// exportConversationMarkdown saves one conversation as a Markdown file the user picks.
export async function exportConversationMarkdown(
  conversation: { id: string; title: string; assetId?: string },
  hostName?: string,
): Promise<void> {
  const pushToast = useUi.getState().pushToast;
  const name = `${sanitizeFileName(conversationTitle(conversation.title)).replace(/\s+/g, "-")}.md`;
  const path = await pickSavePath(name);
  if (!path) return;
  try {
    await aiApi.conversationExport({
      format: "md",
      ids: [conversation.id],
      path,
      labels: conversationExportLabels(),
      ...(conversation.assetId && hostName ? { hostNames: { [conversation.assetId]: hostName } } : {}),
      ...viewerTimeZone(),
    });
    const where = await finishSave(path, name);
    if (!where) pushToast("info", t("已取消保存"));
    else pushToast("success", t("已导出对话到 {where}", { where }));
  } catch (error) {
    pushToast("error", t("导出失败：{value}。请更换保存位置后重试", { value: describeError(error) }));
  }
}
