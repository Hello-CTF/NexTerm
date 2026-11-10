import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type RefObject } from "react";
import {
  useUi,
  requestCloseTab,
  requestCloseTabs,
  requestKillTab,
  closeTabHint,
  closeActionHint,
  hasCustomTitle,
  sanitizeRemoteTabTitle,
  zoomedPaneOf,
  type AppTab,
  type Pane,
  type SplitDirection,
} from "../store";
import { applyAuthoritativeTabTitle, clearAuthoritativeTabTitle } from "../terminalViewStore";
import { formatBinding, useKeybindings, type KeybindingActionId } from "../keybindings";
import { terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { ErrorBoundary } from "../../ui/ErrorBoundary";
import { promptText } from "../../ui/dialogs";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import {
  IconAlert,
  IconChevronLeft,
  IconCheck,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconEdit,
  IconMaximize,
  IconPlus,
  IconSplitH,
  IconSplitV,
  IconMergeH,
  IconStop,
} from "../../ui/icons";
import {
  TabAttentionBadge,
  TabPersistenceMark,
  TabProductionBadge,
  TabProgress,
  TabStripItem,
  TabStripTitle,
  tabIcon,
  tabTooltip,
} from "./tabStrip";
import { PaneForTab } from "./PaneForTab";
import { WorkspaceEmpty } from "./EmptyStates";
import { beginTabDrag, currentTabDrag, endTabDrag, TAB_DRAG_MIME, tabDragFrom, useTabDrag } from "./tabDrag";
import { isTouchShell } from "../shell/shellMode";
import { stateMarkItems } from "../../features/session/stateMarkMenu";
import { t as tr } from "../../i18n";

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

function focusTab(tablist: Element | null, id: string): void {
  tablist?.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`)?.focus();
}

interface StripOverflow {
  left: boolean;
  right: boolean;
}

const NO_OVERFLOW: StripOverflow = { left: false, right: false };

// useTabStripOverflow tracks which ends of the horizontally scrolling tab strip hide tabs, and on desktop maps a
// vertical wheel onto horizontal scrolling, since the strip shows no scrollbar.
function useTabStripOverflow(ref: RefObject<HTMLDivElement | null>, enabled: boolean, tabCount: number): StripOverflow {
  const [overflow, setOverflow] = useState<StripOverflow>(NO_OVERFLOW);
  useEffect(() => {
    const element = ref.current;
    if (!enabled || !element) {
      setOverflow(NO_OVERFLOW);
      return;
    }
    const measure = () => {
      const max = element.scrollWidth - element.clientWidth;
      const next = { left: max > 1 && element.scrollLeft > 1, right: max > 1 && element.scrollLeft < max - 1 };
      setOverflow((current) => (current.left === next.left && current.right === next.right ? current : next));
    };
    const onWheel = (event: WheelEvent) => {
      if (event.ctrlKey || Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
      if (element.scrollWidth <= element.clientWidth) return;
      event.preventDefault();
      // Line-mode deltas (Firefox) count lines; scale them to roughly a short tab's width.
      element.scrollLeft += event.deltaMode === 1 ? event.deltaY * 32 : event.deltaY;
      measure();
    };
    measure();
    element.addEventListener("scroll", measure, { passive: true });
    element.addEventListener("wheel", onWheel, { passive: false });
    let observer: ResizeObserver | null = null;
    if (typeof ResizeObserver !== "undefined") {
      observer = new ResizeObserver(measure);
      observer.observe(element);
      for (const child of element.children) observer.observe(child);
    }
    return () => {
      element.removeEventListener("scroll", measure);
      element.removeEventListener("wheel", onWheel);
      observer?.disconnect();
    };
  }, [ref, enabled, tabCount]);
  return overflow;
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
  /** Owning workspace; its kind decides whether terminals can be created from this pane. */
  workspaceId?: string;
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
  workspaceId,
  leftOpen,
  onToggleLeft,
}: PaneGroupProps) {
  const workspaceKind = useUi((s) => (workspaceId ? s.workspaces.find((w) => w.id === workspaceId)?.kind : undefined));
  const canNewTerminal = workspaceKind === undefined || workspaceKind === "session";
  const setActiveTab = useUi((s) => s.setActiveTab);
  const moveTabToPane = useUi((s) => s.moveTabToPane);
  const moveTabInPane = useUi((s) => s.moveTabInPane);
  const splitPaneWithTab = useUi((s) => s.splitPaneWithTab);
  const toggleZoomPane = useUi((s) => s.toggleZoomPane);
  // The single-pane view already shows one pane full size, so a zoom has no effect there.
  const zoomed = useUi((s) => {
    const workspace = !singlePane && workspaceId ? s.workspaces.find((w) => w.id === workspaceId) : undefined;
    return workspace ? zoomedPaneOf(workspace, s.zoomedPanes) === pane.id : false;
  });
  const moveTab = (fromIndex: number, toIndex: number) =>
    moveTabInPane(pane.id, fromIndex, toIndex);
  const activeTabId = pane.activeTabId ?? pane.tabs[pane.tabs.length - 1]?.id ?? null;
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const drag = useTabDrag();
  const draggingId = drag?.paneId === pane.id ? drag.tabId : null;
  // A tab removed mid-drag (its session ended, a sync dropped it) never delivers dragend: end the drag
  // here so the drop targets do not stay over the terminals.
  const draggedTabGone = draggingId !== null && !pane.tabs.some((tab) => tab.id === draggingId);
  useEffect(() => {
    if (draggedTabGone) endTabDrag();
  }, [draggedTabGone]);
  const paneId = pane.id;
  useEffect(
    () => () => {
      if (currentTabDrag()?.paneId === paneId) endTabDrag();
    },
    [paneId],
  );
  const [dropHint, setDropHint] = useState<{ id: string; edge: "before" | "after" } | null>(null);
  const bindings = useKeybindings();
  const kbLabel = (id: KeybindingActionId) => formatBinding(bindings[id]);

  const renameTab = async (id: string, title: string) => {
    let draft = title;
    let failure = "";
    for (;;) {
      const next = await promptText(failure || tr("重命名标签页（留空恢复自动标题）："), draft);
      if (next === null) return;
      const trimmed = sanitizeRemoteTabTitle(next);
      const current = pane.tabs.find((t) => t.id === id);
      // An empty name clears the custom title and returns the tab to OSC titles.
      if (!trimmed && !(current && hasCustomTitle(current))) return;
      const tabId = current?.tabId;
      // The decision time orders this rename or clear against other devices' decisions.
      const at = Date.now();
      if (!tabId) {
        if (trimmed) applyAuthoritativeTabTitle(id, trimmed, at);
        else clearAuthoritativeTabTitle(id, "", at);
        return;
      }
      try {
        const info = await terminalApi.renameTab(tabId, trimmed, at);
        // The backend may clamp a skewed clock: keep the time it recorded.
        const recordedAt = info.titleChangedAt || at;
        if (trimmed) applyAuthoritativeTabTitle(id, info.title || trimmed, recordedAt);
        else clearAuthoritativeTabTitle(id, info.title ?? "", recordedAt);
        return;
      } catch (error) {
        draft = trimmed;
        failure = tr("重命名失败：{value}", { value: describeError(error) });
      }
    }
  };

  const openTabMenu = (t: (typeof pane.tabs)[number], x: number, y: number) => {
    const items: MenuItem[] = [];
    if (t.kind === "terminal") {
      items.push({
        kind: "item",
        label: tr("重命名"),
        icon: <IconEdit size={12} />,
        onSelect: () => void renameTab(t.id, t.title),
      });
    }
    const running = t.kind === "terminal" && Boolean(t.tabId) && !t.dead && !t.exited;
    items.push({
      kind: "item",
      label: tr("关闭标签页"),
      icon: <IconClose size={12} />,
      hint: closeActionHint(t),
      disabled: !t.closable,
      onSelect: () => void requestCloseTab(t.id),
    });
    const tabIndex = pane.tabs.findIndex((candidate) => candidate.id === t.id);
    items.push({
      kind: "item",
      label: tr("左移标签页"),
      icon: <IconChevronLeft size={12} />,
      accel: "Ctrl+Shift+←",
      disabled: tabIndex <= 0,
      onSelect: () => moveTab(tabIndex, tabIndex - 1),
    });
    items.push({
      kind: "item",
      label: tr("右移标签页"),
      icon: <IconChevronRight size={12} />,
      accel: "Ctrl+Shift+→",
      disabled: tabIndex < 0 || tabIndex >= pane.tabs.length - 1,
      onSelect: () => moveTab(tabIndex, tabIndex + 1),
    });
    const closableLeft = pane.tabs.slice(0, Math.max(0, tabIndex)).filter((candidate) => candidate.closable);
    const closableRight = pane.tabs.slice(tabIndex + 1).filter((candidate) => candidate.closable);
    items.push({
      kind: "item",
      label: tr("关闭左侧全部标签页"),
      icon: <IconClose size={12} />,
      disabled: closableLeft.length === 0,
      onSelect: () => void requestCloseTabs(closableLeft.map((candidate) => candidate.id)),
    });
    items.push({
      kind: "item",
      label: tr("关闭右侧全部标签页"),
      icon: <IconClose size={12} />,
      disabled: closableRight.length === 0,
      onSelect: () => void requestCloseTabs(closableRight.map((candidate) => candidate.id)),
    });
    if (moveTargets.length > 0 || !singlePane) items.push({ kind: "separator" });
    for (const target of moveTargets) {
      items.push({
        kind: "item",
        label: tr("移到{label}", { label: target.label }),
        icon: <IconChevronRight size={12} />,
        onSelect: () => moveTabToPane(t.id, target.paneId),
      });
    }
    if (!singlePane) {
      // Keyboard equivalent of dropping the tab on this pane's right or bottom edge.
      const alone = pane.tabs.length < 2;
      for (const { edge, label, Icon } of [
        { edge: "right", label: tr("移到右侧新窗格"), Icon: IconSplitV },
        { edge: "bottom", label: tr("移到下方新窗格"), Icon: IconSplitH },
      ] as const) {
        items.push({
          kind: "item",
          label,
          icon: <Icon size={12} />,
          hint: alone ? tr("窗格中只有一个标签页") : undefined,
          disabled: alone,
          onSelect: () => void splitPaneWithTab(t.id, pane.id, edge),
        });
      }
    }
    if (running && t.tabId) {
      const marks = stateMarkItems(t, t.tabId);
      if (marks.length > 0) items.push({ kind: "separator" }, ...marks);
    }
    if (running) {
      items.push({ kind: "separator" });
      items.push({
        kind: "item",
        label: tr("结束进程…"),
        icon: <IconStop size={12} />,
        hint: tr("终止进程，无法恢复"),
        danger: true,
        onSelect: () => void requestKillTab(t.id),
      });
    }
    setMenu({ x, y, title: t.title, items });
  };

  const tabScrollRef = useRef<HTMLDivElement>(null);
  const overflow = useTabStripOverflow(tabScrollRef, !isTouchShell(), pane.tabs.length);

  const openOverflowMenu = (x: number, y: number) => {
    setMenu({
      x,
      y,
      title: tr("全部标签页"),
      items: pane.tabs.map((t): MenuItem => {
        const Icon = tabIcon(t);
        return {
          kind: "item",
          label: t.title,
          icon: t.id === activeTabId ? <IconCheck size={12} /> : <Icon size={12} />,
          onSelect: () => setActiveTab(t.id),
        };
      }),
    });
  };
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
        <div
          className={`nx-tabstrip-scroll${overflow.left ? " has-overflow-left" : ""}${overflow.right ? " has-overflow-right" : ""}`}
          ref={tabScrollRef}
          // A tab from another pane dropped on the strip but not on a tab joins the end of this pane.
          onDragOver={(e) => {
            const source = tabDragFrom(e.dataTransfer);
            if (!source || source.paneId === pane.id || e.isDefaultPrevented()) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = "move";
          }}
          onDrop={(e) => {
            const source = tabDragFrom(e.dataTransfer);
            if (!source || source.paneId === pane.id || e.isDefaultPrevented()) return;
            e.preventDefault();
            endTabDrag();
            moveTabToPane(source.tabId, pane.id);
          }}
        >
        {pane.tabs.length === 0 && (
          <span className="px-1 text-xs text-neutral-500">{tr("该窗格暂无标签页")}</span>
        )}
        <div role="tablist" aria-label={tr("标签页")} className="flex items-center gap-[3px]">
          {pane.tabs.map((t) => {
            const Icon = tabIcon(t);
            const isActive = t.id === activeTabId;
            return (
              <TabStripItem key={t.id} tab={t} active={isActive}>
                {(unread, persistence, attention) => (
                  <div
                role="tab"
                data-tab-id={t.id}
                id={`nx-tab-${idPrefix}-${t.id}`}
                aria-selected={isActive}
                aria-controls={`nx-tab-panel-${idPrefix}-${t.id}`}
                tabIndex={isActive ? 0 : -1}
                // Touch shells move tabs from the tab menu and the palette instead of dragging.
                draggable={!isTouchShell()}
                className={`nx-tab ${isActive ? "is-active" : ""}${
                  dropHint?.id === t.id ? ` nx-drop-${dropHint.edge}` : ""
                }${draggingId === t.id ? " nx-drag-source" : ""}`}
                onClick={() => {
                  if (draggingId === t.id) return;
                  setActiveTab(t.id);
                }}
                onDragStart={(e) => {
                  beginTabDrag({ tabId: t.id, paneId: pane.id, paneTabCount: pane.tabs.length });
                  e.dataTransfer.setData(TAB_DRAG_MIME, t.id);
                  e.dataTransfer.effectAllowed = "move";
                }}
                onDragEnd={() => {
                  endTabDrag();
                  setDropHint(null);
                }}
                onDragOver={(e) => {
                  const source = tabDragFrom(e.dataTransfer);
                  if (!source || source.tabId === t.id) return;
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
                  const source = tabDragFrom(e.dataTransfer);
                  if (!source) return;
                  e.preventDefault();
                  setDropHint(null);
                  if (source.tabId === t.id) return;
                  const over = pane.tabs.findIndex((x) => x.id === t.id);
                  const rect = e.currentTarget.getBoundingClientRect();
                  const before = e.clientX < rect.left + rect.width / 2;
                  endTabDrag();
                  if (source.paneId !== pane.id) {
                    // A tab from another pane lands beside the tab it was dropped on.
                    moveTabToPane(source.tabId, pane.id, before ? over : over + 1);
                    return;
                  }
                  const from = pane.tabs.findIndex((x) => x.id === source.tabId);
                  if (from < 0 || over < 0) return;
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
                onAuxClick={(e) => {
                  if (e.button !== 1) return;
                  e.preventDefault();
                  if (t.closable) void requestCloseTab(t.id);
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  openTabMenu(t, e.clientX, e.clientY);
                }}
                title={tabTooltip(t, unread, persistence, attention)}
              >
                <span className="flex shrink-0">
                  <Icon size={13} />
                </span>
                <TabPersistenceMark tab={t} part="icon" />
                <TabAttentionBadge badge={attention} />
                {t.kind === "terminal" && !t.exited && <TabProgress tabId={t.tabId} />}
                <TabStripTitle tab={t} />
                {t.kind === "terminal" && <TabProductionBadge sessionId={t.sessionId} />}
                {t.closeError && (
                  <span className="flex shrink-0 items-center text-red-400" role="img" aria-label={tr("关闭失败：{closeError}", { closeError: t.closeError })}>
                    <IconAlert size={11} />
                  </span>
                )}
                <TabPersistenceMark tab={t} part="failure" />
                {unread && (!attention || attention.kind === "unknown") && (
                  <span
                    className="h-1.5 w-1.5 shrink-0 rounded-full bg-sky-400"
                    role="img"
                    aria-label={tr("有新输出")}
                  />
                )}
                {t.closable && (
                  <button
                    type="button"
                    className="nx-tab-close"
                    aria-label={tr("关闭标签页 {title}", { title: t.title })}
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
        {(overflow.left || overflow.right) && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            title={tr("全部标签页")}
            aria-label={tr("全部标签页")}
            aria-haspopup="menu"
            onClick={(e) => {
              const rect = e.currentTarget.getBoundingClientRect();
              openOverflowMenu(rect.left, rect.bottom);
            }}
          >
            <IconChevronDown size={13} />
          </button>
        )}
        {canNewTerminal && (
          <button
            className="nx-tab-new"
            title={tr("新建终端标签页 ({value})", { value: kbLabel("newTerminal") })}
            aria-label={tr("新建终端标签页")}
            onClick={onNewTerminal}
          >
            <IconPlus size={13} />
          </button>
        )}
        <div className="nx-spacer" />
        {singlePane && paneCount > 1 && (
          <span className="flex items-center" role="group" aria-label={tr("切换窗格")}>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              title={tr("上一个窗格")}
              aria-label={tr("上一个窗格")}
              onClick={() => onCyclePane(-1)}
            >
              <IconChevronLeft size={13} />
            </button>
            <span className="px-0.5 text-[11px] tabular-nums text-neutral-400" aria-live="polite">
              {tr("窗格 {paneNumber}/{paneCount}", { paneNumber, paneCount })}
            </span>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              title={tr("下一个窗格")}
              aria-label={tr("下一个窗格")}
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
              title={tr("向右分屏 ({value})", { value: kbLabel("splitRight") })}
              aria-label={tr("向右分屏")}
              onClick={() => onSplit("right")}
            >
              <IconSplitV size={14} />
            </button>
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm"
              disabled={!splitAllowed}
              title={splitAllowed ? tr("向下分屏 ({value})", { value: kbLabel("splitDown") }) : tr("窗口高度不足，无法向下分屏")}
              aria-label={tr("向下分屏")}
              onClick={() => onSplit("down")}
            >
              <IconSplitH size={14} />
            </button>
          </>
        )}
        {zoomed && (
          <span className="nx-pane-zoom-badge" role="status">
            {tr("已最大化")}
          </span>
        )}
        {split && !singlePane && (
          <button
            type="button"
            className={`nx-icon-btn nx-icon-btn-sm${zoomed ? " is-active" : ""}`}
            aria-pressed={zoomed}
            title={
              zoomed
                ? tr("还原窗格布局 ({value})", { value: kbLabel("toggleZoomPane") })
                : tr("最大化窗格 ({value})", { value: kbLabel("toggleZoomPane") })
            }
            aria-label={zoomed ? tr("还原窗格布局") : tr("最大化窗格")}
            onClick={() => {
              if (!active) onActivate();
              toggleZoomPane(workspaceId);
            }}
          >
            <IconMaximize size={13} />
          </button>
        )}
        {split && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            title={tr("关闭窗格，标签页移到相邻窗格 ({value})", { value: kbLabel("closePane") })}
            aria-label={tr("关闭窗格（保留标签页）")}
            onClick={onClosePane}
          >
            <IconMergeH size={14} />
          </button>
        )}
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={leftOpen ? tr("收起左侧栏 ({value})", { value: kbLabel("toggleSidebar") }) : tr("展开左侧栏 ({value})", { value: kbLabel("toggleSidebar") })}
          aria-label={leftOpen ? tr("收起左侧栏") : tr("展开左侧栏")}
          onClick={onToggleLeft}
        >
          <IconChevronLeft size={15} className={leftOpen ? "" : "rotate-180"} />
        </button>
      </div>

      <div className="min-h-0 flex-1">
        {pane.tabs.length === 0 && <WorkspaceEmpty onNew={onNewTerminal} workspaceId={workspaceId} paneId={pane.id} />}
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
      // Focusable from script so keyboard users can enter a panel without focusable content.
      tabIndex={-1}
      className="h-full min-h-0 outline-none"
    >
      <ErrorBoundary onClose={tab.closable ? () => void requestCloseTab(tab.id) : undefined}>
        <PaneForTab tab={tab} active={visible} workspaceId={workspaceId} onClose={requestCloseTab} />
      </ErrorBoundary>
    </div>
  );
}
