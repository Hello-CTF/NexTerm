import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSessionById, type Workspace } from "../../app/store";
import { hostPortsApi, type HostListener } from "../../ipc/hostPortsApi";
import { hostProcessesApi, type HostProcess } from "../../ipc/hostProcessesApi";
import { confirmDialog } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { IconActivity, IconChevronDown, IconChevronRight, IconRefresh } from "../../ui/icons";
import { forwardAndOpen, targetHostFor } from "../preview/forwardAndOpen";
import { useAssetProductionClassification } from "../production/productionSettings";
import { HostSection, SectionHint } from "./HostSections";
import { buildRows, type ProcessSort } from "./processTree";

const AUTO_REFRESH_MS = 5000;
const MAX_ROWS = 300;
const COLUMN_LABELS: Record<string, string> = { cpu: "CPU", mem: "内存", user: "用户" };

function formatElapsed(p: HostProcess): string {
  if (p.elapsedSec < 0) return p.elapsed || "-";
  const s = p.elapsedSec;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}天${h}小时`;
  if (h > 0) return `${h}小时${m}分`;
  if (m > 0) return `${m}分${s % 60}秒`;
  return `${s}秒`;
}

function formatRss(kib: number): string {
  if (kib >= 1048576) return `${(kib / 1048576).toFixed(1)} GiB`;
  if (kib >= 1024) return `${(kib / 1024).toFixed(1)} MiB`;
  return `${kib} KiB`;
}

// ProcessesSection lists the processes of a connected workspace's host. It reads only on demand (or on the
// opt-in 5 second timer), because the command runs on the host.
export function ProcessesSection({
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
  const production = useAssetProductionClassification(workspace?.assetId)?.production === true;
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["host-detail", "processes", sessionId],
    queryFn: () => hostProcessesApi.list({ sessionId: sessionId ?? "" }),
    enabled: false,
    retry: false,
  });
  // Same key as PortsSection, so both sections show one result.
  const portsKey = useMemo(() => ["host-detail", "ports", sessionId], [sessionId]);
  const portsQuery = useQuery({
    queryKey: portsKey,
    queryFn: () => hostPortsApi.list({ sessionId: sessionId ?? "", processes: true }),
    enabled: false,
    retry: false,
  });
  const [portsError, setPortsError] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(false);
  const [queryText, setQueryText] = useState("");
  const [sort, setSort] = useState<ProcessSort>("cpu");
  const [tree, setTree] = useState(false);
  const [collapsed, setCollapsed] = useState<ReadonlySet<number>>(new Set());
  const [busyPid, setBusyPid] = useState<number | null>(null);
  const [killMessage, setKillMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const loading = useRef(false);

  const refresh = useCallback(async () => {
    if (loading.current || !sessionId) return;
    loading.current = true;
    try {
      const ports = queryClient
        .fetchQuery({
          queryKey: portsKey,
          queryFn: () => hostPortsApi.list({ sessionId, processes: true }),
          staleTime: 0,
        })
        .then(() => setPortsError(false))
        .catch(() => setPortsError(true));
      await Promise.all([query.refetch(), ports]);
    } finally {
      loading.current = false;
    }
  }, [sessionId, queryClient, portsKey, query.refetch]); // eslint-disable-line react-hooks/exhaustive-deps

  const active = connected && visible;
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  useEffect(() => {
    if (!autoRefresh || !active) return;
    const id = setInterval(() => void refreshRef.current(), AUTO_REFRESH_MS);
    return () => clearInterval(id);
  }, [autoRefresh, active]);

  const processes = query.data?.processes;
  const rows = useMemo(
    () => (processes ? buildRows(processes, { query: queryText, sort, tree, collapsed }) : []),
    [processes, queryText, sort, tree, collapsed],
  );
  const listeners = useMemo<HostListener[]>(() => portsQuery.data?.listeners ?? [], [portsQuery.data]);
  const portsByPid = useMemo(() => {
    const map = new Map<number, HostListener[]>();
    for (const l of listeners) {
      if (l.pid === undefined) continue;
      const list = map.get(l.pid);
      if (list) list.push(l);
      else map.set(l.pid, [l]);
    }
    return map;
  }, [listeners]);
  const partial = new Set(query.data?.partial ?? []);
  const missing = (col: string) => partial.has(col);

  const toggle = (pid: number) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (!next.delete(pid)) next.add(pid);
      return next;
    });

  const kill = async (p: HostProcess, force: boolean) => {
    if (!sessionId) return;
    setKillMessage(null);
    setBusyPid(p.pid);
    try {
      const request = { sessionId, pid: p.pid, name: p.name, force };
      const plan = await hostProcessesApi.kill({ ...request, dryRun: true });
      const warning = production ? "【生产主机】此命令将在生产环境的主机上执行，请确认目标无误。\n\n" : "";
      const note = force ? "\n\n强制结束会直接终止进程，进程不会有机会清理。" : "";
      const ok = await confirmDialog(
        `${warning}将在 ${hostName} 上${force ? "强制结束" : "结束"}进程 ${p.name}（PID ${p.pid}），执行：\n${plan.command}${note}`,
      );
      if (!ok) return;
      const result = await hostProcessesApi.kill(request);
      if (result.ok) {
        setKillMessage({
          ok: true,
          text: result.auditError
            ? `已执行，但审计记录写入失败：${result.auditError}`
            : `已向进程 ${p.name}（PID ${p.pid}）发送 ${result.signal}。`,
        });
        loading.current = false;
        void refresh();
      } else {
        const detail = result.output || "命令未成功";
        setKillMessage({
          ok: false,
          text: result.auditError ? `${detail}（已执行，但审计记录写入失败：${result.auditError}）` : detail,
        });
      }
    } catch (e) {
      setKillMessage({ ok: false, text: describeError(e) });
    } finally {
      setBusyPid(null);
    }
  };

  const visibleRows = rows.slice(0, MAX_ROWS);
  const fetching = query.isFetching;
  return (
    <HostSection
      title="进程"
      icon={<IconActivity size={14} />}
      testId="host-processes"
      actions={
        connected && visible ? (
          <>
            <label className="flex items-center gap-1 text-[12px] text-neutral-400">
              <input
                type="checkbox"
                aria-label="自动刷新（5 秒）"
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
              自动刷新（5 秒）
            </label>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-xs"
              aria-label="刷新进程"
              disabled={fetching}
              onClick={() => void refresh()}
            >
              <IconRefresh size={11} className={fetching ? "animate-spin" : ""} />
              刷新
            </button>
          </>
        ) : null
      }
    >
      {!connected || !sessionId ? (
        <SectionHint>打开并连接工作区后可查看进程。</SectionHint>
      ) : (
        <>
          {query.isError ? (
            <p className="m-0 text-[12px] text-red-300">未能读取进程：{describeError(query.error)}</p>
          ) : !query.data ? (
            <SectionHint>点击「刷新」读取进程列表（会在主机上执行 ps 或等效命令）。</SectionHint>
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-1.5">
                <input
                  className="nx-input min-w-0 flex-1 basis-40"
                  aria-label="搜索进程"
                  placeholder="按名称、命令、用户或 PID 搜索"
                  value={queryText}
                  onChange={(e) => setQueryText(e.target.value)}
                />
                <div className="nx-segment" role="group" aria-label="排序">
                  {(["cpu", "mem"] as const).map((k) => (
                    <button
                      key={k}
                      type="button"
                      className={`nx-segment-item ${sort === k ? "is-active" : ""}`}
                      aria-pressed={sort === k}
                      onClick={() => setSort(k)}
                    >
                      {k === "cpu" ? "CPU" : "内存"}
                    </button>
                  ))}
                </div>
                <label className="flex items-center gap-1 text-[12px] text-neutral-400">
                  <input type="checkbox" aria-label="树形" checked={tree} onChange={(e) => setTree(e.target.checked)} />
                  树形
                </label>
              </div>
              {partial.size > 0 && (
                <SectionHint>
                  该主机未提供：{[...partial].map((c) => COLUMN_LABELS[c] ?? c).join("、")}
                </SectionHint>
              )}
              {rows.length === 0 ? (
                <SectionHint>{queryText ? "没有匹配的进程。" : "未发现进程。"}</SectionHint>
              ) : (
                <div className="overflow-x-auto">
                  <table className="nx-table w-full text-[12px]">
                    <thead>
                      <tr>
                        <th>PID</th>
                        <th>名称</th>
                        <th>用户</th>
                        <th>CPU%</th>
                        <th>内存</th>
                        <th>运行时长</th>
                        <th>状态</th>
                        <th>监听端口</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {visibleRows.map(({ process: p, depth, hasChildren, expanded }) => {
                        const ports = portsByPid.get(p.pid) ?? [];
                        return (
                          <tr key={p.pid} data-pid={p.pid}>
                            <td className="nx-mono">{p.pid}</td>
                            <td title={p.command}>
                              <span className="inline-flex items-center gap-0.5" style={{ paddingLeft: depth * 14 }}>
                                {tree &&
                                  (hasChildren ? (
                                    <button
                                      type="button"
                                      className="nx-btn nx-btn-ghost nx-btn-xs"
                                      aria-label={`${expanded ? "折叠" : "展开"} ${p.pid}`}
                                      aria-expanded={expanded}
                                      onClick={() => toggle(p.pid)}
                                    >
                                      {expanded ? <IconChevronDown size={11} /> : <IconChevronRight size={11} />}
                                    </button>
                                  ) : (
                                    <span className="inline-block w-[22px]" />
                                  ))}
                                {p.name}
                              </span>
                            </td>
                            <td>{missing("user") ? "-" : p.user || "-"}</td>
                            <td className="nx-mono">{missing("cpu") ? "-" : p.cpu.toFixed(1)}</td>
                            <td className="nx-mono">
                              {missing("mem") ? "-" : `${p.mem.toFixed(1)}% · ${formatRss(p.rssKiB)}`}
                            </td>
                            <td>{formatElapsed(p)}</td>
                            <td>{p.zombie ? "僵尸" : p.stat || "-"}</td>
                            <td>
                              {ports.length === 0
                                ? "-"
                                : ports.map((l) => (
                                    <span key={`${l.proto}|${l.address}|${l.port}`} className="mr-1 inline-flex items-center gap-0.5">
                                      <span className="nx-mono">{l.port}</span>
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
                                    </span>
                                  ))}
                            </td>
                            <td className="whitespace-nowrap">
                              {p.pid > 1 && (
                                <>
                                  <button
                                    type="button"
                                    className="nx-btn nx-btn-ghost nx-btn-xs"
                                    aria-label={`结束进程 ${p.pid}`}
                                    title={p.zombie ? "僵尸进程已退出，需结束其父进程" : undefined}
                                    disabled={busyPid !== null || p.zombie}
                                    onClick={() => void kill(p, false)}
                                  >
                                    结束
                                  </button>
                                  <button
                                    type="button"
                                    className="nx-btn nx-btn-ghost nx-btn-xs"
                                    aria-label={`强制结束进程 ${p.pid}`}
                                    disabled={busyPid !== null || p.zombie}
                                    onClick={() => void kill(p, true)}
                                  >
                                    强制结束
                                  </button>
                                </>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              )}
              {rows.length > MAX_ROWS && (
                <SectionHint>共 {rows.length} 个进程，仅渲染前 {MAX_ROWS} 个，请用搜索缩小范围。</SectionHint>
              )}
            </>
          )}
          {query.data?.truncated && <SectionHint>进程较多，仅显示一部分。</SectionHint>}
          {portsError && <SectionHint>未能读取端口，监听端口列不可用。</SectionHint>}
          {killMessage && (
            <p
              className={`m-0 whitespace-pre-wrap break-all text-[12px] ${killMessage.ok ? "text-green-300" : "text-red-300"}`}
              role="status"
            >
              {killMessage.text}
            </p>
          )}
        </>
      )}
    </HostSection>
  );
}
