import { useClosedTabs, type ClosedTabEntry } from "../closedTabs";
import { findTabLocation } from "./model";
import type { AppTab, ConnectOutcome, Workspace } from "./types";
import { useUi } from "./useUi";
import { reconnectRestoredWorkspace } from "./workspaceRestore";

// restoreClosedTab pops the most recent (or keyed) closed-tab entry and lands it in a
// workspace chosen before the pop, so a failed placement never consumes the entry.
// Reattach entries are pinned to the workspace owning entry.sessionId — the live
// process is bound to that session, so a workspace re-bound to another session is
// never a valid target — and require the owning session to be connected right now.
// Killed terminals and file targets come back as restorePending tabs that only
// reconnect on an explicit user action.
export async function restoreClosedTab(key?: string): Promise<boolean> {
  const closedTabs = useClosedTabs.getState();
  const entry = key === undefined
    ? closedTabs.stack.entries[0]
    : closedTabs.stack.entries.find((candidate) => candidate.key === key);
  if (!entry) return false;
  const reattach = entry.kind === "detached-terminal" && entry.tabKind === "terminal";
  if (reattach) {
    const session = useUi.getState().sessions.find((s) => s.id === entry.sessionId);
    if (!session || session.status !== "connected") {
      useUi.getState().pushToast("error", "原会话未连接，请重新连接后再恢复后台终端");
      return false;
    }
  }
  const target = resolveClosedTabTarget(entry, reattach);
  const restored = useClosedTabs.getState().restore(key);
  if (!restored) return false;
  applyRestoredClosedTab(restored.tab, target);
  if (!findTabLocation(useUi.getState().workspaces, useUi.getState().activeWorkspaceId, restored.tab.id)) {
    useClosedTabs.getState().capture(entry, useClosedTabs.getState().stack.generation);
    return false;
  }
  return true;
}

function resolveClosedTabTarget(entry: ClosedTabEntry, reattach: boolean): Workspace | undefined {
  const st = useUi.getState();
  const hostsSession = (w: Workspace | undefined): w is Workspace =>
    Boolean(w && w.kind === "session" && w.sessionId);
  if (reattach && entry.sessionId) {
    const pinned = st.workspaces.find((w) => w.kind === "session" && w.sessionId === entry.sessionId);
    if (pinned) return pinned;
    const session = st.sessions.find((s) => s.id === entry.sessionId);
    const workspaceId = st.ensureWorkspace({
      kind: "session",
      sessionId: entry.sessionId,
      ...(entry.assetId ? { assetId: entry.assetId } : {}),
      title: session?.name ?? "会话",
    });
    return useUi.getState().workspaces.find((w) => w.id === workspaceId);
  }
  let target = entry.workspaceId ? st.workspaces.find((w) => w.id === entry.workspaceId) : undefined;
  if (!hostsSession(target) && entry.sessionId) {
    target = st.workspaces.find((w) => w.kind === "session" && w.sessionId === entry.sessionId);
  }
  if (!hostsSession(target) && entry.assetId) {
    target = st.workspaces.find((w) => w.kind === "session" && w.assetId === entry.assetId && w.sessionId);
  }
  if (!hostsSession(target) && (entry.sessionId || entry.assetId)) {
    const session = entry.sessionId ? st.sessions.find((s) => s.id === entry.sessionId) : undefined;
    const workspaceId = st.ensureWorkspace({
      kind: "session",
      ...(entry.sessionId ? { sessionId: entry.sessionId } : {}),
      ...(entry.assetId ? { assetId: entry.assetId } : {}),
      title: session?.name ?? "会话",
    });
    target = useUi.getState().workspaces.find((w) => w.id === workspaceId);
  }
  return target;
}

function applyRestoredClosedTab(tab: AppTab, target: Workspace | undefined): void {
  const placed = target
    ? {
        ...tab,
        ...(target.sessionId ? { sessionId: target.sessionId } : {}),
        ...(target.assetId ? { assetId: target.assetId } : {}),
      }
    : tab;
  useUi.getState().addTab(placed);
}

export async function reconnectRestoredTab(tabId: string): Promise<ConnectOutcome> {
  const st = useUi.getState();
  const hit = findTabLocation(st.workspaces, st.activeWorkspaceId, tabId);
  if (!hit) return { ok: false, error: "标签页不存在" };
  const workspace = hit.workspace;
  if (workspace.deletedTarget) return { ok: false, error: "原资产已删除，请从资产树选择其他目标" };
  const session = st.sessions.find((s) => s.id === workspace.sessionId);
  const live = session && ["connecting", "connected", "reconnecting"].includes(session.status);
  if (live) {
    clearRestoredTabPending(tabId, session.id);
    return { ok: true };
  }
  const outcome = await reconnectRestoredWorkspace(workspace.id);
  if (!outcome.ok) return outcome;
  const current = useUi.getState().workspaces.find((w) => w.id === workspace.id);
  if (current) clearRestoredTabPending(tabId, current.sessionId);
  return { ok: true };
}

function clearRestoredTabPending(tabId: string, sessionId: string | undefined): void {
  useUi.setState((state) => ({
    workspaces: state.workspaces.map((w) => {
      if (!w.panes.some((pane) => pane.tabs.some((tab) => tab.id === tabId))) return w;
      return {
        ...w,
        panes: w.panes.map((p) => ({
          ...p,
          tabs: p.tabs.map((t) =>
            t.id === tabId
              ? { ...t, ...(sessionId ? { sessionId } : {}), restorePending: undefined }
              : t,
          ),
        })),
      };
    }),
  }));
}
