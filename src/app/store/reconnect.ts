import { sessionApi, type SessionInfo } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { confirmHostKeyIfNeeded } from "../hostKeys";
import { captureConnectionAttempt, currentConnectionEpoch, settleWorkspaceConnectionOutcome } from "./connection";
import { sessionStatusEvidence, sessionStatusText } from "./sessionStatus";
import type { SessionReconnectError, SessionRuntimeInfo, Workspace } from "./types";
import { useUi } from "./useUi";

const inflightReconnects = new Map<string, Promise<SessionInfo | null>>();

// SESSION_RECONNECT_WAIT_MS aligns with the backend reconnect lifecycle: internal/session defaults to 10 attempts with
// backoff 1/2/4/8/16/30s (181s total), each SSH dial defaults to 15s, so 6 minutes covers the worst case;
// terminal connected/failed states end the wait early, the timeout is only a fallback.
export const SESSION_RECONNECT_WAIT_MS = 360_000;

export function isSessionReconnectPending(sessionId: string): boolean {
  return inflightReconnects.has(JSON.stringify([useUi.getState().workspaceRuntimeEpoch, sessionId]));
}

export async function reconnectWorkspaceSession(
  workspace: Pick<Workspace, "assetId" | "assetKind" | "title" | "quickTarget"> & { id?: string },
  session: SessionInfo,
  options?: { timeoutMs?: number; intervalMs?: number; isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const context = captureConnectionAttempt();
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  try {
    const fresh = await reconnectSessionAndWait(session, { ...options, isCurrent });
    if (!isCurrent()) return null;
    if (fresh) settleWorkspaceConnectionOutcome(context, workspace, { ok: true }, workspace.id);
    return fresh;
  } catch (error) {
    if (!isCurrent()) return null;
    settleWorkspaceConnectionOutcome(context, workspace, { ok: false, error: describeError(error) }, workspace.id);
    throw error;
  }
}

export function reconnectSessionAndWait(
  session: SessionInfo,
  options?: { timeoutMs?: number; intervalMs?: number; isCurrent?: () => boolean },
): Promise<SessionInfo | null> {
  const epoch = useUi.getState().workspaceRuntimeEpoch;
  const key = JSON.stringify([epoch, session.id]);
  const inflight = inflightReconnects.get(key);
  if (inflight) return inflight;
  const isCurrent = () => currentConnectionEpoch(epoch) && (options?.isCurrent?.() ?? true);
  const promise = runReconnectAndWait(
    session,
    options?.timeoutMs ?? SESSION_RECONNECT_WAIT_MS,
    options?.intervalMs ?? 600,
    isCurrent,
  ).finally(() => {
    if (inflightReconnects.get(key) === promise) inflightReconnects.delete(key);
  });
  inflightReconnects.set(key, promise);
  return promise;
}

function reconnectPollingError(
  session: Pick<SessionRuntimeInfo, "status" | "error" | "errorCode" | "attempt" | "nextRetryAt">,
): SessionReconnectError {
  const error = new Error(session.error || `会话${sessionStatusText(session.status)}`) as SessionReconnectError;
  if (session.errorCode) error.code = session.errorCode;
  if (session.attempt !== undefined) error.attempt = session.attempt;
  if (session.nextRetryAt !== undefined) error.nextRetryAt = session.nextRetryAt;
  return error;
}

async function runReconnectAndWait(
  session: SessionInfo,
  timeoutMs: number,
  intervalMs: number,
  isCurrent: () => boolean,
): Promise<SessionInfo | null> {
  // Events observed from here on are fresh evidence for this reconnect attempt; anything tracked
  // earlier belongs to the previous generation and must not settle the wait.
  const startEvidence = sessionStatusEvidence(session.id);
  const startVersion = startEvidence?.version ?? 0;
  const latestEvidence = () => sessionStatusEvidence(session.id);
  const hasFreshEvidence = (status: string) => {
    const evidence = latestEvidence();
    return evidence !== undefined && evidence.version > startVersion && evidence.status === status;
  };
  // Engagement is tracked separately from the latest status: a terminal event that arrives after
  // the reconnecting event must not erase the proof that this attempt engaged.
  const engagedAfterStart = () => (latestEvidence()?.engagedVersion ?? 0) > startVersion;
  // A post-start terminal event is fresh terminal evidence on its own: surface its typed error
  // immediately instead of waiting for a list frame to observe engagement (or the six-minute timeout).
  const freshTerminalEvidence = () => {
    const evidence = latestEvidence();
    if (!evidence || evidence.version <= startVersion) return undefined;
    return evidence.status === "failed" || evidence.status === "disconnected" ? evidence : undefined;
  };
  // A stale connected list frame can only exist when the previous generation was still alive at
  // reconnect start; a session that was already disconnected/failed has no live frame to mistake.
  const startStatus = startEvidence?.status ?? session.status;
  const staleConnectedPossible = startStatus === "connected" || startStatus === "connecting" || startStatus === "reconnecting";
  if (!isCurrent()) return null;
  try {
    if (!(await confirmHostKeyIfNeeded(session.assetId ?? "", session.kind, isCurrent))) return null;
  } catch (error) {
    if (!isCurrent()) return null;
    throw error;
  }
  if (!isCurrent()) return null;
  let started: boolean;
  try {
    started = await sessionApi.reconnect(session.id);
  } catch (error) {
    if (!isCurrent()) return null;
    throw error;
  }
  if (!isCurrent()) return null;
  if (!started) throw new Error("重新连接未能启动");
  const deadline = Date.now() + timeoutMs;
  // session_reconnect returns first and the backend goroutine sets reconnecting afterwards, so the first list may read the pre-start
  // stale disconnected/failed; only a terminal state seen after connecting/reconnecting counts as failure.
  // A stale connected first frame is ignored the same way: only a connecting/reconnecting transition,
  // fresh status-event evidence (a newer event version, e.g. when joining an in-flight reconnect),
  // or a later live list makes connected count as success.
  let enteredReconnect = false;
  let polls = 0;
  for (;;) {
    if (!isCurrent()) return null;
    let list: SessionRuntimeInfo[];
    try {
      list = await sessionApi.list();
    } catch (error) {
      if (!isCurrent()) return null;
      throw error;
    }
    if (!isCurrent()) return null;
    polls += 1;
    const current = list.find((s) => s.id === session.id);
    if (current?.status === "connecting" || current?.status === "reconnecting") enteredReconnect = true;
    if (engagedAfterStart()) enteredReconnect = true;
    const terminalEvidence = freshTerminalEvidence();
    if (terminalEvidence) throw reconnectPollingError(terminalEvidence);
    if (current?.status === "connected") {
      if (enteredReconnect || polls > 1 || !staleConnectedPossible || hasFreshEvidence("connected")) return current;
    } else if (current && current.status !== "connecting" && current.status !== "reconnecting" && enteredReconnect) {
      throw reconnectPollingError(current);
    }
    if (Date.now() >= deadline) throw new Error("重新连接超时，请检查网络后重试");
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
    if (!isCurrent()) return null;
  }
}
