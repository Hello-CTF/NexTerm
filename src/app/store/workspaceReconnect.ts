import type { SessionInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { isSessionReconnectPending, reconnectWorkspaceSession } from "./reconnect";
import { reconnectRestoredWorkspace, workspaceSessionIsDead } from "./workspaceRestore";
import type { SessionRuntimeInfo, Workspace } from "./types";
import { useUi } from "./useUi";

export type LiveSessionReconnect =
  | { kind: "reconnected"; session: SessionInfo }
  | { kind: "canceled" }
  | { kind: "pending" }
  | { kind: "missing" };

// reconnectLiveSession re-dials a session the backend still tracks and merges the fresh record into the session
// list. Errors propagate so each caller words its own failure. The terminal pane and the workspace reconnect share
// it, so both settle the workspace connection problem and join an in-flight reconnect the same way.
export async function reconnectLiveSession(
  sessionId: string,
  workspaceId?: string,
): Promise<LiveSessionReconnect> {
  const state = useUi.getState();
  const session = state.sessions.find((item) => item.id === sessionId);
  if (!session) return { kind: "missing" };
  if (isSessionReconnectPending(sessionId)) return { kind: "pending" };
  const workspace = workspaceId
    ? state.workspaces.find((item) => item.id === workspaceId)
    : state.workspaces.find((item) => item.sessionId === sessionId);
  const target = workspace ?? {
    title: session.name,
    assetId: session.assetId ?? undefined,
    assetKind: session.kind,
  };
  const fresh = await reconnectWorkspaceSession(target, session);
  if (!fresh) return { kind: "canceled" };
  const latest = useUi.getState();
  latest.setSessions([...latest.sessions.filter((item) => item.id !== fresh.id), fresh]);
  return { kind: "reconnected", session: fresh };
}

export type WorkspaceReconnectState = "connected" | "connecting" | "reconnecting" | "disconnected" | "failed";

// workspaceConnectionState is the host-session state of a host workspace, or undefined for workspaces without one.
// A session record that vanished from the list (deleted server side) reads as disconnected.
export function workspaceConnectionState(
  workspace: Pick<Workspace, "kind" | "sessionId" | "restorePending"> | null | undefined,
  sessions: SessionRuntimeInfo[],
): WorkspaceReconnectState | undefined {
  if (!workspace || workspace.kind !== "session" || !workspace.sessionId || workspace.restorePending) return undefined;
  return sessions.find((item) => item.id === workspace.sessionId)?.status ?? "disconnected";
}

export function workspaceNeedsReconnect(state: WorkspaceReconnectState | undefined): boolean {
  return state === "disconnected" || state === "failed";
}

// workspaceHasOrdinaryTerminals reports whether a reconnect would end running programs: a regular (non-durable)
// terminal dies with its connection, while a durable one re-attaches. Dead and exited tabs have nothing to lose.
export function workspaceHasOrdinaryTerminals(workspace: Pick<Workspace, "panes"> | null | undefined): boolean {
  return (
    workspace?.panes.some((pane) =>
      pane.tabs.some((tab) => tab.kind === "terminal" && tab.durable !== true && !tab.dead && !tab.exited && !tab.restorePending),
    ) ?? false
  );
}

// confirmSuspectReconnect asks before reconnecting a connection that may still be alive, because that drops
// ordinary terminals. Workspaces with only durable terminals reconnect without asking.
export async function confirmSuspectReconnect(workspaceId: string | undefined): Promise<boolean> {
  const workspace = workspaceId ? useUi.getState().workspaces.find((item) => item.id === workspaceId) : undefined;
  if (!workspaceHasOrdinaryTerminals(workspace)) return true;
  const { confirmDialog } = await import("../../ui/dialogs");
  return confirmDialog("普通终端会断开，正在运行的程序会结束");
}

// reconnectWorkspaceConnection reconnects the host session of a workspace from outside any terminal pane: a live
// session is re-dialed in place, a session the backend no longer has is replaced by a fresh connection to the same
// target. Failures and cancellations are reported to the user here, so callers just fire it.
export async function reconnectWorkspaceConnection(workspaceId: string): Promise<boolean> {
  const state = useUi.getState();
  const workspace = state.workspaces.find((item) => item.id === workspaceId);
  if (!workspace?.sessionId) return false;
  const { pushToast } = state;
  try {
    if (state.sessions.some((item) => item.id === workspace.sessionId)) {
      const result = await reconnectLiveSession(workspace.sessionId, workspaceId);
      if (result.kind === "pending") return false;
      if (result.kind === "canceled") {
        pushToast("info", "已取消重连");
        return false;
      }
      if (result.kind === "reconnected") return true;
    }
    if (!workspaceSessionIsDead(workspace, useUi.getState().sessions)) return false;
    const outcome = await reconnectRestoredWorkspace(workspaceId, { preserveUserFocus: true });
    if (outcome.ok) return true;
    pushToast(
      outcome.canceled ? "info" : "error",
      outcome.canceled ? "已取消重连" : `重新连接失败：${outcome.error ?? "未知错误"}`,
    );
    return false;
  } catch (error) {
    pushToast("error", `重新连接失败：${describeError(error)}`);
    return false;
  }
}
