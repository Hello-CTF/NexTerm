import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import {
  useUi,
  requestCloseTab,
  requestCloseTabs,
  requestKillTab,
  closeTabHint,
  closeActionHint,
  sanitizeRemoteTabTitle,
  type AppTab,
  type Pane,
  type SplitDirection,
} from "../store";
import { applyAuthoritativeTabTitle } from "../terminalViewStore";
import { formatBinding, useKeybindings, type KeybindingActionId } from "../keybindings";
import { terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { ErrorBoundary } from "../../ui/ErrorBoundary";
import { promptText } from "../../ui/dialogs";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconAlert,
  IconChevronLeft,
  IconChevronRight,
  IconClose,
  IconEdit,
  IconPlus,
  IconSplitH,
  IconSplitV,
  IconMergeH,
  IconStop,
} from "../../ui/icons";
import { TabProductionBadge, TabStripItem, TabStripTitle, tabIcon, tabTooltip } from "./tabStrip";
import { PaneForTab } from "./PaneForTab";
import { WorkspaceEmpty } from "./EmptyStates";

function handleTablistKeyDown(
  event: ReactKeyboardEvent<HTMLElement>,
  ids: string[],
  currentId: string,
  activate: (id: string) => void,
  moveTab: (fromIndex: number, toIndex: number) => void,
): void {
  if (event.target !== event.currentTarget) return;
  const index = ids.indexOf(currentId);
  if (index < 0) return;
  if ((event.key === "ArrowLeft" || event.key === "ArrowRight") && event.ctrlKey && event.shiftKey) {
    const to = event.key === "ArrowLeft" ? index - 1 : index + 1;
    if (to < 0 || to >= ids.length) return;
    event.preventDefault();
    moveTab(index, to);
    focusTab(event.currentTarget.closest("[role='tablist']"), currentId);
    return;
  }
  let next = -1;
  if (event.key === "ArrowRight") next = (index + 1) % ids.length;
  else if (event.key === "ArrowLeft") next = (index - 1 + ids.length) % ids.length;
  else if (event.key === "Home") next = 0;
  else if (event.key === "End") next = ids.length - 1;
  else if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    activate(currentId);
    return;
  } else return;
  event.preventDefault();
  const nextId = ids[next];
  activate(nextId);
  focusTab(event.currentTarget.closest("[role='tablist']"), nextId);
}

const TAB_DRAG_MIME = "application/x-nexterm-tab";

let tabDragSource: { tabId: string; paneId: string } | null = null;

function focusTab(tablist: Element | null, id: string): void {
  tablist?.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`)?.focus();
}

export interface PaneMoveTarget {
  paneId: string;
  label: string;
}

interface PaneGroupProps {
  pane: Pane;
  idPrefix: string;
  active: boolean;
  // Measures the tab strip; tab content is placed below it by SplitView.
  stripRef: (element: HTMLElement | null) => void;
  // The workspace shows more than one pane.
  split: boolean;
  // Only this pane is on screen (mobile and compact layouts); `paneNumber` of `paneCount` is shown.
  singlePane: boolean;
  paneNumber: number;
  paneCount: number;
  splitAllowed: boolean;
  moveTargets: PaneMoveTarget[];
  onSplit: (direction: SplitDirection) => void;
  onClosePane: () => void;
  onCyclePane: (step: 1 | -1) => void;
  onActivate: () => void;
  onNewTerminal: () => void;
  leftOpen: boolean;
  onToggleLeft: () => void;
}

export function PaneGroup({
  pane,
  idPrefix,
  active,
  stripRef,
  split,
  singlePane,
  paneNumber,
  paneCount,
  splitAllowed,
  moveTargets,
  onSplit,
  onClosePane,
  onCyclePane,
  onActivate,
  onNewTerminal,
  leftOpen,
  onToggleLeft,
}: PaneGroupProps) {
  const setActiveTab = useUi((s) => s.setActiveTab);
  const moveTabToPane = useUi((s) => s.moveTabToPane);
  const moveTabInPane = useUi((s) => s.moveTabInPane);
  const moveTab = (fromIndex: number, toIndex: number) =>
    moveTabInPane(pane.id, fromIndex, toIndex);
  const activeTabId = pane.activeTabId ?? pane.tabs[pane.tabs.length - 1]?.id ?? null;
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropHint, setDropHint] = useState<{ id: string; edge: "before" | "after" } | null>(null);
  const bindings = useKeybindings();
  const kbLabel = (id: KeybindingActionId) => formatBinding(bindings[id]);

  const renameTab = async (id: string, title: string) => {
    let draft = title;
    let failure = "";
    for (;;) {
      const next = await promptText(failure || "重命名标签：", draft);
      if (next === null || !next.trim()) return;
      const trimmed = sanitizeRemoteTabTitle(next);
      if (!trimmed) return;
      const tabId = pane.tabs.find((t) => t.id === id)?.tabId;
      if (!tabId) {
        applyAuthoritativeTabTitle(id, trimmed);
        return;
      }
      try {
        const info = await terminalApi.renameTab(tabId, trimmed);
        applyAuthoritativeTabTitle(id, info.title || trimmed);
        return;
      } catch (error) {
        draft = trimmed;
        failure = `重命名失败：${describeError(error)}`;
      }
    }
  };

  const openTabMenu = (t: (typeof pane.tabs)[number], x: number, y: number) => {
    const items: MenuItem[] = [];
    if (t.kind === "terminal") {
      items.push({
        kind: "item",
        label: "重命名",
        icon: <IconEdit size={12} />,
        onSelect: () => void renameTab(t.id, t.title),
      });
    }
    const running = t.kind === "terminal" && Boolean(t.tabId) && !t.dead && !t.exited;
    items.push({
      kind: "item",
      label: "关闭标签",
      icon: <IconClose size={12} />,
      hint: closeActionHint(t),
      disabled: !t.closable,
      onSelect: () => void requestCloseTab(t.id),
    });
    const tabIndex = pane.tabs.findIndex((candidate) => candidate.id === t.id);
    items.push({
      kind: "item",
      label: "左移标签",
      icon: <IconChevronLeft size={12} />,
      accel: "Ctrl+Shift+←",
      disabled: tabIndex <= 0,
      onSelect: () => moveTab(tabIndex, tabIndex - 1),
    });
    items.push({
      kind: "item",
      label: "右移标签",
      icon: <IconChevronRight size={12} />,
      accel: "Ctrl+Shift+→",
      disabled: tabIndex < 0 || tabIndex >= pane.tabs.length - 1,
      onSelect: () => moveTab(tabIndex, tabIndex + 1),
    });
    const closableLeft = pane.tabs.slice(0, Math.max(0, tabIndex)).filter((candidate) => candidate.closable);
    const closableRight = pane.tabs.slice(tabIndex + 1).filter((candidate) => candidate.closable);
    items.push({
      kind: "item",
      label: "关闭左侧全部标签",
      icon: <IconClose size={12} />,
      disabled: closableLeft.length === 0,
      onSelect: () => void requestCloseTabs(closableLeft.map((candidate) => candidate.id)),
    });
    items.push({
      kind: "item",
      label: "关闭右侧全部标签",
      icon: <IconClose size={12} />,
      disabled: closableRight.length === 0,
      onSelect: () => void requestCloseTabs(closableRight.map((candidate) => candidate.id)),
    });
    if (moveTargets.length > 0) {
      items.push({ kind: "separator" });
      for (const target of moveTargets) {
        items.push({
          kind: "item",
          label: `移到${target.label}`,
          icon: <IconChevronRight size={12} />,
          onSelect: () => moveTabToPane(t.id, target.paneId),
        });
      }
    }
    if (running) {
      items.push({ kind: "separator" });
      items.push({
        kind: "item",
        label: "结束进程…",
        icon: <IconStop size={12} />,
        hint: "终止进程，无法恢复",
        danger: true,
        onSelect: () => void requestKillTab(t.id),
      });
    }
    setMenu({ x, y, title: t.title, items });
  };

  const tabScrollRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // Keep the active tab inside the horizontally scrollable strip (mobile widths overflow it).
    if (!activeTabId) return;
    const target = tabScrollRef.current?.querySelector<HTMLElement>(
      `[data-tab-id="${CSS.escape(activeTabId)}"]`,
    );
    target?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
  }, [activeTabId, pane.tabs.length]);

  return (
    <div
      className="flex h-full min-h-0 flex-col"
      onMouseDown={() => {
        if (!active) onActivate();
      }}
    >
      <div
        ref={stripRef}
        className={`nx-tabstrip is-sub ${split ? "is-split" : ""} ${
          active ? "is-active-pane" : ""
        }`}
      >
        <div className="nx-tabstrip-scroll" ref={tabScrollRef}>
        {pane.tabs.length === 0 && (
          <span className="px-1 text-xs text-neutral-500">这个面板还没有标签</span>
        )}
        <div role="tablist" aria-label="标签页" className="flex items-center gap-[3px]">
          {pane.tabs.map((t) => {
            const Icon = tabIcon(t);
            const isActive = t.id === activeTabId;
            return (
              <TabStripItem key={t.id} tab={t}>
                {(unread) => (
                  <div
                role="tab"
                data-tab-id={t.id}
                id={`nx-tab-${idPrefix}-${t.id}`}
                aria-selected={isActive}
                aria-controls={`nx-tab-panel-${idPrefix}-${t.id}`}
                tabIndex={isActive ? 0 : -1}
                draggable
                className={`nx-tab ${isActive ? "is-active" : ""}${
                  dropHint?.id === t.id ? ` nx-drop-${dropHint.edge}` : ""
                }${draggingId === t.id ? " nx-drag-source" : ""}`}
                onClick={() => {
                  if (draggingId === t.id) return;
                  setActiveTab(t.id);
                }}
                onDragStart={(e) => {
                  tabDragSource = { tabId: t.id, paneId: pane.id };
                  setDraggingId(t.id);
                  e.dataTransfer.setData(TAB_DRAG_MIME, t.id);
                  e.dataTransfer.effectAllowed = "move";
                }}
                onDragEnd={() => {
                  tabDragSource = null;
                  setDraggingId(null);
                  setDropHint(null);
                }}
                onDragOver={(e) => {
                  if (!e.dataTransfer.types.includes(TAB_DRAG_MIME)) return;
                  if (tabDragSource?.paneId !== pane.id || draggingId === t.id) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                  const rect = e.currentTarget.getBoundingClientRect();
                  const edge = e.clientX < rect.left + rect.width / 2 ? "before" : "after";
                  setDropHint((hint) => (hint?.id === t.id && hint.edge === edge ? hint : { id: t.id, edge }));
                }}
                onDragLeave={() => {
                  setDropHint((hint) => (hint?.id === t.id ? null : hint));
                }}
                onDrop={(e) => {
                  if (!e.dataTransfer.types.includes(TAB_DRAG_MIME)) return;
                  if (tabDragSource?.paneId !== pane.id) return;
                  e.preventDefault();
                  setDropHint(null);
                  const dragId = e.dataTransfer.getData(TAB_DRAG_MIME);
                  if (!dragId || dragId === t.id) return;
                  const from = pane.tabs.findIndex((x) => x.id === dragId);
                  const over = pane.tabs.findIndex((x) => x.id === t.id);
                  if (from < 0 || over < 0) return;
                  const rect = e.currentTarget.getBoundingClientRect();
                  const before = e.clientX < rect.left + rect.width / 2;
                  moveTab(from, before ? (from < over ? over - 1 : over) : from < over ? over : over + 1);
                }}
                onKeyDown={(e) => {
                  if ((e.key === "F10" && e.shiftKey) || e.key === "ContextMenu") {
                    e.preventDefault();
                    const rect = e.currentTarget.getBoundingClientRect();
                    openTabMenu(t, rect.left, rect.bottom);
                    return;
                  }
                  handleTablistKeyDown(
                    e,
                    pane.tabs.map((x) => x.id),
                    t.id,
                    setActiveTab,
                    moveTab,
                  );
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  openTabMenu(t, e.clientX, e.clientY);
                }}
                title={tabTooltip(t, unread)}
              >
                <span className="relative flex shrink-0">
                  <Icon size={13} />
                  {t.kind === "terminal" && t.durable === true && (
                    <span
                      className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-emerald-400"
                      role="img"
                      aria-label="持久终端"
                    />
                  )}
                </span>
                <TabStripTitle tab={t} />
                {t.kind === "terminal" && <TabProductionBadge sessionId={t.sessionId} />}
                {t.closeError && (
                  <span className="flex shrink-0 items-center text-red-400" role="img" aria-label={`关闭失败：${t.closeError}`}>
                    <IconAlert size={11} />
                  </span>
                )}
                {unread && (
                  <span
                    className="h-1.5 w-1.5 shrink-0 rounded-full bg-sky-400"
                    role="img"
                    aria-label="有新输出"
                  />
                )}
                {t.closable && (
                  <button
                    type="button"
                    className="nx-tab-close"
                    aria-label={`关闭标签 ${t.title}`}
                    title={closeTabHint(t)}
                    onClick={(e) => {
                      e.stopPropagation();
                      void requestCloseTab(t.id);
                    }}
                  >
                    <IconClose size={10} />
                  </button>
                )}
                  </div>
                )}
              </TabStripItem>
            );
          })}
        </div>
        </div>
        <button
          className="nx-tab-new"
          title={`新建终端标签 (${kbLabel("newTerminal")})`}
          aria-label="新建终端标签"
          onClick={onNewTerminal}
        >
          <IconPlus size={13} />
        </button>
        <div className="nx-spacer" />
        {singlePane && paneCount > 1 && (
          <span className="flex items-center" role="group" aria-label="切换窗格">
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              title="上一个窗格"
              aria-label="上一个窗格"
              onClick={() => onCyclePane(-1)}
            >
              <IconChevronLeft size={13} />
            </button>
            <span className="px-0.5 text-[11px] tabular-nums text-neutral-400" aria-live="polite">
              窗格 {paneNumber}/{paneCount}
            </span>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              title="下一个窗格"
              aria-label="下一个窗格"
              onClick={() => onCyclePane(1)}
            >
              <IconChevronRight size={13} />
            </button>
          </span>
        )}
        {!singlePane && (
          <>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              title={`向右分屏 (${kbLabel("splitRight")})`}
              aria-label="向右分屏"
              onClick={() => onSplit("right")}
            >
              <IconSplitV size={14} />
            </button>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!splitAllowed}
              title={splitAllowed ? `向下分屏 (${kbLabel("splitDown")})` : "窗口高度不足，无法向下分屏"}
              aria-label="向下分屏"
              onClick={() => onSplit("down")}
            >
              <IconSplitH size={14} />
            </button>
          </>
        )}
        {split && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            title={`关闭窗格，标签页移到相邻窗格 (${kbLabel("closePane")})`}
            aria-label="关闭窗格（保留标签页）"
            onClick={onClosePane}
          >
            <IconMergeH size={14} />
          </button>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={leftOpen ? `收起左栏 (${kbLabel("toggleSidebar")})` : `展开左栏 (${kbLabel("toggleSidebar")})`}
          aria-label={leftOpen ? "收起左栏" : "展开左栏"}
          onClick={onToggleLeft}
        >
          <IconChevronLeft size={15} className={leftOpen ? "" : "rotate-180"} />
        </button>
      </div>

      <div className="min-h-0 flex-1">
        {pane.tabs.length === 0 && <WorkspaceEmpty onNew={onNewTerminal} />}
      </div>

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

// PaneTabPanel is one tab's content. SplitView renders it in a workspace-level layer keyed by tab id,
// so it stays mounted when the tab moves to another pane or its pane is renamed.
export function PaneTabPanel({
  tab,
  idPrefix,
  workspaceId,
  visible,
}: {
  tab: AppTab;
  idPrefix: string;
  workspaceId: string;
  visible: boolean;
}) {
  return (
    <div
      role="tabpanel"
      id={`nx-tab-panel-${idPrefix}-${tab.id}`}
      aria-labelledby={`nx-tab-${idPrefix}-${tab.id}`}
      className="h-full min-h-0"
    >
      <ErrorBoundary onClose={tab.closable ? () => void requestCloseTab(tab.id) : undefined}>
        <PaneForTab tab={tab} active={visible} workspaceId={workspaceId} onClose={requestCloseTab} />
      </ErrorBoundary>
    </div>
  );
}
