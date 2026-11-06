// Wording of the workspace connection bar while a session reconnects. Free of imports so the website demo can
// mirror it.

export interface ReconnectSchedule {
  attempt?: number;
  nextRetryAt?: string | number | null;
}

export const RETRY_NOW_LABEL = "立即重试";
export const RETRY_DIALING_LABEL = "连接中";

function retryDeadline(session: Pick<ReconnectSchedule, "nextRetryAt"> | undefined): number | null {
  const value = session?.nextRetryAt;
  if (value === undefined || value === null || value === "") return null;
  const ms = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(ms) && ms > 0 ? ms : null;
}

// reconnectDialing reports that the next attempt is running: its scheduled time has passed, or the user asked to
// retry now and no newer status has arrived.
export function reconnectDialing(session: Pick<ReconnectSchedule, "nextRetryAt"> | undefined, now: number, retrying: boolean): boolean {
  const deadline = retryDeadline(session);
  return retrying || (deadline !== null && deadline <= now);
}

// reconnectingText names the attempt and, once the backend has scheduled one, the seconds until the next try; while
// an attempt is dialing it says so instead of counting down to zero.
export function reconnectingText(session: ReconnectSchedule | undefined, now: number, retrying = false): string {
  const attempt = Math.max(1, Math.floor(session?.attempt ?? 0));
  const deadline = retryDeadline(session);
  if (reconnectDialing(session, now, retrying)) return `正在重新连接（第 ${attempt + (deadline === null ? 0 : 1)} 次尝试中…）`;
  if (deadline === null) return `正在重新连接（第 ${attempt} 次）`;
  const seconds = Math.max(0, Math.ceil((deadline - now) / 1000));
  return `正在重新连接（第 ${attempt} 次，${seconds} 秒后重试）`;
}
