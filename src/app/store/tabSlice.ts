import { terminalApi, dbApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import {
  clearUnknownFileEditorRename,
  fileEditorCaseInsensitive,
  unknownFileEditorRename,
} from "../../features/files/editorGuards";
import { moveItem } from "../tabReorder";
import { clearTabUnread } from "../unreadStore";
import { closedTabEntryFromTab, useClosedTabs } from "../closedTabs";
import {
  explicitWorkspaceMatchesTab,
  findTabLocation,
  isWorkspaceToolTab,
  makePane,
  migrateToolTabs,
  migrateWorkspaceFileLocators,
  nextBroadcastGeneration,
  pickPaneForTab,
  pruneBroadcastToWorkspaces,
  rebindWorkspaceSession,
  sameTab,
} from "./model";
import { rejectPendingFileLocator, resolveWorkspaceId } from "./tabActions";
import type { UiGet, UiSet, UiState } from "./types";

export function createTabSlice(set: UiSet, get: UiGet) {
  return {
    broadcast: null,
    setActiveTab: (id) => {
      clearTabUnread(id);
      set((st) => {
        const hit = findTabLocation(st.workspaces, st.activeWorkspaceId, id);
        if (!hit) return {};
        return {
          activeWorkspaceId: hit.workspace.id,
          workspaces: st.workspaces.map((x) =>
            x.id === hit.workspace.id
              ? {
                  ...x,
                  activePaneId: hit.pane.id,
                  panes: x.panes.map((q) =>
                    q.id === hit.pane.id ? { ...q, activeTabId: id } : q,
                  ),
                }
              : x,
          ),
        };
      });
    },

    addTab: (tab, paneId) => {
      if (rejectPendingFileLocator(tab)) return;
      const paneWorkspace = paneId
        ? get().workspaces.find((w) => w.panes.some((p) => p.id === paneId))
        : undefined;
      if (paneId && (!paneWorkspace || !explicitWorkspaceMatchesTab(tab, paneWorkspace))) {
        get().pushToast("info", "标签与目标工作区不一致");
        return;
      }
      if (tab.kind === "background" && !tab.sessionId && paneWorkspace?.kind !== "session") {
        get().pushToast("info", "后台会话需要明确的主机工作区");
        return;
      }

      const wsId = resolveWorkspaceId(tab, get(), paneWorkspace?.id);
      if (isWorkspaceToolTab(tab)) {
        const current = get();
        const migrated = migrateToolTabs(current.workspaces, current.aiBoards);
        if (migrated !== current.workspaces) set({ workspaces: migrated });
      }
      const w = get().workspaces.find((x) => x.id === wsId);
      if (!w) return;
      let scopedTab = tab;
      if (tab.kind === "background") {
        const sessionId = tab.sessionId ?? paneWorkspace?.sessionId;
        if (w.kind !== "session" || !sessionId || w.sessionId !== sessionId) {
          get().pushToast("info", "后台会话需要明确的主机工作区");
          return;
        }
        scopedTab = {
          ...tab,
          id: `background-${w.id}`,
          sessionId,
          ...(w.assetId ? { assetId: w.assetId } : {}),
        };
      }

      const panes = w.panes.length ? w.panes : [makePane()];
      const target = paneId
        ? panes.find((p) => p.id === paneId)
        : pickPaneForTab(panes, w.activePaneId, scopedTab);
      if (!target) {
        get().pushToast("info", "标签与目标工作区不一致");
        return;
      }

      const caseInsensitive = scopedTab.sessionId
        ? fileEditorCaseInsensitive(get().sessions.find((s) => s.id === scopedTab.sessionId)?.kind ?? "")
        : false;
      for (const p of w.panes) {
        const exists = p.tabs.find((t) => sameTab(t, scopedTab, caseInsensitive));
        if (!exists) continue;
        const tabPatch =
          scopedTab.kind === "history" && scopedTab.assetId === undefined
            ? { ...scopedTab, assetId: exists.assetId }
            : scopedTab;
        set((st) => ({
          activeWorkspaceId: w.id,
          workspaces: st.workspaces.map((x) =>
            x.id === w.id
              ? {
                  ...x,
                  activePaneId: p.id,
                  panes: x.panes.map((q) =>
                    q.id === p.id
                      ? {
                          ...q,
                          tabs: q.tabs.map((t) =>
                            t.id === exists.id ? { ...t, ...tabPatch, id: exists.id } : t,
                          ),
                          activeTabId: exists.id,
                        }
                      : q,
                  ),
                }
              : x,
          ),
        }));
        return;
      }
      set((st) => ({
        activeWorkspaceId: wsId,
        workspaces: st.workspaces.map((x) =>
          x.id === wsId
            ? {
                ...x,
                activePaneId: target.id,
                panes: panes.map((q) =>
                  q.id === target.id
                    ? { ...q, tabs: [...q.tabs, scopedTab], activeTabId: scopedTab.id }
                    : q,
                ),
              }
            : x,
        ),
      }));
    },

    closeTab: async (id, mode, suppressErrorToast) => {
      const st = get();
      const hit = findTabLocation(st.workspaces, st.activeWorkspaceId, id);
      if (!hit) return false;
      const target = hit.tab;
      const wsId = hit.workspace.id;
      const paneId = hit.pane.id;
      const closedTabGeneration = useClosedTabs.getState().stack.generation;
      let closeMode: "kill" | "detach" | undefined;
      let detachConfirmed = false;
      let remoteCloseUnknown = false;
      let remote = false;
      if (target.tabId) {
        try {
          const kind = st.sessions.find((session) => session.id === target.sessionId)?.kind
            ?? st.workspaces.find((workspace) => workspace.id === wsId)?.assetKind;
          remote = !target.containerId && (kind === "ssh" || kind === "device");
          // A regular remote PTY ends with its connection and can never run in the background, whatever was asked.
          const regularRemote = remote && (target.durable === false || (target.durable === undefined && target.retainable === false));
          closeMode = regularRemote && target.exited !== true ? "kill" : (mode ?? (remote && target.exited !== true ? "detach" : undefined));
          if (remote) {
            await terminalApi.closeTab(target.tabId, closeMode, target.sessionId);
          } else {
            await terminalApi.closeTab(target.tabId, closeMode);
          }
          detachConfirmed = closeMode === "detach";
        } catch (e) {
          const code = (e as { code?: string } | null)?.code;
          const endedTab = target.kind === "terminal" && (
            code === "not_found" || (code === "disconnected" && (target.dead || target.exited))
          );
          if (!endedTab) {
            get().updateTab(id, { closeError: describeError(e) });
            if (!suppressErrorToast) get().pushToast("error", `终端操作失败：${describeError(e)}`);
            void get().resyncSessions({ silent: true });
            return false;
          }
          if (remote && closeMode === "detach" && (code === "not_found" || code === "disconnected")) {
            remoteCloseUnknown = true;
          }
        }
      } else if (target.kind === "db" && target.connId) {
        try {
          await dbApi.disconnect(target.connId);
        } catch (e) {
          get().updateTab(id, { closeError: describeError(e) });
          get().pushToast("error", `数据库断开失败：${describeError(e)}`);
          return false;
        }
      }
      set((s2) => {
        const workspaces = s2.workspaces.map((w) => {
          if (w.id !== wsId) return w;
          const pane = w.panes.find((p) => p.id === paneId);
          if (!pane) return w;
          const tabs = pane.tabs.filter((t) => t.id !== id);
          const dropPane = tabs.length === 0 && w.panes.length > 1;
          const panes = dropPane
            ? w.panes.filter((p) => p.id !== paneId)
            : w.panes.map((p) =>
                p.id === paneId
                  ? {
                      ...p,
                      tabs,
                      activeTabId: p.activeTabId === id ? (tabs[tabs.length - 1]?.id ?? null) : p.activeTabId,
                    }
                  : p,
              );
          return {
            ...w,
            panes,
            activePaneId: dropPane ? (panes[0]?.id ?? w.activePaneId) : w.activePaneId,
          };
        });
        return {
          workspaces,
          ...(s2.broadcast
            ? { broadcast: pruneBroadcastToWorkspaces(s2.broadcast, workspaces) }
            : {}),
        };
      });
      const closedEntry = closedTabEntryFromTab(target, {
        workspaceId: wsId,
        detached: detachConfirmed,
      });
      if (closedEntry) useClosedTabs.getState().capture(closedEntry, closedTabGeneration);
      if (remoteCloseUnknown && !suppressErrorToast) {
        get().pushToast("info", "本地标签已关闭；远端持久终端可能仍在运行，可在后台会话中查看");
      }
      void get().resyncSessions({ silent: true });
      return true;
    },

    updateTab: (id, patch) =>
      set((st) => {
        let found = false;
        let from: string | undefined;
        let workspaceId: string | undefined;
        for (const w of st.workspaces) {
          for (const p of w.panes) {
            const t = p.tabs.find((x) => x.id === id);
            if (t) {
              found = true;
              from = t.sessionId;
              workspaceId = w.id;
            }
          }
        }
        if (!found) return st;
        const to =
          typeof patch.sessionId === "string" && from !== undefined && patch.sessionId !== from
            ? patch.sessionId
            : undefined;
        const workspaces =
          to !== undefined && workspaceId !== undefined
            ? rebindWorkspaceSession(st.workspaces, workspaceId, from, to)
            : st.workspaces;
        return {
          workspaces: workspaces.map((w) => {
            let changed = false;
            const panes = w.panes.map((p) => {
              if (!p.tabs.some((t) => t.id === id)) return p;
              changed = true;
              return {
                ...p,
                tabs: p.tabs.map((t) => t.id === id
                  ? { ...t, ...patch, restorePending: patch.tabId ? undefined : (patch.restorePending ?? t.restorePending) }
                  : t),
              };
            });
            return changed ? { ...w, panes } : w;
          }),
        };
      }),

    migrateFileEditorLocators: (sessionId, from, to, kind) =>
      set((st) => migrateWorkspaceFileLocators(st, sessionId, from, to, kind)),

    resolveUnknownFileEditorRename: (sessionId, path, renamed) =>
      set((st) => {
        const operation = unknownFileEditorRename(sessionId, path);
        if (!operation) return st;
        if (!renamed) {
          clearUnknownFileEditorRename(operation.token);
          return st;
        }
        const next = migrateWorkspaceFileLocators(
          st,
          sessionId,
          operation.from,
          operation.to,
          operation.kind,
        );
        clearUnknownFileEditorRename(operation.token);
        return next;
      }),

    moveTabInPane: (paneId, fromIndex, toIndex) =>
      set((st) => {
        for (const w of st.workspaces) {
          const pane = w.panes.find((p) => p.id === paneId);
          if (!pane) continue;
          const tabs = moveItem(pane.tabs, fromIndex, toIndex);
          if (tabs === pane.tabs) return {};
          return {
            workspaces: st.workspaces.map((x) =>
              x.id === w.id
                ? { ...x, panes: x.panes.map((q) => (q.id === paneId ? { ...q, tabs } : q)) }
                : x,
            ),
          };
        }
        return {};
      }),
    setBroadcast: (b) =>
      set({
        broadcast: b
          ? {
              origin: b.origin,
              targetIds: [...new Set(b.targetIds)],
              generation: nextBroadcastGeneration(),
              results: {},
            }
          : null,
      }),
    reportBroadcastTarget: (generation, update) =>
      set((state) => {
        const broadcast = state.broadcast;
        if (
          !broadcast ||
          broadcast.generation !== generation ||
          !broadcast.targetIds.includes(update.targetId)
        ) {
          return state;
        }
        return {
          broadcast: {
            ...broadcast,
            results: {
              ...broadcast.results,
              [update.targetId]: { status: update.status, reason: update.reason },
            },
          },
        };
      }),
  } satisfies Partial<UiState>;
}
