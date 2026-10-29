import { useUi, useActiveWorkspace, GLOBAL_AI_BOARD_KEY, type AiDockTab } from "../../app/store";
import { useKeybindings, formatBinding, formatBindingAria } from "../../app/keybindings";
import { useSyncExternalStore, useState, useEffect, useCallback, useRef, type DragEvent as ReactDragEvent } from "react";
import { subscribeAiTabBusy, getAiTabBusyVersion, aiTabBusyIds, aiTabRunControls } from "./runOwnership";
import { type ContextMenuState, ContextMenu } from "../../ui/ContextMenu";
import { assetApi, aiApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { ask } from "../../ui/dialogs";
import { IconChevronRight, IconClose, IconPlus } from "../../ui/icons";
import { DRAG_AI_TAB, aiDatabaseScopeLabel } from "./panel/aiSidebarShared";
import { AiChatTab } from "./panel/AiChatTab";

export { AI_RESYNC_BACKOFF_MS, AI_RESYNC_MAX_ATTEMPTS, AI_SYNC_NOTICE_DISMISS_MS, AI_HITL_EXPIRY_GRACE_MS, aiCreatedTerminalOf, runStatusOf } from "./panel/aiSidebarShared";


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

  // Clicking "new" while an unused empty tab exists focuses it instead of
  // stacking more blank tabs.
  const handleNewTab = () => {
    const blank = tabs.find((t) => !t.conversationId && !(t.draft ?? "").trim() && !aiTabBusyIds.has(t.id));
    if (blank) {
      setActiveAiTab(boardKey, blank.id);
      return;
    }
    addAiTab(boardKey);
  };

  useEffect(() => {
    if (!activeTabId) return;
    const el = document.querySelector<HTMLElement>(`[data-ai-tab-id="${CSS.escape(activeTabId)}"]`);
    el?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [activeTabId, tabs.length]);

  const retitleTab = async (tab: AiDockTab) => {
    if (!tab.conversationId || retitlingTabId) return;
    setRetitlingTabId(tab.id);
    try {
      const title = await aiApi.conversationRetitle(tab.conversationId);
      updateAiTab(boardKey, tab.id, { title }, aiAccountEpoch);
    } catch (e) {
      pushToast("error", `未能重新生成标题：${describeError(e)}`);
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
        const ok = await ask("AI 正在回答，关闭标签页将停止本次回答。确定要关闭吗？", {
          title: "关闭 AI 标签页",
          kind: "warning",
        });
        if (!ok) return false;
        if (aiTabBusyIds.has(tab.id)) {
          const cancelAndConfirm = aiTabRunControls.get(tab.id);
          if (!cancelAndConfirm) {
            pushToast("error", "未能确认 AI 已停止，标签页已保留。请稍后重试");
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
          aria-label="会话标签页"
          className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
        >
          {tabs.map((t) => {
            const isActive = t.id === activeTabId;
            return (
              <div
                key={t.id}
                role="tab"
                data-ai-tab-id={t.id}
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
                        label: "左移标签页",
                        disabled: tabIndex <= 0,
                        onSelect: () => moveAiTab(boardKey, tabIndex, tabIndex - 1),
                      },
                      {
                        kind: "item",
                        label: "右移标签页",
                        disabled: tabIndex < 0 || tabIndex >= tabs.length - 1,
                        onSelect: () => moveAiTab(boardKey, tabIndex, tabIndex + 1),
                      },
                      { kind: "separator" },
                      { kind: "item", label: "关闭", onSelect: () => requestCloseTab(t) },
                      {
                        kind: "item",
                        label: "关闭左侧全部标签页",
                        disabled: tabIndex <= 0,
                        onSelect: () => closeTabsInOrder(tabs.slice(0, tabIndex)),
                      },
                      {
                        kind: "item",
                        label: "关闭右侧全部标签页",
                        disabled: tabIndex < 0 || tabIndex >= tabs.length - 1,
                        onSelect: () => closeTabsInOrder(tabs.slice(tabIndex + 1)),
                      },
                    ],
                  });
                }}
              >
                {aiTabBusyIds.has(t.id) && (
                  <span className="nx-dot shrink-0" role="img" aria-label="AI 正在回答" title="AI 正在回答" />
                )}
                <span className="truncate">{t.title}</span>
                <button
                  type="button"
                  className="nx-tab-close"
                  aria-label={`关闭标签页 ${t.title}`}
                  title="关闭标签页"
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
          title="新建会话"
          aria-label="新建会话"
          onClick={handleNewTab}
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
