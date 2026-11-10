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
import { Select } from "../../ui/Select";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import { OperationFeedback, OperationProductionDialog } from "./OperationFeedback";
import { ServiceJournal } from "./ServiceJournal";
import { ServiceOperationResults } from "./ServiceOperationResults";
import {
  bulkActionsFor,
  filterServiceUnits,
  isWindowsHost,
  SERVICE_ACTION_LABELS,
  serviceResultMessage,
  SYSTEMD_ROW_ACTIONS,
  WINDOWS_ROW_ACTIONS,
  windowsStartModeLabel,
  windowsStateLabel,
  windowsStateTone,
  type ServiceStateFilter,
} from "./model";
import { useExecOperation } from "./useExecOperation";
import { t } from "../../i18n";

const MAX_TARGETS = 64;

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
    }, windowsSelected ? "Get-CimInstance Win32_Service" : "systemctl list-units --type=service --all");
  };

  const windowsActionSummary = (action: string, unit: string, sessionIds: string[]) => {
    const windows = sessionIds.every((id) => listBatch?.hosts.some((host) => host.result.sessionId === id && isWindowsHost(host)));
    return windows ? `${SERVICE_ACTION_LABELS[action] ?? action} ${unit}` : `systemctl ${action} -- ${unit}`;
  };

  const runAction = (action: string, unit: string, sessionIds: string[]) => {
    if (!unit.trim() || sessionIds.length === 0) return;
    setJournal(null);
    execute({
      batchId: createExecBatchId(), sessionIds, unit: unit.trim(), action, sudo, timeoutMs: 30000, maxConcurrent: 4,
    }, windowsActionSummary(action, unit.trim(), sessionIds));
  };

  const bulkActions = bulkActionsFor(selectedIds, listBatch?.hosts);
  const bulkActionValue = bulkActions.includes(bulkAction) ? bulkAction : "restart";
  const windowsSelected = bulkActions === WINDOWS_ROW_ACTIONS;

  const selectionError = selectedIds.length === 0 ? t("请至少选择一个已连接的 SSH 或受管设备会话") : null;

  return (
    <section className="flex min-h-0 flex-1 flex-col" aria-label={t("systemd 与 Windows 服务管理")} data-testid="services-panel">
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
          legend={t("服务管理目标")}
          searchAriaLabel={t("搜索服务管理目标")}
        />
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex min-w-52 flex-1 flex-col gap-1 text-[11px] text-neutral-500">
            {windowsSelected ? t("跨目标服务名") : t("跨目标 unit")}
            <input
              className="nx-input font-mono text-xs"
              value={unitName}
              disabled={operation.running}
              aria-label={t("批量服务 unit")}
              placeholder={windowsSelected ? "Spooler" : "nginx.service"}
              onChange={(event) => {
                setUnitName(event.target.value);
                operation.clearProduction();
              }}
            />
          </label>
          <div className="flex flex-col gap-1 text-[11px] text-neutral-500">
            {t("操作")}
            <Select className="w-28" value={bulkActionValue} disabled={operation.running} ariaLabel={t("批量服务操作")} onChange={(value) => {
              setBulkAction(value);
              operation.clearProduction();
            }} options={bulkActions.map((action) => ({ value: action, label: SERVICE_ACTION_LABELS[action] ?? action }))} />
          </div>
          <label className="flex cursor-pointer items-center gap-2 pb-2 text-[11px] text-neutral-400">
            <input type="checkbox" className="accent-blue-500" checked={sudo} disabled={operation.running} onChange={(event) => {
              setSudo(event.target.checked);
              operation.clearProduction();
            }} />
            {t("需要权限时使用 sudo -n")}
          </label>
          <span className="nx-spacer" />
          <button type="button" className="nx-btn" disabled={operation.running || targets.length === 0} onClick={() => {
            if (selectionError) {
              setListBatch(null);
              return;
            }
            load();
          }}>
            <IconRefresh size={13} />{t("加载服务")}
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={operation.running || targets.length === 0 || !unitName.trim()} onClick={() => runAction(bulkActionValue, unitName, selectedIds)}>
            <IconPlay size={13} />{t("应用到已选目标")}
          </button>
        </div>
        <div className="text-[11px] leading-relaxed text-neutral-500">
          {t("sudo 仅非交互执行；需要密码或不可用时会显示在对应主机结果中。status 与 journal 为只读，写操作受批量执行的生产环境保护。Windows 主机通过 PowerShell 管理，不使用 sudo；Windows 与 Linux 主机需分开操作。")}
        </div>
        {selectionError && <div className="text-[11px] text-amber-300" role="status">{selectionError}</div>}
      </div>

      <OperationFeedback {...operation} onCancel={() => void operation.cancel()} />
      <OperationProductionDialog pending={operation.production} onConfirm={operation.confirmProduction} onCancel={operation.clearProduction} />

      {!journal && listBatch && (
        <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
          <IconServer size={13} className="text-neutral-500" />
          <input className="nx-input nx-input-sm w-full sm:w-64" value={filter} placeholder={t("过滤 unit / 服务名 / 描述 / 状态")} aria-label={t("过滤服务")} onChange={(event) => setFilter(event.target.value)} />
          <Select className="w-32" value={stateFilter} ariaLabel={t("服务状态过滤")} onChange={(value) => setStateFilter(value as ServiceStateFilter)} options={[
            { value: "all", label: t("全部状态") }, { value: "running", label: t("运行中") }, { value: "failed", label: t("失败") },
            { value: "inactive", label: t("未运行") }, { value: "other", label: t("其他") },
          ]} />
          <span className="nx-hint">{t("批次 {batchId}", { batchId: listBatch.batchId })}</span>
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
            <div className="px-3 py-8 text-center text-[11px] text-neutral-500">{t("选择主机并加载 systemd services 或 Windows 服务；未使用 systemd 的 Linux 主机将标记为「不支持」。")}</div>
          ) : listBatch.hosts.map((host) => {
            const windows = isWindowsHost(host);
            const units = filterServiceUnits(host.units, filter, stateFilter, windows);
            const hostName = host.result.sessionName || host.result.sessionId;
            return (
              <article key={host.result.sessionId} className="border-b border-neutral-800/70 px-3 py-3" data-session-id={host.result.sessionId}>
                <div className="mb-2 flex flex-wrap items-center gap-2 text-[11px]">
                  <h3 className="font-medium text-neutral-200">{hostName}</h3>
                  {host.systemd === "unsupported" ? <span className="nx-badge nx-badge-amber">{t("unsupported：无 systemd")}</span> : (
                    <span className={host.result.status === "success" ? "text-green-300" : "text-red-300"}>{host.result.status === "success" ? (windows ? t("{length} 个 Windows 服务", { length: host.units.length }) : t("{length} 个 unit", { length: host.units.length })) : t("加载失败")}</span>
                  )}
                  {host.result.truncated && <span className="nx-badge nx-badge-amber">{t("输出截断，列表可能不完整")}</span>}
                  <span className="nx-hint">{host.result.sessionId}</span>
                </div>
                {host.result.status !== "success" && (
                  <div role="alert" className="mb-2 whitespace-pre-wrap break-all text-[11px] text-red-300">{serviceResultMessage(host)}</div>
                )}
                {host.result.status === "success" && units.length === 0 && (
                  <div className="py-2 text-[11px] text-neutral-500">{t("没有匹配当前过滤条件的 service unit。")}</div>
                )}
                {units.length > 0 && (
                  windows ? (
                    <div className="nx-cards-wrap overflow-x-auto">
                      <table className="nx-table nx-table-cards nx-table-fixed min-w-[1120px]" data-platform="windows">
                        <thead><tr><th className="w-1/5">{t("服务名")}</th><th className="w-24">{t("状态")}</th><th className="w-28">{t("启动类型")}</th><th className="w-16">PID</th><th>{t("显示名称")}</th><th className="w-[560px]">{t("操作")}</th></tr></thead>
                        <tbody>
                          {units.map((unit) => (
                            <tr key={unit.name} data-unit={unit.name}>
                              <td data-label="" className="truncate font-mono text-neutral-200" title={unit.name}>{unit.name}</td>
                              <td data-label={t("状态")} className={windowsStateTone(unit.active)}>{windowsStateLabel(unit.active)}</td>
                              <td data-label={t("启动类型")}>{windowsStartModeLabel(unit.startMode)}</td>
                              <td data-label="PID">{unit.pid ? unit.pid : ""}</td>
                              <td data-label={t("显示名称")} className="truncate text-neutral-400" title={unit.description}>{unit.description}</td>
                              <td data-label="">
                                <div className="flex flex-nowrap gap-1 @max-[640px]:flex-wrap">
                                  {WINDOWS_ROW_ACTIONS.map((action) => (
                                    <button key={action} type="button" className="nx-btn nx-btn-ghost nx-btn-xs" disabled={operation.running} onClick={() => runAction(action, unit.name, [host.result.sessionId])}>{SERVICE_ACTION_LABELS[action]}</button>
                                  ))}
                                </div>
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                  ) : (
                  <div className="nx-cards-wrap overflow-x-auto">
                    <table className="nx-table nx-table-cards nx-table-fixed min-w-[1120px]">
                      <thead><tr><th className="w-1/4">Unit</th><th className="w-20">LOAD</th><th className="w-20">ACTIVE</th><th className="w-24">SUB</th><th>{t("描述")}</th><th className="w-[490px]">{t("操作")}</th></tr></thead>
                      <tbody>
                        {units.map((unit) => (
                          <tr key={unit.name} data-unit={unit.name}>
                            <td data-label="" className="truncate font-mono text-neutral-200" title={unit.name}>{unit.name}</td>
                            <td data-label="LOAD">{unit.load}</td><td data-label="ACTIVE" className={unit.active === "failed" ? "text-red-300" : unit.active === "active" ? "text-green-300" : "text-neutral-400"}>{unit.active}</td><td data-label="SUB">{unit.sub}</td>
                            <td data-label={t("描述")} className="truncate text-neutral-400" title={unit.description}>{unit.description}</td>
                            <td data-label="">
                              <div className="flex flex-nowrap gap-1 @max-[640px]:flex-wrap">
                                {SYSTEMD_ROW_ACTIONS.map((action) => (
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
                  )
                )}
              </article>
            );
          })}
          {actionBatch && <ServiceOperationResults batch={actionBatch} title={t("服务操作逐目标结果")} />}
        </div>
      )}
    </section>
  );
}
