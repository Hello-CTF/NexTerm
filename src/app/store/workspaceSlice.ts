import { dirtyFileEditors } from "../../features/files/editorGuards";
import { aiTabRunControls, confirmAiRunCancellation, runningAiTabs as getRunningAiTabs, clearAiTabRuns } from "../../features/ai/runOwnership";
import { splitAllowedForHeight } from "../../features/terminal/workspaceLayout";
import { closedTabEntryFromTab, useClosedTabs } from "../closedTabs";
import { reorderWorkspaces } from "../workspaceOrdering";
import { ALL_PROJECTS, reconcileActiveWorkspaceForFocus } from "../projectFocus";
import {
  collectWorkspaceClose,
  confirmDirtyEditors,
  confirmWorkspaceClose,
  disconnectDbTabs,
  executeWorkspaceClose,
  pushWorkspaceCloseToast,
  reclaimTerminals,
  reconcileTerminalCloseState,
  type WorkspaceCloseSummary,
} from "./closeActions";
import {
  explicitWorkspaceMatchesTab,
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
import type { SqlDraft, UiGet, UiSet, UiState } from "./types";

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
            splitRatio: 0.5,
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
          get().pushToast("error", "停止未确认：运行状态不可用，标签已保留");
          return false;
        }
        if (!(await confirmAiRunCancellation(cancelAndConfirm))) return false;
        if (get().workspaceRuntimeEpoch !== epoch) return false;
      }

      const outcome = await executeWorkspaceClose(closePlan);
      if (get().workspaceRuntimeEpoch !== epoch) return false;
      if (outcome.failed > 0) {
        pushWorkspaceCloseToast(target.title, outcome, false);
        return false;
      }
      if (outcome.dbFailed > 0) {
        get().pushToast(
          "error",
          `「${target.title}」保持打开：${outcome.dbFailed}/${closePlan.dbConnIds.length} 个数据库断开失败：${outcome.dbError}`,
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
          `「${target.title}」保持打开：关闭期间新增了未保存内容或其它标签，请处理后再次关闭`,
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

    splitWorkspace: (workspaceId) => {
      const st = get();
      const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
      if (!id) return;
      const w = st.workspaces.find((x) => x.id === id);
      if (!w || w.panes.length >= 2) return;
      const pane = makePane();
      set((s2) => ({
        workspaces: s2.workspaces.map((x) =>
          x.id === id ? { ...x, panes: [...x.panes, pane], activePaneId: pane.id } : x,
        ),
      }));
      const session = st.sessions.find((x) => x.id === w.sessionId);
      if (session) void openTerminalTab(session, undefined, pane.id);
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
        get().pushToast("info", "标签与目标工作区不一致");
        return;
      }
      const id = resolveWorkspaceId(tab, st, workspaceId);
      if (workspaceId !== undefined && id !== workspaceId) {
        get().pushToast("info", "标签与目标工作区不一致");
        return;
      }
      const w = get().workspaces.find((x) => x.id === id);
      if (!w) return;
      if (w.panes.length >= 2) {
        get().addTab(tab, w.panes[1].id);
        return;
      }
      const height = typeof window === "undefined" ? Number.POSITIVE_INFINITY : window.innerHeight;
      if (!splitAllowedForHeight(height)) {
        get().addTab(tab, w.activePaneId ?? w.panes[0]?.id);
        get().pushToast("info", "窗口高度不足，已在当前栏打开");
        return;
      }
      const pane = makePane();
      set((s2) => ({
        workspaces: s2.workspaces.map((x) =>
          x.id === id ? { ...x, panes: [x.panes[0], pane], activePaneId: pane.id } : x,
        ),
      }));
      get().addTab(tab, pane.id);
    },

    unsplitWorkspace: async (paneId, workspaceId) => {
      const st = get();
      const id = workspaceId ?? st.activeWorkspaceId ?? st.workspaces[st.workspaces.length - 1]?.id;
      if (!id) return;
      const w = st.workspaces.find((x) => x.id === id);
      if (!w || w.panes.length < 2) return;
      const target = w.panes.find((p) => p.id === (paneId ?? w.activePaneId)) ?? w.panes[1];
      const keep = w.panes.filter((p) => p.id !== target.id);
      if (keep.length === 0) return;
      if (!(await confirmDirtyEditors(target.tabs, "取消分屏"))) return;
      if (!(await reclaimTerminals(target.tabs, "这个面板", "取消分屏", w.assetKind))) return;
      if (!(await disconnectDbTabs(target.tabs))) return;
      set((s2) => {
        const workspaces = s2.workspaces.map((workspace) =>
          workspace.id === id ? { ...workspace, panes: keep, activePaneId: keep[0].id } : workspace,
        );
        return {
          workspaces,
          ...(s2.broadcast
            ? { broadcast: pruneBroadcastToWorkspaces(s2.broadcast, workspaces) }
            : {}),
        };
      });
    },

    setActivePane: (paneId, workspaceId) =>
      set((st) => {
        const id = workspaceId ?? st.activeWorkspaceId;
        return {
          workspaces: st.workspaces.map((w) =>
            w.id === id && w.panes.some((p) => p.id === paneId) ? { ...w, activePaneId: paneId } : w,
          ),
        };
      }),

    setSplitRatio: (ratio, workspaceId) =>
      set((st) => {
        const id = workspaceId ?? st.activeWorkspaceId;
        const clamped = Math.min(0.85, Math.max(0.15, ratio));
        return {
          workspaces: st.workspaces.map((w) =>
            w.id === id ? { ...w, splitRatio: clamped } : w,
          ),
        };
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
