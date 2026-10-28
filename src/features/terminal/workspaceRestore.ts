import {
  reconnectRestoredWorkspace,
  recordWorkspaceRestoreFailure,
  useUi,
  workspaceSessionIsDead,
  type ConnectOutcome,
  type RestoreWorkspaceOptions,
  type SessionRuntimeInfo,
  type Workspace,
  type WorkspaceRestoreResult,
  type WorkspaceKind,
} from "../../app/store";
import { describeError } from "../../ui/errorText";

export const WORKSPACE_RESTORE_CONCURRENCY = 4;

export interface WorkspaceRestoreCandidate {
  workspaceId: string;
  title: string;
  kind: WorkspaceKind;
  eligible: boolean;
  skipReason?: string;
  containerExecSkipped: boolean;
}

export type ReconnectWorkspace = (
  workspaceId: string,
  options?: RestoreWorkspaceOptions,
) => Promise<ConnectOutcome>;

export function workspaceNeedsRestore(workspace: Workspace, sessions: SessionRuntimeInfo[]): boolean {
  if (workspace.restorePending || workspaceSessionIsDead(workspace, sessions)) return true;
  if (workspace.kind !== "session") return false;
  const session = sessions.find((item) => item.id === workspace.sessionId);
  const live = session && ["connecting", "connected", "reconnecting"].includes(session.status);
  return !live && workspace.panes.some((pane) => pane.tabs.some((tab) => tab.restorePending));
}

export function classifyRestoredWorkspace(workspace: Workspace): WorkspaceRestoreCandidate {
  const tabs = workspace.panes.flatMap((pane) => pane.tabs);
  const containerExecSkipped = tabs.some((tab) => tab.kind === "terminal" && Boolean(tab.containerId));
  const restorableTabs = tabs.filter((tab) => tab.kind !== "terminal" || !tab.containerId);
  let skipReason: string | undefined;
  if (workspace.kind === "db") skipReason = "数据库工作区请在原工作区手动重新连接";
  else if (workspace.kind !== "session") skipReason = "不是主机工作区";
  else if (workspace.deletedTarget) skipReason = "原资产已删除";
  else if (tabs.length === 0) skipReason = "空工作区无需重新连接";
  else if (restorableTabs.length === 0) skipReason = "容器内命令不会自动重新打开";
  else if (!workspace.assetId && !workspace.quickTarget && workspace.assetKind !== "local") {
    skipReason = "缺少可恢复的主机目标";
  }
  return {
    workspaceId: workspace.id,
    title: workspace.title,
    kind: workspace.kind,
    eligible: skipReason === undefined,
    skipReason,
    containerExecSkipped,
  };
}

export function collectWorkspaceRestoreCandidates(
  workspaces: Workspace[],
  sessions: SessionRuntimeInfo[],
): WorkspaceRestoreCandidate[] {
  return workspaces
    .filter((workspace) => workspaceNeedsRestore(workspace, sessions))
    .map(classifyRestoredWorkspace);
}

function setRestoreResult(epoch: number, workspaceId: string, result: WorkspaceRestoreResult): void {
  useUi.setState((state) => {
    if (
      state.workspaceRuntimeEpoch !== epoch ||
      !state.workspaces.some((workspace) => workspace.id === workspaceId)
    ) {
      return state;
    }
    return {
      workspaceRestoreResults: { ...state.workspaceRestoreResults, [workspaceId]: result },
    };
  });
}

export async function reconnectRestoredWorkspaces(
  workspaceIds?: Iterable<string>,
  reconnect: ReconnectWorkspace = reconnectRestoredWorkspace,
): Promise<void> {
  const state = useUi.getState();
  const epoch = state.workspaceRuntimeEpoch;
  const requested = workspaceIds ? new Set(workspaceIds) : null;
  const seen = new Set<string>();
  const candidates = collectWorkspaceRestoreCandidates(state.workspaces, state.sessions).filter(
    (candidate) => {
      if (requested && !requested.has(candidate.workspaceId)) return false;
      if (seen.has(candidate.workspaceId)) return false;
      seen.add(candidate.workspaceId);
      return true;
    },
  );
  const queue: WorkspaceRestoreCandidate[] = [];
  for (const candidate of candidates) {
    if (!candidate.eligible) {
      setRestoreResult(epoch, candidate.workspaceId, { status: "skipped", detail: candidate.skipReason });
      continue;
    }
    setRestoreResult(epoch, candidate.workspaceId, { status: "waiting" });
    queue.push(candidate);
  }

  let nextIndex = 0;
  const worker = async () => {
    for (;;) {
      const candidate = queue[nextIndex++];
      if (!candidate || useUi.getState().workspaceRuntimeEpoch !== epoch) return;
      setRestoreResult(epoch, candidate.workspaceId, { status: "connecting" });
      try {
        const outcome = await reconnect(candidate.workspaceId, { preserveUserFocus: true });
        if (outcome.ok) {
          setRestoreResult(epoch, candidate.workspaceId, {
            status: "success",
            detail: candidate.containerExecSkipped ? "主机已连接；容器内命令标签页未重新打开" : undefined,
          });
        } else if (outcome.canceled) {
          setRestoreResult(epoch, candidate.workspaceId, { status: "canceled", detail: "已取消" });
        } else {
          setRestoreResult(epoch, candidate.workspaceId, {
            status: "failed",
            detail: outcome.error ?? "连接失败",
          });
        }
      } catch (error) {
        if (useUi.getState().workspaceRuntimeEpoch !== epoch) return;
        recordWorkspaceRestoreFailure(candidate.workspaceId, error);
        setRestoreResult(epoch, candidate.workspaceId, {
          status: "failed",
          detail: describeError(error),
        });
      }
    }
  };
  await Promise.all(
    Array.from({ length: Math.min(WORKSPACE_RESTORE_CONCURRENCY, queue.length) }, () => worker()),
  );
}
