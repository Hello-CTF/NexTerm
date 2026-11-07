import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { useSessionById, type Workspace } from "../../app/store";
import { hostPortsApi, type HostListener } from "../../ipc/hostPortsApi";
import { describeError } from "../../ui/errorText";
import { IconNetwork, IconRefresh } from "../../ui/icons";
import { forwardAndOpen, targetHostFor } from "../preview/forwardAndOpen";
import { ConnectivityTest } from "./ConnectivityTest";
import { HostSection, SectionHint } from "./HostSections";
import { ListToolbar, Pager, useListFocus, usePaging, type ListFocus } from "./listControls";

import { isLoopback, PORT_SCOPES, SCOPE_LABELS, SCOPE_TITLES, type PortScope } from "./portScope";
import { t } from "../../i18n";

// matchesListener tests a listener against every whitespace-separated term of the search, each of which may hit the
// port, protocol, bound address, process name or PID.
function matchesListener(listener: HostListener, terms: string[]): boolean {
  const fields = [String(listener.port), listener.proto, listener.address, listener.process ?? "", listener.pid === undefined ? "" : String(listener.pid)]
    .map((field) => field.toLowerCase());
  return terms.every((term) => fields.some((field) => field.includes(term)));
}

function listenerKey(listener: HostListener): string {
  return `${listener.proto}|${listener.address}|${listener.port}`;
}

// PortsSection lists the listening ports of a connected workspace's host. It reads only on demand, because the
// command runs on the host. focus asks it to show one port; onShowProcess opens the process that listens.
export function PortsSection({
  workspace,
  visible,
  hostName,
  focus,
  onShowProcess,
}: {
  workspace: Workspace | undefined;
  visible: boolean;
  hostName: string;
  focus?: ListFocus<number> | null;
  onShowProcess?: (pid: number) => void;
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
  const listeners = useMemo(() => query.data?.listeners ?? [], [query.data]);
  const [queryText, setQueryText] = useState("");
  const [scope, setScope] = useState<PortScope>("all");
  const matched = useMemo(() => {
    const terms = queryText.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return listeners
      .filter((listener) => scope === "all" || (scope === "loopback") === isLoopback(listener.address))
      .filter((listener) => matchesListener(listener, terms))
      .sort((a, b) => a.port - b.port || a.address.localeCompare(b.address) || a.proto.localeCompare(b.proto));
  }, [listeners, queryText, scope]);
  const paging = usePaging(matched, `${queryText}|${scope}`);
  const clearFilters = useCallback(() => {
    setQueryText("");
    setScope("all");
  }, []);
  const highlight = useListFocus({
    focus,
    indexOf: (port) => matched.findIndex((listener) => listener.port === port),
    filtered: queryText !== "" || scope !== "all",
    clearFilters,
    setPage: paging.setPage,
    pageOf: paging.pageOf,
    rowSelector: useCallback((port: number) => `[data-testid="host-ports"] tr[data-port="${port}"]`, []),
  });
  const counts = useMemo(() => {
    const loopback = listeners.filter((listener) => isLoopback(listener.address)).length;
    return { all: listeners.length, exposed: listeners.length - loopback, loopback };
  }, [listeners]);

  return (
    <HostSection
      title={t("端口")}
      icon={<IconNetwork size={14} />}
      testId="host-ports"
      actions={
        connected && visible ? (
          <>
            {query.dataUpdatedAt > 0 && (
              <span className="nx-hint text-[11px]">{t("读取于 {toLocaleTimeString}", { toLocaleTimeString: new Date(query.dataUpdatedAt).toLocaleTimeString() })}</span>
            )}
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-xs"
              aria-label={t("刷新端口")}
              disabled={query.isFetching}
              onClick={() => void query.refetch()}
            >
              <IconRefresh size={11} className={query.isFetching ? "animate-spin" : ""} />
              {query.data ? t("刷新") : t("读取")}
            </button>
          </>
        ) : null
      }
    >
      {!connected || !sessionId ? (
        <SectionHint>{t("打开并连接工作区后可查看监听端口。")}</SectionHint>
      ) : (
        <>
          {query.isError ? (
            <p className="m-0 text-[12px] text-red-300">{t("未能读取端口：{value}", { value: describeError(query.error) })}</p>
          ) : !query.data ? (
            <SectionHint>{query.isFetching ? t("正在读取监听端口…") : t("点击「读取」查看监听端口（会在主机上执行 ss 或 netstat）。")}</SectionHint>
          ) : listeners.length === 0 ? (
            <SectionHint>{t("未发现监听端口。")}</SectionHint>
          ) : (
            <>
              <ListToolbar
                label={t("搜索端口")}
                placeholder={t("按端口、地址、进程或 PID 搜索，空格分隔多个条件")}
                query={queryText}
                onQuery={setQueryText}
                total={listeners.length}
                matched={matched.length}
                unit={t("个")}
              >
                <div className="nx-segment" role="group" aria-label={t("端口范围")}>
                  {PORT_SCOPES.map((key) => (
                    <button
                      key={key}
                      type="button"
                      className={`nx-segment-item ${scope === key ? "is-active" : ""}`}
                      aria-pressed={scope === key}
                      title={SCOPE_TITLES[key]}
                      onClick={() => setScope(key)}
                    >
                      {SCOPE_LABELS[key]} {counts[key]}
                    </button>
                  ))}
                </div>
              </ListToolbar>
              {matched.length === 0 ? (
                <SectionHint>{t("没有匹配的端口。")}</SectionHint>
              ) : (
                <div className="overflow-x-auto">
                  <table className="nx-table w-full text-[12px]">
                    <thead>
                      <tr>
                        <th>{t("端口")}</th>
                        <th>{t("协议")}</th>
                        <th>{t("绑定地址")}</th>
                        <th>{t("进程")}</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {paging.shown.map((l) => (
                        <tr
                          key={listenerKey(l)}
                          data-port={l.port}
                          className={highlight === l.port ? "is-focused" : undefined}
                        >
                          <td className="nx-mono">{l.port}</td>
                          <td>{l.proto}</td>
                          <td className="nx-mono">
                            {l.address}
                            {isLoopback(l.address) && <span className="nx-badge ml-1.5">{t("仅本机")}</span>}
                          </td>
                          <td>
                            {l.process || l.pid !== undefined ? (
                              <span className="inline-flex items-center gap-1">
                                <span>{l.process ?? "-"}</span>
                                {l.pid !== undefined &&
                                  (onShowProcess ? (
                                    <button
                                      type="button"
                                      className="nx-link nx-mono"
                                      title={t("在进程列表中查看")}
                                      aria-label={t("查看进程 {pid}", { pid: l.pid })}
                                      onClick={() => onShowProcess(l.pid as number)}
                                    >
                                      {l.pid}
                                    </button>
                                  ) : (
                                    <span className="nx-mono">({l.pid})</span>
                                  ))}
                              </span>
                            ) : (
                              "-"
                            )}
                          </td>
                          <td className="whitespace-nowrap text-right">
                            <button
                              type="button"
                              className="nx-btn nx-btn-ghost nx-btn-xs"
                              aria-label={t("转发并打开 {port}", { port: l.port })}
                              onClick={() =>
                                void forwardAndOpen({ sessionId, host: targetHostFor(l, listeners), port: l.port })
                              }
                            >
                              {t("转发并打开")}
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <Pager label={t("端口分页")} page={paging.page} pages={paging.pages} onPage={paging.setPage} />
            </>
          )}
          <SectionHint>{t("非 root 用户执行时，lsof/ss 只能列出当前用户进程的端口，进程列可能为空。")}</SectionHint>
          {query.data?.truncated && <SectionHint>{t("端口较多，仅显示一部分。")}</SectionHint>}
          <ConnectivityTest sessionId={sessionId} hostName={hostName} assetId={workspace?.assetId} />
        </>
      )}
    </HostSection>
  );
}
