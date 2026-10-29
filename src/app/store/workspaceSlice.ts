import { dirtyFileEditors } from "../../features/files/editorGuards";
import { aiTabRunControls, confirmAiRunCancellation, runningAiTabs as getRunningAiTabs, clearAiTabRuns } from "../../features/ai/runOwnership";
import {
  SPLIT_MIN_COLS,
  SPLIT_MIN_ROWS,
  splitAllowedForHeight,
  splitFitsPane,
} from "../../features/terminal/workspaceLayout";
import { closedTabEntryFromTab, useClosedTabs } from "../closedTabs";
import { reorderWorkspaces } from "../workspaceOrdering";
import { ALL_PROJECTS, reconcileActiveWorkspaceForFocus } from "../projectFocus";
import {
  collectWorkspaceClose,
  confirmWorkspaceClose,
  executeWorkspaceClose,
  pushWorkspaceCloseToast,
  reconcileTerminalCloseState,
  type WorkspaceCloseSummary,
} from "./closeActions";
import {
  explicitWorkspaceMatchesTab,
  findTabLocation,
  makePane,
  nextTabId,
  normalizeWorkspacePanes,
  omitAiBoard,
  omitWorkspaceRuntime,
  pruneBroadcastToWorkspaces,
  rebindWorkspaceSession,
  workspaceFieldPatch,
} from "./model";
import { openTerminalTab, rejectPendingFileLocator, resolveWorkspaceId } from "./tabActions";
import { fallbackPaneAfterClose, treeWithoutPane, withLayoutTree, workspaceLayoutTree } from "../paneLayout";
import { edgeSplit, EDGE_LABEL } from "../tabDropZone";
import {
  closePane as closeTreePane,
  focusNeighbor,
  paneRects,
  resolveActivePane,
  setSplitRatio as setTreeSplitRatio,
  splitPane as splitTreePane,
  SPLIT_TREE_MAX_DEPTH,
  type SplitNode,
} from "../splitTree";
import type { SqlDraft, UiGet, UiSet, UiState, Workspace } from "./types";

function windowHeight(): number {
  return typeof window === "undefined" ? Number.POSITIVE_INFINITY : window.innerHeight;
}

// paneSize is the pane's on-screen size, or its share of the window when it is not laid out yet.
function paneSize(paneId: string, tree: SplitNode): { width: number; height: number } {
  if (typeof document !== "undefined") {
    const element = document.querySelector<HTMLElement>(`[data-pane-id="${CSS.escape(paneId)}"]`);
    const box = element?.getBoundingClientRect();
    if (box && box.width > 0 && box.height > 0) return { width: box.width, height: box.height };
  }
  const rect = paneRects(tree).find((entry) => entry.paneId === paneId)?.rect;
  if (typeof window === "undefined" || !rect) return { width: Number.POSITIVE_INFINITY, height: Number.POSITIVE_INFINITY };
  return { width: window.innerWidth * rect.width, height: window.innerHeight * rect.height };
}

// splitRefusal explains why `paneId` cannot be split now (window too short, pane too small), or null.
// `label` names the direction in the message ("向右", "向上", ...).
function splitRefusal(direction: "right" | "down", label: string, paneId: string, tree: SplitNode): string | null {
  if (direction === "down" && !splitAllowedForHeight(windowHeight())) return `窗口高度不足，无法${label}分屏`;
  if (!splitFitsPane(direction, paneSize(paneId, tree))) {
    return `窗格太小，无法${label}分屏（每侧至少需要 ${SPLIT_MIN_COLS} 列 × ${SPLIT_MIN_ROWS} 行）`;
  }
  return null;
}

const SPLIT_DEPTH_REFUSAL = `分屏最多嵌套 ${SPLIT_TREE_MAX_DEPTH} 层，已保持当前布局`;

// otherPaneFor picks where "open in the other pane" lands: the pane below the active one, else the
// nearest pane in any direction, else none (the caller splits).
function otherPaneFor(workspace: Workspace): string | null {
  const tree = workspaceLayoutTree(workspace);
  if (!tree || workspace.panes.length < 2) return null;
  const active = resolveActivePane(tree, workspace.activePaneId);
  for (const direction of ["down", "right", "up", "left"] as const) {
    const neighbor = focusNeighbor(tree, active, direction);
    if (neighbor) return neighbor;
  }
  return workspace.panes.find((pane) => pane.id !== active)?.id ?? null;
}

export function createWorkspaceSlice(set: UiSet, get: UiGet) {
  return {
    workspaces: [],
    activeWorkspaceId: null,
    activeProject: ALL_PROJECTS,
    projectInventory: null,
    accountEpoch: 0,
    knownAssetIds: [],
    knownAssetsEpoch: -1,
    advanceAccountEpoch: () => set((state) => ({ accountEpoch: state.accountEpoch + 1 })),
    ensureWorkspace: (spec) => {
      const { workspaces, activeWorkspaceId } = get();
      const exists = workspaces.find((w) => {
        if (spec.sessionId !== undefined) return w.sessionId === spec.sessionId;
        if (spec.connId !== undefined) return w.connId === spec.connId;
        return w.kind === spec.kind;
      });
      if (exists) {
        const patch = workspaceFieldPatch(exists, spec);
        const activeChanged = activeWorkspaceId !== exists.id;
        if (patch || activeChanged) {
          set({
            workspaces: patch
              ? workspaces.map((w) => (w.id === exists.id ? { ...w, ...patch } : w))
              : workspaces,
            activeWorkspaceId: exists.id,
          });
        }
        return exists.id;
      }
      const id = nextTabId(`ws-${spec.kind}`);
      const pane = makePane();
      set({
        workspaces: [
          ...workspaces,
          {
            ...spec,
            id,
            panes: [pane],
            activePaneId: pane.id,
            closable: true,
          },
        ],
        activeWorkspaceId: id,
      });
      return id;
    },

    setActiveWorkspace: (id) => set((state) => ({
      activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, activeWorkspaceId: id }),
    })),
    setActiveProject: (activeProject) => set((state) => ({
      activeProject,
      activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, activeProject }),
    })),
    setProjectInventory: (projectInventory) => set((state) => ({
      projectInventory,
      activeWorkspaceId: reconcileActiveWorkspaceForFocus({ ...state, projectInventory }),
    })),
    markDeletedAssetWorkspaces: (assetId) => set((st) => ({
      workspaces: st.workspaces.map((w) =>
        w.assetId === assetId ? { ...w, deletedTarget: true } : w,
      ),
    })),
    // reconcileDeletedTargets mirrors the authoritative assets list onto open
    // workspaces. A target that was present in the previous snapshot of this
    // account epoch and is now absent is marked deletedTarget (and preserved,
    // never auto-disconnected); a reappearing target clears the mark. The first
    // snapshot of an epoch is only a baseline — an asset never seen cannot be
    // proven deleted, so quick-connect targets and restore-time marks are not
    // mislabeled, and a snapshot from a previous account is ignored entirely.
    reconcileDeletedTargets: (assets, epoch) => set((st) => {
      if (st.accountEpoch !== epoch) return st;
      const present = new Set(assets.map((asset) => asset.id));
      if (st.knownAssetsEpoch !== epoch) {
        return { knownAssetsEpoch: epoch, knownAssetIds: [...present] };
      }
      let changed = false;
      const workspaces = st.workspaces.map((w) => {
        if (!w.assetId) return w;
        const shouldMark = st.knownAssetIds.includes(w.assetId) && !present.has(w.assetId);
        const shouldClear = w.deletedTarget === true && present.has(w.assetId);
        if (!shouldMark && !shouldClear) return w;
        changed = true;
        return { ...w, deletedTarget: shouldMark ? true : undefined };
      });
      const knownChanged = st.knownAssetIds.length !== present.size || st.knownAssetIds.some((id) => !present.has(id));
      if (!changed && !knownChanged) return st;
      return {
        ...(knownChanged ? { knownAssetIds: [...present] } : null),
        ...(changed ? { workspaces } : null),
      };
    }),

    closeWorkspace: async (id, mode) => {
      const sameSqlDraft = (left: SqlDraft | undefined, right: SqlDraft | undefined): boolean => {
        if (!left || !right) return left === right;
        return (
          left.text === right.text &&
          left.selection.from === right.selection.from &&
          left.selection.to === right.selection.to &&
          left.schema === right.schema &&
          left.connId === right.connId &&
          left.dbKind === right.dbKind
        );
      };
      const { workspaces } = get();
      const target = workspaces.find((w) => w.id === id);
      if (!target) return false;
      const epoch = get().workspaceRuntimeEpoch;
      const closedTabGeneration = useClosedTabs.getState().stack.generation;
      const tabs = target.panes.flatMap((p) => p.tabs);
      const summary = collectWorkspaceClose(tabs, target.assetKind, target.kind, id);
      const originalTabIds = new Set(summary.tabs.map((tab) => tab.id));
      await reconcileTerminalCloseState(tabs.map((tab) => tab.id));
      const currentTarget = get().workspaces.find((workspace) => workspace.id === id);
      if (!currentTarget) return false;
      const closePlan: WorkspaceCloseSummary = {
        ...collectWorkspaceClose(
          currentTarget.panes
            .flatMap((pane) => pane.tabs)
            .filter((tab) => originalTabIds.has(tab.id)),
          currentTarget.assetKind,
          currentTarget.kind,
          id,
        ),
        dirty: summary.dirty,
        hasSqlDraft: summary.hasSqlDraft,
        sqlDraft: summary.sqlDraft,
        runningAiTabs: summary.runningAiTabs,
        aiTabIds: summary.aiTabIds,
      };
      if (mode === "detach" && closePlan.blocked.length > 0) {
        get().pushToast(
          "error",
          `「${currentTarget.title}」有 ${closePlan.blocked.length} 个${closePlan.blockedKinds}终端不支持转入后台；工作区已保留，未结束任何进程`,
        );
        return false;
      }
      if (!(await confirmWorkspaceClose(closePlan, currentTarget.title, mode === "detach"))) return false;
      if (get().workspaceRuntimeEpoch !== epoch) return false;
      for (const tab of closePlan.runningAiTabs) {
        const cancelAndConfirm = aiTabRunControls.get(tab.id);
        if (!cancelAndConfirm) {
          get().pushToast("error", "未能确认是否已停止，标签页已保留，请稍后重试");
          return false;
        }
        if (!(await confirmAiRunCancellation(cancelAndConfirm))) return false;
        if (get().workspaceRuntimeEpoch !== epoch) return false;
      }

      const outcome = await executeWorkspaceClose(closePlan);
      if (get().workspaceRuntimeEpoch !== epoch) return false;
      const titleNow = get().workspaces.find((w) => w.id === id)?.title ?? currentTarget.title;
      if (outcome.failed > 0) {
        pushWorkspaceCloseToast(titleNow, outcome, false);
        return false;
      }
      if (outcome.dbFailed > 0) {
        get().pushToast(
          "error",
          `「${titleNow}」保持打开：${outcome.dbFailed}/${closePlan.dbConnIds.length} 个数据库断开失败：${outcome.dbError}`,
        );
      }

      const latest = get();
      const latestTarget = latest.workspaces.find((w) => w.id === id);
      if (!latestTarget) return false;
      const originalDirtyIds = new Set(summary.dirty.map((tab) => tab.id));
      const addedTabs = latestTarget.panes
        .flatMap((pane) => pane.tabs)
        .filter((tab) => !originalTabIds.has(tab.id));
      const latestRunningAiTabs = getRunningAiTabs(target.kind, id);
      const hasNewAiTasks = latestRunningAiTabs.some((tab) => {
        const original = summary.runningAiTabs.find((candidate) => candidate.id === tab.id);
        return original === undefined || original.generation !== tab.generation;
      });
      const hasNewAiTabs = (latest.aiBoards[summary.aiBoardKey]?.tabs ?? []).some(
        (tab) => !summary.aiTabIds.has(tab.id),
      );
      const addedDirty = dirtyFileEditors(
        latestTarget.panes.flatMap((pane) => pane.tabs),
        latest.sessions,
      ).filter((tab) => !originalDirtyIds.has(tab.id));
      const latestDraft = latest.workspaceRuntime[id]?.sqlDraft;
      const addedSqlDraft = !summary.hasSqlDraft && Boolean(latestDraft?.text.trim());
      const changedSqlDraft =
        summary.hasSqlDraft && Boolean(latestDraft?.text.trim()) && !sameSqlDraft(summary.sqlDraft, latestDraft);
      const hasNewContent =
        addedTabs.length > 0 ||
        addedDirty.length > 0 ||
        addedSqlDraft ||
        changedSqlDraft ||
        hasNewAiTabs ||
        hasNewAiTasks;
      const deleteWorkspace = outcome.failed === 0 && outcome.dbFailed === 0 && !hasNewContent;
      if (hasNewContent) {
        get().pushToast(
          "info",
          `「${target.title}」保持打开：关闭期间新增了未保存内容或其它标签页，请处理后再次关闭`,
        );
      }

      const nextWorkspaces = deleteWorkspace
        ? latest.workspaces.filter((w) => w.id !== id)
        : latest.workspaces.map((workspace) => {
            if (workspace.id !== id) return workspace;
            const panes = workspace.panes.map((pane) => ({
              ...pane,
              tabs: pane.tabs.filter((tab) => {
                if (tab.kind === "terminal") return !outcome.succeededTabIds.has(tab.id);
                if (tab.kind === "db" && tab.connId) return !outcome.succeededDbConnIds.has(tab.connId);
                return true;
              }),
            }));
            return normalizeWorkspacePanes({ ...workspace, panes });
          });
      const broadcast = latest.broadcast
        ? pruneBroadcastToWorkspaces(latest.broadcast, nextWorkspaces)
        : null;
      set({
        workspaces: nextWorkspaces,
        activeWorkspaceId:
          latest.activeWorkspaceId === id
            ? (nextWorkspaces[nextWorkspaces.length - 1]?.id ?? null)
            : latest.activeWorkspaceId,
        ...(latest.broadcast ? { broadcast } : {}),
        ...(deleteWorkspace && latest.aiBoards[id]
          ? { aiBoards: omitAiBoard(latest.aiBoards, id) }
          : {}),
        ...(deleteWorkspace && latest.workspaceRuntime[id]
          ? { workspaceRuntime: omitWorkspaceRuntime(latest.workspaceRuntime, id) }
          : {}),
      });
      if (deleteWorkspace) {
        // Terminal tabs were already captured by closeTab with their real close mode;
        // only the remaining restorable kinds (files, log, background) are captured here.
        for (const tab of latestTarget.panes.flatMap((pane) => pane.tabs)) {
          if (tab.kind === "terminal" || tab.kind === "db") continue;
          const entry = closedTabEntryFromTab(tab, { workspaceId: id });
          if (entry) useClosedTabs.getState().capture(entry, closedTabGeneration);
        }
        const board = latest.aiBoards[id];
        if (board) clearAiTabRuns(board.tabs.map((tab) => tab.id));
      }
      if (outcome.failed === 0) pushWorkspaceCloseToast(target.title, outcome, deleteWorkspace);
      return deleteWorkspace;
    },

    splitPane: (direction, paneId, workspaceId) => {
      const st = get();
      const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
      const w = id ? st.workspaces.find((x) => x.id === id) : undefined;
      if (!w) return null;
      const targetId = paneId ?? w.activePaneId;
      const tree = workspaceLayoutTree(w);
      if (!tree || !w.panes.some((pane) => pane.id === targetId)) return null;
      const refusal = splitRefusal(direction, direction === "right" ? "向右" : "向下", targetId, tree);
      if (refusal) {
        get().pushToast("info", refusal);
        return null;
      }
      const pane = makePane();
      const next = splitTreePane(tree, targetId, {
        dir: direction === "right" ? "row" : "col",
        side: "after",
        newPaneId: pane.id,
      });
      if (next === tree) {
        get().pushToast("info", SPLIT_DEPTH_REFUSAL);
        return null;
      }
      set((s2) => ({
        workspaces: s2.workspaces.map((x) =>
          x.id === w.id
            ? withLayoutTree({ ...x, panes: [...x.panes, pane], activePaneId: pane.id }, next)
            : x,
        ),
      }));
      const session = st.sessions.find((x) => x.id === w.sessionId);
      if (session) void openTerminalTab(session, undefined, pane.id);
      return pane.id;
    },

    openInLowerPane: (tab, workspaceId) => {
      if (rejectPendingFileLocator(tab)) return;
      const st = get();
      const explicitWorkspace =
        workspaceId !== undefined ? st.workspaces.find((w) => w.id === workspaceId) : undefined;
      if (
        workspaceId !== undefined &&
        (!explicitWorkspace || !explicitWorkspaceMatchesTab(tab, explicitWorkspace))
      ) {
        get().pushToast("info", "该标签页不属于当前工作区，未执行操作");
        return;
      }
      const id = resolveWorkspaceId(tab, st, workspaceId);
      if (workspaceId !== undefined && id !== workspaceId) {
        get().pushToast("info", "该标签页不属于当前工作区，未执行操作");
        return;
      }
      const w = get().workspaces.find((x) => x.id === id);
      if (!w) return;
      const other = otherPaneFor(w);
      if (other) {
        get().addTab(tab, other);
        return;
      }
      if (!splitAllowedForHeight(windowHeight())) {
        get().addTab(tab, w.activePaneId ?? w.panes[0]?.id);
        get().pushToast("info", "窗口高度不足，已在当前栏打开");
        return;
      }
      const tree = workspaceLayoutTree(w);
      const pane = makePane();
      const next = tree
        ? splitTreePane(tree, w.activePaneId, { dir: "col", side: "after", newPaneId: pane.id })
        : null;
      if (!next || next === tree) {
        get().addTab(tab, w.activePaneId ?? w.panes[0]?.id);
        return;
      }
      set((s2) => ({
        workspaces: s2.workspaces.map((x) =>
          x.id === id
            ? withLayoutTree({ ...x, panes: [...x.panes, pane], activePaneId: pane.id }, next)
            : x,
        ),
      }));
      get().addTab(tab, pane.id);
    },

    // closePane removes a pane from the layout but keeps every tab: its tabs move, in order, behind the
    // tabs of the pane that takes over its space, and its selected tab stays selected there. No tab
    // closes and no session ends; tab content stays mounted because it is keyed by tab id.
    closePane: (paneId, workspaceId) =>
      set((st) => {
        const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
        let changed = false;
        const workspaces = st.workspaces.map((workspace) => {
          if (workspace.id !== id || workspace.panes.length < 2) return workspace;
          const closing = workspace.panes.find((pane) => pane.id === (paneId ?? workspace.activePaneId));
          if (!closing) return workspace;
          const heirId = fallbackPaneAfterClose(workspace, closing.id);
          if (!heirId || heirId === closing.id) return workspace;
          const tree = treeWithoutPane(workspace, closing.id);
          const selected = closing.activeTabId ?? closing.tabs[closing.tabs.length - 1]?.id ?? null;
          const panes = workspace.panes
            .filter((pane) => pane.id !== closing.id)
            .map((pane) =>
              pane.id !== heirId || closing.tabs.length === 0
                ? pane
                : { ...pane, tabs: [...pane.tabs, ...closing.tabs], activeTabId: selected ?? pane.activeTabId },
            );
          changed = true;
          const next = {
            ...workspace,
            panes,
            activePaneId: workspace.activePaneId === closing.id ? heirId : workspace.activePaneId,
          };
          return tree ? withLayoutTree(next, tree) : next;
        });
        return changed ? { workspaces } : {};
      }),

    setActivePane: (paneId, workspaceId) =>
      set((st) => {
        const id = workspaceId ?? st.activeWorkspaceId;
        return {
          workspaces: st.workspaces.map((w) =>
            w.id === id && w.panes.some((p) => p.id === paneId) ? { ...w, activePaneId: paneId } : w,
          ),
        };
      }),

    focusPane: (direction, workspaceId) => {
      const st = get();
      const id = workspaceId ?? st.activeWorkspaceId;
      const w = id ? st.workspaces.find((x) => x.id === id) : undefined;
      const tree = w ? workspaceLayoutTree(w) : null;
      if (!w || !tree) return null;
      const current = resolveActivePane(tree, w.activePaneId);
      const target = focusNeighbor(tree, current, direction);
      if (!target) return null;
      get().setActivePane(target, w.id);
      return target;
    },

    setSplitRatio: (splitId, ratio, workspaceId) =>
      set((st) => {
        const id = workspaceId ?? st.activeWorkspaceId;
        let changed = false;
        const workspaces = st.workspaces.map((w) => {
          if (w.id !== id) return w;
          const tree = workspaceLayoutTree(w);
          const next = tree ? setTreeSplitRatio(tree, splitId, ratio) : tree;
          if (!next || next === tree) return w;
          changed = true;
          return withLayoutTree(w, next);
        });
        return changed ? { workspaces } : {};
      }),

    // splitPaneWithTab is a drop on a pane edge: one state change splits the pane and moves the tab into
    // the new pane, so the tab's content stays mounted (it is keyed by tab id) and no terminal reconnects.
    // A pane left empty by the move closes. A pane never splits off its only tab.
    splitPaneWithTab: (tabId, paneId, edge, workspaceId) => {
      const st = get();
      const hit = findTabLocation(st.workspaces, workspaceId ?? st.activeWorkspaceId, tabId);
      if (!hit || (workspaceId !== undefined && hit.workspace.id !== workspaceId)) return null;
      const w = hit.workspace;
      const tree = workspaceLayoutTree(w);
      if (!tree || !w.panes.some((pane) => pane.id === paneId)) return null;
      if (hit.pane.id === paneId && hit.pane.tabs.length < 2) {
        get().pushToast("info", "窗格中只有一个标签页，无需移到新窗格");
        return null;
      }
      const split = edgeSplit(edge);
      const refusal = splitRefusal(split.direction, EDGE_LABEL[edge], paneId, tree);
      if (refusal) {
        get().pushToast("info", refusal);
        return null;
      }
      const created = makePane();
      let next = splitTreePane(tree, paneId, { dir: split.dir, side: split.side, newPaneId: created.id });
      if (next === tree) {
        get().pushToast("info", SPLIT_DEPTH_REFUSAL);
        return null;
      }
      const sourceTabs = hit.pane.tabs.filter((tab) => tab.id !== tabId);
      const panes = w.panes.flatMap((pane) => {
        if (pane.id !== hit.pane.id) return [pane];
        if (sourceTabs.length === 0) return [];
        const activeTabId = pane.activeTabId === tabId ? (sourceTabs[sourceTabs.length - 1]?.id ?? null) : pane.activeTabId;
        return [{ ...pane, tabs: sourceTabs, activeTabId }];
      });
      panes.push({ ...created, tabs: [hit.tab], activeTabId: tabId });
      if (sourceTabs.length === 0) next = closeTreePane(next, hit.pane.id) ?? next;
      set((s2) => ({
        workspaces: s2.workspaces.map((x) =>
          x.id === w.id ? withLayoutTree({ ...x, panes, activePaneId: created.id }, next) : x,
        ),
        activeWorkspaceId: w.id,
      }));
      return created.id;
    },

    moveTabToPane: (tabId, paneId, index) =>
      set((st) => {
        const hit = findTabLocation(st.workspaces, st.activeWorkspaceId, tabId);
        if (!hit || hit.pane.id === paneId || !hit.workspace.panes.some((pane) => pane.id === paneId)) return {};
        const workspaces = st.workspaces.map((w) => {
          if (w.id !== hit.workspace.id) return w;
          const sourceTabs = hit.pane.tabs.filter((tab) => tab.id !== tabId);
          const moved = {
            ...w,
            activePaneId: paneId,
            panes: w.panes.map((pane) => {
              if (pane.id === hit.pane.id) {
                return {
                  ...pane,
                  tabs: sourceTabs,
                  activeTabId: pane.activeTabId === tabId ? (sourceTabs[sourceTabs.length - 1]?.id ?? null) : pane.activeTabId,
                };
              }
              if (pane.id === paneId) {
                const at = index === undefined ? pane.tabs.length : Math.min(Math.max(0, Math.trunc(index)), pane.tabs.length);
                return { ...pane, tabs: [...pane.tabs.slice(0, at), hit.tab, ...pane.tabs.slice(at)], activeTabId: tabId };
              }
              return pane;
            }),
          };
          if (sourceTabs.length > 0) return moved;
          // A pane left empty by the move closes and its neighbour takes the space.
          const tree = treeWithoutPane(w, hit.pane.id);
          const closed = { ...moved, panes: moved.panes.filter((pane) => pane.id !== hit.pane.id) };
          return tree ? withLayoutTree(closed, tree) : closed;
        });
        return { workspaces, activeWorkspaceId: hit.workspace.id };
      }),

    moveWorkspace: (fromIndex, toIndex) =>
      set((st) => {
        const workspaces = reorderWorkspaces(st.workspaces, fromIndex, toIndex);
        return workspaces === st.workspaces ? {} : { workspaces };
      }),

    updateWorkspaceSession: (workspaceId, sessionId) =>
      set((st) => ({
        workspaces: rebindWorkspaceSession(
          st.workspaces,
          workspaceId,
          st.workspaces.find((w) => w.id === workspaceId)?.sessionId,
          sessionId,
        ),
      })),
  } satisfies Partial<UiState>;
}
