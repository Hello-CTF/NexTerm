import { useEffect, useMemo, useState } from "react";
import type { SessionInfo } from "../../ipc/commands";
import { createExecBatchId } from "../../ipc/execBatchApi";
import {
  servicesApi,
  type ActionRequest,
  type ListRequest,
  type ServiceOperationBatch,
} from "../../ipc/servicesApi";
import { IconPlay, IconRefresh, IconServer } from "../../ui/icons";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import { OperationFeedback, OperationProductionDialog } from "./OperationFeedback";
import { ServiceJournal } from "./ServiceJournal";
import { ServiceOperationResults } from "./ServiceOperationResults";
import { filterServiceUnits, SERVICE_ACTION_LABELS, serviceResultMessage, type ServiceStateFilter } from "./model";
import { useExecOperation } from "./useExecOperation";

const MAX_TARGETS = 64;
const ROW_ACTIONS = ["status", "start", "stop", "restart", "reload", "enable", "disable"] as const;

type ServiceRequest = ListRequest | ActionRequest;

export function ServicesPanel({
  targets,
  sessions,
  metadataError,
}: {
  targets: readonly TargetSetPickerTarget[];
  sessions: SessionInfo[];
  metadataError?: string | null;
}) {
  const [selected, setSelected] = useState<string[]>([]);
  const [sudo, setSudo] = useState(true);
  const [unitName, setUnitName] = useState("nginx.service");
  const [bulkAction, setBulkAction] = useState("restart");
  const [filter, setFilter] = useState("");
  const [stateFilter, setStateFilter] = useState<ServiceStateFilter>("all");
  const [listBatch, setListBatch] = useState<ServiceOperationBatch | null>(null);
  const [actionBatch, setActionBatch] = useState<ServiceOperationBatch | null>(null);
  const [journal, setJournal] = useState<{ sessionId: string; host: string; unit: string } | null>(null);

  const operation = useExecOperation<ServiceRequest, ServiceOperationBatch>(async (request) => {
    if ("action" in request) return servicesApi.action(request);
    return servicesApi.list(request);
  });

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => current.filter((id) => available.has(id)));
  }, [targets]);

  const selectedIds = useMemo(
    () => targets.filter((target) => selected.includes(target.id)).map((target) => target.id),
    [selected, targets],
  );

  const execute = (request: ServiceRequest, summary: string) => {
    void operation.execute(request, summary, (batch) => {
      if ("action" in request) setActionBatch(batch);
      else {
        setListBatch(batch);
        setActionBatch(null);
      }
    });
  };

  const load = () => {
    if (selectedIds.length === 0) {
      return;
    }
    setJournal(null);
    execute({
      batchId: createExecBatchId(), sessionIds: selectedIds, sudo, timeoutMs: 30000, maxConcurrent: 4,
    }, "systemctl list-units --type=service --all");
  };

  const runAction = (action: string, unit: string, sessionIds: string[]) => {
    if (!unit.trim() || sessionIds.length === 0) return;
    setJournal(null);
    execute({
      batchId: createExecBatchId(), sessionIds, unit: unit.trim(), action, sudo, timeoutMs: 30000, maxConcurrent: 4,
    }, `systemctl ${action} -- ${unit.trim()}`);
  };

  const selectionError = selectedIds.length === 0 ? "请至少选择一个已连接的 SSH 或受管设备会话" : null;

  return (
    <section className="flex min-h-0 flex-1 flex-col" aria-label="systemd 服务管理" data-testid="services-panel">
      <div className="flex max-h-[52%] min-h-[220px] flex-col gap-3 overflow-y-auto border-b border-neutral-800/70 px-3 py-3">
        <TargetSetPicker
          targets={targets}
          selectedIds={selectedIds}
          onChange={(ids) => {
            setSelected(ids);
            operation.clearProduction();
          }}
          disabled={operation.running}
          maxTargets={MAX_TARGETS}
          metadataError={metadataError}
          legend="服务管理目标"
          searchAriaLabel="搜索服务管理目标"
        />
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex min-w-52 flex-1 flex-col gap-1 text-[11px] text-neutral-500">
            跨目标 unit
            <input
              className="nx-input font-mono text-xs"
              value={unitName}
              disabled={operation.running}
              aria-label="批量服务 unit"
              placeholder="nginx.service"
              onChange={(event) => {
                setUnitName(event.target.value);
                operation.clearProduction();
              }}
            />
          </label>
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            操作
            <select className="nx-select w-28" value={bulkAction} disabled={operation.running} aria-label="批量服务操作" onChange={(event) => {
              setBulkAction(event.target.value);
              operation.clearProduction();
            }}>
              {Object.entries(SERVICE_ACTION_LABELS).map(([action, label]) => <option key={action} value={action}>{label}</option>)}
            </select>
          </label>
          <label className="flex cursor-pointer items-center gap-2 pb-2 text-[11px] text-neutral-400">
            <input type="checkbox" className="accent-blue-500" checked={sudo} disabled={operation.running} onChange={(event) => {
              setSudo(event.target.checked);
              operation.clearProduction();
            }} />
            需要权限时使用 sudo -n
          </label>
          <span className="nx-spacer" />
          <button type="button" className="nx-btn" disabled={operation.running || targets.length === 0} onClick={() => {
            if (selectionError) {
              setListBatch(null);
              return;
            }
            load();
          }}>
            <IconRefresh size={13} />加载服务
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={operation.running || targets.length === 0 || !unitName.trim()} onClick={() => runAction(bulkAction, unitName, selectedIds)}>
            <IconPlay size={13} />应用到已选目标
          </button>
        </div>
        <div className="text-[11px] leading-relaxed text-neutral-500">
          sudo 仅非交互执行；需要密码或不可用时会显示在对应主机结果中。status 与 journal 为只读，写操作通过 execbatch 生产保护。
        </div>
        {selectionError && <div className="text-[11px] text-amber-300" role="status">{selectionError}</div>}
      </div>

      <OperationFeedback {...operation} onCancel={() => void operation.cancel()} />
      <OperationProductionDialog pending={operation.production} onConfirm={operation.confirmProduction} onCancel={operation.clearProduction} />

      {!journal && listBatch && (
        <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
          <IconServer size={13} className="text-neutral-500" />
          <input className="nx-input nx-input-sm w-64" value={filter} placeholder="过滤 unit / 描述 / 状态" aria-label="过滤服务" onChange={(event) => setFilter(event.target.value)} />
          <select className="nx-select nx-select-sm w-32" value={stateFilter} aria-label="服务状态过滤" onChange={(event) => setStateFilter(event.target.value as ServiceStateFilter)}>
            <option value="all">全部状态</option><option value="running">运行中</option><option value="failed">失败</option><option value="inactive">未运行</option><option value="other">其他</option>
          </select>
          <span className="nx-hint">批次 {listBatch.batchId}</span>
        </div>
      )}

      {journal ? (
        <ServiceJournal
          session={sessions.find((session) => session.id === journal.sessionId) ?? { id: journal.sessionId, assetId: null, name: journal.host, kind: "ssh", status: "connected", tabs: [], createdAt: 0 }}
          host={journal.host}
          unit={journal.unit}
          onClose={() => setJournal(null)}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          {!listBatch ? (
            <div className="px-3 py-8 text-center text-[11px] text-neutral-500">选择主机并加载 systemd services；无 systemd 的主机会明确标记为 unsupported。</div>
          ) : listBatch.hosts.map((host) => {
            const units = filterServiceUnits(host.units, filter, stateFilter);
            const hostName = host.result.sessionName || host.result.sessionId;
            return (
              <article key={host.result.sessionId} className="border-b border-neutral-800/70 px-3 py-3" data-session-id={host.result.sessionId}>
                <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px]">
                  <h3 className="font-medium text-neutral-200">{hostName}</h3>
                  {host.systemd === "unsupported" ? <span className="nx-badge nx-badge-amber">unsupported：无 systemd</span> : (
                    <span className={host.result.status === "success" ? "text-green-300" : "text-red-300"}>{host.result.status === "success" ? `${host.units.length} 个 unit` : "加载失败"}</span>
                  )}
                  {host.result.truncated && <span className="nx-badge nx-badge-amber">输出截断，列表可能不完整</span>}
                  <span className="nx-hint">{host.result.sessionId}</span>
                </div>
                {host.result.status !== "success" && (
                  <div role="alert" className="mb-2 whitespace-pre-wrap break-all text-[11px] text-red-300">{serviceResultMessage(host)}</div>
                )}
                {host.result.status === "success" && units.length === 0 && (
                  <div className="py-2 text-[11px] text-neutral-500">没有匹配当前过滤条件的 service unit。</div>
                )}
                {units.length > 0 && (
                  <div className="overflow-x-auto">
                    <table className="nx-table nx-table-fixed min-w-[1120px]">
                      <thead><tr><th className="w-1/4">Unit</th><th className="w-20">LOAD</th><th className="w-20">ACTIVE</th><th className="w-24">SUB</th><th>描述</th><th className="w-[490px]">操作</th></tr></thead>
                      <tbody>
                        {units.map((unit) => (
                          <tr key={unit.name} data-unit={unit.name}>
                            <td className="truncate font-mono text-neutral-200" title={unit.name}>{unit.name}</td>
                            <td>{unit.load}</td><td className={unit.active === "failed" ? "text-red-300" : unit.active === "active" ? "text-green-300" : "text-neutral-400"}>{unit.active}</td><td>{unit.sub}</td>
                            <td className="truncate text-neutral-400" title={unit.description}>{unit.description}</td>
                            <td>
                              <div className="flex flex-nowrap gap-1">
                                {ROW_ACTIONS.map((action) => (
                                  <button key={action} type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={operation.running} onClick={() => runAction(action, unit.name, [host.result.sessionId])}>{SERVICE_ACTION_LABELS[action]}</button>
                                ))}
                                <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={operation.running} onClick={() => setJournal({ sessionId: host.result.sessionId, host: hostName, unit: unit.name })}>journal</button>
                              </div>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </article>
            );
          })}
          {actionBatch && <ServiceOperationResults batch={actionBatch} title="服务操作逐目标结果" />}
        </div>
      )}
    </section>
  );
}
