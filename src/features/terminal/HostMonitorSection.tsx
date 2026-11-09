import { useMemo, useState, type ReactNode } from "react";
import { setTerminalHostMonitor, useTerminalPrefs } from "../../app/preferences";
import { useSessionById, type Workspace } from "../../app/store";
import type { HostMonitorSnapshot } from "../../ipc/hostMonitorApi";
import { formatBytes } from "../../ui/format";
import { Select } from "../../ui/Select";
import { MetricsPanel } from "../fleet/MetricsPanel";
import { MetricSparkline, SPARKLINE_STROKES } from "../fleet/MetricsTrend";
import { formatUptime } from "../fleet/format";
import { deviceIdOfHost } from "../hosts/hostDetail";
import { useHostMonitor, type HostMonitorPoint } from "./hostMonitor";
import { t, translateMessage } from "../../i18n";

function formatRate(bytesPerSecond: number | null | undefined): string {
  if (bytesPerSecond === null || bytesPerSecond === undefined || !Number.isFinite(bytesPerSecond)) return "—";
  return `${formatBytes(Math.round(bytesPerSecond))}/s`;
}

function percent(value: number | null | undefined): string {
  return value === null || value === undefined ? "—" : `${value.toFixed(1)}%`;
}

// The interface that carried the most traffic since boot, which is the uplink on almost every host.
function busiestInterface(snapshot: HostMonitorSnapshot): string | null {
  let best: string | null = null;
  let bestBytes = -1;
  for (const iface of snapshot.network) {
    const bytes = iface.rxBytes + iface.txBytes;
    if (bytes > bestBytes) {
      best = iface.name;
      bestBytes = bytes;
    }
  }
  return best;
}

function Row({ label, value, children }: { label: string; value: string; children?: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="flex items-baseline gap-2 text-[11.5px]">
        <span className="nx-hint shrink-0 text-[11px]">{label}</span>
        <span className="min-w-0 flex-1 truncate text-right font-mono text-neutral-100">{value}</span>
      </div>
      {children}
    </div>
  );
}

function LiveMetrics({ snapshot, history }: { snapshot: HostMonitorSnapshot; history: readonly HostMonitorPoint[] }) {
  const [chosen, setChosen] = useState<string | null>(null);
  const names = snapshot.network.map((iface) => iface.name);
  const iface = chosen && names.includes(chosen) ? chosen : busiestInterface(snapshot);
  const current = snapshot.network.find((item) => item.name === iface);
  const memory = snapshot.memory;
  const load = snapshot.load;
  const cpuSeries = useMemo(() => history.map((point) => point.cpu), [history]);
  const memSeries = useMemo(() => history.map((point) => point.mem), [history]);
  const rxSeries = useMemo(() => history.map((point) => (iface ? point.net[iface]?.rx ?? null : null)), [history, iface]);
  const txSeries = useMemo(() => history.map((point) => (iface ? point.net[iface]?.tx ?? null : null)), [history, iface]);
  const netMax = Math.max(0, ...rxSeries.map((v) => v ?? 0), ...txSeries.map((v) => v ?? 0));

  return (
    <div className="flex flex-col gap-2" data-testid="host-monitor-metrics">
      <Row label={snapshot.cpuCount ? t("CPU（{count} 核）", { count: snapshot.cpuCount }) : "CPU"} value={percent(snapshot.cpuPercent)}>
        <MetricSparkline values={cpuSeries} max={100} label={t("CPU 趋势")} stroke={SPARKLINE_STROKES.cpu} />
      </Row>
      {memory && (
        <Row label={t("内存")} value={`${formatBytes(memory.usedBytes)} / ${formatBytes(memory.totalBytes)}`}>
          <MetricSparkline values={memSeries} max={100} label={t("内存趋势")} stroke={SPARKLINE_STROKES.mem} />
          {memory.swapTotalBytes > 0 && (
            <div className="nx-hint text-right text-[10.5px]">
              {t("交换 {used} / {total}", { used: formatBytes(memory.swapUsedBytes), total: formatBytes(memory.swapTotalBytes) })}
            </div>
          )}
        </Row>
      )}
      {load && <Row label={t("负载")} value={`${load.one.toFixed(2)} ${load.five.toFixed(2)} ${load.fifteen.toFixed(2)}`} />}
      {iface && (
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-[11.5px]">
            <span className="nx-hint shrink-0 text-[11px]">{t("网络")}</span>
            <Select
              className="min-w-0 flex-1"
              ariaLabel={t("网络接口")}
              value={iface}
              options={names.map((name) => ({ value: name, label: name }))}
              onChange={setChosen}
            />
          </div>
          <div className="mt-0.5 flex justify-between font-mono text-[11.5px]">
            <span className="text-green-300">↓ {formatRate(current?.rxRate)}</span>
            <span className="text-violet-300">↑ {formatRate(current?.txRate)}</span>
          </div>
          <MetricSparkline values={rxSeries} max={netMax} label={t("下行趋势")} stroke={SPARKLINE_STROKES.rx} />
          <MetricSparkline values={txSeries} max={netMax} label={t("上行趋势")} stroke={SPARKLINE_STROKES.tx} />
        </div>
      )}
      {snapshot.disks.length > 0 && (
        <div className="flex min-w-0 flex-col gap-1">
          <span className="nx-hint text-[11px]">{t("磁盘")}</span>
          {snapshot.disks.map((disk) => (
            <div key={disk.mount} className="min-w-0" title={disk.filesystem}>
              <div className="flex items-baseline gap-2 text-[11px]">
                <span className="min-w-0 flex-1 truncate font-mono text-neutral-300">{disk.mount}</span>
                <span className="shrink-0 font-mono text-neutral-100">
                  {formatBytes(disk.usedBytes)} / {formatBytes(disk.totalBytes)}
                </span>
              </div>
              <div className="h-1 overflow-hidden rounded bg-neutral-800" role="meter" aria-label={t("{mount} 已用", { mount: disk.mount })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(disk.usedPercent)}>
                <div
                  className={disk.usedPercent >= 90 ? "h-full bg-red-400" : disk.usedPercent >= 75 ? "h-full bg-amber-400" : "h-full bg-sky-400"}
                  style={{ width: `${Math.min(100, Math.max(0, disk.usedPercent))}%` }}
                />
              </div>
            </div>
          ))}
        </div>
      )}
      {snapshot.uptimeSeconds !== null && snapshot.uptimeSeconds !== undefined && (
        <Row label={t("运行时长")} value={formatUptime(snapshot.uptimeSeconds)} />
      )}
    </div>
  );
}

function SessionMonitor({ sessionId, active }: { sessionId: string; active: boolean }) {
  const { snapshot, history, error } = useHostMonitor(sessionId, active);
  if (error && !snapshot) return <p className="m-0 text-[11.5px] text-red-300">{t("主机监控请求失败：{error}", { error })}</p>;
  if (!snapshot) return <p className="nx-hint m-0 text-[11.5px]">{t("正在采集主机指标…")}</p>;
  if (snapshot.status === "unsupported") {
    return (
      <p className="nx-hint m-0 text-[11.5px]" data-testid="host-monitor-unsupported">
        {snapshot.reason ? translateMessage(snapshot.reason) : t("此主机不支持主机监控")}
      </p>
    );
  }
  return (
    <div className="flex flex-col gap-1.5">
      {snapshot.status === "error" && (
        <p className="m-0 text-[11.5px] text-amber-300" role="status">
          {t("{reason}，{seconds} 秒后重试", { reason: snapshot.reason ? translateMessage(snapshot.reason) : t("采样失败"), seconds: Math.ceil((snapshot.retryAfterMs ?? snapshot.intervalMs) / 1000) })}
        </p>
      )}
      {error && <p className="m-0 text-[11.5px] text-red-300">{t("主机监控请求失败：{error}", { error })}</p>}
      {snapshot.status === "ok" && <LiveMetrics snapshot={snapshot} history={history} />}
    </div>
  );
}

// HostMonitorSection shows live host metrics in the session sidebar. Live sampling runs one read-only command over the
// session's own connection per interval, only while the monitor is on, the sidebar is shown and the session is connected.
// A fleet device reports its metrics itself, so its session shows the device metrics instead of probing.
export function HostMonitorSection({ workspace, visible }: { workspace: Workspace | undefined; visible: boolean }) {
  const enabled = useTerminalPrefs().hostMonitor === "on";
  const sessionId = workspace?.sessionId ?? undefined;
  const session = useSessionById(sessionId);
  const connected = Boolean(sessionId) && !workspace?.restorePending && session?.status === "connected";
  const deviceId = workspace?.assetId ? deviceIdOfHost(workspace.assetId) : null;
  const ssh = session?.kind === "ssh";

  let body: ReactNode;
  if (!enabled) {
    body = <p className="nx-hint m-0 text-[11.5px]">{t("开启后每 3 秒通过当前连接在主机上运行一条只读命令，采集 CPU、内存、负载、网络和磁盘。")}</p>;
  } else if (deviceId) {
    body = visible ? <MetricsPanel deviceId={deviceId} online={connected} /> : null;
  } else if (!sessionId || !connected) {
    body = <p className="nx-hint m-0 text-[11.5px]">{t("会话连接后显示主机指标。")}</p>;
  } else if (!ssh) {
    body = <p className="nx-hint m-0 text-[11.5px]">{t("主机监控只支持 SSH 会话和受管设备。")}</p>;
  } else {
    body = <SessionMonitor sessionId={sessionId} active={visible} />;
  }

  return (
    <div className="flex flex-col gap-1.5" data-testid="host-monitor">
      <label className="flex items-center gap-1.5 text-[11.5px] text-neutral-300">
        <input type="checkbox" checked={enabled} onChange={(event) => setTerminalHostMonitor(event.target.checked ? "on" : "off")} />
        {t("实时采集主机指标")}
      </label>
      {body}
    </div>
  );
}
