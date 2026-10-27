import { useMemo } from "react";
import { FileTree } from "../files/FileTree";
import { useTerminalCwd, type CwdFollowControl } from "../files/cwdFollow";
import { IconFolderOpen, IconInfo } from "../../ui/icons";
import { useSessionPanel } from "./sessionPanelStore";
import { type SessionPanelContext } from "./sessionContext";
import { useThrottledValue } from "./useThrottledValue";
import { PanelEmpty, SessionGate, type SessionGateCopy } from "./PanelEmpty";

// Terminal directory changes reach the file listing at most this often; a burst of cd commands lists only the last one.
export const CWD_FOLLOW_THROTTLE_MS = 400;

const FILES_COPY: SessionGateCopy = {
  icon: IconFolderOpen,
  noTerminal: "切换到一个终端标签，这里会显示它所在主机的文件，并跟随终端的当前目录。",
  ready: "文件",
};

// FilesTab browses the context terminal's host, starting at and following the terminal's working directory (OSC 7).
// Navigating inside the tree stops the follow for that session until 跟随终端目录 is pressed again.
export function FilesTab({ context }: { context: SessionPanelContext | null }) {
  const sessionId = context?.sessionId ?? null;
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

  if (context?.containerId) {
    return (
      <PanelEmpty
        icon={IconInfo}
        title="容器终端"
        hint="这是容器内的终端，文件页签只浏览主机文件；容器内的文件请在容器面板的「文件」页查看。"
      />
    );
  }
  return (
    <SessionGate context={context} copy={FILES_COPY}>
      {(_session, id) => <FileTree key={id} sessionId={id} follow={follow} embedded />}
    </SessionGate>
  );
}
