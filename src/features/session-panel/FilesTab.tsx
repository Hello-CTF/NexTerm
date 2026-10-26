import { useMemo, type ReactNode } from "react";
import { useUi } from "../../app/store";
import { FileTree } from "../files/FileTree";
import { useTerminalCwd, type CwdFollowControl } from "../files/cwdFollow";
import { IconFolderOpen, IconInfo, IconLoader, IconTerminal } from "../../ui/icons";
import { useSessionPanel } from "./sessionPanelStore";
import { type SessionPanelContext } from "./sessionContext";
import { useThrottledValue } from "./useThrottledValue";

// Terminal directory changes reach the file listing at most this often; a burst of cd commands lists only the last one.
export const CWD_FOLLOW_THROTTLE_MS = 400;

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

// FilesTab browses the context terminal's host, starting at and following the terminal's working directory (OSC 7).
// Navigating inside the tree stops the follow for that session until 跟随终端目录 is pressed again.
export function FilesTab({ context }: { context: SessionPanelContext | null }) {
  const sessionId = context?.sessionId ?? null;
  const session = useUi((s) => (sessionId ? s.sessions.find((x) => x.id === sessionId) : undefined));
  const rawCwd = useTerminalCwd(context?.terminalId);
  const cwd = useThrottledValue(rawCwd, CWD_FOLLOW_THROTTLE_MS, context?.key ?? "");
  const detached = useSessionPanel((s) => (sessionId ? s.detached.includes(sessionId) : false));
  const setDetached = useSessionPanel((s) => s.setDetached);

  const follow = useMemo<CwdFollowControl | undefined>(
    () =>
      sessionId
        ? {
            cwd,
            attached: !detached,
            detach: () => setDetached(sessionId, true),
            attach: () => setDetached(sessionId, false),
          }
        : undefined,
    [sessionId, cwd, detached, setDetached],
  );

  if (!context) {
    return (
      <PanelEmpty
        icon={IconTerminal}
        title="没有聚焦的终端"
        hint="切换到一个终端标签，这里会显示它所在主机的文件，并跟随终端的当前目录。"
      />
    );
  }
  if (context.containerId) {
    return (
      <PanelEmpty
        icon={IconInfo}
        title="容器终端"
        hint="这是容器内的终端，文件页签只浏览主机文件；容器内的文件请在容器面板的「文件」页查看。"
      />
    );
  }
  if (!sessionId || !session) {
    return <PanelEmpty icon={IconInfo} title="会话已关闭" hint={`${context.hostLabel} 的会话已不存在，重新连接后可浏览文件。`} />;
  }
  if (session.status === "connecting" || session.status === "reconnecting") {
    return <PanelEmpty icon={IconLoader} busy title={`正在连接 ${context.hostLabel}`} hint="连接完成后显示文件。" />;
  }
  if (session.status === "disconnected" || session.status === "failed") {
    return (
      <PanelEmpty
        icon={IconFolderOpen}
        title={`${context.hostLabel} 已离线`}
        hint="主机连接已断开，重新连接后可浏览文件。"
      />
    );
  }
  return <FileTree key={sessionId} sessionId={sessionId} follow={follow} embedded />;
}
