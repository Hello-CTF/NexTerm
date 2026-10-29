import { useEffect, useState } from "react";
import {
  confirmSuspectReconnect,
  isConnectionLostTab,
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
import { reattachLostTabs } from "../../features/terminal/backgroundSessionsAccess";
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
// While the active tab is a terminal that shows its own "该终端已失效" overlay (tab.dead), the pane already
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
  const reattach = async (tabIds: string[]) => {
    if (busy) return;
    setBusy(true);
    try {
      const outcome = await reattachLostTabs(tabIds);
      const { pushToast } = useUi.getState();
      if (outcome.failed > 0) pushToast("error", `${outcome.failed} 个终端未能重新接管：${outcome.error ?? "未知错误"}`);
    } finally {
      setBusy(false);
    }
  };
  const openBackground = () => {
    if (!ws.sessionId) return;
    useUi.getState().addTab({ id: `background-${ws.id}`, kind: "background", title: "后台会话", sessionId: ws.sessionId, closable: true });
  };
  if (state !== "disconnected" && state !== "failed" && state !== "reconnecting" && !suspect) {
    // The host session is up, but durable terminals may have lost their attachment (the stream stopped without proof
    // the process ended). They stay recoverable from the host, so say so and offer to take them back.
    const lost = ws.panes.flatMap((pane) => pane.tabs).filter(isConnectionLostTab);
    if (state !== "connected" || lost.length === 0) return null;
    if (lost.length === 1 && activeTab?.id === lost[0].id) return null;
    return (
      <div
        role="status"
        data-testid="workspace-connection-bar"
        data-state="terminals-lost"
        className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-b border-[var(--nx-border)] bg-[color-mix(in_srgb,var(--nx-bg-raised)_55%,var(--nx-bg-pane))] py-1 pr-1 pl-2.5 text-xs text-[var(--nx-fg)] pointer-coarse:py-1.5"
      >
        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" aria-hidden="true" />
        <span className="min-w-[min(12rem,70%)] flex-1 break-words">{lost.length} 个终端连接丢失，主机上的进程可能仍在运行</span>
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm shrink-0 pointer-coarse:min-h-11 pointer-coarse:px-4"
          disabled={busy}
          onClick={() => void reattach(lost.map((tab) => tab.id))}
        >
          <IconRefresh size={13} className={busy ? "animate-spin" : undefined} />
          重新接管
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm shrink-0 pointer-coarse:min-h-11 pointer-coarse:px-4"
          onClick={openBackground}
        >
          在后台会话中查看
        </button>
      </div>
    );
  }
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
  const dot = reconnecting || suspect ? "bg-[var(--nx-fg-tertiary)]" : "bg-red-500";
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
            {reason ? (
              <details className="text-[var(--nx-fg-tertiary)]">
                <summary className="cursor-pointer">查看详情</summary>
                <span className="break-all">{reason}</span>
              </details>
            ) : null}
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
          {suspect ? "立即重新连接" : "重新连接"}
        </button>
      )}
    </div>
  );
}
