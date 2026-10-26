import { assetApi, dbApi, sessionApi, type SessionInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import {
  captureConnectionAttempt,
  currentConnectionEpoch,
  ensureVaultReadyFor,
  reconnectDeadHost,
  settleWorkspaceConnectionOutcome,
} from "./connection";
import { makePane, nextTabId } from "./model";
import { reconnectSessionAndWait } from "./reconnect";
import { openTerminalTab } from "./tabActions";
import type { AppTab, ConnectOutcome, RestoreWorkspaceOptions, SessionRuntimeInfo, Workspace } from "./types";
import { useUi } from "./useUi";

const inflightRestoreConnects = new Map<string, {
  workspace: Workspace;
  epoch: number;
  promise: Promise<ConnectOutcome>;
}>();

export function workspaceSessionIsDead(
  workspace: Pick<Workspace, "kind" | "sessionId">,
  sessions: SessionRuntimeInfo[],
): boolean {
  if (workspace.kind !== "session" || !workspace.sessionId) return false;
  const session = sessions.find((item) => item.id === workspace.sessionId);
  return !session || session.status === "disconnected" || session.status === "failed";
}

export function recordWorkspaceRestoreFailure(workspaceId: string, error: unknown): void {
  const workspace = useUi.getState().workspaces.find((item) => item.id === workspaceId);
  if (!workspace) return;
  settleWorkspaceConnectionOutcome(
    captureConnectionAttempt(),
    workspace,
    { ok: false, error: describeError(error) },
    workspaceId,
  );
}

export function reconnectRestoredWorkspace(
  workspaceId: string,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  const epoch = state.workspaceRuntimeEpoch;
  const workspace = state.workspaces.find((item) => item.id === workspaceId);
  const inflight = inflightRestoreConnects.get(workspaceId);
  if (inflight && workspace === inflight.workspace && epoch === inflight.epoch) return inflight.promise;
  const context = captureConnectionAttempt();
  const attempt = connectRestoredWorkspace(workspaceId, options);
  const tracked = attempt.then((outcome) => {
    if (workspace && currentConnectionEpoch(epoch)) {
      settleWorkspaceConnectionOutcome(context, workspace, outcome, workspaceId);
    }
    return outcome;
  }).finally(() => {
    if (inflightRestoreConnects.get(workspaceId)?.promise === tracked) inflightRestoreConnects.delete(workspaceId);
  });
  if (workspace) inflightRestoreConnects.set(workspaceId, { workspace, epoch, promise: tracked });
  return tracked;
}

async function connectRestoredWorkspace(
  workspaceId: string,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  const workspace = state.workspaces.find((item) => item.id === workspaceId);
  if (!workspace) return { ok: false, error: "工作区无需重新连接" };
  if (!workspace.restorePending) return reconnectDeadRestoredWorkspace(workspace, options);
  if (workspace.deletedTarget) return { ok: false, error: "原资产已删除，请从资产树选择其他目标" };
  const epoch = state.workspaceRuntimeEpoch;
  const stillPending = () => {
    const current = useUi.getState();
    return current.workspaceRuntimeEpoch === epoch &&
      current.workspaces.some((item) => item === workspace && item.restorePending &&
        item.assetId === workspace.assetId && item.quickTarget?.id === workspace.quickTarget?.id);
  };

  try {
    if (workspace.kind === "db") {
      if (!workspace.assetId) return { ok: false, error: "数据库目标已丢失，请从资产树重新连接" };
      const asset = await assetApi.get(workspace.assetId);
      if (!stillPending()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
      if (!stillPending()) return { ok: false, canceled: true };
      const { connId } = await dbApi.connect(workspace.assetId);
      if (!stillPending()) {
        await dbApi.disconnect(connId).catch(() => undefined);
        return { ok: false, canceled: true };
      }
      useUi.setState((current) => ({
        ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspaceId }),
        workspaces: current.workspaces.map((item) => item.id === workspaceId ? {
          ...item,
          connId,
          restorePending: undefined,
          panes: item.panes.map((pane) => ({
            ...pane,
            tabs: pane.tabs.map((tab) => ({
              ...tab,
              connId: tab.kind === "db" ? connId : tab.connId,
              restorePending: undefined,
            })),
          })),
        } : item),
      }));
      return { ok: true };
    }

    if (workspace.assetId && !workspace.assetId.startsWith("device/")) {
      const asset = await assetApi.get(workspace.assetId);
      if (!stillPending()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
    }
    if (!stillPending()) return { ok: false, canceled: true };
    const session = await reconnectDeadHost(workspace, { isCurrent: stillPending });
    if (!session) return { ok: false, canceled: true };
    if (!stillPending()) {
      await sessionApi.disconnect(session.id).catch(() => undefined);
      return { ok: false, canceled: true };
    }
    useUi.setState((current) => ({
      ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspaceId }),
      sessions: [...current.sessions.filter((item) => item.id !== session.id), session],
      workspaces: current.workspaces.map((item) => item.id === workspaceId ? {
        ...item,
        sessionId: session.id,
        restorePending: undefined,
        panes: item.panes.map((pane) => ({
          ...pane,
          tabs: pane.tabs.map((tab) => ({
            ...tab,
            sessionId: session.id,
            restorePending: tab.kind === "terminal" ? true : undefined,
          })),
        })),
      } : item),
    }));
    if (workspace.assetKind !== "device" && !workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) {
      if (options?.preserveUserFocus) addRestoredTerminalWithoutFocus(workspaceId, session);
      else await openTerminalTab(session);
    }
    return { ok: true };
  } catch (error) {
    if (!stillPending()) return { ok: false, canceled: true };
    return { ok: false, error: describeError(error) };
  }
}

async function reconnectDeadRestoredWorkspace(
  workspace: Workspace,
  options?: RestoreWorkspaceOptions,
): Promise<ConnectOutcome> {
  const state = useUi.getState();
  if (!workspaceSessionIsDead(workspace, state.sessions)) {
    return { ok: false, error: "工作区无需重新连接" };
  }
  if (workspace.deletedTarget) return { ok: false, error: "原资产已删除，请从资产树选择其他目标" };
  const epoch = state.workspaceRuntimeEpoch;
  const existing = state.sessions.find((item) => item.id === workspace.sessionId);
  const stillCurrent = () => {
    const current = useUi.getState();
    return current.workspaceRuntimeEpoch === epoch && current.workspaces.some(
      (item) => item === workspace && !item.restorePending && item.sessionId === workspace.sessionId &&
        item.assetId === workspace.assetId && item.quickTarget?.id === workspace.quickTarget?.id,
    );
  };
  try {
    if (workspace.assetId && !workspace.assetId.startsWith("device/")) {
      const asset = await assetApi.get(workspace.assetId);
      if (!stillCurrent()) return { ok: false, canceled: true };
      if (!(await ensureVaultReadyFor(asset, epoch))) return { ok: false, canceled: true };
    }
    if (!stillCurrent()) return { ok: false, canceled: true };
    if (existing) {
      const fresh = await reconnectSessionAndWait(existing, { isCurrent: stillCurrent });
      if (!fresh || !stillCurrent()) return { ok: false, canceled: true };
      useUi.setState((current) => ({
        ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspace.id }),
        sessions: [...current.sessions.filter((item) => item.id !== fresh.id), fresh],
      }));
      return { ok: true };
    }

    const session = await reconnectDeadHost(workspace, { isCurrent: stillCurrent });
    if (!session) return { ok: false, canceled: true };
    if (!stillCurrent()) {
      await sessionApi.disconnect(session.id).catch(() => undefined);
      return { ok: false, canceled: true };
    }
    useUi.setState((current) => ({
      ...(options?.preserveUserFocus ? {} : { activeWorkspaceId: workspace.id }),
      sessions: [...current.sessions.filter((item) => item.id !== session.id), session],
      workspaces: current.workspaces.map((item) => item.id === workspace.id ? {
        ...item,
        sessionId: session.id,
        panes: item.panes.map((pane) => ({
          ...pane,
          tabs: pane.tabs.map((tab) => {
            if (tab.kind !== "terminal") return { ...tab, sessionId: session.id, restorePending: undefined };
            if (tab.containerId) {
              return {
                ...tab,
                sessionId: session.id,
                tabId: undefined,
                restorePending: true,
                dead: undefined,
                exited: true,
              };
            }
            return { ...tab, sessionId: session.id, restorePending: undefined, dead: false };
          }),
        })),
      } : item),
    }));
    if (workspace.assetKind !== "device" && !workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))) {
      if (options?.preserveUserFocus) addRestoredTerminalWithoutFocus(workspace.id, session);
      else await openTerminalTab(session);
    }
    return { ok: true };
  } catch (error) {
    if (!stillCurrent()) return { ok: false, canceled: true };
    return { ok: false, error: describeError(error) };
  }
}

function addRestoredTerminalWithoutFocus(workspaceId: string, session: SessionInfo): void {
  useUi.setState((current) => ({
    workspaces: current.workspaces.map((workspace) => {
      if (
        workspace.id !== workspaceId || workspace.sessionId !== session.id ||
        workspace.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal"))
      ) {
        return workspace;
      }
      const panes = workspace.panes.length > 0 ? workspace.panes : [makePane()];
      const activePane = panes.find((pane) => pane.id === workspace.activePaneId) ?? panes[0];
      const tab: AppTab = {
        id: nextTabId(`term-${session.id}`),
        kind: "terminal",
        title: "终端 1",
        sessionId: session.id,
        closable: true,
      };
      return {
        ...workspace,
        panes: panes.map((pane) => pane.id === activePane.id
          ? { ...pane, tabs: [...pane.tabs, tab] }
          : pane),
      };
    }),
  }));
}
