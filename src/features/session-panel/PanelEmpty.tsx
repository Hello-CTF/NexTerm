import { type ReactNode } from "react";
import { useUi, type SessionRuntimeInfo } from "../../app/store";
import { IconInfo, IconLoader, IconTerminal } from "../../ui/icons";
import { type SessionPanelContext } from "./sessionContext";

export function PanelEmpty({
  icon: Icon,
  title,
  hint,
  busy = false,
  action,
}: {
  icon: typeof IconInfo;
  title: string;
  hint?: string;
  busy?: boolean;
  action?: ReactNode;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 px-6 text-center">
      <span className="nx-empty-icon">
        <Icon size={16} className={busy ? "animate-spin" : undefined} />
      </span>
      <div role="status" className="text-[12.5px] font-medium text-neutral-200">{title}</div>
      {hint ? <div className="text-[11.5px] text-neutral-500">{hint}</div> : null}
      {action}
    </div>
  );
}

export interface SessionGateCopy {
  // icon is shown for the offline state.
  icon: typeof IconInfo;
  // noTerminal explains what the tab shows once a terminal has focus.
  noTerminal: string;
  // ready names what appears once the host connects ("文件", "容器").
  ready: string;
}

export type ConnectedSession = SessionRuntimeInfo;

// SessionGate shows the shared empty states of the host-bound tabs (no focused terminal, closed session, connecting,
// offline) and renders its children only for a connected host session.
export function SessionGate({
  context,
  copy,
  children,
}: {
  context: SessionPanelContext | null;
  copy: SessionGateCopy;
  children: (session: ConnectedSession, sessionId: string) => ReactNode;
}) {
  const sessionId = context?.sessionId ?? null;
  const session = useUi((s) => (sessionId ? s.sessions.find((x) => x.id === sessionId) : undefined));
  if (!context) {
    return <PanelEmpty icon={IconTerminal} title="没有聚焦的终端" hint={copy.noTerminal} />;
  }
  if (!sessionId || !session) {
    return (
      <PanelEmpty
        icon={IconInfo}
        title="会话已关闭"
        hint={`${context.hostLabel} 的会话已不存在，重新连接后可查看${copy.ready}。`}
      />
    );
  }
  if (session.status === "connecting" || session.status === "reconnecting") {
    return <PanelEmpty icon={IconLoader} busy title={`正在连接 ${context.hostLabel}`} hint={`连接完成后显示${copy.ready}。`} />;
  }
  if (session.status === "disconnected" || session.status === "failed") {
    return (
      <PanelEmpty
        icon={copy.icon}
        title={`${context.hostLabel} 已离线`}
        hint={`主机连接已断开，重新连接后可查看${copy.ready}。`}
      />
    );
  }
  return <>{children(session, sessionId)}</>;
}
