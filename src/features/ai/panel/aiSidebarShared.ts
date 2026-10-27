import { type AiPermissionMode } from "../../../ipc/commands";
import { type ChatItem, type ConfirmItem, type StatusLine } from "../conversation";
import type { AiRunDto } from "../../../ipc/types";
import { useUi, GLOBAL_AI_BOARD_KEY } from "../../../app/store";
import { formatBytes } from "../../../ui/format";


export interface RefChip {
  id: string;
  kind: "asset" | "tab" | "file";
  label: string;
  detail: string;
  refId: string;
  path?: string;
  sessionId?: string;
}

export const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: "只读", hint: "AI 只能查看，修改一律拒绝；已开启免确认授权的操作除外" },
  { value: "read_write", label: "读写", hint: "查看直接执行，修改前先问你；选「本会话允许」后，本次对话内同类操作不再询问" },
  { value: "silent", label: "自动执行", hint: "普通修改直接执行，高危和无法判断的操作仍会问你" },
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

export const CONFIRM_RESOLUTION: Record<"allow" | "allow_session" | "allow_persistent" | "deny", string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  allow_persistent: "已永久允许（可随时在授权管理中撤销）",
  deny: "已拒绝",
};

export type SlashItem = { key: string; label: string; hint: string; run: () => void };

export const CONFIRM_HELP =
  "「允许一次」只放行这一次；「本会话允许」在本次对话中不再询问同类操作；「永久允许」写入授权管理，可随时撤销。高危、无法判断和生产环境的操作每次都会询问。想让某类操作每次都先确认，可在「设置 → AI 拦截规则」中添加。";

export function confirmTitle(risk: string | undefined): string {
  if (risk === "danger") return "高危操作，需要你确认";
  if (risk === "unknowable") return "无法判断影响，需要你确认";
  return "需要你确认";
}

export const RULE_CAPABLE_TOOLS = new Set(["write_file", "edit_file", "exec_commands", "send_keys"]);

export function permanentScopeText(card: ConfirmItem): string {
  if (card.tool === "write_file" || card.tool === "edit_file") {
    const action = card.tool === "write_file" ? "写入" : "修改";
    return `永久允许${action} ${card.rulePattern || "该路径"} 下的文件`;
  }
  if (card.tool === "send_keys") return "永久允许该设备发送普通按键";
  return "永久允许该设备执行普通命令";
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
  if (run.status === "canceled") return { type: "canceled", message: "已停止本轮" };
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
  if (run.status === "failed") return { type: "error", message: run.error || "AI 任务失败" };
  if (run.status === "expired") return { type: "error", message: run.error || "AI 任务已过期" };
  if (run.status === "interrupted" && run.finishedAt != null) {
    return { type: "error", message: run.error || "AI 任务已中断" };
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
  // visible overrides the right dock's open state when the assistant is hosted elsewhere (the session side panel).
  visible?: boolean;
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
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 轮 · 正在思考…` : "正在思考…";
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

export function toolLabel(tool: string): string {
  switch (tool) {
    case "exec_commands":
      return "执行命令";
    case "write_file":
      return "写入文件";
    case "edit_file":
      return "修改文件";
    case "send_keys":
      return "发送按键";
    case "read_screen":
      return "读取屏幕";
    case "terminal_create":
      return "新建终端";
    case "docker_exec":
      return "容器内执行";
    case "docker_control":
      return "容器操作";
    case "db_query":
      return "数据库查询";
    case "redis_command":
      return "Redis 命令";
    case "send_reminder":
      return "定时提醒";
    case "shell_history":
      return "读取 shell 历史";
    default:
      return tool;
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
