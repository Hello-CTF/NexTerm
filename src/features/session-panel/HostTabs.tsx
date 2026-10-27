import { lazy, useMemo } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { fsApi } from "../../ipc/commands";
import { isDockerNotInstalled, isDockerUnavailable } from "../docker/dockerErrors";
import { Lazy } from "../../ui/LazyBoundary";
import { IconBox, IconInfo, IconList, IconNetwork } from "../../ui/icons";
import { type SessionPanelContext } from "./sessionContext";
import { PanelEmpty, SessionGate, type ConnectedSession, type SessionGateCopy } from "./PanelEmpty";

// The host tabs reuse the full feature panels, loaded only when their tab opens.
const DockerPanel = lazy(() => import("../docker/DockerPanel").then((m) => ({ default: m.DockerPanel })));
const ForwardPanel = lazy(() => import("../forward/ForwardPanel").then((m) => ({ default: m.ForwardPanel })));
const MultiHostLogTail = lazy(() =>
  import("../logs/MultiHostLogTail").then((m) => ({ default: m.MultiHostLogTail })),
);

const CONTAINERS_COPY: SessionGateCopy = {
  icon: IconBox,
  noTerminal: "切换到一个终端标签，这里会显示它所在主机的 Docker 容器。",
  ready: "容器",
};

const PORTS_COPY: SessionGateCopy = {
  icon: IconNetwork,
  noTerminal: "切换到一个终端标签，这里会显示经由它所在主机的端口转发。",
  ready: "端口转发",
};

const LOGS_COPY: SessionGateCopy = {
  icon: IconList,
  noTerminal: "切换到一个终端标签，这里可以跟随它所在主机的日志文件、journal 或容器日志。",
  ready: "日志",
};

// HostContainers observes the panel's own docker-ps query (no extra fetch) so a host without a running Docker gets a
// plain explanation with a retry.
function HostContainers({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const ps = useQuery({ queryKey: ["docker-ps", sessionId], enabled: false });
  const unavailable = ps.isError && !ps.data && isDockerUnavailable(ps.error);
  return (
    <>
      {unavailable && (
        <PanelEmpty
          icon={IconBox}
          title={isDockerNotInstalled(ps.error) ? "这台主机未安装 Docker" : "这台主机未运行 Docker"}
          hint="安装或启动 Docker 后可重试。"
          action={
            <button
              type="button"
              className="nx-btn"
              onClick={() => void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] })}
            >
              重试
            </button>
          }
        />
      )}
      <div className={unavailable ? "hidden" : undefined}>
        <Lazy>
          <DockerPanel sessionId={sessionId} />
        </Lazy>
      </div>
    </>
  );
}

// ContainersTab lists the Docker containers of the context terminal's host. A container terminal still shows its
// host's containers, which include the one it runs in.
export function ContainersTab({ context }: { context: SessionPanelContext | null }) {
  return (
    <SessionGate context={context} copy={CONTAINERS_COPY}>
      {(_session, sessionId) => (
        <div className="h-full min-h-0 overflow-auto" data-testid="session-panel-containers">
          <HostContainers key={sessionId} sessionId={sessionId} />
        </div>
      )}
    </SessionGate>
  );
}

// PortsTab shows the port forwards that run over the context host's connection and creates new ones on it.
export function PortsTab({ context }: { context: SessionPanelContext | null }) {
  return (
    <SessionGate context={context} copy={PORTS_COPY}>
      {(_session, sessionId) => (
        <div className="h-full min-h-0 overflow-auto" data-testid="session-panel-ports">
          <Lazy>
            <ForwardPanel key={sessionId} sessionId={sessionId} visible />
          </Lazy>
        </div>
      )}
    </SessionGate>
  );
}

// DeviceLogs gates managed devices on their path style: Windows devices cannot run the tail commands.
function DeviceLogs({ session }: { session: ConnectedSession }) {
  const roots = useQuery({ queryKey: ["session-panel-path-style", session.id], queryFn: () => fsApi.roots(session.id) });
  if (roots.data?.pathStyle === "windows") {
    return <PanelEmpty icon={IconInfo} title="Windows 设备暂不支持日志跟随" hint="日志跟随需要 POSIX 主机。" />;
  }
  return <HostLogs session={session} />;
}

function HostLogs({ session }: { session: ConnectedSession }) {
  const sessions = useMemo(() => [session], [session]);
  return (
    <div className="h-full min-h-0" data-testid="session-panel-logs">
      <Lazy>
        <MultiHostLogTail sessions={sessions} singleHost />
      </Lazy>
    </div>
  );
}

// LogsTab follows logs on the context host only. Log tailing runs read-only commands over SSH or the device bridge,
// so local terminals have no logs tab content. Switching to another host stops the running follow.
export function LogsTab({ context }: { context: SessionPanelContext | null }) {
  return (
    <SessionGate context={context} copy={LOGS_COPY}>
      {(session, sessionId) =>
        session.kind === "ssh" ? (
          <HostLogs key={sessionId} session={session} />
        ) : session.kind === "device" ? (
          <DeviceLogs key={sessionId} session={session} />
        ) : (
          <PanelEmpty
            icon={IconInfo}
            title="此会话不支持日志跟随"
            hint="日志跟随仅支持 SSH 主机与受管设备；本地日志请在终端里查看。"
          />
        )
      }
    </SessionGate>
  );
}
