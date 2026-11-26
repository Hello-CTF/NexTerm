import { type AiPermissionMode } from "../../../ipc/commands";
import { type ChatItem, type ConfirmItem, type StatusLine } from "../conversation";
import type { AiRunDto } from "../../../ipc/types";
import { useUi, GLOBAL_AI_BOARD_KEY } from "../../../app/store";
import { formatBytes } from "../../../ui/format";
import { describeErrorSource } from "../../../ui/errorText";
import { t } from "../../../i18n";

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
  // UI only, never sent: the text is something the user selected (a terminal or editor selection, not command
  // output), and where it came from so the chip can jump back: a store tab, or a file and line range.
  selection?: boolean;
  sourceTabId?: string;
  sourceFile?: RefFileSource;
}

export interface RefFileSource {
  sessionId: string;
  path: string;
  // 1-based, inclusive; absent for a whole-file reference.
  line?: number;
  endLine?: number;
}

export { composeUserMessage, splitUserRefs } from "../userRefs";

export const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: t("只读"), hint: t("AI 只能查看，修改一律拒绝") },
  { value: "read_write", label: t("读写"), hint: t("查看操作直接执行，修改前需要你确认；选择「本对话允许」后，本次对话内同类操作不再询问") },
  { value: "silent", label: t("自动执行"), hint: t("普通修改直接执行，高危和无法判断的操作仍需要你确认") },
  {
    value: "full_access",
    label: t("完全放行"),
    hint: t("除禁止的操作外直接执行，包括高危和无法判断的操作；命中拦截规则仍需确认。生产环境按「自动执行」处理，跨主机或多主机的修改仍需确认，禁止的操作始终拒绝，定时任务不使用此模式"),
  },
];

export const MODE_LABEL: Record<AiPermissionMode, string> = {
  read_only: t("只读"),
  read_write: t("读写"),
  silent: t("自动执行"),
  full_access: t("完全放行"),
};

// Full access is the one mode that skips confirmation for high-risk commands,
// so choosing it always goes through an explicit confirmation first.
export function fullAccessConfirmText(perHost: boolean): string {
  return t(
    "确定要切换到「完全放行」吗？\n\n{scope}，AI 将直接执行除禁止操作外的所有操作，包括高危和无法判断的命令（例如重启主机、运行解释器代码），执行前不再询问，可能造成数据丢失或服务中断。\n\n仍会询问：命中拦截规则的操作；生产环境中的高危和无法判断的操作；跨主机或多主机的修改。禁止的操作始终拒绝，定时任务不使用此模式。",
    { scope: perHost ? t("在这台主机上") : t("在所有未单独设置模式的主机上") },
  );
}

// appliedPermissionMode is the mode a tool call on this scope is decided with: full access never reaches a production
// target, which the backend decides as 自动执行.
export function appliedPermissionMode(mode: AiPermissionMode, production: boolean): { mode: AiPermissionMode; capped: boolean } {
  if (production && mode === "full_access") return { mode: "silent", capped: true };
  return { mode, capped: false };
}

// permissionSourceText says why a mode applies: the global mode, or this host's own setting.
export function permissionSourceText(source: "global" | "host"): string {
  return source === "host" ? t("这台主机单独设置") : t("全局默认");
}

export const PLAN_MODE_HINT =t("先生成方案，批准后执行");

export const PLAN_EXECUTE_MESSAGE = t("按上面的方案执行。");

export const AI_RESYNC_BACKOFF_MS = 1000;
export const AI_RESYNC_MAX_ATTEMPTS = 2;
export const AI_SYNC_NOTICE_DISMISS_MS = 2400;
export const AI_HITL_EXPIRY_GRACE_MS = 500;
// A run whose channel delivered nothing for this long is caught up from its
// persisted events by the terminal poll.
export const AI_LIVE_DELIVERY_WINDOW_MS = 10_000;

// isStaleInteractionError recognizes the backend's refusal of a confirmation or
// answer that no longer matches a pending request: another client handled it,
// it expired, or its run ended.
export function isStaleInteractionError(e: unknown): boolean {
  const source = describeErrorSource(e);
  return source.includes("确认已过期、重复或不属于当前工具调用") || source.includes("AI 任务不存在或已结束");
}

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
  return t("永久允许{value}", { value: card.permanentScope ?? "" });
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

// runScopeForRefs picks the scope a run is sent with. Without an active terminal (its tab closed, or a non-terminal
// view in front) a message that @-references exactly one terminal targets that terminal, as if it were active;
// otherwise the run would have no terminal to act on and could only report the closed one.
export function runScopeForRefs(scope: Record<string, unknown>, refs: readonly RefChip[]): Record<string, unknown> {
  if (scope.tabId || scope.connId) return scope;
  const tabs = refs.filter((ref) => ref.kind === "tab" && ref.refId && ref.sessionId);
  if (tabs.length !== 1) return scope;
  return { sessionId: tabs[0].sessionId, tabId: tabs[0].refId };
}

export function aiDatabaseScopeLabel(
  workspace: AiWorkspaceScope,
  asset: { name: string; kind: string } | null,
): string {
  const kind = workspace.dbKind || asset?.kind || workspace.assetKind;
  const kindLabel = kind ? AI_DB_KIND_LABEL[kind] ?? kind : t("未知类型");
  const target = asset?.name?.trim() || workspace.connId || workspace.title || t("未命名连接");
  return t("数据库：{target} · {kindLabel}", { target, kindLabel });
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
  if (run.status === "canceled") return { type: "canceled", message: t("已停止") };
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
  if (run.status === "failed") return { type: "error", message: run.error || t("AI 任务失败，请重试") };
  if (run.status === "expired") return { type: "error", message: run.error || t("AI 任务已过期，请重新发送") };
  if (run.status === "interrupted" && run.finishedAt != null) {
    return { type: "error", message: run.error || t("AI 任务被中断，请重试") };
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
    fr.onerror = () => reject(new Error(t("读取图片失败")));
    fr.readAsDataURL(file);
  });
}

export function statusText(s: StatusLine): string {
  if (s.phase === "paused") return t("接管已暂停：{value}", { value: s.detail ?? t("用户暂停") });
  if (s.phase === "compacting") return s.detail ?? t("上下文接近上限，正在压缩早期的命令输出…");
  if (s.phase === "thinking") return s.turn ? t("第 {turn} 步 · 正在思考…", { turn: s.turn }) : t("正在思考…");
  if (s.phase === "tool_args") {
    return t("{value} · 已生成参数 {value2}", { value: preparingLabel(s.tool), value2: formatBytes(s.chars ?? 0) });
  }
  return s.detail ?? s.phase;
}

export function preparingLabel(tool?: string): string {
  switch (tool) {
    case "write_file":
      return t("正在准备写入内容");
    case "edit_file":
      return t("正在准备修改内容");
    case "exec_commands":
      return t("正在准备命令");
    case "send_keys":
      return t("正在准备按键");
    default:
      return t("正在准备工具参数");
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
