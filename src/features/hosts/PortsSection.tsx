import { useQuery } from "@tanstack/react-query";
import { useSessionById, type Workspace } from "../../app/store";
import { hostPortsApi } from "../../ipc/hostPortsApi";
import { describeError } from "../../ui/errorText";
import { IconNetwork, IconRefresh } from "../../ui/icons";
import { forwardAndOpen, targetHostFor } from "../preview/forwardAndOpen";
import { ConnectivityTest } from "./ConnectivityTest";
import { HostSection, SectionHint } from "./HostSections";

// PortsSection lists the listening ports of a connected workspace's host. It reads only on demand, because the
// command runs on the host.
export function PortsSection({
  workspace,
  visible,
  hostName,
}: {
  workspace: Workspace | undefined;
  visible: boolean;
  hostName: string;
}) {
  const session = useSessionById(workspace?.sessionId);
  const sessionId = workspace?.sessionId;
  const connected = Boolean(sessionId) && !workspace?.restorePending && session?.status === "connected";
  // Manual refresh only: no automatic fetch, so opening the page never runs a command on the host.
  const query = useQuery({
    queryKey: ["host-detail", "ports", sessionId],
    queryFn: () => hostPortsApi.list({ sessionId: sessionId ?? "", processes: true }),
    enabled: false,
    retry: false,
  });
  const listeners = query.data?.listeners ?? [];
  return (
    <HostSection
      title="端口"
      icon={<IconNetwork size={14} />}
      testId="host-ports"
      actions={
        connected && visible ? (
          <button
            type="button"
            className="nx-btn nx-btn-ghost nx-btn-xs"
            aria-label="刷新端口"
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            <IconRefresh size={11} className={query.isFetching ? "animate-spin" : ""} />
            刷新
          </button>
        ) : null
      }
    >
      {!connected || !sessionId ? (
        <SectionHint>打开并连接工作区后可查看监听端口。</SectionHint>
      ) : (
        <>
          {query.isError ? (
            <p className="m-0 text-[12px] text-red-300">未能读取端口：{describeError(query.error)}</p>
          ) : !query.data ? (
            <SectionHint>点击「刷新」读取监听端口（会在主机上执行 ss 或 netstat）。</SectionHint>
          ) : query.data.listeners.length === 0 ? (
            <SectionHint>未发现监听端口。</SectionHint>
          ) : (
            <div className="overflow-x-auto">
              <table className="nx-table w-full text-[12px]">
                <thead>
                  <tr>
                    <th>端口</th>
                    <th>协议</th>
                    <th>绑定地址</th>
                    <th>进程</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {listeners.map((l) => (
                    <tr key={`${l.proto}|${l.address}|${l.port}`}>
                      <td className="nx-mono">{l.port}</td>
                      <td>{l.proto}</td>
                      <td className="nx-mono">{l.address}</td>
                      <td>{l.process ? `${l.process}${l.pid !== undefined ? ` (${l.pid})` : ""}` : l.pid ?? "-"}</td>
                      <td>
                        <button
                          type="button"
                          className="nx-btn nx-btn-ghost nx-btn-xs"
                          aria-label={`转发并打开 ${l.port}`}
                          onClick={() =>
                            void forwardAndOpen({ sessionId, host: targetHostFor(l, listeners), port: l.port })
                          }
                        >
                          转发并打开
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <SectionHint>非 root 用户执行时，lsof/ss 只能列出当前用户进程的端口，进程列可能为空。</SectionHint>
          {query.data?.truncated && <SectionHint>端口较多，仅显示一部分。</SectionHint>}
          <ConnectivityTest sessionId={sessionId} hostName={hostName} assetId={workspace?.assetId} />
        </>
      )}
    </HostSection>
  );
}
