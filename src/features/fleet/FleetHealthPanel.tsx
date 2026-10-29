import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUi } from "../../app/store";
import { filterAssetsForProject, projectFocusLabel } from "../../app/projectFocus";
import { assetApi, type Asset, type SessionInfo } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import {
  createExecBatchId,
  execBatchApi,
  type ExecBatchHealthResult,
  type ExecBatchHealthTarget,
} from "../../ipc/execBatchApi";
import { describeError } from "../../ui/errorText";
import { IconRefresh, IconServer } from "../../ui/icons";
import { formatDockerBytes } from "../docker/diskFormat";
import { formatUptime } from "./format";
import {
  FLEET_HEALTH_MAX_INTERVAL_MS,
  FLEET_HEALTH_MIN_INTERVAL_MS,
  normalizeFleetHealthInterval,
  readFleetHealthSettings,
  writeFleetHealthSettings,
  type FleetHealthProjectSettings,
} from "./healthSettings";

const EMPTY_GROUPS: never[] = [];
const MAX_TARGETS = 64;
const HEALTH_TIMEOUT_MS = 30_000;

const BATCH_STATUS_LABELS: Record<string, string> = {
  success: "成功",
  exit_error: "非零退出",
  transport_error: "传输错误",
  timeout: "超时",
  canceled: "已取消",
  failed: "失败",
};

const METRIC_LABELS: Record<string, string> = {
  uptime: "uptime",
  load: "load",
  disk: "disk",
  memory: "memory",
  docker: "docker df",
};

interface HealthHost {
  asset: Asset;
  target: ExecBatchHealthTarget;
}

function collectHealthHosts(
  assets: readonly Asset[],
  sessions: readonly SessionInfo[],
): HealthHost[] {
  const connected = new Map<string, SessionInfo>();
  for (const session of sessions) {
    if (session.kind !== "ssh" || session.status !== "connected" || !session.assetId) continue;
    if (!connected.has(session.assetId)) connected.set(session.assetId, session);
  }
  return assets
    .filter((asset) => asset.kind === "ssh")
    .map((asset) => ({
      asset,
      target: {
        assetId: asset.id,
        ...(connected.get(asset.id) ? { sessionId: connected.get(asset.id)?.id } : {}),
      },
    }));
}

function percent(value: number | null | undefined): string {
  return value === null || value === undefined ? "—" : `${value.toFixed(1)}%`;
}

function loadValues(result: ExecBatchHealthResult | undefined): string {
  if (!result || result.load1 == null || result.load5 == null || result.load15 == null) return "—";
  return `${result.load1.toFixed(2)} / ${result.load5.toFixed(2)} / ${result.load15.toFixed(2)}`;
}

function MetricError({ result, metric }: { result: ExecBatchHealthResult | undefined; metric: string }) {
  const error = result?.metricErrors?.find((candidate) => candidate.metric === metric);
  if (!error) return null;
  return (
    <div className="mt-1 max-w-48 whitespace-normal break-words text-[10px] text-red-300" data-testid={`${metric}-error`}>
      {METRIC_LABELS[metric] ?? metric}: {error.error}
    </div>
  );
}

export function FleetHealthPanel({ sessions, visible }: { sessions: SessionInfo[]; visible: boolean }) {
  const activeProject = useUi((state) => state.activeProject);
  const groups = useUi((state) => state.projectInventory?.groups ?? EMPTY_GROUPS);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), enabled: visible, retry: false });
  const groupsQuery = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    enabled: visible,
    retry: false,
  });
  const projectGroups = groupsQuery.data ?? groups;
  const projectAssets = useMemo(
    () => filterAssetsForProject(assetsQuery.data ?? [], activeProject, projectGroups),
    [assetsQuery.data, activeProject, projectGroups],
  );
  const hosts = useMemo(() => collectHealthHosts(projectAssets, sessions), [projectAssets, sessions]);
  const probeHosts = useMemo(() => hosts.slice(0, MAX_TARGETS), [hosts]);
  const targetSignature = probeHosts.map((host) => `${host.target.assetId}:${host.target.sessionId ?? ""}`).join("|");

  const accountID = useAuth((state) => state.user?.id) ?? (WEB ? "anonymous" : "desktop");
  const settingsScope = `${accountID}:${activeProject}`;
  const [settingsState, setSettingsState] = useState<{
    scope: string;
    settings: FleetHealthProjectSettings;
  }>(() => ({ scope: settingsScope, settings: readFleetHealthSettings(activeProject, accountID) }));
  const settings = settingsState.scope === settingsScope
    ? settingsState.settings
    : readFleetHealthSettings(activeProject, accountID);
  const [results, setResults] = useState<Record<string, ExecBatchHealthResult>>({});
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [lastRefreshed, setLastRefreshed] = useState<number | null>(null);

  const mountedRef = useRef(false);
  const visibleRef = useRef(visible);
  const generationRef = useRef(0);
  const inFlightRef = useRef<{ batchId: string; generation: number } | null>(null);
  visibleRef.current = visible;

  const cancelInFlight = useCallback((updateView: boolean) => {
    generationRef.current += 1;
    const pending = inFlightRef.current;
    inFlightRef.current = null;
    if (pending) void execBatchApi.healthCancel(pending.batchId).catch(() => undefined);
    if (updateView && mountedRef.current) setRunning(false);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      cancelInFlight(false);
    };
  }, [cancelInFlight]);

  useEffect(() => {
    cancelInFlight(true);
    setResults({});
    setLastRefreshed(null);
    setError(null);
    setHistoryError(null);
  }, [activeProject, visible, cancelInFlight]);

  const refresh = useCallback(async () => {
    if (!visibleRef.current || inFlightRef.current || probeHosts.length === 0) return;
    const batchId = createExecBatchId();
    const generation = generationRef.current;
    const pending = { batchId, generation };
    inFlightRef.current = pending;
    setRunning(true);
    setError(null);
    setHistoryError(null);
    try {
      const batch = await execBatchApi.healthProbe({
        batchId,
        targets: probeHosts.map((host) => host.target),
        timeoutMs: HEALTH_TIMEOUT_MS,
        maxConcurrent: 4,
      });
      if (!mountedRef.current || !visibleRef.current || generationRef.current !== generation || inFlightRef.current !== pending) return;
      setResults(Object.fromEntries(batch.results.map((result) => [result.assetId, result])));
      setHistoryError(batch.historyError ?? null);
      setLastRefreshed(Date.now());
    } catch (cause) {
      if (mountedRef.current && visibleRef.current && generationRef.current === generation && inFlightRef.current === pending) {
        setError(describeError(cause));
      }
    } finally {
      if (inFlightRef.current === pending) {
        inFlightRef.current = null;
        if (mountedRef.current) setRunning(false);
      }
    }
  }, [probeHosts]);

  useEffect(() => {
    if (!visible || !settings.autoPoll || probeHosts.length === 0) return;
    void refresh();
    const timer = window.setInterval(() => void refresh(), settings.intervalMs);
    return () => window.clearInterval(timer);
  }, [visible, activeProject, settings.autoPoll, settings.intervalMs, targetSignature, probeHosts.length, refresh]);

  const updateSettings = (patch: Partial<FleetHealthProjectSettings>) => {
    const next = {
      ...settings,
      ...patch,
      intervalMs: normalizeFleetHealthInterval(patch.intervalMs ?? settings.intervalMs),
    };
    writeFleetHealthSettings(activeProject, next, accountID);
    setSettingsState({ scope: settingsScope, settings: next });
  };

  const resultFor = (assetID: string) => results[assetID];
  const overflow = new Set(hosts.slice(MAX_TARGETS).map((host) => host.asset.id));

  return (
    <section className="nx-pane" aria-label="主机概览" data-testid="fleet-health-panel">
      <div className="nx-toolbar flex-wrap">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">主机概览</h2>
        <span className="nx-badge">{projectFocusLabel(activeProject, projectGroups)}</span>
        <span className="nx-hint">只读 · 无告警 · 默认手动刷新</span>
        <span className="nx-spacer" />
        <button
          type="button"
          className="nx-btn nx-btn-sm"
          disabled={running || probeHosts.length === 0 || !visible}
          onClick={() => void refresh()}
        >
          <IconRefresh size={12} className={running ? "animate-spin" : undefined} />
          {running ? "正在刷新" : "刷新"}
        </button>
      </div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-neutral-800/70 px-3 py-2 text-[11px]">
        <label className="flex items-center gap-1.5 text-neutral-300">
          <input
            type="checkbox"
            className="nx-check"
            aria-label="按项目自动刷新"
            checked={settings.autoPoll}
            onChange={(event) => updateSettings({ autoPoll: event.target.checked })}
          />
          此项目自动刷新
        </label>
        <label className="flex items-center gap-1.5 text-neutral-500">
          间隔（分钟）
          <input
            type="number"
            className="nx-input nx-input-sm w-20"
            aria-label="自动刷新间隔（分钟）"
            min={FLEET_HEALTH_MIN_INTERVAL_MS / 60_000}
            max={FLEET_HEALTH_MAX_INTERVAL_MS / 60_000}
            step={1}
            value={settings.intervalMs / 60_000}
            onChange={(event) => updateSettings({ intervalMs: Number(event.target.value) * 60_000 })}
          />
        </label>
        <span className="nx-hint">仅在本页打开时自动刷新，切换到其他页面后停止</span>
        {lastRefreshed !== null && <span className="nx-hint">上次刷新 {new Date(lastRefreshed).toLocaleTimeString()}</span>}
      </div>

      <div className="nx-cards-wrap min-h-0 flex-1 overflow-auto p-3">
        {(assetsQuery.isPending || groupsQuery.isPending) && <div className="nx-hint">正在读取项目主机…</div>}
        {(assetsQuery.isError || groupsQuery.isError) && (
          <div role="alert" className="mb-2 text-[11px] text-red-300">
            未能读取项目中的主机：{describeError(assetsQuery.error ?? groupsQuery.error)}
          </div>
        )}
        {error && <div role="alert" className="mb-2 text-[11px] text-red-300">未能刷新：{error}</div>}
        {historyError && <div role="alert" className="mb-2 text-[11px] text-red-300">未能读取最近的批量执行记录：{historyError}</div>}
        {overflow.size > 0 && (
          <div role="status" className="mb-2 text-[11px] text-amber-300">
            单批最多探测 {MAX_TARGETS} 台；超出的 {overflow.size} 台保留在网格中并标记为未探测。
          </div>
        )}
        {!assetsQuery.isPending && hosts.length === 0 && (
          <div className="rounded border border-dashed border-neutral-800 px-3 py-6 text-center text-[11px] text-neutral-500">
            当前项目暂无 SSH 主机
          </div>
        )}
        {hosts.length > 0 && (
          <table className="nx-table nx-table-cards min-w-[1240px]">
            <thead>
              <tr>
                <th>主机</th>
                <th>Reachability</th>
                <th>Latency</th>
                <th>Uptime</th>
                <th>Load 1 / 5 / 15</th>
                <th>Disk</th>
                <th>Memory</th>
                <th>Docker 可回收</th>
                <th>最近批量执行</th>
              </tr>
            </thead>
            <tbody>
              {hosts.map((host) => {
                const result = resultFor(host.asset.id);
                const skipped = overflow.has(host.asset.id);
                return (
                  <tr key={host.asset.id} data-asset-id={host.asset.id}>
                    <td data-label="">
                      <div className="font-medium text-neutral-200">{host.asset.name}</div>
                      <div className="font-mono text-[10px] text-neutral-500">{host.asset.host ?? host.asset.id}</div>
                      {!host.target.sessionId && <div className="mt-1 text-[10px] text-neutral-500">未连接 SSH</div>}
                    </td>
                    <td data-label="Reachability">
                      <span className={result?.reachable ? "text-green-300" : result || skipped ? "text-red-300" : "text-neutral-500"}>
                        {skipped ? "未探测" : running && !result ? "探测中…" : !result ? "未探测" : result.reachable ? "可达" : "不可达 / 失败"}
                      </span>
                      {skipped && <div className="mt-1 text-[10px] text-amber-300">超出单批上限</div>}
                      {result?.error && <div className="mt-1 max-w-52 whitespace-normal break-words text-[10px] text-red-300">{result.error}</div>}
                    </td>
                    <td data-label="Latency" className="nx-mono">{result ? `${result.latencyMs} ms` : "—"}</td>
                    <td data-label="Uptime">
                      {result?.uptimeSeconds != null ? formatUptime(result.uptimeSeconds) : "—"}
                      <MetricError result={result} metric="uptime" />
                    </td>
                    <td data-label="Load" className="nx-mono">
                      {loadValues(result)}
                      <MetricError result={result} metric="load" />
                    </td>
                    <td data-label="Disk">
                      {percent(result?.diskUsedPercent)}
                      <MetricError result={result} metric="disk" />
                    </td>
                    <td data-label="Memory">
                      {percent(result?.memoryUsedPercent)}
                      <MetricError result={result} metric="memory" />
                    </td>
                    <td data-label="Docker 可回收">
                      {result?.dockerReclaimableBytes != null ? formatDockerBytes(result.dockerReclaimableBytes) : "—"}
                      <MetricError result={result} metric="docker" />
                    </td>
                    <td data-label="最近批量执行">
                      {result?.lastBatch ? (
                        <>
                          <div className={result.lastBatch.status === "success" ? "text-green-300" : "text-red-300"}>
                            {BATCH_STATUS_LABELS[result.lastBatch.status] ?? result.lastBatch.status}
                          </div>
                          <div className="text-[10px] text-neutral-500" title={result.lastBatch.batchId}>
                            {new Date(result.lastBatch.startedAt).toLocaleString()}
                          </div>
                        </>
                      ) : result ? "暂无" : "—"}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      <div className="border-t border-neutral-800/70 px-3 py-1.5 text-[10px] text-neutral-500">
        完整指标需要已连接 SSH；未连接的主机只检查能否连通。运行时长、负载、磁盘、内存和 Docker 占用在一次只读命令中读取。
      </div>
    </section>
  );
}
