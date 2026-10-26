import type { SessionInfo } from "../../ipc/commands";
import type { ConnectProgressEvent } from "../../ipc/events";
import { formatTime } from "../../ui/format";
import type { SessionRuntimeInfo, SessionStatusEvent, SessionStatusEvidence } from "./types";

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

export function sessionStatusText(status: SessionInfo["status"] | undefined): string {
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
