import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { openTerminalTab } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";
import { createExecBatchId } from "../../ipc/execBatchApi";
import { forwardRuleApi } from "../../ipc/forwardRuleApi";
import {
  servicesApi,
  type FindRequest,
  type FinderBatch,
  type FinderMatch,
  type KillRequest,
  type ServiceOperationBatch,
} from "../../ipc/servicesApi";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { IconNetwork, IconSearch, IconTerminal, IconTrash } from "../../ui/icons";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import { OperationFeedback, OperationProductionDialog } from "./OperationFeedback";
import { ServiceOperationResults } from "./ServiceOperationResults";
import {
  formatListenAddress,
  listenerTargetHost,
  matchCommand,
  matchPID,
  parseLocalListenPort,
} from "./model";
import { useExecOperation } from "./useExecOperation";

const MAX_TARGETS = 64;

type QueryKind = "port" | "process" | "regex";

interface ForwardDraft {
  sessionId: string;
  assetId: string;
  host: string;
  address: string;
  port: number;
  listenPort: string;
}

export function ProcessFinderPanel({
  targets,
  sessions,
  metadataError,
}: {
  targets: readonly TargetSetPickerTarget[];
  sessions: SessionInfo[];
  metadataError?: string | null;
}) {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<string[]>([]);
  const [queryKind, setQueryKind] = useState<QueryKind>("process");
  const [queryValue, setQueryValue] = useState("nginx");
  const [sudo, setSudo] = useState(true);
  const [rowError, setRowError] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [forwardDraft, setForwardDraft] = useState<ForwardDraft | null>(null);
  const [forwardBusy, setForwardBusy] = useState(false);

  const finder = useExecOperation<FindRequest, FinderBatch>(servicesApi.find);
  const killer = useExecOperation<KillRequest, ServiceOperationBatch>(servicesApi.kill);
  const busy = finder.running || killer.running || forwardBusy;

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => current.filter((id) => available.has(id)));
  }, [targets]);

  const selectedIds = useMemo(
    () => targets.filter((target) => selected.includes(target.id)).map((target) => target.id),
    [selected, targets],
  );

  const find = () => {
    setRowError(null);
    setMessage(null);
    setForwardDraft(null);
    if (selectedIds.length === 0) {
      setRowError("请至少选择一个已连接的 SSH 或受管设备会话");
      return;
    }
    if (!queryValue.trim()) {
      setRowError("请输入端口、进程名或正则");
      return;
    }
    void finder.execute({
      batchId: createExecBatchId(), sessionIds: selectedIds,
      query: { kind: queryKind, value: queryValue.trim() }, sudo, timeoutMs: 30000, maxConcurrent: 4,
    }, `find ${queryKind} ${queryValue.trim()}`);
  };

  const openTerminal = async (sessionId: string) => {
    const session = sessions.find((candidate) => candidate.id === sessionId);
    setRowError(null);
    setMessage(null);
    if (!session) {
      setRowError("该会话已不可用，请重新查询后再打开终端");
      return;
    }
    try {
      await openTerminalTab(session);
      setMessage(`已打开 ${session.name} 的终端`);
    } catch (cause) {
      setRowError(`打开终端失败：${describeError(cause)}`);
    }
  };

  const beginForward = (sessionId: string, host: string, match: FinderMatch) => {
    setRowError(null);
    setMessage(null);
    const session = sessions.find((candidate) => candidate.id === sessionId);
    const listener = match.listener;
    if (!session?.assetId) {
      setRowError("临时 SSH 会话没有资产 ID，不能保存 P-35 转发规则");
      return;
    }
    if (!listener || listener.port < 1) {
      setRowError("该进程行没有可转发的监听端口");
      return;
    }
    setForwardDraft({
      sessionId,
      assetId: session.assetId,
      host,
      address: listenerTargetHost(listener.address),
      port: listener.port,
      listenPort: String(listener.port),
    });
  };

  const saveForward = async () => {
    const draft = forwardDraft;
    if (!draft || forwardBusy) return;
    const listenPort = parseLocalListenPort(draft.listenPort);
    setRowError(null);
    setMessage(null);
    if (!listenPort) {
      setRowError("本地监听端口必须是 1-65535 的整数");
      return;
    }
    setForwardBusy(true);
    try {
      const rule = await forwardRuleApi.save({
        assetId: draft.assetId,
        name: `${draft.host} ${draft.address}:${draft.port}`,
        listenPort,
        targetHost: draft.address,
        targetPort: draft.port,
      });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["forwardRules"] }),
        queryClient.invalidateQueries({ queryKey: ["forwards"] }),
      ]);
      setForwardDraft(null);
      setMessage(`已保存转发规则 127.0.0.1:${rule.listenPort} → ${rule.targetHost}:${rule.targetPort}；未自动启动`);
    } catch (cause) {
      setRowError(`保存转发规则失败：${describeError(cause)}`);
    } finally {
      setForwardBusy(false);
    }
  };

  const kill = async (sessionId: string, host: string, match: FinderMatch) => {
    const pid = matchPID(match);
    if (pid <= 0 || busy) return;
    setRowError(null);
    setMessage(null);
    const confirmed = await ask(
      `确定向 ${host} 的 PID ${pid} 发送 SIGTERM？\n\n${matchCommand(match)}\n\n生产主机还会要求 P-31 二次确认；取消不会执行 kill。`,
      { title: "确认 kill 进程", kind: "warning" },
    );
    if (!confirmed) return;
    void killer.execute({
      batchId: createExecBatchId(), sessionId, pid, sudo, timeoutMs: 30000,
    }, `kill -TERM -- ${pid}`);
  };

  const result = finder.result;
  const matchCount = result?.hosts.reduce((count, host) => count + host.matches.length, 0) ?? 0;

  return (
    <section className="flex min-h-0 flex-1 flex-col" aria-label="端口与进程查找" data-testid="process-finder-panel">
      <form className="flex max-h-[52%] min-h-[220px] flex-col gap-3 overflow-y-auto border-b border-neutral-800/70 px-3 py-3" onSubmit={(event) => {
        event.preventDefault();
        find();
      }}>
        <TargetSetPicker
          targets={targets}
          selectedIds={selectedIds}
          onChange={(ids) => {
            setSelected(ids);
            finder.clearProduction();
          }}
          disabled={busy}
          maxTargets={MAX_TARGETS}
          metadataError={metadataError}
          legend="端口 / 进程目标"
          searchAriaLabel="搜索端口进程目标"
        />
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            查询类型
            <select className="nx-select w-32" value={queryKind} disabled={busy} aria-label="进程查询类型" onChange={(event) => {
              const kind = event.target.value as QueryKind;
              setQueryKind(kind);
              setQueryValue(kind === "port" ? "8080" : kind === "regex" ? "nginx|postgres" : "nginx");
              finder.clearProduction();
            }}>
              <option value="port">端口</option><option value="process">进程名</option><option value="regex">正则（RE2）</option>
            </select>
          </label>
          <label className="flex min-w-52 flex-1 flex-col gap-1 text-[11px] text-neutral-500">
            查询
            <input className="nx-input font-mono text-xs" value={queryValue} disabled={busy} aria-label="端口进程查询" onChange={(event) => {
              setQueryValue(event.target.value);
              finder.clearProduction();
            }} />
          </label>
          <label className="flex cursor-pointer items-center gap-2 pb-2 text-[11px] text-neutral-400">
            <input type="checkbox" className="accent-blue-500" checked={sudo} disabled={busy} onChange={(event) => {
              setSudo(event.target.checked);
              finder.clearProduction();
              killer.clearProduction();
            }} />
            使用 sudo -n 读取完整进程信息
          </label>
          <span className="nx-spacer" />
          <button type="submit" className="nx-btn nx-btn-primary" disabled={busy || targets.length === 0}>
            <IconSearch size={13} />查找
          </button>
        </div>
        <div className="text-[11px] text-neutral-500">
          每台主机执行 ss -ltnp（缺失时 netstat -ltnp）与 ps -eo pid,user,%cpu,%mem,etime,args；过滤在 Go 端完成，查询不会进入 shell。
        </div>
      </form>

      <OperationFeedback {...finder} onCancel={() => void finder.cancel()} />
      <OperationFeedback {...killer} onCancel={() => void killer.cancel()} />
      <OperationProductionDialog pending={finder.production} onConfirm={finder.confirmProduction} onCancel={finder.clearProduction} />
      <OperationProductionDialog pending={killer.production} onConfirm={killer.confirmProduction} onCancel={killer.clearProduction} />
      {rowError && <div role="alert" className="border-b border-red-900/50 px-3 py-1.5 text-[11px] text-red-300">{rowError}</div>}
      {message && <div role="status" className="border-b border-neutral-800/70 px-3 py-1.5 text-[11px] text-green-300">{message}</div>}

      {forwardDraft && (
        <form className="flex flex-wrap items-end gap-2 border-b border-neutral-800/70 bg-neutral-900/30 px-3 py-2" aria-label="创建转发规则" onSubmit={(event) => {
          event.preventDefault();
          void saveForward();
        }}>
          <span className="pb-2 text-[11px] text-neutral-400">{forwardDraft.host} · 目标 {forwardDraft.address}:{forwardDraft.port}</span>
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">本地监听端口
            <input className="nx-input nx-input-sm w-28 font-mono" value={forwardDraft.listenPort} aria-label="finder 本地监听端口" inputMode="numeric" onChange={(event) => setForwardDraft({ ...forwardDraft, listenPort: event.target.value })} />
          </label>
          <button type="submit" className="nx-btn nx-btn-primary nx-btn-sm" disabled={forwardBusy}><IconNetwork size={12} />{forwardBusy ? "保存中…" : "保存规则"}</button>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={forwardBusy} onClick={() => setForwardDraft(null)}>取消</button>
          <span className="pb-2 text-[10px] text-neutral-500">仅保存，不自动启动</span>
        </form>
      )}

      <div className="min-h-0 flex-1 overflow-auto">
        {!result ? (
          <div className="px-3 py-8 text-center text-[11px] text-neutral-500">输入查询并选择目标；host × match 结果会保留工具缺失与 sudo 失败。</div>
        ) : (
          <>
            <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2 text-[11px]">
              <span className="font-medium text-neutral-200">{matchCount} 个匹配</span>
              <span className="nx-hint">批次 {result.batchId}</span>
              {result.hosts.some((host) => host.result.truncated) && <span className="nx-badge nx-badge-amber">部分输出截断，匹配可能不完整</span>}
            </div>
            {result.hosts.map((host) => {
              const hostName = host.result.sessionName || host.result.sessionId;
              const hostIncomplete = host.result.status !== "success" || Boolean(host.listenerError) || Boolean(host.processError) || host.result.truncated;
              return (
                <section key={host.result.sessionId} className="border-b border-neutral-800/70" data-session-id={host.result.sessionId}>
                  <div className="flex flex-wrap items-center gap-2 bg-neutral-900/20 px-3 py-2 text-[11px]">
                    <h3 className="font-medium text-neutral-200">{hostName}</h3>
                    <span className="nx-hint">监听器：{host.listenerTool || "不可用"} · {host.matches.length} 个匹配</span>
                    {host.result.truncated && <span className="nx-badge nx-badge-amber">输出截断：该主机结果不完整</span>}
                    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void openTerminal(host.result.sessionId)}><IconTerminal size={11} />终端</button>
                  </div>
                  {host.listenerError && <div role="alert" className="whitespace-pre-wrap break-all px-3 py-1 text-[11px] text-red-300">监听查询失败：{host.listenerError}</div>}
                  {host.processError && <div role="alert" className="whitespace-pre-wrap break-all px-3 py-1 text-[11px] text-red-300">进程查询失败：{host.processError}</div>}
                  {host.result.status !== "success" && !host.listenerError && !host.processError && (
                    <div role="alert" className="px-3 py-1 text-[11px] text-red-300">{host.result.error || host.result.stderr || host.result.status}</div>
                  )}
                  {host.matches.length === 0 ? (
                    hostIncomplete ? (
                      <div role="alert" className="px-3 py-2 text-[11px] text-amber-300">
                        {host.result.truncated ? "输出截断，无法确认是否还有匹配行。" : "探测失败，无法确认是否存在匹配行。"}
                      </div>
                    ) : (
                      <div className="px-3 py-2 text-[11px] text-neutral-500">该主机没有匹配行。</div>
                    )
                  ) : (
                    <div className="nx-cards-wrap overflow-x-auto px-3 pb-2">
                      <table className="nx-table nx-table-cards min-w-[1080px]">
                        <thead><tr><th>类型</th><th>监听</th><th>PID</th><th>用户</th><th>%CPU</th><th>%MEM</th><th>运行时间</th><th>进程</th><th>行操作</th></tr></thead>
                        <tbody>
                          {host.matches.map((match, index) => {
                            const pid = matchPID(match);
                            const listener = match.listener;
                            const process = match.process;
                            return (
                              <tr key={`${match.kind}-${pid}-${listener?.port ?? 0}-${index}`} data-pid={pid || undefined}>
                                <td data-label="类型">{match.kind === "listener" ? "监听" : "进程"}</td>
                                <td data-label="监听" className="nx-mono">{listener ? formatListenAddress(listener.address, listener.port) : "—"}</td>
                                <td data-label="PID" className="nx-mono">{pid || "—"}</td>
                                <td data-label="用户">{process?.user ?? "—"}</td><td data-label="%CPU">{process?.cpu ?? "—"}</td><td data-label="%MEM">{process?.memory ?? "—"}</td><td data-label="运行时间">{process?.elapsed ?? "—"}</td>
                                <td data-label="进程" className="max-w-96 truncate font-mono text-neutral-400" title={matchCommand(match)}>{matchCommand(match)}</td>
                                <td data-label="">
                                  <div className="flex flex-nowrap gap-1 @max-[640px]:flex-wrap">
                                    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void openTerminal(host.result.sessionId)}><IconTerminal size={11} />终端</button>
                                    {listener && <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={busy} onClick={() => beginForward(host.result.sessionId, hostName, match)}><IconNetwork size={11} />转发</button>}
                                    <button type="button" className="nx-btn nx-btn-danger nx-btn-xs" disabled={busy || pid <= 0} title={pid <= 0 ? "无可用 PID" : "确认后发送 SIGTERM"} onClick={() => void kill(host.result.sessionId, hostName, match)}><IconTrash size={11} />kill</button>
                                  </div>
                                </td>
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  )}
                </section>
              );
            })}
          </>
        )}
        {killer.result && <ServiceOperationResults batch={killer.result} title="kill 逐目标结果" />}
      </div>
    </section>
  );
}
