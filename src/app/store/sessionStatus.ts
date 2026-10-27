import { sessionApi, type SessionInfo } from "../../ipc/commands";
import type { ConnectProgressEvent } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { formatTime } from "../../ui/format";
import { reconnectSessionAndWait, reconnectWorkspaceSession } from "./reconnect";
import type { SessionRuntimeInfo, SessionStatusEvent, SessionStatusEvidence, UiState } from "./types";
import { useUi } from "./useUi";

export const abnormalDisconnectNotices = new Set<string>();

// Latest status-event evidence per session: backend status events carry a monotonically increasing
// version, so a manual reconnect can tell a stale pre-reconnect list frame from a fresh one. Engagement
// (connecting/reconnecting) is tracked as its own high-water mark so a later terminal event cannot erase
// the proof that the current attempt engaged. Cleared with the sessions.

export const sessionStatusEvidenceMap = new Map<string, SessionStatusEvidence>();

export function sessionStatusEvidence(sessionId: string): SessionStatusEvidence | undefined {
  return sessionStatusEvidenceMap.get(sessionId);
}

export function trackSessionStatusEvidence(p: SessionStatusEvent): void {
  if (typeof p.version !== "number" || !Number.isFinite(p.version) || p.version <= 0) return;
  const previous = sessionStatusEvidenceMap.get(p.sessionId);
  if (previous && previous.version >= p.version) return;
  const status = p.status as SessionInfo["status"];
  const engagedVersion = Math.max(
    previous?.engagedVersion ?? 0,
    status === "connecting" || status === "reconnecting" ? p.version : 0,
  );
  sessionStatusEvidenceMap.set(p.sessionId, {
    version: p.version,
    status,
    error: p.error ?? null,
    errorCode: p.errorCode ?? null,
    attempt: p.attempt,
    nextRetryAt: p.nextRetryAt,
    engagedVersion,
  });
}

export function sessionReconnectStatusText(
  session: Pick<SessionRuntimeInfo, "status" | "attempt" | "nextRetryAt"> | undefined,
): string {
  if (session?.status !== "reconnecting") return "";
  const attempt = Math.max(0, Math.floor(session.attempt ?? 0));
  const retryAt = typeof session.nextRetryAt === "number"
    ? session.nextRetryAt
    : session.nextRetryAt
      ? Date.parse(session.nextRetryAt)
      : null;
  const next = retryAt !== null && Number.isFinite(retryAt) && retryAt > 0
    ? `下次重连 ${formatTime(retryAt)}`
    : null;
  return [attempt > 0 ? `重连尝试 ${attempt} 次` : "准备第 1 次重连", next]
    .filter(Boolean)
    .join(" · ");
}

const retryableSessionErrorCodes = new Set(["ssh", "timeout", "disconnected"]);

export function applySessionStatusEvent(p: SessionStatusEvent): void {
  trackSessionStatusEvidence(p);
  const state = useUi.getState();
  const current = state.sessions.find((session) => session.id === p.sessionId);
  const attempt = p.attempt ?? current?.attempt ?? 0;
  const retryableFailed = p.status === "failed" &&
    (current?.status === "reconnecting" || attempt > 0) &&
    retryableSessionErrorCodes.has(p.errorCode ?? "");
  state.setSessions(state.sessions.map((session) => session.id === p.sessionId
    ? {
        ...session,
        status: p.status as SessionInfo["status"],
        error: p.error ?? null,
        errorCode: p.errorCode ?? null,
        attempt: p.attempt ?? 0,
        nextRetryAt: p.nextRetryAt ?? null,
        liveness: p.liveness ?? null,
      }
    : session));
  if (p.status !== "connecting" && p.status !== "reconnecting" && current?.assetId) {
    state.setConnectProgress(current.assetId, null);
  }
  if (p.status === "connecting" || p.status === "reconnecting" || p.status === "connected") {
    abnormalDisconnectNotices.delete(p.sessionId);
  }
  if (p.status === "failed" && !retryableFailed) {
    // A first connect that stops at an unconfirmed host key is answered by the confirmation dialog and an
    // automatic second attempt (connectWithHostKeyConfirm), so a toast here would only be noise.
    const awaitingHostKeyDialog = (p.errorCode === "host_key_pending" || p.errorCode === "host_key_changed") &&
      current?.status !== "reconnecting" && current?.status !== "connected" && attempt === 0;
    if (p.error && !awaitingHostKeyDialog) state.pushToast("error", `会话失败：${p.error}`);
    return;
  }
  if (p.status !== "disconnected" && !retryableFailed) return;
  if (!p.error && !p.errorCode) {
    abnormalDisconnectNotices.delete(p.sessionId);
    return;
  }
  if (abnormalDisconnectNotices.has(p.sessionId)) return;
  abnormalDisconnectNotices.add(p.sessionId);
  const name = current?.name ?? p.sessionId;
  const message = retryableFailed
    ? `会话「${name}」自动重连失败：${p.error ?? p.errorCode}`
    : `会话「${name}」异常断开：${p.error ?? p.errorCode}`;
  state.pushToast("error", message, {
    label: "重新连接",
    onSelect: () => void reconnectSessionAfterDisconnect(p.sessionId),
  });
}

async function reconnectSessionAfterDisconnect(sessionId: string): Promise<void> {
  const state = useUi.getState();
  const session = state.sessions.find((item) => item.id === sessionId);
  if (!session) {
    state.pushToast("error", "重连失败：会话已不存在，请刷新会话列表");
    return;
  }
  // Route through the target-aware workspace reconnect so the connection problem settles against
  // the workspace target: success clears it, failure records it, a canceled attempt adds nothing.
  const workspace = state.workspaces.find((item) => item.sessionId === sessionId);
  try {
    const fresh = workspace
      ? await reconnectWorkspaceSession(workspace, session)
      : await reconnectSessionAndWait(session);
    if (!fresh) return;
    const latest = useUi.getState();
    latest.setSessions([...latest.sessions.filter((item) => item.id !== fresh.id), fresh]);
  } catch (error) {
    useUi.getState().pushToast("error", `重连失败：${describeError(error)}`);
  }
}

// isSessionSuspect reports a connected session whose heartbeats are going unanswered.
export function isSessionSuspect(status: SessionInfo["status"] | undefined, liveness: string | null | undefined): boolean {
  return status === "connected" && liveness === "suspect";
}

export function sessionStatusText(status: SessionInfo["status"] | undefined, liveness?: string | null): string {
  if (isSessionSuspect(status, liveness)) return "连接不稳定";
  if (status === "connected") return "已连接";
  if (status === "reconnecting") return "重连中";
  if (status === "connecting") return "连接中";
  if (status === "failed") return "连接失败";
  return "已断开";
}

export function connectStageText(stage: string): string {
  if (stage === "auth") return "准备认证";
  if (stage === "dial") return "TCP 拨号";
  if (stage === "channel") return "经跳板打开通道";
  if (stage === "handshake") return "SSH 握手认证";
  return stage;
}

export function connectProgressText(progress: ConnectProgressEvent): string {
  const hop =
    progress.hop > 0 ? `跳板 ${progress.hop}/${progress.hops}` : "目标主机";
  return `${hop} · ${connectStageText(progress.stage)}`;
}

// foregroundProbeTargets lists the sessions shown in the active workspace (its own session and the
// session of each pane's visible tab) that a foreground probe can act on: connected sessions are
// checked, reconnecting ones retry now. Failed and disconnected sessions keep their manual retry.
export function foregroundProbeTargets(
  state: Pick<UiState, "workspaces" | "activeWorkspaceId" | "sessions">,
): string[] {
  const workspace = state.workspaces.find((item) => item.id === state.activeWorkspaceId);
  if (!workspace) return [];
  const candidates = new Set<string>();
  if (workspace.sessionId) candidates.add(workspace.sessionId);
  for (const pane of workspace.panes) {
    const tab = pane.tabs.find((item) => item.id === pane.activeTabId);
    if (tab?.sessionId) candidates.add(tab.sessionId);
  }
  return [...candidates].filter((id) => {
    const status = state.sessions.find((session) => session.id === id)?.status;
    return status === "connected" || status === "reconnecting";
  });
}

// probeForegroundSessions asks the backend to check the visible sessions now. Results arrive as
// session status events (suspect, reconnecting, connected); a probe that fails is left to the
// heartbeat, which reports the same states. Single-flight: a trigger while a round is still running
// joins that round, and any number of such triggers cause one trailing round after it ends, so state that
// changed mid-round (a new visible session) is still probed. The returned promise covers the trailing round.
let foregroundProbe: Promise<void> | null = null;
let foregroundProbeAgain = false;

export function probeForegroundSessions(): Promise<void> {
  if (foregroundProbe) {
    foregroundProbeAgain = true;
    return foregroundProbe;
  }
  foregroundProbe = (async () => {
    do {
      foregroundProbeAgain = false;
      const targets = foregroundProbeTargets(useUi.getState());
      await Promise.all(targets.map((sessionId) => sessionApi.probeLiveness(sessionId).catch(() => undefined)));
    } while (foregroundProbeAgain);
  })().finally(() => {
    foregroundProbe = null;
  });
  return foregroundProbe;
}
