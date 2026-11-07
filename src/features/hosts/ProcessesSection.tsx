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
import { ListToolbar, Pager, useListFocus, usePaging, type ListFocus } from "./listControls";
import { buildRows, type ProcessSort } from "./processTree";
import { t } from "../../i18n";

const AUTO_REFRESH_MS = 5000;
const COLUMN_LABELS: Record<string, string> = { cpu: "CPU", mem: t("内存"), user: t("用户"), elapsed: t("运行时长") };
const CPU_TITLE = t("POSIX：相对单核的百分比，多线程进程可超过 100；Windows：占全部 CPU 的比例。");
const WINDOWS_CRITICAL = new Set(["csrss", "wininit", "smss", "services", "lsass", "winlogon"]);

function isCriticalWindows(p: HostProcess, platform: string | undefined): boolean {
  if (platform !== "windows") return false;
  if (p.pid === 4) return true;
  return WINDOWS_CRITICAL.has(p.name.toLowerCase().replace(/\.exe$/, ""));
}

function formatElapsed(p: HostProcess): string {
  if (p.elapsedSec < 0) return p.elapsed || "-";
  const s = p.elapsedSec;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return t("{d}天{h}小时", { d, h });
  if (h > 0) return t("{h}小时{m}分", { h, m });
  if (m > 0) return t("{m}分{value}秒", { m, value: s % 60 });
  return t("{s}秒", { s });
}

function formatRss(kib: number): string {
  if (kib >= 1048576) return `${(kib / 1048576).toFixed(1)} GiB`;
  if (kib >= 1024) return `${(kib / 1024).toFixed(1)} MiB`;
  return `${kib} KiB`;
}

// ProcessesSection lists the processes of a connected workspace's host. It reads only on demand (or on the
// opt-in 5 second timer), because the command runs on the host. focus asks it to show one PID, reading the list
// first when it has none; onShowPort opens a listening port in the ports list.
export function ProcessesSection({
  workspace,
  visible,
  hostName,
  focus,
  onShowPort,
}: {
  workspace: Workspace | undefined;
  visible: boolean;
  hostName: string;
  focus?: ListFocus<number> | null;
  onShowPort?: (port: number) => void;
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
  const pendingRefresh = useRef(false);

  // queueIfBusy: run one more refresh after the current one instead of dropping the request.
  const refresh = useCallback(async (queueIfBusy = false) => {
    if (!sessionId) return;
    if (loading.current) {
      if (queueIfBusy) pendingRefresh.current = true;
      return;
    }
    loading.current = true;
    try {
      do {
        pendingRefresh.current = false;
        const ports = queryClient
          .fetchQuery({
            queryKey: portsKey,
            queryFn: () => hostPortsApi.list({ sessionId, processes: true }),
            staleTime: 0,
          })
          .then(() => setPortsError(false))
          .catch(() => setPortsError(true));
        await Promise.all([query.refetch(), ports]);
      } while (pendingRefresh.current);
    } finally {
      loading.current = false;
    }
  }, [sessionId, queryClient, portsKey, query.refetch]); // eslint-disable-line react-hooks/exhaustive-deps

  const active = connected && visible;
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  useEffect(() => {
    if (!autoRefresh || !active) return;
    const id = setInterval(() => {
      if (!document.hidden) void refreshRef.current();
    }, AUTO_REFRESH_MS);
    const onVisibility = () => {
      if (!document.hidden) void refreshRef.current();
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVisibility);
    };
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
  const skippedRows = query.data?.skippedRows ?? 0;
  const platform = query.data?.platform;
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
      const request = { sessionId, pid: p.pid, ppid: p.ppid, name: p.argName ?? p.name, started: p.started, force };
      const plan = await hostProcessesApi.kill({ ...request, dryRun: true });
      const warning = production ? t("【生产主机】此命令将在生产环境的主机上执行，请确认目标无误。\n\n") : "";
      const note = force ? t("\n\n强制结束会直接终止进程，进程不会有机会清理。") : "";
      const ok = await confirmDialog(
        t("{warning}将在 {hostName} 上{value}进程 {name}（PID {pid}），执行：\n{command}{note}", { warning, hostName, value: force ? t("强制结束") : t("结束"), name: p.name, pid: p.pid, command: plan.command, note }),
      );
      if (!ok) return;
      const result = await hostProcessesApi.kill(request);
      if (result.ok) {
        setKillMessage({
          ok: true,
          text: result.auditError
            ? t("已执行，但审计记录写入失败：{auditError}", { auditError: result.auditError })
            : t("已向进程 {name}（PID {pid}）发送 {signal}。", { name: p.name, pid: p.pid, signal: result.signal }),
        });
        void refresh(true);
      } else {
        const detail = result.output || t("命令未成功");
        setKillMessage({
          ok: false,
          text: result.auditError ? t("{detail}（已执行，但审计记录写入失败：{auditError}）", { detail, auditError: result.auditError }) : detail,
        });
      }
    } catch (e) {
      setKillMessage({ ok: false, text: describeError(e) });
    } finally {
      setBusyPid(null);
    }
  };

  const paging = usePaging(rows, `${queryText}|${sort}|${tree}`);
  const clearFilters = useCallback(() => setQueryText(""), []);
  // A PID asked for before the list was read: read it once, then find the row.
  const requested = useRef<number | null>(null);
  useEffect(() => {
    if (!focus || requested.current === focus.nonce || query.data || !connected) return;
    requested.current = focus.nonce;
    void refresh();
  }, [focus, query.data, connected, refresh]);
  const highlight = useListFocus({
    focus,
    indexOf: (pid) => rows.findIndex((row) => row.process.pid === pid),
    filtered: queryText !== "",
    clearFilters,
    setPage: paging.setPage,
    pageOf: paging.pageOf,
    rowSelector: useCallback((pid: number) => `[data-testid="host-processes"] tr[data-pid="${pid}"]`, []),
  });
  const fetching = query.isFetching;
  return (
    <HostSection
      title={t("进程")}
      icon={<IconActivity size={14} />}
      testId="host-processes"
      actions={
        connected && visible ? (
          <>
            {query.dataUpdatedAt > 0 && (
              <span className="nx-hint text-[11px]">{t("读取于")} {new Date(query.dataUpdatedAt).toLocaleTimeString()}</span>
            )}
            <label className="flex items-center gap-1 text-[12px] text-neutral-400">
              <input
                type="checkbox"
                aria-label={t("自动刷新（5 秒）")}
                checked={autoRefresh}
                onChange={(e) => setAutoRefresh(e.target.checked)}
              />
              {t("自动刷新（5 秒）")}
            </label>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-xs"
              aria-label={t("刷新进程")}
              disabled={fetching}
              onClick={() => void refresh()}
            >
              <IconRefresh size={11} className={fetching ? "animate-spin" : ""} />
              {query.data ? t("刷新") : t("读取")}
            </button>
          </>
        ) : null
      }
    >
      {!connected || !sessionId ? (
        <SectionHint>{t("打开并连接工作区后可查看进程。")}</SectionHint>
      ) : (
        <>
          {query.isError ? (
            <p className="m-0 text-[12px] text-red-300">{t("未能读取进程：")}{describeError(query.error)}</p>
          ) : !query.data ? (
            <SectionHint>{fetching ? t("正在读取进程列表…") : t("点击「读取」查看进程列表（会在主机上执行 ps 或等效命令）。")}</SectionHint>
          ) : (
            <>
              <ListToolbar
                label={t("搜索进程")}
                placeholder={t("按名称、命令、用户或 PID 搜索")}
                query={queryText}
                onQuery={setQueryText}
                total={processes?.length ?? 0}
                matched={queryText ? rows.length : processes?.length ?? 0}
                unit={t("个")}
              >
                <div className="nx-segment" role="group" aria-label={t("排序")}>
                  {(["cpu", "mem"] as const).map((k) => (
                    <button
                      key={k}
                      type="button"
                      className={`nx-segment-item ${sort === k ? "is-active" : ""}`}
                      aria-pressed={sort === k}
                      onClick={() => setSort(k)}
                    >
                      {k === "cpu" ? "CPU" : t("内存")}
                    </button>
                  ))}
                </div>
                <label className="flex items-center gap-1 text-[12px] text-neutral-400">
                  <input type="checkbox" aria-label={t("树形")} checked={tree} onChange={(e) => setTree(e.target.checked)} />
                  {t("树形")}
                </label>
              </ListToolbar>
              {partial.size > 0 && (
                <SectionHint>
                  {t("该主机未提供：")}{[...partial].map((c) => COLUMN_LABELS[c] ?? c).join("、")}
                </SectionHint>
              )}
              {skippedRows > 0 && <SectionHint>{t("有")} {skippedRows} {t("行无法解析，已跳过。")}</SectionHint>}
              {rows.length === 0 ? (
                <SectionHint>{queryText ? t("没有匹配的进程。") : t("未发现进程。")}</SectionHint>
              ) : (
                <div className="overflow-x-auto">
                  <table className="nx-table w-full text-[12px]">
                    <thead>
                      <tr>
                        <th>PID</th>
                        <th>{t("名称")}</th>
                        <th>{t("用户")}</th>
                        <th title={CPU_TITLE}>CPU%</th>
                        <th>{t("内存")}</th>
                        <th>{t("运行时长")}</th>
                        <th>{t("状态")}</th>
                        <th>{t("监听端口")}</th>
                        <th />
                      </tr>
                    </thead>
                    <tbody>
                      {paging.shown.map(({ process: p, depth, hasChildren, expanded }) => {
                        const ports = portsByPid.get(p.pid) ?? [];
                        return (
                          <tr key={p.pid} data-pid={p.pid} className={highlight === p.pid ? "is-focused" : undefined}>
                            <td className="nx-mono">{p.pid}</td>
                            <td title={p.command}>
                              <span className="inline-flex items-center gap-0.5" style={{ paddingLeft: depth * 14 }}>
                                {tree &&
                                  (hasChildren ? (
                                    <button
                                      type="button"
                                      className="nx-btn nx-btn-ghost nx-btn-xs"
                                      aria-label={`${expanded ? t("折叠") : t("展开")} ${p.pid}`}
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
                            <td>{missing("elapsed") ? "-" : formatElapsed(p)}</td>
                            <td>{p.zombie ? t("僵尸") : p.stat || "-"}</td>
                            <td>
                              {ports.length === 0
                                ? "-"
                                : ports.map((l) => (
                                    <span key={`${l.proto}|${l.address}|${l.port}`} className="mr-1 inline-flex items-center gap-0.5">
                                      {onShowPort ? (
                                        <button
                                          type="button"
                                          className="nx-link nx-mono"
                                          title={t("在端口列表中查看")}
                                          aria-label={t("查看端口 {port}", { port: l.port })}
                                          onClick={() => onShowPort(l.port)}
                                        >
                                          {l.port}
                                        </button>
                                      ) : (
                                        <span className="nx-mono">{l.port}</span>
                                      )}
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
                                    </span>
                                  ))}
                            </td>
                            <td className="whitespace-nowrap">
                              {p.pid > 1 && !isCriticalWindows(p, platform) && (
                                <>
                                  <button
                                    type="button"
                                    className="nx-btn nx-btn-ghost nx-btn-xs"
                                    aria-label={t("结束进程 {pid}", { pid: p.pid })}
                                    title={p.zombie ? t("僵尸进程已退出，需结束其父进程") : undefined}
                                    disabled={busyPid !== null || p.zombie}
                                    onClick={() => void kill(p, false)}
                                  >
                                    {t("结束")}
                                  </button>
                                  <button
                                    type="button"
                                    className="nx-btn nx-btn-ghost nx-btn-xs"
                                    aria-label={t("强制结束进程 {pid}", { pid: p.pid })}
                                    disabled={busyPid !== null || p.zombie}
                                    onClick={() => void kill(p, true)}
                                  >
                                    {t("强制结束")}
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
              <Pager label={t("进程分页")} page={paging.page} pages={paging.pages} onPage={paging.setPage} />
            </>
          )}
          {query.data?.truncated && <SectionHint>{t("进程较多，仅显示一部分。")}</SectionHint>}
          {portsError && <SectionHint>{t("未能读取端口，监听端口列不可用。")}</SectionHint>}
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
