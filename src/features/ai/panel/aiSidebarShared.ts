import { type AiPermissionMode } from "../../../ipc/commands";
import { type ChatItem, type ConfirmItem, type StatusLine } from "../conversation";
import type { AiRunDto } from "../../../ipc/types";
import { useUi, GLOBAL_AI_BOARD_KEY } from "../../../app/store";
import { formatBytes } from "../../../ui/format";

export { CONFIRM_HELP, CONFIRM_RESOLUTION, confirmTitle, toolLabel } from "./aiConfirmLabels";


export interface RefChip {
  id: string;
  kind: "asset" | "tab" | "file" | "text";
  label: string;
  detail: string;
  refId: string;
  path?: string;
  sessionId?: string;
  // Inline content for kind "text"; sent as a data reference, never merged into the message.
  text?: string;
}

export { composeUserMessage, splitUserRefs } from "../userRefs";

export const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: "只读", hint: "AI 只能查看，修改一律拒绝" },
  { value: "read_write", label: "读写", hint: "查看操作直接执行，修改前需要你确认；选择「本会话允许」后，本次对话内同类操作不再询问" },
  { value: "silent", label: "自动执行", hint: "普通修改直接执行，高危和无法判断的操作仍需要你确认" },
];

export const MODE_LABEL: Record<AiPermissionMode, string> = {
  read_only: "只读",
  read_write: "读写",
  silent: "自动执行",
};

export const PLAN_MODE_HINT = "先生成方案，批准后执行";

export const PLAN_EXECUTE_MESSAGE = "按上面的方案执行。";

export const AI_RESYNC_BACKOFF_MS = 1000;
export const AI_RESYNC_MAX_ATTEMPTS = 2;
export const AI_SYNC_NOTICE_DISMISS_MS = 2400;
export const AI_HITL_EXPIRY_GRACE_MS = 500;

export function latestPlanApproved(items: ChatItem[]): boolean {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.role === "plan") return false;
    if (item.role === "user" && item.text.trim() === PLAN_EXECUTE_MESSAGE) return true;
  }
  return false;
}

export interface AiCreatedTerminal {
  tabId: string;
  sessionId: string;
  title?: string;
}

// aiCreatedTerminalOf extracts the structured terminal_create foreground
// payload the backend puts on a successful tool result event.
export function aiCreatedTerminalOf(ev: Record<string, unknown>): AiCreatedTerminal | null {
  if (ev.type !== "toolResult" && ev.type !== "subagentToolResult") return null;
  if (ev.ok !== true) return null;
  const raw = ev.terminal;
  if (!raw || typeof raw !== "object") return null;
  const terminal = raw as Record<string, unknown>;
  if (typeof terminal.tabId !== "string" || terminal.tabId === "") return null;
  if (typeof terminal.sessionId !== "string" || terminal.sessionId === "") return null;
  return {
    tabId: terminal.tabId,
    sessionId: terminal.sessionId,
    title: typeof terminal.title === "string" && terminal.title !== "" ? terminal.title : undefined,
  };
}

export type SlashItem = { key: string; label: string; hint: string; run: () => void };

export function permanentScopeText(card: ConfirmItem): string {
  return `永久允许${card.permanentScope ?? ""}`;
}

export function extractUrls(text: string): string[] {
  const found = text.match(/https?:\/\/[^\s"'<>）)\]]+/g) ?? [];
  return [...new Set(found)];
}

export const DRAG_AI_TAB = "application/x-nexterm-ai-tab";

export type AiWorkspaceScope = {
  kind: string;
  title: string;
  assetId?: string;
  connId?: string;
  dbKind?: string;
  assetKind?: string;
};

export const AI_DB_KIND_LABEL: Record<string, string> = {
  mysql: "MySQL",
  postgres: "PostgreSQL",
  redis: "Redis",
};

export function aiScopeForWorkspace(
  workspace: AiWorkspaceScope | null,
  sessionId?: string,
  tabId?: string,
): Record<string, unknown> {
  const scope: Record<string, unknown> = { sessionId, tabId };
  if (workspace?.kind === "session" && workspace.assetId) scope.assetId = workspace.assetId;
  if (workspace?.kind !== "db" || !workspace.connId) return scope;
  scope.connId = workspace.connId;
  if (workspace.assetId) scope.assetId = workspace.assetId;
  return scope;
}

export function aiDatabaseScopeLabel(
  workspace: AiWorkspaceScope,
  asset: { name: string; kind: string } | null,
): string {
  const kind = workspace.dbKind || asset?.kind || workspace.assetKind;
  const kindLabel = kind ? AI_DB_KIND_LABEL[kind] ?? kind : "未知类型";
  const target = asset?.name?.trim() || workspace.connId || workspace.title || "未命名连接";
  return `数据库：${target} · ${kindLabel}`;
}

export function runStatusOf(runs: AiRunDto[]): "running" | "interrupted" | null {
  if (runs.some((run) => run.status === "running")) return "running";
  if (runs.some((run) => run.status === "interrupted" && run.finishedAt == null)) return "interrupted";
  return null;
}

export function currentAiBoardKey(): string {
  const state = useUi.getState();
  const active =
    state.workspaces.find((workspace) => workspace.id === state.activeWorkspaceId) ??
    state.workspaces[state.workspaces.length - 1] ??
    null;
  return active && active.kind !== "tools" ? active.id : GLOBAL_AI_BOARD_KEY;
}

export function terminalEventOf(run: AiRunDto): Record<string, unknown> | null {
  if (run.status === "canceled") return { type: "canceled", message: "已停止" };
  if (run.status === "completed") {
    return {
      type: "done",
      answer: run.answer,
      turns: run.turns,
      tokensIn: run.tokensIn,
      tokensOut: run.tokensOut,
      cacheCreationTokens: run.cacheCreationTokens,
      latencyMs: run.latencyMs,
    };
  }
  if (run.status === "failed") return { type: "error", message: run.error || "AI 任务失败，请重试" };
  if (run.status === "expired") return { type: "error", message: run.error || "AI 任务已过期，请重新发送" };
  if (run.status === "interrupted" && run.finishedAt != null) {
    return { type: "error", message: run.error || "AI 任务被中断，请重试" };
  }
  return null;
}

export interface AiChatTabProps {
  sessionId?: string;
  tabId?: string;
  boardKey: string;
  conversationId?: string;
  active: boolean;
  tabKey: string;
  draft?: string;
  onDraftChange: (v: string) => void;
  onConversationChange: (conversationId: string | undefined, title?: string) => void;
  onTitleChange: (title: string) => void;
}

export function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result));
    fr.onerror = () => reject(new Error("读取图片失败"));
    fr.readAsDataURL(file);
  });
}

export function statusText(s: StatusLine): string {
  if (s.phase === "paused") return `接管已暂停：${s.detail ?? "用户暂停"}`;
  if (s.phase === "compacting") return s.detail ?? "上下文接近上限，正在压缩早期的命令输出…";
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 步 · 正在思考…` : "正在思考…";
  if (s.phase === "tool_args") {
    return `${preparingLabel(s.tool)} · 已生成参数 ${formatBytes(s.chars ?? 0)}`;
  }
  return s.detail ?? s.phase;
}

export function preparingLabel(tool?: string): string {
  switch (tool) {
    case "write_file":
      return "正在准备写入内容";
    case "edit_file":
      return "正在准备修改内容";
    case "exec_commands":
      return "正在准备命令";
    case "send_keys":
      return "正在准备按键";
    default:
      return "正在准备工具参数";
  }
}

export function interceptionRuleFromCard(card: ConfirmItem): string | null {
  if (card.tool === "write_file" || card.tool === "edit_file") {
    const path = card.preview?.path.trim();
    return path ? path.slice(0, 120) : null;
  }
  if (card.tool === "exec_commands") {
    const commands = (card.commands ?? []).map((command) => command.trim()).filter(Boolean);
    return commands.length > 0 ? commands.join("\n").slice(0, 120) : null;
  }
  return null;
}
export type AiChatBase = Omit<AiChatTabProps, "conversationId"> & { boundConversationId: string | undefined };
