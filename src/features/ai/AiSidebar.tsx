import { memo, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type DragEvent as ReactDragEvent } from "react";
import { ask, askChoice, promptText } from "../../ui/dialogs";
import { aiApi, assetApi, modelApi, terminalApi, type AiPermissionConfig, type AiPermissionMode } from "../../ipc/commands";
import { productionSkillsApi } from "../settings/skillsApi";
import { createAiChannel, disposeChannel, onChannelReopen, type IpcChannel } from "../../ipc/events";
import type { AiHitlEventDto, AiHitlSnapshotDto, AiRunDto, MessageDto } from "../../ipc/types";
import {
  GLOBAL_AI_BOARD_KEY,
  nextTabId,
  useActiveWorkspace,
  useUi,
  type AiDockTab,
  type TakeoverState,
} from "../../app/store";
import {
  registerTakeoverResumeHandler,
  takeoverPaused,
  withTakeoverPaused,
} from "../../app/TakeoverBanner";
import { formatBinding, formatBindingAria, useKeybindings } from "../../app/keybindings";
import { describeError } from "../../ui/errorText";
import { formatBytes } from "../../ui/format";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { DESKTOP } from "../../ipc/env";
import { ModelPanel } from "./ModelPanel";
import { GrantPanel } from "./GrantPanel";
import { CronPanel } from "./CronPanel";
import { AgentRosterPanel } from "./AgentRosterPanel";
import { AgentRunHistoryPanel } from "./AgentRunHistoryPanel";
import { AgentToolCard } from "./AgentToolCard";
import { WatchPanel } from "./WatchPanel";
import { createAgentRosterApi, createAgentRunHistoryApi } from "./agentProduction";
import {
  ToolHandleRegistry,
  approvalFromConfirmCard,
  buildAgentToolCall,
  runSummaryFromAttempt,
} from "./agentToolData";
import { refreshAgentRosterSnapshot, useAgentRosterSnapshot } from "./agentRosterSnapshot";
import type { AgentApprovalDecision, AgentRosterEntry, AgentToolCall } from "./agentTypes";
import { watchPanelApi } from "../../ipc/watchApi";
import { useAuth } from "../auth/store";
import { ModelSelector } from "./ModelSelector";
import { Markdown } from "./Markdown";
import { ProductionWriteRiskNotice } from "../production/ProductionWarning";
import { useAssetProductionClassification } from "../production/productionSettings";
import {
  evaluateEnvironmentConfirmation,
  targetKey,
  type EnvironmentConfirmationChallenge,
} from "../production/environmentConfirmation";
import { diffLineText, hasVisibleChange, simpleDiff } from "./diff";
import {
  answerInput,
  confirmationInput,
  earlierMessageHistoryStart,
  MESSAGE_HISTORY_PAGE_SIZE,
  messageHistoryStart,
} from "./aiWire";
import {
  historyToItems,
  pendingInteraction,
  type ApplyResult,
  type ChatItem,
  type ConfirmItem,
  type QuestionItem,
  type StatusLine,
  type SubagentTimeline,
  type ToolItem,
} from "./conversation";
import { createConversationStream, type ConversationStream } from "./conversationStream";
import { useConversationFollow } from "./conversationFollow";
import {
  findResumableRun,
  isRunTerminal,
  pendingDeadline,
  replayableJobIds,
  replayableRuns,
  replayRunEventsAfter,
  RUN_RESTORE_LIMIT,
} from "./runRestore";
import { findItemMatches, stepMatch } from "./conversationSearch";
import { useVirtualWindow } from "./conversationVirtual";
import {
  aiRunBlocksStart,
  aiTabBusyIds,
  aiTabRunControls,
  bindAiRunJob,
  clearAiTabRun,
  registerAiRunGeneration,
  confirmAiRunTerminal,
  createAiRun,
  finishAiRunSpawn,
  getAiTabBusyVersion,
  isCurrentAiRun,
  markAiRunCancellationUnconfirmed,
  requestAiRunCancellation,
  setAiTabBusy,
  settleAiRun,
  subscribeAiTabBusy,
  type AiRunSlot,
} from "./runOwnership";
import { UsageRing } from "./UsageRing";
import {
  IconAlert,
  IconBot,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconChevronUp,
  IconClock,
  IconClose,
  IconEdit,
  IconHistory,
  IconImage,
  IconInfo,
  IconList,
  IconLoader,
  IconMonitor,
  IconPlay,
  IconPlus,
  IconRefresh,
  IconSearch,
  IconShield,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

interface RefChip {
  id: string;
  kind: "asset" | "tab" | "file";
  label: string;
  detail: string;
  refId: string;
  path?: string;
  sessionId?: string;
}

const MODE_OPTIONS: { value: AiPermissionMode; label: string; hint: string }[] = [
  { value: "read_only", label: "只读", hint: "AI 只能查看，修改一律拒绝；已开启免确认授权的操作除外" },
  { value: "read_write", label: "读写", hint: "查看直接执行，修改前先问你；选「本会话允许」后，本次对话内同类操作不再询问" },
  { value: "silent", label: "自动执行", hint: "普通修改直接执行，高危和无法判断的操作仍会问你" },
];

const MODE_LABEL: Record<AiPermissionMode, string> = {
  read_only: "只读",
  read_write: "读写",
  silent: "自动执行",
};

const PLAN_MODE_HINT = "先生成方案，批准后执行";

const PLAN_EXECUTE_MESSAGE = "按上面的方案执行。";

export const AI_RESYNC_BACKOFF_MS = 1000;
export const AI_RESYNC_MAX_ATTEMPTS = 2;
export const AI_SYNC_NOTICE_DISMISS_MS = 2400;
export const AI_HITL_EXPIRY_GRACE_MS = 500;

function latestPlanApproved(items: ChatItem[]): boolean {
  for (let i = items.length - 1; i >= 0; i--) {
    const item = items[i];
    if (item.role === "plan") return false;
    if (item.role === "user" && item.text.trim() === PLAN_EXECUTE_MESSAGE) return true;
  }
  return false;
}

interface AiCreatedTerminal {
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

const CONFIRM_RESOLUTION: Record<"allow" | "allow_session" | "allow_persistent" | "deny", string> = {
  allow: "已允许一次",
  allow_session: "本会话已允许此类",
  allow_persistent: "已永久允许（可随时在授权管理中撤销）",
  deny: "已拒绝",
};

type SlashItem = { key: string; label: string; hint: string; run: () => void };

const CONFIRM_HELP =
  "「允许一次」只放行这一次；「本会话允许」在本次对话中不再询问同类操作；「永久允许」写入授权管理，可随时撤销。高危、无法判断和生产环境的操作每次都会询问。想让某类操作每次都先确认，可在「设置 → AI 拦截规则」中添加。";

function confirmTitle(risk: string | undefined): string {
  if (risk === "danger") return "高危操作，需要你确认";
  if (risk === "unknowable") return "无法判断影响，需要你确认";
  return "需要你确认";
}

const RULE_CAPABLE_TOOLS = new Set(["write_file", "edit_file", "exec_commands", "send_keys"]);

function permanentScopeText(card: ConfirmItem): string {
  if (card.tool === "write_file" || card.tool === "edit_file") {
    const action = card.tool === "write_file" ? "写入" : "修改";
    return `永久允许${action} ${card.rulePattern || "该路径"} 下的文件`;
  }
  if (card.tool === "send_keys") return "永久允许该设备发送普通按键";
  return "永久允许该设备执行普通命令";
}

function extractUrls(text: string): string[] {
  const found = text.match(/https?:\/\/[^\s"'<>）)\]]+/g) ?? [];
  return [...new Set(found)];
}

const DRAG_AI_TAB = "application/x-nexterm-ai-tab";

type AiWorkspaceScope = {
  kind: string;
  title: string;
  assetId?: string;
  connId?: string;
  dbKind?: string;
  assetKind?: string;
};

const AI_DB_KIND_LABEL: Record<string, string> = {
  mysql: "MySQL",
  postgres: "PostgreSQL",
  redis: "Redis",
};

function aiScopeForWorkspace(
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

function aiDatabaseScopeLabel(
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

function currentAiBoardKey(): string {
  const state = useUi.getState();
  const active =
    state.workspaces.find((workspace) => workspace.id === state.activeWorkspaceId) ??
    state.workspaces[state.workspaces.length - 1] ??
    null;
  return active && active.kind !== "tools" ? active.id : GLOBAL_AI_BOARD_KEY;
}

function terminalEventOf(run: AiRunDto): Record<string, unknown> | null {
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

export function AiSidebar({ sessionId, tabId }: { sessionId?: string; tabId?: string }) {
  const setRightOpen = useUi((s) => s.setRightOpen);
  const rightWidth = useUi((s) => s.rightWidth);
  const pushToast = useUi((s) => s.pushToast);
  const takeover = useUi((s) => s.takeover);
  const activeWorkspace = useActiveWorkspace();
  const aiBoards = useUi((s) => s.aiBoards);
  const ensureAiBoard = useUi((s) => s.ensureAiBoard);
  const addAiTab = useUi((s) => s.addAiTab);
  const closeAiTab = useUi((s) => s.closeAiTab);
  const setActiveAiTab = useUi((s) => s.setActiveAiTab);
  const updateAiTab = useUi((s) => s.updateAiTab);
  const moveAiTab = useUi((s) => s.moveAiTab);
  const aiAccountEpoch = useUi((s) => s.aiAccountEpoch);
  const bindings = useKeybindings();
  const aiSidebarKeyLabel = formatBinding(bindings.toggleAiSidebar);
  const reclaimKeyLabel = formatBinding(bindings.reclaimTakeover);
  useSyncExternalStore(subscribeAiTabBusy, getAiTabBusyVersion);
  const [tabMenu, setTabMenu] = useState<ContextMenuState | null>(null);
  const [retitlingTabId, setRetitlingTabId] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<{ tabId: string; before: boolean } | null>(null);
  const [dbAsset, setDbAsset] = useState<{ id: string; name: string; kind: string } | null>(null);
  const dbAssetId = activeWorkspace?.kind === "db" ? activeWorkspace.assetId : undefined;

  useEffect(() => {
    let current = true;
    setDbAsset(null);
    if (dbAssetId) {
      void (async () => {
        try {
          const asset = await assetApi.get(dbAssetId);
          if (current) setDbAsset({ id: dbAssetId, name: asset.name, kind: asset.kind });
        } catch {
          if (current) setDbAsset(null);
        }
      })();
    }
    return () => {
      current = false;
    };
  }, [dbAssetId]);

  const boardKey =
    activeWorkspace && activeWorkspace.kind !== "tools" ? activeWorkspace.id : GLOBAL_AI_BOARD_KEY;
  useEffect(() => {
    ensureAiBoard(boardKey);
  }, [boardKey, ensureAiBoard]);

  const board = aiBoards[boardKey];
  const tabs = board?.tabs ?? [];
  const activeTabId = board?.activeTabId ?? tabs[tabs.length - 1]?.id ?? null;

  const handleConversationChange = useCallback(
    (tabId: string, conversationId: string | undefined, title?: string) => {
      updateAiTab(
        boardKey,
        tabId,
        conversationId === undefined
          ? { conversationId: undefined, title: "新会话" }
          : { conversationId, ...(title ? { title } : {}) },
        aiAccountEpoch,
      );
    },
    [boardKey, updateAiTab, aiAccountEpoch],
  );
  const handleTitleChange = useCallback(
    (tabId: string, title: string) => updateAiTab(boardKey, tabId, { title }, aiAccountEpoch),
    [boardKey, updateAiTab, aiAccountEpoch],
  );

  const retitleTab = async (tab: AiDockTab) => {
    if (!tab.conversationId || retitlingTabId) return;
    setRetitlingTabId(tab.id);
    try {
      const title = await aiApi.conversationRetitle(tab.conversationId);
      updateAiTab(boardKey, tab.id, { title }, aiAccountEpoch);
    } catch (e) {
      pushToast("error", `重新生成标题失败：${describeError(e)}`);
    } finally {
      setRetitlingTabId(null);
    }
  };

  const closeRequestsRef = useRef(new Map<string, Promise<boolean>>());
  const closeTabWithGuard = (tab: AiDockTab): Promise<boolean> => {
    const existing = closeRequestsRef.current.get(tab.id);
    if (existing) return existing;
    const request = (async () => {
      if (aiTabBusyIds.has(tab.id)) {
        const ok = await ask("这一轮仍在运行，关闭将停止它。关闭这个标签？", {
          title: "关闭 AI 标签",
          kind: "warning",
        });
        if (!ok) return false;
        if (aiTabBusyIds.has(tab.id)) {
          const cancelAndConfirm = aiTabRunControls.get(tab.id);
          if (!cancelAndConfirm) {
            pushToast("error", "停止未确认：运行状态不可用，标签已保留");
            return false;
          }
          if (!(await cancelAndConfirm())) return false;
        }
      }
      closeAiTab(boardKey, tab.id, aiAccountEpoch);
      return true;
    })();
    const tracked = request.finally(() => {
      if (closeRequestsRef.current.get(tab.id) === tracked) closeRequestsRef.current.delete(tab.id);
    });
    closeRequestsRef.current.set(tab.id, tracked);
    return tracked;
  };
  const requestCloseTab = (tab: AiDockTab) => {
    void closeTabWithGuard(tab);
  };
  const closeTabsInOrder = (targets: AiDockTab[]) => {
    void (async () => {
      for (const tab of targets) {
        if (!(await closeTabWithGuard(tab))) break;
      }
    })();
  };

  const dropBefore = (e: ReactDragEvent<HTMLElement>): boolean => {
    const rect = e.currentTarget.getBoundingClientRect();
    return e.clientX < rect.left + rect.width / 2;
  };

  const dropMoveFor = (
    e: ReactDragEvent<HTMLElement>,
    targetId: string,
  ): { from: number; to: number } | null => {
    const dragId = e.dataTransfer.getData(DRAG_AI_TAB);
    if (!dragId || dragId === targetId) return null;
    const from = tabs.findIndex((t) => t.id === dragId);
    const to = tabs.findIndex((t) => t.id === targetId);
    if (from < 0 || to < 0) return null;
    let insert = dropBefore(e) ? to : to + 1;
    if (from < insert) insert -= 1;
    return { from, to: insert };
  };

  const scopeLabel = activeWorkspace?.kind === "db"
    ? aiDatabaseScopeLabel(activeWorkspace, dbAsset?.id === dbAssetId ? dbAsset : null)
    : activeWorkspace && activeWorkspace.kind !== "tools"
      ? activeWorkspace.title
      : "当前实例 / 全局";

  return (
    <aside
      className="flex h-full shrink-0 flex-col overflow-y-auto border-l border-neutral-800/60 bg-neutral-950"
      style={{ width: rightWidth }}
    >
      <div className="flex h-[38px] shrink-0 items-center gap-1.5 border-b border-neutral-800/60 px-2 pl-3 [@media(max-height:480px)]:h-8">
        <span className="text-[12.5px] font-semibold text-neutral-100">AI 助手</span>
        <span className="min-w-0 truncate text-[11px] text-neutral-500" title={scopeLabel}>
          {scopeLabel}
        </span>
        {takeover && (
          <span
            className="nx-badge nx-badge-red"
            title={
              bindings.reclaimTakeover
                ? `终端接管进行中：按 ${reclaimKeyLabel} 收回控制权`
                : "终端接管进行中"
            }
          >
            <span className="nx-dot nx-dot-pulse" />
            接管中
          </span>
        )}
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={`收起 AI 侧栏${bindings.toggleAiSidebar ? ` (${aiSidebarKeyLabel})` : ""}`}
          aria-keyshortcuts={formatBindingAria(bindings.toggleAiSidebar) ?? undefined}
          onClick={() => setRightOpen(false)}
        >
          <IconChevronRight size={14} />
        </button>
      </div>

      <div className="nx-ai-tabstrip flex h-[34px] shrink-0 items-center gap-1 border-b border-neutral-800/60 pl-1.5 pr-1 [@media(max-height:480px)]:h-7">
        <div
          role="tablist"
          aria-label="AI 任务标签"
          className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
        >
          {tabs.map((t) => {
            const isActive = t.id === activeTabId;
            return (
              <div
                key={t.id}
                role="tab"
                aria-selected={isActive}
                tabIndex={isActive ? 0 : -1}
                draggable
                className={`nx-tab h-6 max-w-[140px] gap-1 px-2 text-[11px] ${isActive ? "is-active" : ""} ${
                  dropTarget?.tabId === t.id
                    ? dropTarget.before
                      ? "is-drop-before"
                      : "is-drop-after"
                    : ""
                }`}
                title={t.title}
                onClick={() => setActiveAiTab(boardKey, t.id)}
                onKeyDown={(e) => {
                  if (e.ctrlKey && e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
                    e.preventDefault();
                    const tabIndex = tabs.findIndex((candidate) => candidate.id === t.id);
                    moveAiTab(boardKey, tabIndex, tabIndex + (e.key === "ArrowLeft" ? -1 : 1));
                    return;
                  }
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    setActiveAiTab(boardKey, t.id);
                  }
                }}
                onDragStart={(e) => {
                  e.dataTransfer.setData(DRAG_AI_TAB, t.id);
                  e.dataTransfer.effectAllowed = "move";
                }}
                onDragOver={(e) => {
                  if (!e.dataTransfer.types.includes(DRAG_AI_TAB)) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                  const before = dropBefore(e);
                  setDropTarget((current) =>
                    current?.tabId === t.id && current.before === before
                      ? current
                      : { tabId: t.id, before },
                  );
                }}
                onDragLeave={() => {
                  setDropTarget((current) => (current?.tabId === t.id ? null : current));
                }}
                onDrop={(e) => {
                  if (!e.dataTransfer.types.includes(DRAG_AI_TAB)) return;
                  e.preventDefault();
                  setDropTarget(null);
                  const move = dropMoveFor(e, t.id);
                  if (!move) return;
                  moveAiTab(boardKey, move.from, move.to);
                }}
                onDragEnd={() => setDropTarget(null)}
                onContextMenu={(e) => {
                  e.preventDefault();
                  const tabIndex = tabs.findIndex((candidate) => candidate.id === t.id);
                  setTabMenu({
                    x: e.clientX,
                    y: e.clientY,
                    title: t.title,
                    items: [
                      {
                        kind: "item",
                        label: "重新生成标题",
                        disabled:
                          !t.conversationId ||
                          aiTabBusyIds.has(t.id) ||
                          retitlingTabId === t.id,
                        onSelect: () => void retitleTab(t),
                      },
                      {
                        kind: "item",
                        label: "左移标签",
                        disabled: tabIndex <= 0,
                        onSelect: () => moveAiTab(boardKey, tabIndex, tabIndex - 1),
                      },
                      {
                        kind: "item",
                        label: "右移标签",
                        disabled: tabIndex < 0 || tabIndex >= tabs.length - 1,
                        onSelect: () => moveAiTab(boardKey, tabIndex, tabIndex + 1),
                      },
                      { kind: "separator" },
                      { kind: "item", label: "关闭", onSelect: () => requestCloseTab(t) },
                      {
                        kind: "item",
                        label: "关闭左侧全部标签",
                        disabled: tabIndex <= 0,
                        onSelect: () => closeTabsInOrder(tabs.slice(0, tabIndex)),
                      },
                      {
                        kind: "item",
                        label: "关闭右侧全部标签",
                        disabled: tabIndex < 0 || tabIndex >= tabs.length - 1,
                        onSelect: () => closeTabsInOrder(tabs.slice(tabIndex + 1)),
                      },
                    ],
                  });
                }}
              >
                {aiTabBusyIds.has(t.id) && (
                  <span className="nx-dot nx-dot-pulse shrink-0" aria-label="运行中" />
                )}
                <span className="truncate">{t.title}</span>
                <button
                  type="button"
                  className="nx-tab-close"
                  aria-label={`关闭标签 ${t.title}`}
                  title="关闭标签"
                  onClick={(e) => {
                    e.stopPropagation();
                    requestCloseTab(t);
                  }}
                >
                  <IconClose size={10} />
                </button>
              </div>
            );
          })}
        </div>
        <button
          className="nx-icon-btn nx-icon-btn-sm shrink-0"
          title="新建 AI 标签"
          aria-label="新建 AI 标签"
          onClick={() => addAiTab(boardKey)}
        >
          <IconPlus size={13} />
        </button>
      </div>

      {tabs.map((t) => (
        <div
          key={`${boardKey}:${t.id}`}
          className={t.id === activeTabId ? "flex min-h-0 flex-1 flex-col" : "hidden"}
        >
          <AiChatTab
            sessionId={sessionId}
            tabId={tabId}
            boardKey={boardKey}
            conversationId={t.conversationId}
            active={t.id === activeTabId}
            tabKey={t.id}
            draft={t.draft}
            onDraftChange={(v) => updateAiTab(boardKey, t.id, { draft: v }, aiAccountEpoch)}
            onConversationChange={(id, title) => handleConversationChange(t.id, id, title)}
            onTitleChange={(title) => handleTitleChange(t.id, title)}
          />
        </div>
      ))}

      <ContextMenu state={tabMenu} onClose={() => setTabMenu(null)} />
    </aside>
  );
}

interface AiChatTabProps {
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

function AiChatTab({
  sessionId,
  tabId,
  boardKey,
  conversationId: boundConversationId,
  active,
  tabKey,
  draft,
  onDraftChange,
  onConversationChange,
  onTitleChange,
}: AiChatTabProps) {
  const rightOpen = useUi((s) => s.rightOpen);
  const anyAiBusy = useUi((s) => s.aiBusy);
  const takeoverState = useUi((s) => s.takeover);
  const pushToast = useUi((s) => s.pushToast);
  const workspaces = useUi((s) => s.workspaces);
  const sessions = useUi((s) => s.sessions);
  const activeWorkspace = useActiveWorkspace();
  const aiScope = useMemo(() => {
    let workspace = activeWorkspace;
    if (workspace?.kind === "session" && !workspace.assetId) {
      const currentSessionId = sessionId ?? workspace.sessionId;
      const current = sessions.find((item) => item.id === currentSessionId);
      if (current?.assetId) workspace = { ...workspace, assetId: current.assetId };
    }
    return aiScopeForWorkspace(workspace, sessionId, tabId);
  }, [activeWorkspace, sessionId, tabId, sessions]);
  const hasAiScope = Boolean(sessionId || (activeWorkspace?.kind === "db" && activeWorkspace.connId));
  const permScope = hasAiScope ? aiScope : undefined;
  const aiScopeAssetId = typeof aiScope.assetId === "string" ? aiScope.assetId : undefined;
  const aiScopeProduction = useAssetProductionClassification(aiScopeAssetId);
  const activeRef = useRef(active);
  activeRef.current = active;
  const mountedRef = useRef(false);
  const restoreGenerationRef = useRef(0);
  const isLifecycleActive = useCallback(
    () => mountedRef.current && currentAiBoardKey() === boardKey,
    [boardKey],
  );
  const [busy, setBusyState] = useState(false);
  const busyRef = useRef(false);
  const setBusy = useCallback((v: boolean) => {
    const run = activeRunRef.current;
    const cancellationPending = run !== null && run.cancellation !== "idle" && !run.settled;
    const next = !v && cancellationPending ? true : v;
    busyRef.current = next;
    if (mountedRef.current) setBusyState(next);
    setAiTabBusy(tabKey, next);
  }, [tabKey]);
  const bindings = useKeybindings();
  const reclaimKeyLabel = formatBinding(bindings.reclaimTakeover);
  const streamRef = useRef<ConversationStream | null>(null);
  if (!streamRef.current) streamRef.current = createConversationStream();
  const stream = streamRef.current;
  const subscribe = useCallback((onChange: () => void) => stream.subscribe(() => onChange()), [stream]);
  const getSnapshot = useCallback(() => stream.getState(), [stream]);
  const conv = useSyncExternalStore(subscribe, getSnapshot);

  // Tool-card handle tracking: the verified roster handle of each tool call
  // is captured from the actual toolCall/toolResult event stream (live,
  // replayed, and cross-restart resumed alike) instead of ever being
  // re-derived. The registry fails closed on conflicting evidence, and the
  // roster snapshot joins host/cwd/presence through real ai_roster_list data.
  const toolHandlesRef = useRef<ToolHandleRegistry | null>(null);
  if (toolHandlesRef.current === null) toolHandlesRef.current = new ToolHandleRegistry();
  const [toolHandleVersion, setToolHandleVersion] = useState(0);
  const lastRefreshedHandleVersionRef = useRef(0);
  const pushAgentEvent = useCallback(
    (generation: number, ev: Record<string, unknown>): ReturnType<typeof stream.pushEvent> => {
      if (toolHandlesRef.current?.record(generation, ev)) setToolHandleVersion((version) => version + 1);
      return stream.pushEvent(generation, ev);
    },
    [stream],
  );

  const [input, setInput] = useState(draft ?? "");
  const onDraftChangeRef = useRef(onDraftChange);
  onDraftChangeRef.current = onDraftChange;
  useEffect(() => {
    onDraftChangeRef.current(input);
  }, [input]);
  const [questionInput, setQuestionInput] = useState("");
  const [submittingCardId, setSubmittingCardId] = useState<string | null>(null);
  const submittingCardIdRef = useRef(submittingCardId);
  submittingCardIdRef.current = submittingCardId;
  const [syncNotice, setSyncNotice] = useState<{
    generation: number;
    phase: "syncing" | "synced" | "failed";
    count: number;
  } | null>(null);
  const runSequenceRef = useRef(0);
  const activeRunRef = useRef<AiRunSlot | null>(null);
  const [activeRun, setActiveRunState] = useState<AiRunSlot | null>(null);
  const activeGeneration = activeRun?.generation ?? null;
  const setActiveRun = useCallback((run: AiRunSlot) => {
    activeRunRef.current = run;
    if (mountedRef.current) setActiveRunState(run);
  }, []);
  const stopRef = useRef<(closeTab: boolean) => Promise<boolean>>(() => Promise.resolve(false));
  const runControl = useCallback(() => stopRef.current(true), []);
  const catchupChains = useRef(new Map<number, Promise<{ applied: number; failed: boolean }>>());
  const terminalPollRef = useRef<(generation: number, jobId: string) => Promise<void>>(async () => undefined);
  const terminalPollFailuresRef = useRef(0);
  const runChannelRef = useRef<{
    channel: IpcChannel<unknown>;
    dispose: () => void;
    generation: number;
  } | null>(null);
  const syncTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const resyncTimerRef = useRef<{ generation: number; timer: ReturnType<typeof setTimeout> } | null>(null);
  const restoredChannelRef = useRef<{
    channel: IpcChannel<unknown>;
    offReopen: () => void;
    jobId: string;
    generation: number;
    expiryTimer: ReturnType<typeof setTimeout> | null;
    expiryRetries: number;
  } | null>(null);
  const beginRun = (kind: "chat" | "takeover" = "chat"): AiRunSlot => {
    restoreGenerationRef.current += 1;
    const run = createAiRun(++runSequenceRef.current, kind);
    registerAiRunGeneration(tabKey, run.generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(run);
    stream.beginRun(run.generation, kind);
    return run;
  };
  const disposeRestoredChannel = useCallback(() => {
    const restored = restoredChannelRef.current;
    if (!restored) return;
    restoredChannelRef.current = null;
    if (restored.expiryTimer !== null) clearTimeout(restored.expiryTimer);
    restored.offReopen();
    disposeChannel(restored.channel);
  }, []);
  useEffect(() => {
    mountedRef.current = true;
    const catchupChainsMap = catchupChains.current;
    const requestSnapshots = requestSnapshotsRef.current;
    const cancelWaiters = cancelWaitersRef.current;
    return () => {
      mountedRef.current = false;
      restoreGenerationRef.current += 1;
      runSequenceRef.current += 1;
      runChannelRef.current?.dispose();
      disposeRestoredChannel();
      if (syncTimerRef.current !== null) {
        clearTimeout(syncTimerRef.current);
        syncTimerRef.current = null;
      }
      if (resyncTimerRef.current !== null) {
        clearTimeout(resyncTimerRef.current.timer);
        resyncTimerRef.current = null;
      }
      catchupChainsMap.clear();
      requestSnapshots.clear();
      for (const waiter of cancelWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      cancelWaiters.clear();
      cancelOperationRef.current = null;
      queueMicrotask(() => {
        if (!mountedRef.current) stream.dispose();
      });
    };
  }, [stream, disposeRestoredChannel]);
  const clearResyncTimer = useCallback((generation?: number) => {
    const pending = resyncTimerRef.current;
    if (!pending) return;
    if (generation !== undefined && pending.generation !== generation) return;
    clearTimeout(pending.timer);
    resyncTimerRef.current = null;
  }, []);
  const clearResyncState = useCallback((generation: number) => {
    clearResyncTimer(generation);
    setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
  }, [clearResyncTimer]);
  const markSyncing = useCallback((generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation)) return;
    if (syncTimerRef.current !== null) {
      clearTimeout(syncTimerRef.current);
      syncTimerRef.current = null;
    }
    setSyncNotice({ generation, phase: "syncing", count: 0 });
  }, []);
  const markSynced = useCallback((generation: number, count: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation)) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "synced", count } : prev,
    );
    if (syncTimerRef.current !== null) clearTimeout(syncTimerRef.current);
    syncTimerRef.current = setTimeout(() => {
      setSyncNotice((prev) => (prev && prev.generation === generation ? null : prev));
    }, AI_SYNC_NOTICE_DISMISS_MS);
  }, []);
  const markSyncFailed = useCallback((generation: number) => {
    const current = activeRunRef.current;
    if (!isCurrentAiRun(current, generation) || current.settled) return;
    setSyncNotice((prev) =>
      prev && prev.generation === generation ? { generation, phase: "failed", count: 0 } : prev,
    );
  }, []);
  const confirmCard = pendingInteraction(conv, activeGeneration, "confirm");
  const questionCard = pendingInteraction(conv, activeGeneration, "question");
  const confirmCardRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (confirmCard?.id) confirmCardRef.current?.focus({ preventScroll: true });
  }, [confirmCard?.id]);
  const productionNames = confirmCard?.requiredEnvironmentNames;
  // The confirm state is bound to the owning card id and honored only while
  // that id is the current card: a directly replaced card starts locked in
  // the same render, never relying on a passive effect for the safety reset.
  const [productionConfirm, setProductionConfirm] = useState<{
    cardId: string;
    canConfirm: boolean;
    submittedNames: string[];
  } | null>(null);
  const productionConfirmRef = useRef(productionConfirm);
  productionConfirmRef.current = productionConfirm;
  const handleProductionState = useCallback((canConfirm: boolean, submittedNames: string[]) => {
    const cardId = confirmCard?.id;
    if (!cardId) return;
    setProductionConfirm((current) =>
      current !== null &&
      current.cardId === cardId &&
      current.canConfirm === canConfirm &&
      current.submittedNames.join("\u0000") === submittedNames.join("\u0000")
        ? current
        : { cardId, canConfirm, submittedNames },
    );
  }, [confirmCard?.id]);
  const currentProductionConfirm =
    productionConfirm !== null && productionConfirm.cardId === confirmCard?.id
      ? productionConfirm
      : null;
  const productionBlocked = productionNames !== undefined && currentProductionConfirm?.canConfirm !== true;
  const hitlWaiting = confirmCard
    ? "等待你确认后继续…"
    : questionCard
      ? "等待你回答后继续…"
      : null;
  useEffect(() => {
    setQuestionInput("");
  }, [questionCard?.id]);
  const [planMode, setPlanMode] = useState(false);
  const [modelPanelOpen, setModelPanelOpen] = useState(false);
  const [conversationId, setConversationId] = useState<string | undefined>(boundConversationId);
  const [conversationError, setConversationError] = useState<string | null>(null);
  const conversationIdRef = useRef<string | undefined>(undefined);
  const restoreBindingRef = useRef<{ id: string; generation: number } | null>(null);
  const onConversationChangeRef = useRef(onConversationChange);
  onConversationChangeRef.current = onConversationChange;
  const updateConversationId = useCallback((id: string | undefined, title?: string) => {
    if (!isLifecycleActive()) return;
    if (conversationIdRef.current !== id) {
      // A real conversation switch invalidates the tool-handle tracking of
      // the previous conversation; replayed events of the new one re-record.
      toolHandlesRef.current?.clear();
      setToolHandleVersion(0);
      lastRefreshedHandleVersionRef.current = 0;
    }
    conversationIdRef.current = id;
    restoreBindingRef.current = id === undefined
      ? null
      : { id, generation: restoreGenerationRef.current };
    setConversationId(id);
    onConversationChangeRef.current(id, title);
  }, [isLifecycleActive]);
  const isRestoreActive = useCallback(
    (generation: number, id?: string) =>
      isLifecycleActive() &&
      restoreGenerationRef.current === generation &&
      (id === undefined || conversationIdRef.current === id),
    [isLifecycleActive],
  );
  const messageHistoryRef = useRef<{
    id: string;
    messages: MessageDto[];
    skipJobIds: Set<string>;
  } | null>(null);
  const messageWindowStartRef = useRef(0);
  const [messageWindowStart, setMessageWindowStart] = useState(0);
  const resetMessageHistory = useCallback(
    (id: string, messages: MessageDto[], runs: AiRunDto[]) => {
      const start = messageHistoryStart(messages.length, MESSAGE_HISTORY_PAGE_SIZE);
      const skipJobIds = replayableJobIds(runs);
      messageHistoryRef.current = { id, messages, skipJobIds };
      messageWindowStartRef.current = start;
      setMessageWindowStart(start);
      stream.reset(historyToItems(stream.getState(), messages.slice(start), skipJobIds));
    },
    [stream],
  );
  const clearMessageHistory = useCallback(() => {
    messageHistoryRef.current = null;
    messageWindowStartRef.current = 0;
    setMessageWindowStart(0);
  }, []);
  const loadOlderMessages = () => {
    const history = messageHistoryRef.current;
    if (!isLifecycleActive() || !history || history.id !== conversationIdRef.current) return;
    const next = earlierMessageHistoryStart(messageWindowStartRef.current, MESSAGE_HISTORY_PAGE_SIZE);
    if (next === messageWindowStartRef.current) return;
    messageWindowStartRef.current = next;
    setMessageWindowStart(next);
    stream.replaceHistory(
      historyToItems(stream.getState(), history.messages.slice(next), history.skipJobIds),
    );
  };
  const deletedConversationIdsRef = useRef<Set<string>>(new Set());
  const [perm, setPerm] = useState<AiPermissionConfig | null>(null);
  const [permError, setPermError] = useState<string | null>(null);
  const [permOpen, setPermOpen] = useState(false);
  const [cronOpen, setCronOpen] = useState(false);
  const [agentOpen, setAgentOpen] = useState(false);
  const [watchOpen, setWatchOpen] = useState(false);
  const [images, setImages] = useState<string[]>([]);
  const [refs, setRefs] = useState<RefChip[]>([]);
  const [atOpen, setAtOpen] = useState(false);
  // Typing "/" at the start of the message opens the command menu: built-in
  // commands first, then the user's enabled skills. Lists load when the menu
  // opens and reload the next time.
  const [skillOptions, setSkillOptions] = useState<{ name: string; description: string }[] | null>(null);
  const [modelOptions, setModelOptions] = useState<{ id: string; name: string; model: string; active: boolean }[] | null>(null);
  const [slashIndex, setSlashIndex] = useState(0);
  const [compacting, setCompacting] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [conversations, setConversations] = useState<{ id: string; title: string; updatedAt: number }[]>([]);
  const [runStatusByConv, setRunStatusByConv] = useState<Record<string, "running" | "interrupted">>({});
  const [historyStatus, setHistoryStatus] = useState<"loading" | "error" | "ready">("ready");
  const [historyError, setHistoryError] = useState<string | null>(null);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const follow = useConversationFollow(conv.items);
  const virtual = useVirtualWindow(conv.items.length);
  const { scrollRef: followScrollRef } = follow;
  const { scrollRef: virtualScrollRef, revealIndex: virtualRevealIndex, clearReveal: virtualClearReveal } = virtual;
  const setScrollEl = useCallback(
    (el: HTMLElement | null) => {
      followScrollRef(el);
      virtualScrollRef(el);
    },
    [followScrollRef, virtualScrollRef],
  );
  const [conversationRuns, setConversationRuns] = useState<AiRunDto[]>([]);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [matchCursor, setMatchCursor] = useState(-1);
  const searchMatches = useMemo(
    () => findItemMatches(conv.items, searchQuery),
    [conv.items, searchQuery],
  );
  const activeMatchIndex = matchCursor >= 0 ? (searchMatches[matchCursor] ?? -1) : -1;
  const jumpToMatch = (direction: 1 | -1) => {
    const next = stepMatch(matchCursor, searchMatches.length, direction);
    setMatchCursor(next);
    const target = searchMatches[next];
    if (target !== undefined) virtual.revealIndex(target);
  };
  useEffect(() => {
    const next = searchMatches.length > 0 ? 0 : -1;
    setMatchCursor(next);
    const target = next >= 0 ? searchMatches[next] : undefined;
    if (target !== undefined) virtualRevealIndex(target);
    else virtualClearReveal();
  }, [searchMatches, virtualRevealIndex, virtualClearReveal]);
  useEffect(() => {
    if (!searchOpen) virtualClearReveal();
  }, [searchOpen, virtualClearReveal]);

  const [permDeviceOverride, setPermDeviceOverride] = useState(false);
  const loadPermission = useCallback(async (): Promise<AiPermissionConfig | null> => {
    try {
      const effective = await aiApi.getPermission(permScope);
      if (permScope) {
        const global = await aiApi.getPermission();
        setPermDeviceOverride(global.mode !== effective.mode);
      } else {
        setPermDeviceOverride(false);
      }
      setPerm(effective);
      setPermError(null);
      return effective;
    } catch (e) {
      setPermError(describeError(e));
      return null;
    }
  }, [permScope]);

  useEffect(() => {
    void loadPermission();
  }, [loadPermission]);

  useEffect(() => {
    if (permOpen) void loadPermission();
  }, [permOpen, loadPermission]);

  useEffect(() => {
    if (!conversationId) {
      setCronOpen(false);
      setAgentOpen(false);
    }
  }, [conversationId]);

  const savePerm = async (next: AiPermissionConfig) => {
    const prev = perm;
    setPerm(next);
    try {
      await aiApi.setPermission(next, permScope);
      if (permScope) setPermDeviceOverride(true);
    } catch (e) {
      setPerm(prev);
      pushToast("error", `保存权限失败：${describeError(e)}`);
    }
  };

  const switchMode = (mode: AiPermissionConfig["mode"]) => {
    void (async () => {
      const latest = await loadPermission();
      if (latest) await savePerm({ ...latest, mode });
    })();
  };

  const followGlobalMode = () => {
    void (async () => {
      if (!permScope) return;
      const prev = perm;
      try {
        await aiApi.setPermission(prev ?? { mode: "read_write", dangerRules: [] }, permScope, true);
        setPermDeviceOverride(false);
        await loadPermission();
      } catch (e) {
        setPerm(prev);
        pushToast("error", `保存权限失败：${describeError(e)}`);
      }
    })();
  };

  const slashQuery = /^\/([a-z0-9-]*)$/.exec(input)?.[1] ?? null;
  const modelQuery = /^\/model\s+(.*)$/i.exec(input)?.[1] ?? null;
  useEffect(() => {
    if (slashQuery === null) {
      setSkillOptions(null);
      return;
    }
    if (skillOptions !== null) return;
    let alive = true;
    productionSkillsApi.list().then(
      (entries) => {
        if (alive) setSkillOptions(entries.filter((entry) => !entry.disabled).map(({ name, description }) => ({ name, description })));
      },
      () => {
        if (alive) setSkillOptions([]);
      },
    );
    return () => {
      alive = false;
    };
  }, [slashQuery, skillOptions]);
  useEffect(() => {
    if (modelQuery === null) {
      setModelOptions(null);
      return;
    }
    if (modelOptions !== null) return;
    let alive = true;
    modelApi.overview().then(
      (view) => {
        if (alive) setModelOptions(view.profiles.map((profile) => ({ id: profile.id, name: profile.name, model: profile.model, active: profile.id === view.activeId })));
      },
      () => {
        if (alive) setModelOptions([]);
      },
    );
    return () => {
      alive = false;
    };
  }, [modelQuery, modelOptions]);
  useEffect(() => {
    setSlashIndex(0);
  }, [slashQuery, modelQuery]);

  const refCandidates = useMemo<RefChip[]>(() => {
    const out: RefChip[] = [];
    const sess = useUi.getState().sessions.find((s) => s.id === sessionId);
    if (sess) {
      out.push({
        id: `asset:${sess.id}`,
        kind: "asset",
        label: sess.name,
        detail: `${sess.kind} 会话`,
        refId: sess.id,
      });
    }
    for (const w of workspaces) {
      for (const p of w.panes) {
        for (const t of p.tabs) {
          if (t.kind === "terminal" && t.tabId) {
            out.push({
              id: `tab:${t.id}`,
              kind: "tab",
              label: t.title,
              detail: `${w.title} 的终端`,
              refId: t.tabId,
            });
            continue;
          }
          if ((t.kind === "files" || t.kind === "log") && t.path && t.sessionId) {
            out.push({
              id: `file:${t.id}`,
              kind: "file",
              label: t.title,
              detail: t.path,
              refId: t.id,
              path: t.path,
              sessionId: t.sessionId,
            });
          }
        }
      }
    }
    return out;
  }, [sessionId, workspaces]);

  const composeMessage = (text: string, refList: RefChip[]): string => {
    if (refList.length === 0) return text;
    const list = refList.map((r) => `- ${r.label}（${r.detail}）`).join("\n");
    return `[引用对象]\n${list}\n\n${text}`;
  };

  const replayHitl = useCallback(async (generation: number, jobId: string) => {
    const canApply = () =>
      isLifecycleActive() && isCurrentAiRun(activeRunRef.current, generation);
    const plan = stream.planHitlReplay(generation);
    let events: AiHitlEventDto[] = [];
    try {
      events = await aiApi.hitlEvents(jobId, plan.afterSeq);
    } catch {
      events = [];
    }
    if (!canApply()) return;
    try {
      const snapshot = await aiApi.hitlSnapshot(jobId);
      if (canApply()) stream.applyHitlReplay(generation, plan, events, snapshot);
    } catch {
      if (canApply() && events.length > 0) stream.applyHitlReplay(generation, plan, events, null);
    }
  }, [stream, isLifecycleActive]);

  const cancelOperationRef = useRef<{
    generation: number;
    jobId: string | null;
    cancelSent: boolean;
  } | null>(null);
  const cancelWaitersRef = useRef(
    new Set<{
      generation: number;
      closeTab: boolean;
      resolve: (confirmed: boolean) => void;
      timer: ReturnType<typeof setTimeout>;
    }>(),
  );
  const hasCloseWaiter = (generation: number) =>
    [...cancelWaitersRef.current].some((waiter) => waiter.generation === generation && waiter.closeTab);

  const confirmCancellation = useCallback((generation: number) => {
    if (cancelOperationRef.current?.generation === generation) cancelOperationRef.current = null;
    for (const waiter of [...cancelWaitersRef.current]) {
      if (waiter.generation !== generation) continue;
      clearTimeout(waiter.timer);
      cancelWaitersRef.current.delete(waiter);
      waiter.resolve(true);
    }
  }, []);

  const unconfirmCancellation = useCallback((generation: number, error?: unknown) => {
    const current = activeRunRef.current;
    if (isCurrentAiRun(current, generation) && current.cancellation !== "idle") {
      setActiveRun(markAiRunCancellationUnconfirmed(current));
    }
    let resolved = 0;
    for (const waiter of [...cancelWaitersRef.current]) {
      if (waiter.generation !== generation) continue;
      clearTimeout(waiter.timer);
      cancelWaitersRef.current.delete(waiter);
      waiter.resolve(false);
      resolved += 1;
    }
    if (resolved > 0) {
      pushToast(
        "error",
        error === undefined
          ? "停止未确认：等待任务结束超时，可重试或查看任务"
          : `停止未确认：${describeError(error)}`,
      );
    }
  }, [setActiveRun, pushToast]);

  const confirmCancellationFromQuery = async (
    operation: { generation: number; jobId: string | null },
  ): Promise<boolean> => {
    const conversationId = conversationIdRef.current;
    if (!conversationId || !operation.jobId) return false;
    try {
      const runs = await aiApi.runs(conversationId);
      if (cancelOperationRef.current?.generation !== operation.generation) return false;
      const terminalRun = runs.find((run) => run.id === operation.jobId && isRunTerminal(run));
      if (!terminalRun) return false;
      if (!hasCloseWaiter(operation.generation)) {
        await catchUpRunEvents(operation.generation, operation.jobId);
        const hasOutcome = stream.getState().attempts.some(
          (attempt) => attempt.generation === operation.generation && attempt.outcome,
        );
        if (!hasOutcome) {
          const terminalEvent = terminalEventOf(terminalRun);
          if (terminalEvent) {
            pushAgentEvent(operation.generation, terminalEvent);
            stream.flush();
          }
        }
      }
      settleRestoredRun(operation.generation);
      return true;
    } catch {
      return false;
    }
  };

  const driveCancellation = async () => {
    const operation = cancelOperationRef.current;
    const current = activeRunRef.current;
    if (!operation || !isCurrentAiRun(current, operation.generation)) return;
    if (current.settled && current.cancellation === "idle") {
      confirmCancellation(operation.generation);
      return;
    }
    if (current.spawnPending || !current.jobId) return;
    operation.jobId = current.jobId;
    if (!operation.cancelSent) {
      operation.cancelSent = true;
      try {
        if (current.kind === "takeover") await aiApi.takeoverCancel(current.jobId);
        else await aiApi.cancel(current.jobId);
      } catch (error) {
        if (cancelOperationRef.current !== operation) return;
        operation.cancelSent = false;
        if (await confirmCancellationFromQuery(operation)) return;
        if (cancelOperationRef.current !== operation) return;
        unconfirmCancellation(operation.generation, error);
        return;
      }
      if (cancelOperationRef.current !== operation) return;
      for (const item of stream.getState().items) {
        if (item.attempt === operation.generation && item.role === "user" && item.steer === "pending") {
          stream.resolveSteer(operation.generation, item.id, "dropped");
        }
      }
      pushToast("info", current.kind === "takeover" ? "已请求停止接管，等待终端退出" : "已请求停止，等待任务结束");
    }
    if (!isLifecycleActive()) {
      const deadline = Date.now() + 4500;
      while (cancelOperationRef.current === operation) {
        if (await confirmCancellationFromQuery(operation)) return;
        const remaining = deadline - Date.now();
        if (remaining <= 0) return;
        await new Promise<void>((resolve) => setTimeout(resolve, Math.min(100, remaining)));
      }
      return;
    }
    await confirmCancellationFromQuery(operation);
  };

  const requestCancellation = (closeTab: boolean): Promise<boolean> => {
    const current = activeRunRef.current;
    if (!current || (current.settled && current.cancellation === "idle")) return Promise.resolve(true);
    const retry = current.cancellation === "unconfirmed";
    setActiveRun(requestAiRunCancellation(current));
    if (cancelOperationRef.current?.generation !== current.generation) {
      cancelOperationRef.current = {
        generation: current.generation,
        jobId: null,
        cancelSent: false,
      };
    } else if (retry) {
      cancelOperationRef.current.cancelSent = false;
    }
    const generation = current.generation;
    const promise = new Promise<boolean>((resolve) => {
      const waiter = {
        generation,
        closeTab,
        resolve,
        timer: setTimeout(() => {
          if (!cancelWaitersRef.current.delete(waiter)) return;
          const latest = activeRunRef.current;
          if (isCurrentAiRun(latest, generation) && latest.cancellation !== "idle") {
            setActiveRun(markAiRunCancellationUnconfirmed(latest));
          }
          pushToast("error", "停止未确认：等待任务结束超时，可重试或查看任务");
          resolve(false);
        }, 5000),
      };
      cancelWaitersRef.current.add(waiter);
    });
    void driveCancellation();
    return promise;
  };

  const settleRestoredRun = useCallback((generation: number, confirmed = true) => {
    const current = activeRunRef.current;
    if (!confirmed && isCurrentAiRun(current, generation) && current.cancellation !== "idle") return;
    if (isCurrentAiRun(current, generation)) {
      const next = confirmed ? confirmAiRunTerminal(current) : settleAiRun(current);
      setActiveRun(next);
      if (!current.spawnPending) setBusy(false);
    }
    const live = runChannelRef.current;
    if (live && live.generation === generation) live.dispose();
    const restored = restoredChannelRef.current;
    if (restored && restored.generation === generation) disposeRestoredChannel();
    clearResyncState(generation);
    if (confirmed) {
      confirmCancellation(generation);
      if (activeRunRef.current?.generation === generation) clearAiTabRun(tabKey);
    }
  }, [setActiveRun, setBusy, disposeRestoredChannel, clearResyncState, confirmCancellation, tabKey]);

  // maybeOpenAiCreatedTerminal opens a terminal_create foreground tab in the
  // workspace, the same recipe as the background-sessions takeover. It fires
  // only for events of the live run; historical replays never re-open old tabs.
  const maybeOpenAiCreatedTerminal = useCallback((generation: number, ev: Record<string, unknown>, result: ApplyResult) => {
    const current = activeRunRef.current;
    if (!isLifecycleActive() || !isCurrentAiRun(current, generation) || current.settled) return;
    for (const candidate of [{ ev, accepted: result.accepted }, ...(result.drained ?? [])]) {
      if (!candidate.accepted) continue;
      const terminal = aiCreatedTerminalOf(candidate.ev);
      if (!terminal) continue;
      if (!isLifecycleActive()) return;
      const st = useUi.getState();
      const session = st.sessions.find((s) => s.id === terminal.sessionId);
      const activate = activeRef.current && st.rightOpen;
      const previousActiveWorkspaceId = st.activeWorkspaceId;
      const previousWorkspaces = st.workspaces;
      st.addTab({
        id: nextTabId(`term-${terminal.sessionId}`),
        kind: "terminal",
        title: terminal.title || session?.name || terminal.sessionId,
        renamedByUser: terminal.title ? true : undefined,
        sessionId: terminal.sessionId,
        tabId: terminal.tabId,
        closable: true,
      });
      if (activate) continue;
      const previousById = new Map(previousWorkspaces.map((workspace) => [workspace.id, workspace]));
      useUi.setState((state) => ({
        activeWorkspaceId: previousActiveWorkspaceId,
        workspaces: state.workspaces.map((workspace) => {
          const previous = previousById.get(workspace.id);
          if (!previous) return workspace;
          return {
            ...workspace,
            activePaneId: previous.activePaneId,
            panes: workspace.panes.map((pane) => {
              const previousPane = previous.panes.find((candidatePane) => candidatePane.id === pane.id);
              return previousPane ? { ...pane, activeTabId: previousPane.activeTabId } : pane;
            }),
          };
        }),
      }));
    }
  }, [isLifecycleActive]);

  const replayRunEvents = useCallback(async (
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    let applied = 0;
    const { failed } = await replayRunEventsAfter(
      (afterSeq, limit) => aiApi.runEvents(jobId, afterSeq, limit),
      stream.lastSeq(generation),
      (events) => {
        for (const event of events) {
          if (!isLifecycleActive()) return;
          const result = pushAgentEvent(generation, event as Record<string, unknown>);
          if (result.accepted) applied += 1;
          maybeOpenAiCreatedTerminal(generation, event as Record<string, unknown>, result);
          if (result.terminal) settleRestoredRun(generation);
        }
        if (isLifecycleActive()) stream.flush();
      },
      { shouldContinue: isLifecycleActive },
    );
    return { applied, failed };
  }, [stream, isLifecycleActive, maybeOpenAiCreatedTerminal, settleRestoredRun, pushAgentEvent]);

  const catchUpRunEvents = useCallback((
    generation: number,
    jobId: string,
  ): Promise<{ applied: number; failed: boolean }> => {
    const chains = catchupChains.current;
    const prev = chains.get(generation) ?? Promise.resolve({ applied: 0, failed: false });
    const next = prev
      .catch(() => ({ applied: 0, failed: true }))
      .then(() => replayRunEvents(generation, jobId));
    chains.set(generation, next);
    const cleanup = () => {
      if (chains.get(generation) === next) chains.delete(generation);
    };
    void next.then(cleanup, cleanup);
    return next;
  }, [replayRunEvents]);

  const resyncRun = useCallback((generation: number, jobId: string, attempt = 0) => {
    clearResyncTimer(generation);
    markSyncing(generation);
    void catchUpRunEvents(generation, jobId).then((result) => {
      if (!isLifecycleActive()) return;
      if (!result.failed) {
        markSynced(generation, result.applied);
        return;
      }
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      if (attempt >= AI_RESYNC_MAX_ATTEMPTS) {
        markSyncFailed(generation);
        if (current.cancellation === "idle") {
          setActiveRun(settleAiRun(current));
          setBusy(false);
        }
        return;
      }
      clearResyncTimer(generation);
      resyncTimerRef.current = {
        generation,
        timer: setTimeout(
          () => resyncRun(generation, jobId, attempt + 1),
          AI_RESYNC_BACKOFF_MS * (attempt + 1),
        ),
      };
    });
    void replayHitl(generation, jobId);
  }, [clearResyncTimer, markSyncing, catchUpRunEvents, isLifecycleActive, markSynced, markSyncFailed, setActiveRun, setBusy, replayHitl]);

  terminalPollRef.current = async (generation, jobId) => {
    const current = activeRunRef.current;
    const id = conversationIdRef.current;
    if (!isLifecycleActive() || !isCurrentAiRun(current, generation) || current.settled || current.jobId !== jobId || !id) return;
    try {
      const run = await aiApi.run(jobId);
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, generation)) return;
      terminalPollFailuresRef.current = 0;
      if (run.conversationId !== id || !isRunTerminal(run)) return;
      await catchUpRunEvents(generation, jobId);
      const latest = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(latest, generation) || latest.settled || latest.jobId !== jobId) return;
      const hasOutcome = stream.getState().attempts.some(
        (attempt) => attempt.generation === generation && attempt.outcome,
      );
      if (!hasOutcome) {
        const event = terminalEventOf(run);
        if (event) {
          pushAgentEvent(generation, event);
          stream.flush();
        }
      }
      settleRestoredRun(generation);
    } catch {
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, generation)) return;
      terminalPollFailuresRef.current += 1;
      if (terminalPollFailuresRef.current === 3) {
        pushToast("error", "AI 任务状态检查失败，正在重试；可刷新会话确认结果");
      }
    }
  };

  useEffect(() => {
    const generation = activeRun?.generation;
    const jobId = activeRun?.jobId;
    if (generation === undefined || !jobId || activeRun?.settled) return;
    terminalPollFailuresRef.current = 0;
    let inFlight = false;
    const timer = window.setInterval(() => {
      if (inFlight) return;
      inFlight = true;
      void terminalPollRef.current(generation, jobId).finally(() => { inFlight = false; });
    }, 5000);
    return () => window.clearInterval(timer);
  }, [activeRun?.generation, activeRun?.jobId, activeRun?.settled]);

  const replayRestoredIfBound = useCallback((jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId) return;
    void replayRunEvents(restored.generation, jobId);
  }, [replayRunEvents]);

  const armExpiryWatch = useCallback((jobId: string) => {
    const restored = restoredChannelRef.current;
    if (!restored || restored.jobId !== jobId) return;
    if (restored.expiryRetries <= 0) {
      settleRestoredRun(restored.generation, false);
      return;
    }
    restored.expiryRetries -= 1;
    restored.expiryTimer = setTimeout(() => {
      const current = restoredChannelRef.current;
      if (!current || current.jobId !== jobId) return;
      current.expiryTimer = null;
      void replayRunEvents(current.generation, jobId).finally(() => {
        const settled = activeRunRef.current?.settled ?? true;
        if (!settled) armExpiryWatch(jobId);
      });
    }, 3000);
  }, [settleRestoredRun, replayRunEvents]);

  const scheduleExpiryWatch = useCallback((snapshot: AiHitlSnapshotDto) => {
    const deadline = pendingDeadline(snapshot);
    if (deadline === null) return null;
    const delay = Math.min(
      Math.max(0, deadline - Date.now()) + AI_HITL_EXPIRY_GRACE_MS,
      2_000_000_000,
    );
    return setTimeout(() => {
      const restored = restoredChannelRef.current;
      if (!restored) return;
      void replayRunEvents(restored.generation, restored.jobId).finally(() => {
        const settled = activeRunRef.current?.settled ?? true;
        if (!settled) armExpiryWatch(restored.jobId);
      });
    }, delay);
  }, [replayRunEvents, armExpiryWatch]);

  const restoreConversationRuns = useCallback(async (
    id: string,
    restoreGeneration: number,
    preloaded?: AiRunDto[],
  ) => {
    let runs: AiRunDto[];
    if (preloaded) {
      runs = preloaded.slice(0, RUN_RESTORE_LIMIT);
    } else {
      try {
        runs = (await aiApi.runs(id, RUN_RESTORE_LIMIT)).slice(0, RUN_RESTORE_LIMIT);
      } catch {
        return;
      }
    }
    if (!isRestoreActive(restoreGeneration, id)) return;
    setConversationRuns(runs);
    if (runs.length === 0) return;
    const replay = replayableRuns(runs);
    const generations = new Map<string, number>();
    for (const run of replay) {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const generation = ++runSequenceRef.current;
      generations.set(run.id, generation);
      stream.beginRun(generation, "chat");
      stream.bindJob(generation, run.id);
      await replayRunEventsAfter(
        (afterSeq, limit) => aiApi.runEvents(run.id, afterSeq, limit),
        0,
        (events) => {
          for (const event of events) {
            if (!isRestoreActive(restoreGeneration, id)) return;
            pushAgentEvent(generation, event as Record<string, unknown>);
          }
          if (isRestoreActive(restoreGeneration, id)) stream.flush();
        },
        { shouldContinue: () => isRestoreActive(restoreGeneration, id) },
      );
      if (!isRestoreActive(restoreGeneration, id)) return;
    }
    const resumable = await findResumableRun(
      runs,
      async (jobId) => {
        try {
          return await aiApi.hitlSnapshot(jobId);
        } catch {
          return null;
        }
      },
      () => isRestoreActive(restoreGeneration, id),
    );
    if (!resumable || !isRestoreActive(restoreGeneration, id)) return;
    const generation = generations.get(resumable.run.id);
    if (generation === undefined) return;
    if (activeRunRef.current && !activeRunRef.current.settled) return;
    const channel = createAiChannel((ev) => {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") {
          const restored = restoredChannelRef.current;
          if (restored && restored.generation === generation) disposeRestoredChannel();
          clearResyncState(generation);
        }
        return;
      }
      const result = pushAgentEvent(generation, ev);
      if (result.gap) resyncRun(generation, resumable.run.id);
      maybeOpenAiCreatedTerminal(generation, ev, result);
      if (!result.terminal) return;
      settleRestoredRun(generation);
      if (type === "error" && result.accepted) pushToast("error", `AI: ${ev.message as string}`);
    });
    const offReopen = onChannelReopen(channel, () => {
      if (!isRestoreActive(restoreGeneration, id)) return;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, generation) || current.settled) return;
      resyncRun(generation, resumable.run.id);
    });
    disposeRestoredChannel();
    restoredChannelRef.current = {
      channel,
      offReopen,
      jobId: resumable.run.id,
      generation,
      expiryTimer: scheduleExpiryWatch(resumable.snapshot),
      expiryRetries: 8,
    };
    registerAiRunGeneration(tabKey, generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(bindAiRunJob(createAiRun(generation), resumable.run.id));
    setBusy(true);
    void replayHitl(generation, resumable.run.id);
  }, [
    isRestoreActive,
    stream,
    disposeRestoredChannel,
    clearResyncState,
    resyncRun,
    maybeOpenAiCreatedTerminal,
    settleRestoredRun,
    scheduleExpiryWatch,
    setActiveRun,
    setBusy,
    replayHitl,
    runControl,
    pushToast,
    tabKey,
    pushAgentEvent,
  ]);

  const restoreConversation = useCallback(
    async (id: string) => {
      if (!isLifecycleActive()) return;
      const restoreGeneration = ++restoreGenerationRef.current;
      const startSeq = runSequenceRef.current;
      updateConversationId(id);
      try {
        const list = await aiApi.conversationList();
        if (!isRestoreActive(restoreGeneration, id)) return;
        const hit = list.find((c) => c.id === id);
        if (!hit) {
          updateConversationId(undefined);
          clearMessageHistory();
          stream.reset();
          setConversationError(null);
          return;
        }
        if (hit.title) updateConversationId(id, hit.title);
      } catch {
        if (!isRestoreActive(restoreGeneration, id)) return;
      }
      let msgs: MessageDto[];
      let runs: AiRunDto[];
      try {
        [msgs, runs] = await Promise.all([
          aiApi.messages(id),
          aiApi
            .runs(id, RUN_RESTORE_LIMIT)
            .then((rows) => rows.slice(0, RUN_RESTORE_LIMIT))
            .catch((): AiRunDto[] => []),
        ]);
      } catch (e) {
        if (!isRestoreActive(restoreGeneration, id)) return;
        setConversationError(describeError(e));
        return;
      }
      if (!isRestoreActive(restoreGeneration, id) || runSequenceRef.current !== startSeq) return;
      setConversationError(null);
      resetMessageHistory(id, msgs, runs);
      if (!isRestoreActive(restoreGeneration, id) || runSequenceRef.current !== startSeq) return;
      void restoreConversationRuns(id, restoreGeneration, runs);
    },
    [clearMessageHistory, isLifecycleActive, isRestoreActive, resetMessageHistory, restoreConversationRuns, stream, updateConversationId],
  );

  // Conversation restore driven by the external binding (persisted board); proactive in-tab switches already synced the ref and do not restore again.
  useEffect(() => {
    if (boundConversationId === undefined) {
      if (conversationIdRef.current !== undefined) {
        restoreGenerationRef.current += 1;
        conversationIdRef.current = undefined;
        setConversationId(undefined);
        clearMessageHistory();
        stream.reset();
      }
      return;
    }
    const binding = restoreBindingRef.current;
    if (
      conversationIdRef.current === boundConversationId &&
      binding?.id === boundConversationId &&
      binding.generation === restoreGenerationRef.current
    ) {
      return;
    }
    void restoreConversation(boundConversationId);
  }, [boundConversationId, clearMessageHistory, restoreConversation, stream]);

  const hydrateMessageIds = async (convId: string) => {
    try {
      const msgs = await aiApi.messages(convId);
      if (!isLifecycleActive() || conversationIdRef.current !== convId) return;
      stream.hydrateUserMessageIds(msgs);
    } catch {
      return;
    }
  };

  const requestSnapshotsRef = useRef(
    new Map<number, { message: string; images: string[]; refs: RefChip[]; planMode: boolean }>(),
  );
  const send = async (override?: {
    message?: string;
    images?: string[];
    refs?: RefChip[];
    planMode?: boolean;
  }) => {
    const message = (override?.message ?? input).trim();
    const sentImages = override?.images ?? images;
    const sentRefs = override?.refs ?? refs;
    const usePlan = override?.planMode ?? planMode;
    if (
      (!message && sentImages.length === 0) ||
      busyRef.current ||
      aiTabBusyIds.has(tabKey) ||
      aiRunBlocksStart(activeRunRef.current)
    ) {
      return;
    }
    if (permOpen) setPermOpen(false);
    const run = beginRun();
    requestSnapshotsRef.current.set(run.generation, {
      message,
      images: [...sentImages],
      refs: [...sentRefs],
      planMode: usePlan,
    });
    setBusy(true);
    setInput("");
    setRefs([]);
    setAtOpen(false);
    setImages([]);
    stream.appendUser(run.generation, message, sentImages.length);

    let channelDisposed = false;
    const channel = createAiChannel((ev) => {
      if (!isLifecycleActive()) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") {
          clearResyncState(run.generation);
          dispose();
        }
        return;
      }
      const result = pushAgentEvent(run.generation, ev);
      if (result.gap && current.jobId) resyncRun(run.generation, current.jobId);
      maybeOpenAiCreatedTerminal(run.generation, ev, result);
      if (!result.terminal) return;
      settleRestoredRun(run.generation);
      if (type === "error" && result.accepted) pushToast("error", `AI: ${ev.message as string}`);
    });

    const offHitlReopen = onChannelReopen(channel, () => {
      if (!isLifecycleActive()) return;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || current.settled || !current.jobId) return;
      resyncRun(run.generation, current.jobId);
    });
    const dispose = () => {
      if (channelDisposed) return;
      channelDisposed = true;
      if (runChannelRef.current?.channel === channel) runChannelRef.current = null;
      offHitlReopen();
      disposeChannel(channel);
    };
    runChannelRef.current = { channel, dispose, generation: run.generation };

    try {
      const payloadRefs = sentRefs.map((r) => ({
        kind: r.kind,
        id: r.refId,
        label: r.label,
        path: r.path,
        sessionId: r.sessionId,
      }));
      const res = await aiApi.chat({
        conversationId,
        scope: aiScope,
        message: composeMessage(message, sentRefs),
        images: sentImages.length ? sentImages : undefined,
        refs: payloadRefs.length ? payloadRefs : undefined,
        planMode: usePlan || undefined,
        channel,
      });
      const current = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(current, run.generation)) {
        dispose();
        return;
      }
      if (!deletedConversationIdsRef.current.has(res.conversationId)) {
        updateConversationId(res.conversationId);
      }
      if (current.settled && current.cancellation === "idle") {
        setActiveRun(finishAiRunSpawn(current));
        setBusy(false);
        return;
      }
      setActiveRun(bindAiRunJob(current, res.jobId));
      stream.bindJob(run.generation, res.jobId);
      void driveCancellation();
      if (stream.hasGap(run.generation)) resyncRun(run.generation, res.jobId);
      void hydrateMessageIds(res.conversationId);
    } catch (e) {
      const current = activeRunRef.current;
      if (isLifecycleActive() && isCurrentAiRun(current, run.generation)) {
        const result = pushAgentEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        setActiveRun(settleAiRun(finishAiRunSpawn(current)));
        setBusy(false);
        if (current.cancellation !== "idle") unconfirmCancellation(run.generation, e);
        clearResyncState(run.generation);
      }
      dispose();
    }
  };

  const steer = async () => {
    const message = input.trim();
    if (!message) return;
    const run = activeRunRef.current;
    if (!run || run.settled) {
      pushToast("info", "当前任务已结束，请将补充指令作为新消息发送。");
      return;
    }
    if (run.spawnPending) {
      pushToast("info", "当前任务正在启动，请稍后重试。");
      return;
    }
    if (images.length > 0) {
      pushToast("info", "运行中的补充指令暂不支持图片，请先移除图片");
      return;
    }
    const target = run;
    const jobId = run.jobId;
    if (run.kind !== "chat" || !jobId) {
      pushToast("info", "终端接管这一轮不支持补充指令");
      return;
    }
    if (permOpen) setPermOpen(false);
    const text = composeMessage(message, refs);
    setInput("");
    const itemId = stream.appendSteer(target.generation, message);
    try {
      await aiApi.steer(jobId, text);
    } catch (e) {
      stream.resolveSteer(target.generation, itemId, "dropped");
      setInput((prev) => (prev ? `${message}\n${prev}` : message));
      pushToast("error", `补充指令未送达：${describeError(e)}`);
    }
  };

  const approvePlan = () => {
    if (latestPlanApproved(conv.items)) {
      pushToast("info", "该计划已执行，不会重复执行；需要调整时请让 AI 重新生成方案");
      return;
    }
    setPlanMode(false);
    void send({ message: PLAN_EXECUTE_MESSAGE, images: [], refs: [], planMode: false });
  };
  const approvePlanRef = useRef(approvePlan);
  approvePlanRef.current = approvePlan;
  const handleApprovePlan = useCallback(() => approvePlanRef.current(), []);

  const retryRun = (item: ChatItem) => {
    if (item.role !== "outcome" || item.outcome !== "error" || !item.retryable) return;
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "另一轮仍在运行，请先停止再重试");
      return;
    }
    const source = [...conv.items]
      .reverse()
      .find(
        (candidate): candidate is Extract<ChatItem, { role: "user" }> =>
          candidate.attempt === item.attempt && candidate.role === "user" && candidate.steer === undefined,
      );
    if (!source) {
      pushToast("info", "找不到这一轮的消息原文，请手动重新发送");
      return;
    }
    if (source.text.trim() === PLAN_EXECUTE_MESSAGE) {
      pushToast("info", "该计划已执行，不能通过重试再次执行；需要调整时请让 AI 重新生成方案");
      return;
    }
    const snapshot = item.attempt === null ? undefined : requestSnapshotsRef.current.get(item.attempt);
    if (snapshot) {
      void send(snapshot);
      return;
    }
    const jobId = item.attempt === null
      ? undefined
      : conv.attempts.find((attempt) => attempt.generation === item.attempt)?.jobId;
    const planMode = conversationRuns.find((run) => run.id === jobId)?.planMode ?? false;
    void send({ message: source.text, images: [], refs: [], planMode });
  };
  const retryRunRef = useRef(retryRun);
  retryRunRef.current = retryRun;
  const handleRetryRun = useCallback((item: ChatItem) => retryRunRef.current(item), []);

  const [editing, setEditing] = useState<{ itemId: string; messageId: string } | null>(null);
  const [editSubmitting, setEditSubmitting] = useState(false);
  const editSubmittingRef = useRef(false);
  const startEditResend = (item: ChatItem) => {
    if (item.role !== "user" || !item.messageId) return;
    setEditing({ itemId: item.id, messageId: item.messageId });
    setInput(item.text);
    setAtOpen(false);
    inputRef.current?.focus();
  };
  const startEditResendRef = useRef(startEditResend);
  startEditResendRef.current = startEditResend;
  const handleEditResend = useCallback((item: ChatItem) => startEditResendRef.current(item), []);

  const submitEditResend = async () => {
    const edit = editing;
    const text = input.trim();
    const conversationId = conversationIdRef.current;
    if (!edit || !text || editSubmittingRef.current) return;
    if (!conversationId) {
      pushToast("error", "编辑并重新发送需要先打开一个会话");
      return;
    }
    editSubmittingRef.current = true;
    setEditSubmitting(true);
    try {
      await aiApi.editResend(conversationId, edit.messageId);
    } catch (e) {
      editSubmittingRef.current = false;
      setEditSubmitting(false);
      pushToast("error", `编辑并重新发送失败：${describeError(e)}`);
      return;
    }
    try {
      const current = activeRunRef.current;
      if (current && !current.settled) {
        setActiveRun(settleAiRun(current));
        stream.cancelRun(current.generation, false);
        clearResyncState(current.generation);
      }
      setBusy(false);
      const live = runChannelRef.current;
      if (live) live.dispose();
      disposeRestoredChannel();
      stream.truncateAfterItem(edit.itemId);
      setEditing(null);
      await send({ message: text });
    } finally {
      editSubmittingRef.current = false;
      setEditSubmitting(false);
    }
  };

  // One formal confirmation path shared by the card buttons and the agent
  // approval delegate: the busy guard, takeover/restored-channel routing,
  // failure replay/resync and interaction resolution are never a reduced
  // copy. Returns whether the decision reached the backend.
  const submitConfirmCard = useCallback(
    async (
      card: ConfirmItem,
      decision: "allow" | "allow_session" | "allow_persistent" | "deny",
      environmentNames?: string[],
    ): Promise<boolean> => {
      const run = activeRunRef.current;
      const id = run?.jobId;
      if (!run || !id) {
        pushToast("info", "当前任务正在启动，请稍后重试。");
        return false;
      }
      if (submittingCardIdRef.current) return false;
      setSubmittingCardId(card.id);
      try {
        const input = confirmationInput(
          { jobId: id, callId: card.callId, nonce: card.nonce },
          decision,
          environmentNames,
        );
        const channel = card.jobId === restoredChannelRef.current?.jobId ? restoredChannelRef.current.channel : undefined;
        if (run.kind === "takeover") {
          await aiApi.takeoverConfirm(input);
        } else if (channel) {
          await aiApi.confirm(input, channel);
        } else {
          await aiApi.confirm(input);
        }
      } catch (e) {
        pushToast("error", `确认失败：${describeError(e)}`);
        if (run.kind === "chat" && isCurrentAiRun(activeRunRef.current, run.generation)) {
          void replayHitl(run.generation, id);
        }
        replayRestoredIfBound(id);
        return false;
      } finally {
        setSubmittingCardId(null);
      }
      if (isCurrentAiRun(activeRunRef.current, run.generation)) {
        stream.resolveInteraction(run.generation, card.id, card.nonce, CONFIRM_RESOLUTION[decision]);
      }
      return true;
    },
    [stream, pushToast, replayHitl, replayRestoredIfBound],
  );

  const confirm = async (decision: "allow" | "allow_session" | "allow_persistent" | "deny") => {
    const run = activeRunRef.current;
    const card = confirmCard;
    const id = run?.jobId;
    if (!run || !id || !card) {
      pushToast("info", "当前任务正在启动，请稍后重试。");
      return;
    }
    if (submittingCardId) return;
    // A production challenge is only answered with the user's actual typed
    // environment names, and only with the state bound to this exact card;
    // anything less never reaches the backend.
    if (decision !== "deny" && card.requiredEnvironmentNames) {
      if (currentProductionConfirm === null || !currentProductionConfirm.canConfirm) return;
    }
    await submitConfirmCard(
      card,
      decision,
      decision !== "deny" && card.requiredEnvironmentNames && currentProductionConfirm ? currentProductionConfirm.submittedNames : undefined,
    );
  };

  const answer = async (option?: string) => {
    const run = activeRunRef.current;
    const card = questionCard;
    const id = run?.jobId;
    const text = option ?? questionInput;
    if (!run || !id || !card) {
      pushToast("info", "当前任务正在启动，请稍后重试。");
      return;
    }
    if (!text.trim()) {
      pushToast("info", "请输入回答或选择选项。");
      return;
    }
    if (submittingCardId) return;
    setSubmittingCardId(card.id);
    try {
      const input = answerInput({ jobId: id, callId: card.callId, nonce: card.nonce }, text);
      const channel = card.jobId === restoredChannelRef.current?.jobId ? restoredChannelRef.current.channel : undefined;
      if (channel) {
        await aiApi.answer(input, channel);
      } else {
        await aiApi.answer(input);
      }
    } catch (e) {
      pushToast("error", `回答失败：${describeError(e)}`);
      if (run.kind === "chat" && isCurrentAiRun(activeRunRef.current, run.generation)) {
        void replayHitl(run.generation, id);
      }
      replayRestoredIfBound(id);
      return;
    } finally {
      setSubmittingCardId(null);
    }
    if (isCurrentAiRun(activeRunRef.current, run.generation)) {
      stream.resolveInteraction(run.generation, card.id, card.nonce, `已回答：${text}`);
    }
  };

  // Agent production wiring: the reviewed roster/history panels consume the
  // real generated IPC through the adapter (no unsafeCall), and the approval
  // delegate maps reviewed decisions onto the pending HITL interaction.
  const authUser = useAuth((s) => s.user);
  const agentOwnerId = authUser?.id ?? "";
  const agentOwnerName = authUser?.display_name || authUser?.username || "";
  const resolveAgentApproval = useCallback(
    async (requestId: string, decision: AgentApprovalDecision) => {
      const run = activeRunRef.current;
      const card = pendingInteraction(stream.getState(), run?.generation ?? null, "confirm");
      if (!run?.jobId || !card || card.requestId !== requestId) {
        throw { code: "conflict", message: "审批请求不存在或已解决，请稍后刷新重试" };
      }
      // The same production challenge gate as the card buttons: only the
      // user's actual typed names, bound to this exact card, reach the
      // backend — never an auto-echoed or missing answer.
      let environmentNames: string[] | undefined;
      if (decision !== "deny" && card.requiredEnvironmentNames) {
        const challenge = productionConfirmRef.current;
        if (!challenge || challenge.cardId !== card.id || !challenge.canConfirm) {
          throw { code: "conflict", message: "生产环境挑战未完成：请先在确认卡片中输入环境名称" };
        }
        environmentNames = challenge.submittedNames;
      }
      const mapped =
        decision === "approve_once"
          ? "allow"
          : decision === "approve_session"
            ? "allow_session"
            : decision === "approve_persistent"
              ? "allow_persistent"
              : "deny";
      const submitted = await submitConfirmCard(card, mapped, environmentNames);
      if (!submitted) {
        throw { code: "internal", message: "审批提交失败，请重试" };
      }
    },
    [stream, submitConfirmCard],
  );
  const agentRosterApi = useMemo(
    () =>
      conversationId === undefined
        ? null
        : createAgentRosterApi({
            conversationId,
            identity: { ownerId: agentOwnerId, ownerName: agentOwnerName },
            activeJobId: () => activeRunRef.current?.jobId ?? "",
            resolveApproval: resolveAgentApproval,
          }),
    [conversationId, agentOwnerId, agentOwnerName, resolveAgentApproval],
  );
  const agentHistoryApi = useMemo(() => createAgentRunHistoryApi(), []);
  const watchOwner = agentOwnerId === "" ? "local" : agentOwnerId;

  // The roster snapshot joins the recorded handles onto real roster rows,
  // with an independent slot per conversation. It reloads once per
  // recorded-handle change, and again when the slot is empty (for example
  // after a store reset): a handle the roster never covers stays honestly
  // unknown instead of spinning reloads, and a failed load never retries in
  // a loop.
  const rosterSnapshot = useAgentRosterSnapshot(conversationId);
  const rosterByHandle = useMemo(() => {
    const map = new Map<string, AgentRosterEntry>();
    for (const entry of rosterSnapshot ?? []) map.set(entry.handle, entry);
    return map;
  }, [rosterSnapshot]);
  useEffect(() => {
    if (!conversationId || !agentRosterApi || toolHandleVersion === 0) return;
    if (lastRefreshedHandleVersionRef.current === toolHandleVersion && rosterSnapshot !== null) return;
    lastRefreshedHandleVersionRef.current = toolHandleVersion;
    void refreshAgentRosterSnapshot(agentRosterApi, conversationId);
  }, [conversationId, agentRosterApi, toolHandleVersion, rosterSnapshot]);
  const runRowsByJobId = useMemo(() => {
    const map = new Map<string, (typeof conversationRuns)[number]>();
    for (const row of conversationRuns) map.set(row.id, row);
    return map;
  }, [conversationRuns]);
  const agentToolCallFor = useCallback(
    (item: ToolItem): AgentToolCall | null => {
      const lookup = toolHandlesRef.current?.lookup(item.attempt, item.callId) ?? { kind: "unknown" as const };
      if (lookup.kind !== "handle") return null;
      const attempt = conv.attempts.find((candidate) => candidate.generation === item.attempt);
      const run = attempt
        ? runSummaryFromAttempt(attempt.outcome, attempt.jobId ? runRowsByJobId.get(attempt.jobId) : undefined)
        : null;
      const confirm = [...conv.items]
        .reverse()
        .find(
          (candidate): candidate is ConfirmItem =>
            candidate.role === "confirm" &&
            candidate.callId === item.callId &&
            (item.attempt === null || candidate.attempt === item.attempt),
        );
      return buildAgentToolCall(
        item,
        lookup.handle,
        rosterByHandle.get(lookup.handle),
        run,
        approvalFromConfirmCard(confirm),
      );
    },
    [conv.attempts, conv.items, runRowsByJobId, rosterByHandle],
  );
  const submitAgentApproval = useCallback(
    (requestId: string, decision: AgentApprovalDecision) => {
      void resolveAgentApproval(requestId, decision).catch((error: unknown) => {
        pushToast("error", `审批失败：${describeError(error)}`);
      });
    },
    [resolveAgentApproval, pushToast],
  );
  const mentionAgentHandle = useCallback((token: string) => {
    setInput((prev) => {
      const base = prev.replace(/\s+$/, "");
      return base === "" ? `${token} ` : `${base} ${token} `;
    });
    inputRef.current?.focus();
  }, []);
  const openAgentTerminal = useCallback(
    (tabId: string, entry: AgentRosterEntry) => {
      const state = useUi.getState();
      for (const workspace of state.workspaces) {
        for (const pane of workspace.panes) {
          const existing = pane.tabs.find((tab) => tab.tabId === tabId);
          if (existing) {
            state.setActiveWorkspace(workspace.id);
            state.setActivePane(pane.id, workspace.id);
            state.setActiveTab(existing.id);
            return;
          }
        }
      }
      // No live view yet: resolve the owning session through the real backend
      // tab lists (live first, then durable per session), then attach a
      // terminal tab bound to that durable backend tab — never a new shell.
      void (async () => {
        try {
          const live = await terminalApi.listLive();
          const liveMatches = live.filter((tab) => tab.tabId === tabId);
          if (liveMatches.length > 1) {
            // Fail closed: ambiguous backend ownership never picks arbitrarily.
            pushToast("error", `句柄 ${entry.handle} 的终端在多个会话中命中，无法安全定位`);
            return;
          }
          let sessionId = liveMatches[0]?.sessionId;
          if (sessionId === undefined) {
            // Probe durable tabs only on sessions that can answer (connected,
            // durable-capable ssh/device), tolerating individual failures so
            // one broken session never masks another session's hit.
            const candidates = state.sessions.filter(
              (session) =>
                session.status === "connected" && (session.kind === "ssh" || session.kind === "device"),
            );
            const probed = await Promise.allSettled(
              candidates.map(async (session) => {
                const rows = await terminalApi.listDurable(session.id);
                return rows.some((row) => row.id === tabId) ? session.id : undefined;
              }),
            );
            const hits = probed
              .filter((result): result is PromiseFulfilledResult<string | undefined> => result.status === "fulfilled")
              .map((result) => result.value)
              .filter((id): id is string => id !== undefined);
            const failures = probed.length - probed.filter((result) => result.status === "fulfilled").length;
            if (hits.length > 1) {
              pushToast("error", `句柄 ${entry.handle} 的终端在多个会话中命中，无法安全定位`);
              return;
            }
            if (hits.length === 0) {
              pushToast(
                "error",
                failures > 0
                  ? `无法定位句柄 ${entry.handle} 的终端会话：已搜索会话均未命中，另有 ${failures} 个会话搜索失败`
                  : `无法定位句柄 ${entry.handle} 的终端会话（可能已断开或已退出），请刷新句柄列表`,
              );
              return;
            }
            sessionId = hits[0];
          }
          const session = state.sessions.find((item) => item.id === sessionId);
          if (!session) {
            pushToast("error", `句柄 ${entry.handle} 的终端会话不在当前会话列表中，请刷新句柄列表`);
            return;
          }
          const workspaceId = state.ensureWorkspace({
            kind: "session",
            sessionId: session.id,
            assetId: session.assetId ?? undefined,
            title: session.name,
            assetKind: session.kind,
          });
          state.setActiveWorkspace(workspaceId);
          state.addTab({
            id: nextTabId(`term-${session.id}`),
            kind: "terminal",
            title: entry.handle,
            sessionId: session.id,
            tabId,
            closable: true,
          });
        } catch (e) {
          pushToast("error", `打开句柄终端失败：${describeError(e)}`);
        }
      })();
    },
    [pushToast],
  );

  const stop = () => {
    void requestCancellation(false);
  };
  stopRef.current = requestCancellation;
  useEffect(() => {
    if (!aiTabBusyIds.has(tabKey) || !aiTabRunControls.has(tabKey)) {
      aiTabRunControls.set(tabKey, runControl);
    }
    const cancelWaiters = cancelWaitersRef.current;
    return () => {
      if (aiTabRunControls.get(tabKey) !== runControl) return;
      if (activeRunRef.current && !activeRunRef.current.settled) return;
      setAiTabBusy(tabKey, false);
      clearAiTabRun(tabKey);
      for (const waiter of cancelWaiters) {
        clearTimeout(waiter.timer);
        waiter.resolve(false);
      }
      cancelWaiters.clear();
      cancelOperationRef.current = null;
    };
  }, [tabKey, runControl]);

  const loadRunStatuses = async (ids: string[]) => {
    const entries = await Promise.all(
      ids.map(async (id) => {
        try {
          const runs = await aiApi.runs(id, 20);
          return [id, runStatusOf(Array.isArray(runs) ? runs : [])] as const;
        } catch {
          return [id, null] as const;
        }
      }),
    );
    setRunStatusByConv((prev) => {
      const next = { ...prev };
      for (const [id, status] of entries) {
        if (status) next[id] = status;
        else delete next[id];
      }
      return next;
    });
  };

  const loadConversations = async () => {
    setHistoryStatus("loading");
    setHistoryError(null);
    let ids: string[] = [];
    try {
      const list = await aiApi.conversationList();
      ids = list.map((c) => c.id);
      setConversations(
        list.map((c) => ({ id: c.id, title: c.title, updatedAt: c.updatedAt })),
      );
      setHistoryStatus("ready");
      const currentId = conversationIdRef.current;
      const current = currentId ? list.find((c) => c.id === currentId) : undefined;
      if (current?.title) onTitleChange(current.title);
    } catch (e) {
      setHistoryStatus("error");
      setHistoryError(describeError(e));
      return;
    }
    void loadRunStatuses(ids);
  };

  const toggleHistory = () => {
    const next = !historyOpen;
    setHistoryOpen(next);
    if (next) void loadConversations();
  };

  const openConversation = async (id: string, title?: string) => {
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "这一轮仍在运行，请先停止再切换会话");
      return;
    }
    const restoreGeneration = ++restoreGenerationRef.current;
    const generation = activeRunRef.current?.generation ?? null;
    try {
      const [msgs, runs] = await Promise.all([
        aiApi.messages(id),
        aiApi
          .runs(id, RUN_RESTORE_LIMIT)
          .then((rows) => rows.slice(0, RUN_RESTORE_LIMIT))
          .catch((): AiRunDto[] => []),
      ]);
      if (
        !isRestoreActive(restoreGeneration) ||
        aiRunBlocksStart(activeRunRef.current) ||
        (activeRunRef.current?.generation ?? null) !== generation
      ) {
        return;
      }
      updateConversationId(id, title);
      resetMessageHistory(id, msgs, runs);
      setConversationError(null);
      setEditing(null);
      setHistoryOpen(false);
      void restoreConversationRuns(id, restoreGeneration, runs);
    } catch (e) {
      if (isRestoreActive(restoreGeneration)) {
        pushToast("error", `打开会话失败：${describeError(e)}`);
      }
    }
  };

  const newConversation = () => {
    if (busy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "这一轮仍在运行，请先停止再新建会话");
      return;
    }
    restoreGenerationRef.current += 1;
    updateConversationId(undefined);
    clearMessageHistory();
    setConversationRuns([]);
    setConversationError(null);
    setEditing(null);
    stream.reset();
    setHistoryOpen(false);
  };

  const deleteConversation = async (c: { id: string; title: string }) => {
    const blocksReset = (id: string) =>
      id === conversationIdRef.current && aiRunBlocksStart(activeRunRef.current);
    if (blocksReset(c.id)) {
      pushToast("info", "这一轮仍在运行，请先停止再删除当前会话");
      return;
    }
    try {
      const runs = await aiApi.runs(c.id, 20);
      const inFlight = runStatusOf(Array.isArray(runs) ? runs : []);
      if (inFlight) {
        pushToast(
          "info",
          inFlight === "running"
            ? "这个会话还有正在运行的 AI 任务，等它结束或先停止再删除"
            : "这个会话还有中断待恢复的 AI 任务，先打开会话处理完再删除",
        );
        return;
      }
    } catch {
    }
    const ok = await ask(`删除会话「${c.title || "（未命名会话）"}」？消息记录、运行记录和定时任务将一并清除，不可恢复。`, {
      kind: "warning",
    });
    if (!ok) return;
    if (blocksReset(c.id)) {
      pushToast("info", "这一轮仍在运行，请先停止再删除当前会话");
      return;
    }
    try {
      await aiApi.conversationDelete(c.id);
    } catch (e) {
      pushToast("error", `删除会话失败：${describeError(e)}`);
      return;
    }
    setConversations((prev) => prev.filter((it) => it.id !== c.id));
    setRunStatusByConv((prev) => {
      const next = { ...prev };
      delete next[c.id];
      return next;
    });
    if (isLifecycleActive() && conversationIdRef.current === c.id) {
      deletedConversationIdsRef.current.add(c.id);
      restoreGenerationRef.current += 1;
      updateConversationId(undefined);
      clearMessageHistory();
      setConversationError(null);
      setEditing(null);
      if (!aiRunBlocksStart(activeRunRef.current)) stream.reset();
    }
  };

  const onInputChange = (v: string) => {
    setInput(v);
    const at = v.lastIndexOf("@");
    if (at < 0) {
      setAtOpen(false);
      return;
    }
    const prevCh = at === 0 ? " " : v[at - 1];
    if (/\s/.test(prevCh)) setAtOpen(true);
  };

  const pickSkill = (name: string) => {
    setInput(`/${name} `);
    inputRef.current?.focus();
  };

  const compactConversation = async () => {
    const id = conversationIdRef.current;
    if (!id) {
      pushToast("info", "当前还没有对话内容，不需要压缩");
      return;
    }
    if (busy) {
      pushToast("info", "这一轮仍在运行，结束后再压缩");
      return;
    }
    setCompacting(true);
    try {
      const result = await aiApi.compactConversation(id);
      pushToast("success", `已把 ${result.messages} 条消息压缩成摘要，之后的回答基于摘要`);
      await restoreConversation(id);
    } catch (e) {
      pushToast("error", `压缩失败：${describeError(e)}`);
    } finally {
      setCompacting(false);
    }
  };

  const switchModel = async (profile: { id: string; name: string }) => {
    setInput("");
    try {
      await modelApi.activate(profile.id);
      useUi.getState().bumpModelProfilesRevision();
      pushToast("success", `已切换到 ${profile.name}`);
    } catch (e) {
      pushToast("error", `切换模型失败：${describeError(e)}`);
    }
  };

  const slashCommands: SlashItem[] = [
    { key: "new", label: "/new", hint: "新建对话", run: () => { setInput(""); newConversation(); } },
    {
      key: "plan",
      label: "/plan",
      hint: planMode ? "关闭计划模式" : "开启计划模式：先给方案，确认后再执行",
      run: () => {
        setInput("");
        setPlanMode((value) => !value);
        pushToast("info", planMode ? "已关闭计划模式" : "已开启计划模式");
      },
    },
    { key: "model", label: "/model", hint: "切换模型", run: () => setInput("/model ") },
    {
      key: "compact",
      label: "/compact",
      hint: compacting ? "正在压缩…" : "把当前对话压缩成摘要，节省上下文",
      run: () => {
        setInput("");
        void compactConversation();
      },
    },
    { key: "permissions", label: "/permissions", hint: "打开 AI 权限与免确认授权", run: () => { setInput(""); setPermOpen(true); } },
  ];
  const slashMenuOpen = slashQuery !== null || modelQuery !== null;
  const slashItems: SlashItem[] =
    modelQuery !== null
      ? (modelOptions ?? [])
          .filter((profile) => `${profile.name} ${profile.model}`.toLowerCase().includes(modelQuery.trim().toLowerCase()))
          .map((profile) => ({
            key: `model:${profile.id}`,
            label: profile.name,
            hint: profile.active ? `${profile.model} · 当前` : profile.model,
            run: () => void switchModel(profile),
          }))
      : slashQuery !== null
        ? [
            ...slashCommands.filter((command) => command.key.startsWith(slashQuery)),
            ...(skillOptions ?? [])
              .filter((skill) => skill.name.includes(slashQuery))
              .map((skill) => ({ key: `skill:${skill.name}`, label: `/${skill.name}`, hint: skill.description, run: () => pickSkill(skill.name) })),
          ].slice(0, 10)
        : [];
  const slashEmptyText =
    modelQuery !== null
      ? modelOptions === null
        ? "读取模型…"
        : modelOptions.length === 0
          ? "还没有配置模型"
          : "没有匹配的模型"
      : "没有匹配的命令或技能";

  const pickRef = (ref: RefChip) => {
    setRefs((prev) => (prev.some((r) => r.id === ref.id) ? prev : [...prev, ref]));
    setInput((prev) => prev.replace(/@[^@\s]*$/, ""));
    setAtOpen(false);
    inputRef.current?.focus();
  };

  const appendImages = (files: File[]) => {
    if (files.length === 0) return;
    void Promise.all(files.map(readAsDataUrl))
      .then((urls) => setImages((prev) => [...prev, ...urls]))
      .catch((err) => pushToast("error", describeError(err)));
  };

  const onPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const fromItems = Array.from(e.clipboardData.items)
      .filter((it) => it.kind === "file" && it.type.startsWith("image/"))
      .map((it) => it.getAsFile())
      .filter((f): f is File => f !== null);
    const files = [...fromItems];
    for (const file of Array.from(e.clipboardData.files)) {
      if (!file.type.startsWith("image/")) continue;
      if (files.some((f) => f.name === file.name && f.size === file.size && f.type === file.type)) {
        continue;
      }
      files.push(file);
    }
    if (files.length === 0) return;
    e.preventDefault();
    appendImages(files);
  };

  const onPickImages = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []).filter((f) => f.type.startsWith("image/"));
    e.target.value = "";
    appendImages(files);
  };

  const takeoverPausedRef = useRef(false);
  const resumeTakeoverRef = useRef<(generation: number, jobId: string) => Promise<void>>(async () => undefined);

  const updateTakeoverPaused = (jobId: string | null, paused: boolean) => {
    takeoverPausedRef.current = paused;
    if (!jobId) return;
    const current = useUi.getState().takeover;
    if (current?.jobId === jobId) useUi.getState().setTakeover(withTakeoverPaused(current, paused));
  };

  const clearTakeoverForJob = (jobId: string) => {
    const current = useUi.getState().takeover;
    if (current?.jobId === jobId) useUi.getState().setTakeover(null);
  };

  const createTakeoverChannel = (run: AiRunSlot, onTerminal: () => void) => {
    let channelDisposed = false;
    const emit = (ev: Record<string, unknown>) => {
      if (channelDisposed || !isLifecycleActive()) return;
      const type = ev.type as string;
      const current = activeRunRef.current;
      if (!isCurrentAiRun(current, run.generation) || (current.settled && current.cancellation === "idle")) {
        if (type === "done" || type === "error" || type === "canceled") dispose();
        return;
      }
      const result = pushAgentEvent(run.generation, ev);
      if (result.accepted && type === "paused") {
        updateTakeoverPaused(activeRunRef.current?.jobId ?? run.jobId, true);
      } else if (result.accepted && type === "resumed") {
        updateTakeoverPaused(activeRunRef.current?.jobId ?? run.jobId, false);
      }
      if (!result.terminal) return;
      takeoverPausedRef.current = false;
      onTerminal();
      settleRestoredRun(run.generation);
      if (type === "error" && result.accepted) pushToast("error", `接管：${ev.message as string}`);
      dispose();
    };
    const channel = createAiChannel(emit);
    const dispose = () => {
      if (channelDisposed) return;
      channelDisposed = true;
      if (runChannelRef.current?.channel === channel) runChannelRef.current = null;
      disposeChannel(channel);
    };
    return { channel, dispose, emit };
  };

  const resumeTakeoverRun = async (generation: number, jobId: string) => {
    const current = activeRunRef.current;
    const banner = useUi.getState().takeover;
    if (
      !isLifecycleActive() ||
      !isCurrentAiRun(current, generation) ||
      current.kind !== "takeover" ||
      current.jobId !== jobId ||
      current.settled ||
      !banner ||
      banner.jobId !== jobId ||
      banner.tabId !== tabId
    ) {
      throw new Error("接管任务已不在当前会话，请重新打开对应终端后重试");
    }
    const next = createTakeoverChannel(current, () => clearTakeoverForJob(jobId));
    try {
      const result = await aiApi.takeoverResume({
        tabId: banner.tabId,
        token: banner.token,
        jobId,
        channel: next.channel,
      });
      if (result.jobId !== jobId) throw new Error("接管恢复返回了不同的任务 ID");
      const latest = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(latest, generation) || latest.settled) {
        next.dispose();
        return;
      }
      const previous = runChannelRef.current;
      runChannelRef.current = { channel: next.channel, dispose: next.dispose, generation };
      previous?.dispose();
      stream.bindJob(generation, jobId);
      if (takeoverPausedRef.current) next.emit({ type: "resumed" });
      const latestBanner = useUi.getState().takeover;
      if (latestBanner?.jobId === jobId) {
        useUi.getState().setTakeover(
          withTakeoverPaused({ ...latestBanner, token: result.token }, false),
        );
      }
    } catch (e) {
      next.dispose();
      throw e;
    }
  };
  resumeTakeoverRef.current = resumeTakeoverRun;

  const activeRunGeneration = activeRun?.generation;
  const activeRunKind = activeRun?.kind;
  const activeRunJobId = activeRun?.jobId;
  const activeRunSettled = activeRun?.settled;
  useEffect(() => {
    if (activeRunKind !== "takeover" || !activeRunJobId || activeRunSettled) return;
    return registerTakeoverResumeHandler(
      activeRunJobId,
      () => resumeTakeoverRef.current(activeRunGeneration ?? 0, activeRunJobId),
    );
  }, [activeRunGeneration, activeRunKind, activeRunJobId, activeRunSettled]);

  useEffect(() => {
    const restored = takeoverState;
    if (
      !active ||
      !tabId ||
      !restored?.jobId ||
      restored.tabId !== tabId ||
      !takeoverPaused(restored) ||
      activeRunRef.current
    ) {
      return;
    }
    restoreGenerationRef.current += 1;
    const generation = ++runSequenceRef.current;
    const run = bindAiRunJob(createAiRun(generation, "takeover"), restored.jobId);
    registerAiRunGeneration(tabKey, generation);
    aiTabRunControls.set(tabKey, runControl);
    setActiveRun(run);
    stream.beginRun(generation, "takeover");
    stream.bindJob(generation, restored.jobId);
    stream.resumeFromNextEvent(generation);
    takeoverPausedRef.current = true;
    setBusy(true);
  }, [active, runControl, setActiveRun, setBusy, stream, tabId, tabKey, takeoverState]);

  const runTakeover = async () => {
    if (!tabId) {
      pushToast("error", "终端接管需要先打开一个终端标签");
      return;
    }
    const instruction = await promptText(
      "终端接管任务（例如：安装并启动 nginx）",
      "",
      { multiLine: true },
    );
    if (!instruction || !isLifecycleActive()) return;
    const choice = await askChoice("允许 AI 直接操作这台终端吗？", {
      title: "终端接管（实验性功能）",
      level: "warning",
      choices: [
        { key: "allow", label: "允许", hint: "AI 可以执行命令", primary: true },
        { key: "readonly", label: "只读", hint: "AI 只能查看，不能执行命令" },
        { key: "abort", label: "取消", hint: "不启动接管", danger: true },
      ],
    });
    if ((choice !== "allow" && choice !== "readonly") || !isLifecycleActive()) return;
    const allowWrite = choice === "allow";

    if (anyAiBusy || aiRunBlocksStart(activeRunRef.current)) {
      pushToast("info", "另一轮 AI 仍在运行，请先停止再接管");
      return;
    }
    takeoverPausedRef.current = false;
    const run = beginRun("takeover");
    const holder = { settled: false };
    let banner: TakeoverState | null = null;
    const clearTakeover = () => {
      if (banner && useUi.getState().takeover?.jobId === banner.jobId) useUi.getState().setTakeover(null);
    };
    const { channel, dispose } = createTakeoverChannel(run, () => {
      holder.settled = true;
      clearTakeover();
    });
    runChannelRef.current = { channel, dispose, generation: run.generation };

    setBusy(true);
    let ownershipToken: string | undefined;
    try {
      ownershipToken = (await aiApi.takeoverEnter(tabId)).token;
      if (!isLifecycleActive() || !isCurrentAiRun(activeRunRef.current, run.generation)) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管页面已关闭").catch(() => undefined);
        dispose();
        return;
      }
      const result = await aiApi.takeoverRun({
        tabId,
        token: ownershipToken,
        instruction,
        allowWrite,
        channel,
      });
      ownershipToken = result.token;
      const current = activeRunRef.current;
      if (!isLifecycleActive() || !isCurrentAiRun(current, run.generation)) {
        dispose();
        return;
      }
      if ((current.settled && current.cancellation === "idle") || holder.settled) {
        setActiveRun(finishAiRunSpawn(current));
        setBusy(false);
        return;
      }
      setActiveRun(bindAiRunJob(current, result.jobId));
      stream.bindJob(run.generation, result.jobId);
      void driveCancellation();
      banner = {
        tabId,
        jobId: result.jobId,
        token: result.token,
        task: instruction,
        allowWrite,
        startedAt: Date.now(),
      };
      if (takeoverPausedRef.current) banner = withTakeoverPaused(banner, true);
      useUi.getState().setTakeover(banner);
      pushToast(
        "info",
        bindings.reclaimTakeover
          ? `接管已启动，可通过顶部横幅或 ${reclaimKeyLabel} 收回控制权。`
          : "接管已启动，可通过顶部横幅收回控制权。",
      );
    } catch (e) {
      if (ownershipToken) {
        await aiApi.takeoverExit(tabId, ownershipToken, "接管启动失败").catch(() => undefined);
      }
      const current = activeRunRef.current;
      if (isLifecycleActive() && isCurrentAiRun(current, run.generation)) {
        const result = pushAgentEvent(run.generation, {
          type: "error",
          message: describeError(e),
        });
        if (result.accepted && !current.settled) pushToast("error", describeError(e));
        setActiveRun(settleAiRun(finishAiRunSpawn(current)));
        setBusy(false);
        if (current.cancellation !== "idle") unconfirmCancellation(run.generation, e);
        clearTakeover();
      }
      dispose();
    }
  };

  if (!rightOpen || !active) return null;

  return (
    <>
      <div className="flex h-[32px] shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 [@media(max-height:480px)]:h-7">
        <div className="nx-spacer" />
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${searchOpen ? "is-active" : ""}`}
          title="搜索对话内容"
          aria-expanded={searchOpen}
          onClick={() => setSearchOpen((v) => !v)}
        >
          <IconSearch size={14} />
        </button>
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${cronOpen ? "is-active" : ""}`}
          title={
            conversationId
              ? "当前会话的定时任务"
              : "定时任务挂在会话上：先发送消息开始一个会话"
          }
          disabled={!conversationId}
          onClick={() => setCronOpen((v) => !v)}
        >
          <IconClock size={14} />
        </button>
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${agentOpen ? "is-active" : ""}`}
          title={
            conversationId
              ? "当前会话的 Agent 句柄与 run 历史"
              : "Agent 句柄挂在会话上：先发送消息开始一个会话"
          }
          disabled={!conversationId}
          onClick={() => setAgentOpen((v) => !v)}
        >
          <IconBot size={14} />
        </button>
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${watchOpen ? "is-active" : ""}`}
          title="Watch 监视"
          aria-expanded={watchOpen}
          onClick={() => setWatchOpen((v) => !v)}
        >
          <IconMonitor size={14} />
        </button>
        <button
          className={`nx-icon-btn nx-icon-btn-sm ${historyOpen ? "is-active" : ""}`}
          title="历史会话"
          onClick={toggleHistory}
        >
          <IconHistory size={14} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title="新建会话" onClick={newConversation}>
          <IconPlus size={14} />
        </button>
      </div>

      {searchOpen && (
        <div className="flex shrink-0 items-center gap-1 border-b border-neutral-800/60 px-2 py-1">
          <input
            className="nx-input nx-input-sm min-w-0 flex-1"
            placeholder="搜索对话内容，Enter 下一个，Shift+Enter 上一个"
            aria-label="搜索对话内容"
            value={searchQuery}
            autoFocus
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                jumpToMatch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                e.preventDefault();
                setSearchOpen(false);
              }
            }}
          />
          <span className="shrink-0 font-mono text-[10.5px] text-neutral-500" aria-live="polite">
            {searchQuery.trim()
              ? searchMatches.length > 0
                ? `${matchCursor >= 0 ? matchCursor + 1 : 0}/${searchMatches.length}`
                : "无匹配"
              : ""}
          </span>
          <button
            className="nx-icon-btn shrink-0"
            title="上一个匹配（Shift+Enter）"
            aria-label="上一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(-1)}
          >
            <IconChevronUp size={12} />
          </button>
          <button
            className="nx-icon-btn shrink-0"
            title="下一个匹配（Enter）"
            aria-label="下一个匹配"
            disabled={searchMatches.length === 0}
            onClick={() => jumpToMatch(1)}
          >
            <IconChevronDown size={12} />
          </button>
          <button
            className="nx-icon-btn shrink-0"
            title="关闭搜索"
            aria-label="关闭搜索"
            onClick={() => setSearchOpen(false)}
          >
            <IconClose size={11} />
          </button>
        </div>
      )}

      {historyOpen && (
        <div className="max-h-[40%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-1.5">
          {historyStatus === "loading" ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">历史会话加载中…</div>
          ) : historyStatus === "error" ? (
            <div className="px-2 py-3 text-center">
              <div className="nx-hint text-red-300">历史会话加载失败 · {historyError}</div>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5"
                onClick={() => void loadConversations()}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : conversations.length === 0 ? (
            <div className="nx-hint px-2 py-3 text-center text-[11.5px]">还没有历史会话</div>
          ) : (
            conversations.map((c) => (
              <div key={c.id} className="flex items-center gap-0.5">
                <button
                  className="nx-menu-item flex-1"
                  title={c.title || "（未命名会话）"}
                  onClick={() => void openConversation(c.id, c.title)}
                >
                  <span className="nx-menu-label">{c.title || "（未命名会话）"}</span>
                  <span className="nx-menu-hint">
                    {c.updatedAt ? new Date(c.updatedAt).toLocaleDateString() : ""}
                  </span>
                </button>
                {runStatusByConv[c.id] && (
                  <span
                    className={`nx-badge shrink-0 ${runStatusByConv[c.id] === "running" ? "nx-badge-amber" : "nx-badge-purple"}`}
                    title={runStatusByConv[c.id] === "running" ? "这个会话有正在运行的 AI 任务" : "这个会话有中断待恢复的 AI 任务"}
                  >
                    {runStatusByConv[c.id] === "running" ? "运行中" : "待恢复"}
                  </span>
                )}
                <button
                  className="nx-icon-btn nx-icon-btn-sm shrink-0"
                  title={`删除「${c.title || "（未命名会话）"}」`}
                  aria-label={`删除会话「${c.title || "（未命名会话）"}」`}
                  onClick={() => void deleteConversation(c)}
                >
                  <IconTrash size={12} />
                </button>
              </div>
            ))
          )}
        </div>
      )}

      {permOpen && (
        <div className="max-h-[70%] min-w-0 shrink-0 overflow-x-hidden overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
          <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
            <IconShield size={12} />
            AI 权限
            <span className="nx-spacer" />
            <button
              className="nx-icon-btn nx-icon-btn-sm"
              title="关闭"
              onClick={() => setPermOpen(false)}
            >
              <IconClose size={11} />
            </button>
          </div>

          {permError ? (
            <div className="px-2 py-3 text-center" role="alert">
              <div className="nx-hint text-red-300">权限读取失败 · {permError}</div>
              <button className="nx-btn nx-btn-ghost nx-btn-xs mt-1.5" onClick={() => void loadPermission()}>
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          ) : !perm ? (
            <div className="nx-hint px-2 py-3 text-center text-[11px]">权限读取中…</div>
          ) : (
            <>
              <div className="mb-1 flex items-center gap-1.5 text-[11px] text-neutral-300">
                {permScope ? "这台主机的模式" : "默认模式"}
                <span className="nx-spacer" />
                {permScope &&
                  (permDeviceOverride ? (
                    <button
                      className="text-[10.5px] text-neutral-500 hover:text-neutral-300"
                      title="清除这台主机的专属模式，改用全局默认模式"
                      onClick={followGlobalMode}
                    >
                      改为跟随全局
                    </button>
                  ) : (
                    <span className="text-[10.5px] text-neutral-500">跟随全局默认</span>
                  ))}
              </div>
              <div className="mb-1 grid grid-cols-3 gap-1" role="radiogroup" aria-label="AI 权限模式">
                {MODE_OPTIONS.map((m) => (
                  <button
                    key={m.value}
                    role="radio"
                    aria-checked={perm.mode === m.value}
                    title={m.hint}
                    className={`nx-btn nx-btn-xs justify-center ${perm.mode === m.value ? "nx-btn-primary" : "nx-btn-outline"}`}
                    onClick={() => switchMode(m.value)}
                  >
                    {m.label}
                  </button>
                ))}
              </div>
              <div className="mb-2.5 text-[10.5px] leading-relaxed text-neutral-500">
                {MODE_OPTIONS.find((m) => m.value === perm.mode)?.hint}
              </div>

              <button
                type="button"
                role="switch"
                aria-checked={planMode}
                className={`nx-menu-item mb-2.5 w-full ${planMode ? "bg-blue-500/15" : ""}`}
                title={PLAN_MODE_HINT}
                onClick={() => setPlanMode((v) => !v)}
              >
                <span className={`shrink-0 ${planMode ? "text-blue-300" : "text-neutral-500"}`}>
                  <IconList size={12} />
                </span>
                <span className="nx-menu-label">计划模式</span>
                <span className="nx-menu-hint">{planMode ? "已开启：先给方案，你确认后再执行" : "已关闭"}</span>
              </button>

              <div className="mb-1 border-t border-neutral-800/60 pt-2 text-[11px] text-neutral-300">免确认授权</div>
              <div className="mb-2.5">
                <GrantPanel currentAssetId={aiScopeAssetId} />
              </div>

              <div className="flex items-center gap-1.5 border-t border-neutral-800/60 pt-2">
                <span className="text-[11px] text-neutral-300">拦截规则</span>
                <span className="nx-count">
                  {perm.dangerRules.length > 0 ? `${perm.dangerRules.length} 条` : "未设置"}
                </span>
                <span className="nx-spacer" />
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  title="在设置中管理拦截规则"
                  onClick={() =>
                    useUi
                      .getState()
                      .addTab({ id: "settings", kind: "settings", title: "设置", closable: true })
                  }
                >
                  去设置
                </button>
              </div>
              <div className="mt-1.5 text-[10px] leading-relaxed text-neutral-500">
                高危和无法判断的操作始终询问，禁止的操作始终拒绝，任何模式和授权都不会跳过。
              </div>
            </>
          )}
        </div>
      )}

      {cronOpen && conversationId && (
        <CronPanel
          conversationId={conversationId}
          hostName={activeWorkspace?.kind === "session" ? activeWorkspace.title : undefined}
          onClose={() => setCronOpen(false)}
        />
      )}

      {agentOpen && conversationId && agentRosterApi && (
        // Keyed by conversation: a switch remounts both panels, so the first
        // committed frame of the new conversation can never show, and the
        // composer can never mention, the previous conversation's rows —
        // regardless of how the new conversation's load settles or fails.
        <>
          <AgentRosterPanel
            key={`roster-${conversationId}`}
            api={agentRosterApi}
            onMention={mentionAgentHandle}
            onOpened={openAgentTerminal}
          />
          <AgentRunHistoryPanel
            key={`history-${conversationId}`}
            api={agentHistoryApi}
            scope={{ ownerId: agentOwnerId, conversationId }}
          />
        </>
      )}

      {watchOpen && (
        <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60">
          <WatchPanel api={watchPanelApi} owner={watchOwner} onClose={() => setWatchOpen(false)} />
        </div>
      )}


      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div
          ref={setScrollEl}
          role="log"
          aria-label="AI 对话记录"
          className="h-full overflow-y-auto p-3"
        >
          {messageWindowStart > 0 && (
            <div className="mb-2 flex justify-center">
              <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={loadOlderMessages}>
                加载更早消息
              </button>
            </div>
          )}
          {conv.items.length === 0 && conversationError && (
            <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
              <span className="nx-empty-icon">
                <IconAlert size={19} />
              </span>
              <div className="text-xs text-red-300">会话加载失败 · {conversationError}</div>
              <button
                className="nx-btn nx-btn-outline nx-btn-xs"
                onClick={() => {
                  const id = conversationIdRef.current;
                  if (id) void restoreConversation(id);
                }}
              >
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          )}
          {conv.items.length === 0 && !conversationError && (
            <div className="flex flex-col items-center gap-3 px-2 pt-10 text-center">
              <span className="nx-empty-icon">
                <IconBot size={19} />
              </span>
              <div className="text-xs text-neutral-400">新建会话</div>
            </div>
          )}
          {virtual.range.padTop > 0 && (
            <div style={{ height: virtual.range.padTop }} aria-hidden="true" />
          )}
          {conv.items.slice(virtual.range.start, virtual.range.end).map((item, i) => {
            const index = virtual.range.start + i;
            const planApproved =
              item.role === "plan" &&
              conv.items.some((candidate, candidateIndex) => candidateIndex > index && candidate.role === "user" && candidate.text.trim() === PLAN_EXECUTE_MESSAGE);
            return (
              <div
                key={item.id}
                data-conversation-index={index}
                data-index={index}
                ref={virtual.measureRef}
                className={
                  [
                    i > 0 ? "mt-2.5" : null,
                    index === activeMatchIndex ? "rounded-xl ring-2 ring-amber-400/70" : null,
                  ]
                    .filter(Boolean)
                    .join(" ") || undefined
                }
              >
                <ChatBubble
                  item={item}
                  streaming={busy && index === conv.items.length - 1}
                  planApproved={planApproved}
                  onApprovePlan={handleApprovePlan}
                  onRetry={handleRetryRun}
                  onEdit={handleEditResend}
                  agentToolCall={item.role === "tool" ? agentToolCallFor(item) : null}
                  onAgentApproval={submitAgentApproval}
                  agentApprovalBusy={submittingCardId !== null}
                />
              </div>
            );
          })}
          {virtual.range.padBottom > 0 && (
            <div style={{ height: virtual.range.padBottom }} aria-hidden="true" />
          )}
          {confirmCard && (
            <div
              ref={confirmCardRef}
              tabIndex={-1}
              role="group"
              aria-label={`${confirmTitle(confirmCard.risk)}：${toolLabel(confirmCard.tool)}`}
              className={`${confirmCard.risk === "danger" ? "nx-alert nx-alert-danger" : "nx-alert"} mt-2.5 outline-none`}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  event.preventDefault();
                  event.stopPropagation();
                  void confirm("deny");
                }
              }}
            >
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconShield size={13} className="shrink-0" />
                <span className="min-w-0 truncate">{confirmTitle(confirmCard.risk)}</span>
                <span className="nx-spacer" />
                <span className={confirmCard.risk === "danger" ? "nx-badge nx-badge-red" : "nx-badge nx-badge-amber"}>
                  {toolLabel(confirmCard.tool)}
                </span>
                <span
                  className="shrink-0 cursor-help text-neutral-500"
                  title={CONFIRM_HELP}
                  aria-label={CONFIRM_HELP}
                  role="img"
                >
                  <IconInfo size={12} />
                </span>
              </div>
              <ConfirmBody card={confirmCard} />
              {productionNames && (
                <ProductionEnvironmentChallenge
                  key={confirmCard.id}
                  names={productionNames}
                  onStateChange={handleProductionState}
                />
              )}
              <div className="flex flex-wrap items-center gap-1.5">
                <button
                  className={`nx-btn ${confirmCard.risk === "danger" ? "nx-btn-danger-solid" : "nx-btn-primary"} nx-btn-xs`}
                  title="只允许这一次"
                  disabled={submittingCardId === confirmCard.id || productionBlocked}
                  onClick={() => void confirm("allow")}
                >
                  允许一次
                </button>
                {confirmCard.sessionScope && (
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs"
                    title={`本次对话中不再询问：${confirmCard.sessionScope}`}
                    disabled={submittingCardId === confirmCard.id || productionBlocked}
                    onClick={() => void confirm("allow_session")}
                  >
                    本会话允许
                  </button>
                )}
                {confirmCard.risk === "needs_confirm" && RULE_CAPABLE_TOOLS.has(confirmCard.tool) && (
                  <button
                    className="nx-btn nx-btn-outline nx-btn-xs"
                    title={`${permanentScopeText(confirmCard)}；高危或无法判断的操作仍会询问，可随时在授权管理中撤销`}
                    disabled={submittingCardId === confirmCard.id || productionBlocked}
                    onClick={() => void confirm("allow_persistent")}
                  >
                    永久允许
                  </button>
                )}
                <button
                  className="nx-btn nx-btn-ghost nx-btn-xs"
                  title="拒绝后 AI 会收到「用户拒绝了此操作」（Esc）"
                  disabled={submittingCardId === confirmCard.id}
                  onClick={() => void confirm("deny")}
                >
                  拒绝
                </button>
                <span className="nx-spacer" />
                {interceptionRuleFromCard(confirmCard) && (
                  <button
                    className="text-[10.5px] text-neutral-500 underline-offset-2 hover:text-neutral-300 hover:underline"
                    title="打开设置中的拦截规则，并预填当前操作"
                    onClick={() => {
                      const rule = interceptionRuleFromCard(confirmCard);
                      if (!rule) return;
                      const ui = useUi.getState();
                      ui.setAiRulePrefill(rule);
                      ui.addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
                    }}
                  >
                    以后都拦截
                  </button>
                )}
              </div>
              {confirmCard.sessionScope && (
                <div className="mt-1.5 text-[10.5px] leading-relaxed text-neutral-500">
                  「本会话允许」后，本次对话内不再询问：{confirmCard.sessionScope}
                </div>
              )}
            </div>
          )}

          {questionCard && (
            <form
              className="nx-alert mt-2.5"
              onSubmit={(event) => {
                event.preventDefault();
                void answer();
              }}
            >
              <div className="mb-1.5 flex items-center gap-1.5 font-semibold">
                <IconAlert size={13} />
                AI 需要你回答
              </div>
              <div className="mb-2 whitespace-pre-wrap text-[12px] leading-relaxed text-neutral-200">
                {questionCard.question}
              </div>
              {questionCard.options.length > 0 && (
                <div className="mb-2 flex flex-wrap gap-1.5">
                  {questionCard.options.map((option) => (
                    <button
                      key={option}
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-xs"
                      disabled={submittingCardId === questionCard.id}
                      onClick={() => void answer(option)}
                    >
                      {option}
                    </button>
                  ))}
                </div>
              )}
              <div className="flex items-end gap-1.5">
                <textarea
                  className="nx-textarea pointer-coarse:text-[16px] min-h-[36px] flex-1"
                  rows={2}
                  value={questionInput}
                  placeholder="输入回答…"
                  aria-label="回答 AI 的问题"
                  onChange={(event) => setQuestionInput(event.target.value)}
                />
                <button
                  type="submit"
                  className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
                  disabled={!questionInput.trim() || submittingCardId === questionCard.id}
                >
                  回答
                </button>
              </div>
            </form>
          )}

          {conv.todos.length > 0 && (
            <div className="mt-2.5 rounded-lg border border-neutral-800/60 bg-neutral-900/50 px-2.5 py-2">
              <div className="mb-1 flex items-center gap-1.5 text-[10.5px] text-neutral-400">
                <IconList size={11} />
                任务清单
                <span className="text-[var(--nx-fg-tertiary)]">
                  {conv.todos.filter((t) => t.status === "completed").length}/{conv.todos.length}
                </span>
              </div>
              <div className="flex max-h-28 flex-col gap-0.5 overflow-y-auto">
                {conv.todos.map((t, i) => (
                  <div key={i} className="flex items-start gap-1.5 text-[11px] leading-snug">
                    <span
                      className={
                        t.status === "completed"
                          ? "text-green-300"
                          : t.status === "in_progress"
                            ? "text-blue-300"
                            : "text-[var(--nx-fg-tertiary)]"
                      }
                    >
                      {t.status === "completed" ? "✓" : t.status === "in_progress" ? "▸" : "○"}
                    </span>
                    <span
                      className={
                        t.status === "completed"
                          ? "text-neutral-500 line-through"
                          : "text-neutral-300"
                      }
                    >
                      {t.content}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
        {follow.newOutput && (
          <button
            type="button"
            className="absolute bottom-2 left-1/2 z-10 -translate-x-1/2 rounded-full border border-neutral-700 bg-neutral-800/95 px-3 py-1 text-[11px] text-neutral-200 shadow-lg hover:bg-neutral-700"
            aria-label="回到最新输出"
            onClick={follow.jumpToLatest}
          >
            ↓ 新输出
          </button>
        )}
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 p-2.5 [padding-bottom:calc(0.625rem+var(--nx-kb-inset,0px))]">
        {planMode && (
          <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-blue-300">
            <IconList size={10} className="shrink-0" />
            <span className="truncate">计划模式 · {PLAN_MODE_HINT}</span>
          </div>
        )}
        {editing && (
          <div className="mb-1.5 flex items-center gap-1.5 rounded-lg border border-blue-500/30 bg-blue-500/10 px-2 py-1 text-[11px] text-blue-300">
            <IconEdit size={10} className="shrink-0" />
            <span className="min-w-0 flex-1 truncate">
              {editSubmitting ? "正在编辑并重新发送…" : "编辑并重新发送将删除此消息后的消息和运行记录"}
            </span>
            <button
              className="nx-btn nx-btn-primary nx-btn-xs shrink-0"
              disabled={!input.trim() || editSubmitting}
              onClick={() => void submitEditResend()}
            >
              编辑并重新发送
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
              disabled={editSubmitting}
              onClick={() => setEditing(null)}
            >
              取消
            </button>
          </div>
        )}
        {activeRun && activeRun.cancellation !== "idle" && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300"
            role="status"
            aria-live="polite"
          >
            <IconAlert size={10} className="shrink-0" />
            <span className="truncate">
              {activeRun.cancellation === "cancelling" ? "正在停止，等待任务结束…" : "停止未确认"}
            </span>
            {activeRun.cancellation === "unconfirmed" && !activeRun.settled && (
              <button className="nx-btn nx-btn-outline nx-btn-xs shrink-0" onClick={stop}>
                重试
              </button>
            )}
            {activeRun.cancellation === "unconfirmed" && (
              <button className="nx-btn nx-btn-ghost nx-btn-xs shrink-0" onClick={follow.jumpToLatest}>
                查看任务
              </button>
            )}
          </div>
        )}
        {syncNotice && syncNotice.generation === activeGeneration && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            {syncNotice.phase === "syncing" ? (
              <>
                <IconLoader size={10} className="animate-spin text-amber-300/80" />
                <span className="truncate">连接已恢复，正在补齐输出…</span>
              </>
            ) : syncNotice.phase === "failed" ? (
              <>
                <IconAlert size={10} className="shrink-0 text-red-300" />
                <span className="truncate">补齐失败，输出可能不完整</span>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs shrink-0"
                  onClick={() => {
                    const current = activeRunRef.current;
                    if (current?.jobId) resyncRun(current.generation, current.jobId);
                  }}
                >
                  重新补齐
                </button>
              </>
            ) : (
              <span className="truncate text-[var(--nx-fg-tertiary)]">
                {syncNotice.count > 0 ? `已补齐断线期间的 ${syncNotice.count} 条输出` : "连接已恢复，输出无缺失"}
              </span>
            )}
          </div>
        )}
        {busy && hitlWaiting && (
          <div
            className="nx-ai-hitl-waiting mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconAlert size={10} className="shrink-0 text-amber-300" />
            <span className="truncate">{hitlWaiting}</span>
          </div>
        )}
        {busy && !hitlWaiting && conv.status && (
          <div
            className="mb-1.5 flex items-center gap-1.5 text-[11px] text-neutral-500 [@media(max-height:480px)]:sr-only"
            role="status"
            aria-live="polite"
          >
            <IconLoader size={10} className="animate-spin text-amber-300/80" />
            <span className="truncate">{statusText(conv.status)}</span>
          </div>
        )}
        {aiScopeProduction?.production && (
          <div className="mb-1.5">
            <ProductionWriteRiskNotice
              environment={aiScopeProduction.environment}
              production
              operation="AI"
            />
          </div>
        )}
        {refs.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1">
            {refs.map((r) => (
              <span key={r.id} className="nx-chip nx-chip-accent" title={r.detail}>
                <span className="truncate">{r.label}</span>
                <button
                  className="nx-chip-x"
                  title="移除引用"
                  onClick={() => setRefs((prev) => prev.filter((x) => x.id !== r.id))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {images.length > 0 && (
          <div className="mb-1.5 flex flex-wrap gap-1.5">
            {images.map((src, i) => (
              <span key={i} className="nx-attach" title="粘贴的图片">
                <img src={src} alt="" />
                <button
                  className="nx-attach-x"
                  title="移除图片"
                  onClick={() => setImages((prev) => prev.filter((_, j) => j !== i))}
                >
                  <IconClose size={10} />
                </button>
              </span>
            ))}
          </div>
        )}

        {slashMenuOpen && (
          <div className="mb-1.5 max-h-56 overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1" role="listbox" aria-label="命令与技能">
            {slashItems.length === 0 ? (
              <div className="px-2 py-1.5 text-[11.5px] text-neutral-500">{slashEmptyText}</div>
            ) : (
              slashItems.map((item, index) => (
                <button
                  key={item.key}
                  role="option"
                  aria-selected={index === slashIndex}
                  className={`nx-menu-item w-full ${index === slashIndex ? "bg-neutral-800/70" : ""}`}
                  onMouseEnter={() => setSlashIndex(index)}
                  onClick={() => item.run()}
                >
                  <span className="nx-menu-label">{item.label}</span>
                  <span className="nx-menu-hint truncate">{item.hint}</span>
                </button>
              ))
            )}
            {modelQuery === null && skillOptions !== null && skillOptions.length === 0 && (
              <div className="border-t border-neutral-800/70 px-2 pt-1.5 text-[10.5px] text-neutral-500">在「设置 → AI 技能」里写好的技能也会出现在这里。</div>
            )}
          </div>
        )}

        {atOpen && refCandidates.length > 0 && (
          <div className="mb-1.5 max-h-40 overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1">
            {refCandidates.map((r) => (
              <button
                key={r.id}
                className="nx-menu-item w-full"
                onClick={() => pickRef(r)}
              >
                <span className="nx-menu-label">{r.label}</span>
                <span className="nx-menu-hint">{r.detail}</span>
              </button>
            ))}
          </div>
        )}

        <textarea
          ref={inputRef}
          className="nx-textarea pointer-coarse:text-[16px] max-h-40 min-h-[64px] w-full [@media(max-height:480px)]:h-9 [@media(max-height:480px)]:min-h-0"
          placeholder={
            busy
              ? "运行中：Enter 发送补充指令，AI 会在当前步骤完成后收到"
              : sessionId
                ? DESKTOP
                  ? "向 NexTerm 提问，@ 引用资产或终端标签，/ 命令与技能"
                  : "向 NexTerm 提问，@ 引用资产或终端标签，/ 命令与技能，可直接粘贴图片"
                : "未连接会话（仍可全局提问）"
          }
          aria-label="消息输入"
          value={input}
          rows={3}
          onChange={(e) => onInputChange(e.target.value)}
          onPaste={onPaste}
          onKeyDown={(e) => {
            if (slashMenuOpen && slashItems.length > 0) {
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                const step = e.key === "ArrowDown" ? 1 : -1;
                setSlashIndex((index) => (index + step + slashItems.length) % slashItems.length);
                return;
              }
              if ((e.key === "Enter" && !e.shiftKey && !isImeKeyEvent(e)) || e.key === "Tab") {
                e.preventDefault();
                slashItems[Math.min(slashIndex, slashItems.length - 1)].run();
                return;
              }
            }
            if (e.key === "Escape" && atOpen) {
              e.preventDefault();
              setAtOpen(false);
              return;
            }
            if (e.key === "Enter" && !e.shiftKey) {
              if (isImeKeyEvent(e)) return;
              e.preventDefault();
              if (busy) void steer();
              else if (editing) void submitEditResend();
              else void send();
            }
          }}
        />

        <div className="nx-ai-composer-actions mt-1.5 flex flex-wrap items-center gap-1">
          <ModelSelector onManage={() => setModelPanelOpen(true)} />
          <input
            ref={imageInputRef}
            type="file"
            accept="image/*"
            multiple
            className="hidden"
            aria-hidden="true"
            tabIndex={-1}
            onChange={onPickImages}
          />
          <button
            className="nx-icon-btn nx-icon-btn-sm"
            title={DESKTOP ? "添加图片" : "添加图片（也可以直接粘贴）"}
            aria-label="添加图片"
            onClick={() => imageInputRef.current?.click()}
          >
            <IconImage size={13} />
          </button>
          <div className="nx-spacer" />
          <UsageRing
            usage={conv.usage}
            runs={conversationRuns}
            loadSummary={() => modelApi.usageSummary()}
          />
          <button
            className={`nx-icon-btn nx-icon-btn-sm ${
              perm?.mode === "silent" ? "is-active" : ""
            } ${permOpen ? "bg-neutral-800 text-neutral-100" : ""}`}
            title={
              permError
                ? "AI 权限读取失败（点击查看并重试）"
                : perm
                  ? `AI 权限与模式：${MODE_LABEL[perm.mode]}（${permScope ? "这台主机" : "全局默认"}），含计划模式和免确认授权`
                  : "AI 权限与模式：读取中（点击设置）"
            }
            onClick={() => setPermOpen((v) => !v)}
          >
            <IconShield size={13} />
          </button>
          <button
            className="nx-icon-btn nx-icon-btn-sm text-red-400 hover:text-red-300"
            title={
              tabId
                ? bindings.reclaimTakeover
                  ? `终端接管（实验性功能）：AI 直接接手当前终端，随时按 ${reclaimKeyLabel} 收回控制权`
                  : "终端接管（实验性功能）：AI 直接接手当前终端"
                : "终端接管需要先打开一个终端标签"
            }
            disabled={!tabId || busy}
            onClick={() => void runTakeover()}
          >
            <IconMonitor size={13} />
          </button>
          {busy ? (
            <button
              className="nx-send-btn nx-send-btn-stop"
              title={
                activeRun?.cancellation === "cancelling"
                  ? "停止当前任务：已受理，等待终态"
                  : activeRun?.cancellation === "unconfirmed"
                    ? "停止当前任务：停止未确认，点击重试"
                    : "停止当前任务（保留已完成的输出）"
              }
              disabled={activeRun?.cancellation === "cancelling"}
              onClick={() => void stop()}
            >
              <IconStopSquare />
            </button>
          ) : (
            <button
              className="nx-send-btn"
              title={editing ? "编辑并重新发送" : "发送 (Enter)"}
              disabled={(!input.trim() && images.length === 0) || editSubmitting}
              onClick={() => (editing ? void submitEditResend() : void send())}
            >
              <IconSendArrow />
            </button>
          )}
        </div>
      </div>

      {modelPanelOpen && <ModelPanel onClose={() => setModelPanelOpen(false)} />}
    </>
  );
}

function IconSendArrow() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path
        d="M8 12.5V3.5M8 3.5L4 7.5M8 3.5l4 4"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function IconStopSquare() {
  return (
    <svg width="15" height="15" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <rect x="4.5" y="4.5" width="7" height="7" rx="1.5" fill="currentColor" />
    </svg>
  );
}

function readAsDataUrl(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader();
    fr.onload = () => resolve(String(fr.result));
    fr.onerror = () => reject(new Error("读取图片失败"));
    fr.readAsDataURL(file);
  });
}

function statusText(s: StatusLine): string {
  if (s.phase === "paused") return `接管已暂停：${s.detail ?? "用户暂停"}`;
  if (s.phase === "compacting") return s.detail ?? "上下文接近上限，正在压缩早期的命令输出…";
  if (s.phase === "thinking") return s.turn ? `第 ${s.turn} 轮 · 正在思考…` : "正在思考…";
  if (s.phase === "tool_args") {
    return `${preparingLabel(s.tool)} · 已生成参数 ${formatBytes(s.chars ?? 0)}`;
  }
  return s.detail ?? s.phase;
}

function preparingLabel(tool?: string): string {
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

function toolLabel(tool: string): string {
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

function interceptionRuleFromCard(card: ConfirmItem): string | null {
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

const ChatBubble = memo(function ChatBubble({
  item,
  streaming,
  planApproved,
  onApprovePlan,
  onRetry,
  onEdit,
  agentToolCall,
  onAgentApproval,
  agentApprovalBusy,
}: {
  item: ChatItem;
  streaming: boolean;
  planApproved: boolean;
  onApprovePlan: () => void;
  onRetry: (item: ChatItem) => void;
  onEdit: (item: ChatItem) => void;
  agentToolCall: AgentToolCall | null;
  onAgentApproval: (requestId: string, decision: AgentApprovalDecision) => void;
  agentApprovalBusy: boolean;
}) {
  if (item.role === "user") {
    return (
      <div className="group/user relative ml-10 rounded-[10px] rounded-br-[3px] border border-blue-500/25 bg-blue-500/20 px-3 py-2 text-[12.3px] leading-relaxed text-[var(--nx-fg-on-tint)]">
        {item.messageId ? (
          <button
            type="button"
            className="absolute right-1.5 top-1.5 rounded p-0.5 text-[var(--nx-fg-soft)] opacity-0 transition-opacity hover:text-neutral-100 focus:opacity-100 group-hover/user:opacity-100"
            title="编辑并重新发送"
            aria-label="编辑并重新发送"
            onClick={() => onEdit(item)}
          >
            <IconEdit size={11} />
          </button>
        ) : null}
        {item.imageCount ? (
          <div className="mb-1 flex items-center gap-1 text-[11px] text-[var(--nx-fg-soft)]">
            <IconImage size={11} />
            {item.imageCount} 张图片
          </div>
        ) : null}
        <pre className="font-sans break-words whitespace-pre-wrap">{item.text}</pre>
        {item.steer ? (
          <div className="mt-1 text-[10.5px] text-[var(--nx-fg-soft)]">
            {item.steer === "pending"
              ? "补充指令 · 等待送达…"
              : item.steer === "delivered"
                ? "补充指令 · 已送达"
                : "补充指令 · 未送达"}
          </div>
        ) : null}
      </div>
    );
  }
  if (item.role === "assistant") {
    return (
      <div className="mr-2 rounded-[10px] rounded-bl-[3px] border border-neutral-800 bg-neutral-800 px-3 py-2 text-[12.3px] leading-relaxed text-neutral-200">
        <Markdown text={item.text} streaming={streaming} />
      </div>
    );
  }
  if (item.role === "reasoning") {
    return (
      <details
        className="ml-1 border-l-2 border-neutral-700 pl-2.5 text-[11.5px] leading-relaxed text-neutral-500"
        open={streaming}
      >
        <summary className="cursor-pointer select-none text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          思考过程（{item.text.length} 字）{streaming ? " · 进行中" : ""}
        </summary>
        <div className="mt-1 whitespace-pre-wrap italic">{item.text}</div>
      </details>
    );
  }
  if (item.role === "confirm" || item.role === "question") {
    return <InteractionRecord item={item} />;
  }
  if (item.role === "compact") {
    return (
      <details className="rounded-lg border border-dashed border-neutral-800 px-2.5 py-1.5 text-[11px] text-neutral-500">
        <summary className="cursor-pointer select-none">
          已压缩上下文{item.messages > 0 ? `（${item.messages} 条消息）` : ""}，之后的回答基于下面的摘要
        </summary>
        <div className="mt-1.5 whitespace-pre-wrap text-[11.5px] leading-relaxed text-neutral-400">{item.text}</div>
      </details>
    );
  }
  if (item.role === "outcome") {
    if (item.outcome === "error") {
      return (
        <div
          role="alert"
          className="flex items-start gap-1.5 rounded-lg border border-red-500/30 bg-red-500/[0.08] px-2.5 py-2 text-[11.5px] leading-relaxed text-red-200"
        >
          <IconAlert size={12} className="mt-0.5 shrink-0 text-red-300" />
          {item.maxIterations ? (
            <span className="nx-badge nx-badge-amber shrink-0">达到步数上限</span>
          ) : null}
          <span className="whitespace-pre-wrap">本轮出错：{item.text}</span>
          {item.retryable ? (
            <button
              className="nx-btn nx-btn-outline nx-btn-xs ml-auto shrink-0"
              title="在同一会话里按原消息重新发起一轮"
              onClick={() => onRetry(item)}
            >
              <IconRefresh size={11} />
              重试
            </button>
          ) : null}
        </div>
      );
    }
    return (
      <div
        role="status"
        className={`flex items-center justify-center gap-1.5 py-0.5 text-[10.5px] ${
          item.outcome === "canceled" ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
        }`}
      >
        {item.text}
      </div>
    );
  }
  if (item.role === "diff") return <DiffBubble item={item} />;
  if (item.role === "plan") {
    return <PlanBubble item={item} approved={planApproved} onApprove={onApprovePlan} />;
  }
  if (item.role === "tool") {
    // A tool call with a verified roster handle renders the reviewed agent
    // tool card (roster-joined identity, run evidence and approval actions);
    // anything else keeps the plain bubble and claims no roster identity.
    if (agentToolCall) {
      return (
        <AgentToolCard
          call={agentToolCall}
          onApproval={onAgentApproval}
          approvalBusy={agentApprovalBusy}
        />
      );
    }
    return <ToolBubble item={item} />;
  }
  return null;
});

function InteractionRecord({ item }: { item: ConfirmItem | QuestionItem }) {
  const pending = item.resolution === undefined;
  const text =
    item.role === "confirm"
      ? pending
        ? `等待确认：${toolLabel(item.tool)}`
        : `${toolLabel(item.tool)} · ${item.resolution}`
      : pending
        ? `等待回答：${item.question}`
        : `提问 · ${item.resolution}`;
  return (
    <div
      className={`flex items-center gap-1.5 text-[10.5px] ${
        pending ? "text-[var(--nx-fg-warning)]" : "text-[var(--nx-fg-tertiary)]"
      }`}
      title={text}
    >
      {item.role === "confirm" ? <IconShield size={10} className="shrink-0" /> : <IconAlert size={10} className="shrink-0" />}
      <span className="truncate">{text}</span>
    </div>
  );
}

const POLICY_DENIED_PREFIX = "权限策略已拒绝: ";
const USER_DENIED_TEXT = "用户拒绝了此操作";

// blockedOutcome recognizes the two refusals the backend reports as tool
// failures, so the card shows them as interceptions rather than errors.
function blockedOutcome(text: string): { by: "policy" | "user"; reason: string } | null {
  const trimmed = text.trim();
  if (trimmed.startsWith(POLICY_DENIED_PREFIX)) return { by: "policy", reason: trimmed.slice(POLICY_DENIED_PREFIX.length) };
  if (trimmed === USER_DENIED_TEXT) return { by: "user", reason: "" };
  return null;
}

function ToolBubble({ item }: { item: Extract<ChatItem, { role: "tool" }> }) {
  const [open, setOpen] = useState(false);
  const running = item.summary === undefined;
  const full = item.text ?? "";
  const preview = item.summary ?? "";
  const blocked = !running && !item.ok && !item.panic ? blockedOutcome(full || preview) : null;
  const expandable = !blocked && full.length > preview.length;
  const body = blocked ? (blocked.by === "policy" ? `拦截原因：${blocked.reason}` : "你拒绝了这次操作，未执行。") : open ? full : preview;
  return (
    <div
      className="rounded-lg border border-neutral-800 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]"
      aria-busy={running}
    >
      <div className="flex items-center gap-1.5">
        <span className="nx-badge nx-badge-purple" title={item.name}>{toolLabel(item.name)}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.display}>
          {item.display}
        </span>
        <span className="sr-only">
          {running ? "工具执行中" : item.panic ? "工具执行出错" : item.ok ? "工具执行成功" : blocked ? (blocked.by === "policy" ? "已被权限策略拦截" : "你已拒绝") : "工具执行失败"}
        </span>
        {item.panic && !running ? <span className="nx-badge nx-badge-red">出错</span> : null}
        {blocked ? (
          <span className={blocked.by === "policy" ? "nx-badge nx-badge-amber" : "nx-badge"}>
            <IconShield size={10} className="mr-0.5 inline" />
            {blocked.by === "policy" ? "已拦截" : "已拒绝"}
          </span>
        ) : running ? (
          <IconLoader size={11} className="animate-spin text-amber-300" />
        ) : item.ok ? (
          <IconCheck size={12} className="text-green-300" />
        ) : (
          <IconXCircle size={12} className="text-red-300" />
        )}
        {!running && !blocked && item.exitCode !== null && item.exitCode !== undefined && (
          <span className="font-mono text-[10px] text-neutral-500">{item.exitCode}</span>
        )}
      </div>
      {item.subagent ? <SubagentTimelineView timeline={item.subagent} /> : null}
      {body && (
        <pre
          className={`mt-1.5 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] ${
            open ? "max-h-[26rem] text-neutral-300" : "max-h-32 text-neutral-500"
          }`}
        >
          {body}
        </pre>
      )}
      {expandable && (
        <button
          className="mt-1.5 flex items-center gap-1 text-[10.5px] text-neutral-500 hover:text-neutral-300"
          aria-expanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
          {open ? "收起" : `展开完整输出（${full.length} 字）`}
        </button>
      )}
    </div>
  );
}

function SubagentTimelineView({ timeline }: { timeline: SubagentTimeline }) {
  const [open, setOpen] = useState(timeline.status === "running");
  const running = timeline.status === "running";
  const label = running
    ? "子任务 · 运行中"
    : timeline.status === "completed"
      ? "子任务 · 已完成"
      : timeline.status === "canceled"
        ? "子任务 · 已取消"
        : "子任务 · 失败";
  return (
    <div className="mt-1.5 border-t border-neutral-800 pt-1.5">
      <button
        className="flex w-full items-center gap-1.5 text-[10.5px] text-neutral-400 hover:text-neutral-200"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {running ? (
          <IconLoader size={10} className="shrink-0 animate-spin text-amber-300" />
        ) : timeline.status === "completed" ? (
          <IconCheck size={11} className="shrink-0 text-green-300" />
        ) : (
          <IconXCircle size={11} className="shrink-0 text-red-300" />
        )}
        <IconBot size={11} className="shrink-0 text-neutral-500" />
        <span className="min-w-0 flex-1 truncate text-left">{label}</span>
        <span className="shrink-0 text-neutral-500">
          {timeline.text.length} 字 · {timeline.tools.length} 个工具
        </span>
        <IconChevronRight size={10} className={open ? "rotate-90" : undefined} />
      </button>
      {open ? (
        <div className="mt-1 space-y-1">
          {timeline.text ? (
            <pre className="max-h-24 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-400">
              {timeline.text}
            </pre>
          ) : null}
          {timeline.tools.map((tool) => (
            <div key={tool.callId} className="flex items-center gap-1.5 text-[10.5px]">
              {tool.status === "running" ? (
                <IconLoader size={9} className="shrink-0 animate-spin text-amber-300" />
              ) : tool.status === "ok" ? (
                <IconCheck size={10} className="shrink-0 text-green-300" />
              ) : (
                <IconXCircle size={10} className="shrink-0 text-red-300" />
              )}
              <span className="shrink-0 font-mono text-neutral-400">{tool.name || "tool"}</span>
              {tool.panic && tool.status !== "running" ? <span className="nx-badge nx-badge-red">出错</span> : null}
              {tool.summary ? (
                <span className="min-w-0 flex-1 truncate text-neutral-500" title={tool.summary}>
                  {tool.summary}
                </span>
              ) : null}
            </div>
          ))}
          {!running ? (
            <div className="text-[10.5px] text-neutral-500">
              {timeline.status === "completed"
                ? (timeline.summary ?? "已完成")
                : (timeline.error ?? timeline.summary ?? "已结束")}
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

function PlanBubble({
  item,
  approved,
  onApprove,
}: {
  item: Extract<ChatItem, { role: "plan" }>;
  approved: boolean;
  onApprove: () => void;
}) {
  return (
    <div className="rounded-lg border border-blue-500/30 bg-blue-500/[0.07] px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconList size={12} className="shrink-0 text-blue-300" />
        <span className="font-medium text-[var(--nx-fg-on-tint)]">方案已提交</span>
        <span className="text-neutral-500">{approved ? "已批准" : "还没有执行任何操作"}</span>
      </div>
      <div className="max-h-72 overflow-y-auto">
        <Markdown text={item.text} className="text-neutral-300" />
      </div>
      {!approved && (
        <button className="nx-btn nx-btn-outline nx-btn-xs mt-2" onClick={onApprove}>
          <IconPlay size={11} />
          批准并执行方案
        </button>
      )}
    </div>
  );
}

function ConfirmBody({ card }: { card: ConfirmItem }) {
  const pv = card.preview;
  const urls = extractUrls([...(card.commands ?? []), card.rendered].join("\n"));
  const urlBlock =
    urls.length > 0 ? (
      <div className="mb-1.5 rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5">
        <div className="mb-0.5 text-[10.5px] text-neutral-500">包含 URL</div>
        {urls.map((url) => (
          <div key={url} className="truncate font-mono text-[10.5px] text-blue-300" title={url}>
            {url}
          </div>
        ))}
      </div>
    ) : null;
  if (card.commands && card.commands.length > 0) {
    return (
      <>
        <div className="mb-1.5 max-h-52 overflow-auto whitespace-pre-wrap rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed text-neutral-200">
          {card.commands.map((command, index) => (
            <div key={index}>$ {command}</div>
          ))}
        </div>
        {urlBlock}
        {card.reason ? (
          <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
            {card.reason}
          </div>
        ) : null}
      </>
    );
  }
  if (!pv) {
    return (
      <>
        <pre className="mb-1.5 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[11px] text-neutral-200">
          {card.rendered}
        </pre>
        {urlBlock}
        {card.reason ? (
          <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
            {card.reason}
          </div>
        ) : null}
      </>
    );
  }
  return (
    <>
      <div className="mb-1 flex items-baseline gap-1.5 font-mono text-[11px]">
        <span className="shrink-0 text-neutral-500">
          {pv.kind === "create" ? "将新建" : "将修改"}
        </span>
        <span className="min-w-0 flex-1 truncate text-neutral-200" title={pv.path}>
          {pv.path}
        </span>
      </div>
      <div className="mb-1.5 max-h-52 overflow-auto whitespace-pre-wrap rounded border border-neutral-800 bg-neutral-950/70 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={pv.before} after={pv.after} />
      </div>
      {urlBlock}
      {card.reason ? (
        <div className="mb-1.5 text-[10.5px] leading-relaxed text-[var(--nx-fg-warning)]">
          {card.reason}
        </div>
      ) : null}
      <details className="mb-2">
        <summary className="cursor-pointer text-[10.5px] text-[var(--nx-fg-tertiary)] hover:text-neutral-400">
          查看原始参数
        </summary>
        <pre className="mt-1 max-h-28 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] text-neutral-500">
          {card.rendered}
        </pre>
      </details>
    </>
  );
}

// R-01 typed environment-name confirmation for a production-classified AI
// target. Each required name gets one input; the user types the actual
// environment names themselves — the submitted set is their own input,
// evaluated by the shared Go-semantics view-model, never echoed from the
// challenge. The backend re-validates the set at resume.
function ProductionEnvironmentChallenge({
  names,
  onStateChange,
}: {
  names: string[];
  onStateChange: (canConfirm: boolean, submittedNames: string[]) => void;
}) {
  const [typedByTarget, setTypedByTarget] = useState<Record<string, string>>({});
  const challenge: EnvironmentConfirmationChallenge = useMemo(
    () => ({
      targetSetHash: "ai-production-challenge",
      requiredEnvironmentNames: names,
      productionTargets: names.map((name, index) => ({
        sessionId: `required-${index}`,
        sessionName: name,
        environment: name,
      })),
    }),
    [names],
  );
  const state = evaluateEnvironmentConfirmation(challenge, typedByTarget, challenge.targetSetHash);
  const canConfirm = state.canConfirm;
  const submittedNames = state.submittedNames;
  const submittedKey = submittedNames.join("\u0000");
  useEffect(() => {
    onStateChange(canConfirm, submittedNames);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [canConfirm, submittedKey, onStateChange]);
  return (
    <div className="mb-2 rounded border border-amber-900/50 bg-amber-950/10 px-2.5 py-2" role="note">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-amber-300">
        <IconAlert size={12} />
        生产环境写入风险：AI 写入将需要逐次输入实际环境名确认，本会话不会记住授权。
      </div>
      <ul className="flex flex-col gap-1.5" aria-label="生产环境名列表">
        {state.targets.map(({ target, typed, missing, matches }) => (
          <li key={targetKey(target)} className="flex items-center gap-2 text-[11px]">
            <span className="nx-badge nx-badge-amber shrink-0">{target.environment}</span>
            <input
              className="nx-input font-mono text-xs"
              aria-label={`输入环境名 ${target.environment}`}
              autoComplete="off"
              value={typed}
              onChange={(event) =>
                setTypedByTarget((current) => ({ ...current, [targetKey(target)]: event.target.value }))
              }
            />
            {!missing && !matches && <span className="shrink-0 text-red-300">环境名不匹配</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function DiffBubble({ item }: { item: Extract<ChatItem, { role: "diff" }> }) {
  return (
    <div className="rounded-lg border border-neutral-700 bg-neutral-900/70 px-2.5 py-2 text-[11.5px]">
      <div className="mb-1.5 flex items-center gap-1.5">
        <IconEdit size={12} className="shrink-0 text-neutral-400" />
        <span className="shrink-0 font-medium text-neutral-200">文件已修改</span>
        <span className="min-w-0 flex-1 truncate font-mono text-neutral-400" title={item.path}>
          {item.path}
        </span>
      </div>
      <pre className="max-h-44 overflow-auto whitespace-pre-wrap font-mono text-[10.5px] leading-relaxed">
        <DiffLines before={item.before} after={item.after} />
      </pre>
    </div>
  );
}

function DiffLines({ before, after }: { before: string; after: string }) {
  const lines = simpleDiff(before, after);
  if (!hasVisibleChange(lines)) {
    return (
      <div className="text-neutral-500">
        {before === after ? "（内容没有变化）" : "（仅文件末尾换行不同）"}
      </div>
    );
  }
  return (
    <>
      {lines.map((l, i) => (
        <div
          key={i}
          className={
            l.kind === "add" ? "nx-diff-add" : l.kind === "del" ? "nx-diff-del" : "text-neutral-500"
          }
        >
          {diffLineText(l)}
        </div>
      ))}
    </>
  );
}
