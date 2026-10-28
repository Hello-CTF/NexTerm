import { useEffect, useState } from "react";
import {
  confirmSuspectReconnect,
  isSessionSuspect,
  reconnectWorkspaceConnection,
  useSessionById,
  useUi,
  workspaceConnectionState,
  type AppTab,
  type SessionRuntimeInfo,
  type Workspace,
} from "../store";
import { sessionApi } from "../../ipc/commands";
import { IconRefresh } from "../../ui/icons";

function retryDeadline(session: Pick<SessionRuntimeInfo, "nextRetryAt"> | undefined): number | null {
  const value = session?.nextRetryAt;
  if (value === undefined || value === null || value === "") return null;
  const ms = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(ms) && ms > 0 ? ms : null;
}

// reconnectingText names the attempt and, once the backend has scheduled one, the seconds until the next try.
export function reconnectingText(
  session: Pick<SessionRuntimeInfo, "attempt" | "nextRetryAt"> | undefined,
  now: number,
): string {
  const attempt = Math.max(1, Math.floor(session?.attempt ?? 0));
  const deadline = retryDeadline(session);
  if (deadline === null) return `正在重新连接（第 ${attempt} 次）`;
  const seconds = Math.max(0, Math.ceil((deadline - now) / 1000));
  return `正在重新连接（第 ${attempt} 次，${seconds} 秒后重试）`;
}

// WorkspaceConnectionBar is the workspace-level connection notice: it follows the host session of the active
// workspace whatever tab is open, and is the only reconnect surface when the workspace has no terminal.
// While the active tab is a terminal that shows its own "这个终端已失效" overlay (tab.dead), the pane already
// offers a reconnect for the same host, so the bar steps aside instead of stacking a second one.
export function WorkspaceConnectionBar({
  ws,
  activeTab,
}: {
  ws: Workspace | null | undefined;
  activeTab: AppTab | null;
}) {
  const session = useSessionById(ws?.sessionId);
  const state = useUi((s) => workspaceConnectionState(ws, s.sessions));
  const [now, setNow] = useState(() => Date.now());
  const [busy, setBusy] = useState(false);
  const reconnecting = state === "reconnecting";
  const suspect = isSessionSuspect(session?.status, session?.liveness);
  useEffect(() => {
    if (!reconnecting) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [reconnecting]);

  if (!ws || ws.deletedTarget) return null;
  if (state !== "disconnected" && state !== "failed" && state !== "reconnecting" && !suspect) return null;
  if (activeTab?.kind === "terminal" && activeTab.dead === true) return null;

  const reason = session?.error?.trim();
  const reconnect = async () => {
    if (busy) return;
    setBusy(true);
    try {
      // A suspect link may still be alive, so dropping ordinary terminals needs the user's consent.
      if (suspect && !(await confirmSuspectReconnect(ws.id))) return;
      await reconnectWorkspaceConnection(ws.id);
    } finally {
      setBusy(false);
    }
  };
  const retryNow = () => {
    if (ws.sessionId) void sessionApi.probeLiveness(ws.sessionId).catch(() => undefined);
  };
  const dot = reconnecting || suspect ? "bg-[var(--nx-fg-tertiary)] animate-pulse" : "bg-red-500";
  return (
    <div
      role="status"
      data-testid="workspace-connection-bar"
      data-state={state}
      className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--nx-border)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_55%,var(--nx-bg-pane))] py-1 pr-1 pl-2.5 text-xs text-[var(--nx-fg)] pointer-coarse:py-1.5"
    >
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} aria-hidden="true" />
      <span className="min-w-[min(12rem,70%)] flex-1 break-words">
        {reconnecting ? (
          reconnectingText(session, now)
        ) : suspect ? (
          "连接不稳定"
        ) : (
          <>
            与 {ws.title} 的连接已断开
            {reason ? <span className="text-[var(--nx-fg-tertiary)]">：{reason}</span> : null}
          </>
        )}
      </span>
      {reconnecting && (
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0 pointer-coarse:min-h-11 pointer-coarse:px-4"
          onClick={retryNow}
        >
          <IconRefresh size={13} />
          立即重试
        </button>
      )}
      {!reconnecting && (
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0 pointer-coarse:min-h-11 pointer-coarse:px-4"
          disabled={busy}
          onClick={() => void reconnect()}
        >
          <IconRefresh size={13} className={busy ? "animate-spin" : undefined} />
          {suspect ? "立即重连" : "重新连接"}
        </button>
      )}
    </div>
  );
}
