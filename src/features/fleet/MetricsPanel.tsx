// 设备指标面板: 设备管理视图与资产树「设备」分区共用。数据来自
// /fleet/devices/:id/metrics (近 24 小时采样), 含 CPU 趋势线 (MetricsChart)、
// CPU/内存/磁盘最新值与进度条、运行时长、采样数; 加载/错误(可重试)/空态
// 三态齐备, 手动刷新重新拉取。

import { useCallback, useEffect, useRef, useState } from "react";
import { type FleetMetricsSample } from "../../ipc/fleetApi";
import { fleetClient } from "./clients";
import { describeError } from "../../ui/errorText";
import { IconRefresh } from "../../ui/icons";
import { formatBytes, formatTime, formatUptime } from "./format";
import { MetricsChart } from "./MetricsChart";

export function MetricsPanel({ deviceId, online }: { deviceId: string; online: boolean }) {
  const [samples, setSamples] = useState<FleetMetricsSample[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const loadSeq = useRef(0);

  const load = useCallback(() => {
    const seq = ++loadSeq.current;
    setLoading(true);
    setError(null);
    fleetClient()
      .metrics(deviceId)
      .then((r) => {
        if (seq === loadSeq.current) setSamples(r.samples);
      })
      .catch((e: unknown) => {
        if (seq === loadSeq.current) setError(describeError(e));
      })
      .finally(() => {
        if (seq === loadSeq.current) setLoading(false);
      });
  }, [deviceId]);

  useEffect(() => {
    load();
  }, [load]);

  const refreshButton = (
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" aria-label="刷新指标" onClick={load} disabled={loading}>
      <IconRefresh size={11} className={loading ? "animate-spin" : ""} />
      {error ? "重试" : "刷新"}
    </button>
  );

  if (loading && samples === null) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="nx-hint py-1">指标加载中…</span>
        {refreshButton}
      </div>
    );
  }
  if (error) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="text-red-300">指标加载失败 · {error}</span>
        {refreshButton}
      </div>
    );
  }
  if (!samples || samples.length === 0) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="nx-hint py-1">{online ? "设备还没有上报指标（默认每 60 秒一次）。" : "设备离线，没有近期指标。"}</span>
        {refreshButton}
      </div>
    );
  }
  const latest = samples[samples.length - 1];
  const memPct = latest.mem_total > 0 ? Math.min(100, (latest.mem_used / latest.mem_total) * 100) : 0;
  const diskPct = latest.disk_total > 0 ? Math.min(100, (latest.disk_used / latest.disk_total) * 100) : 0;
  return (
    <div className="flex flex-col gap-2">
      <MetricsChart samples={samples} />
      <div className="grid grid-cols-2 gap-x-4 gap-y-2 text-[12px] sm:grid-cols-4">
        <div>
          <div className="nx-hint text-[11px]">CPU</div>
          <div className="font-mono text-neutral-100">{latest.cpu_pct.toFixed(1)}%</div>
          <div className="mt-1 h-1.5 rounded bg-neutral-800">
            <div className="h-1.5 rounded bg-sky-400/70" style={{ width: `${Math.min(100, Math.max(0, latest.cpu_pct))}%` }} />
          </div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">内存</div>
          <div className="font-mono text-neutral-100">
            {formatBytes(latest.mem_used)} / {formatBytes(latest.mem_total)}
          </div>
          <div className="mt-1 h-1.5 rounded bg-neutral-800">
            <div className="h-1.5 rounded bg-blue-400/70" style={{ width: `${memPct}%` }} />
          </div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">磁盘</div>
          <div className="font-mono text-neutral-100">
            {formatBytes(latest.disk_used)} / {formatBytes(latest.disk_total)}
          </div>
          <div className="mt-1 h-1.5 rounded bg-neutral-800">
            <div className="h-1.5 rounded bg-amber-400/70" style={{ width: `${diskPct}%` }} />
          </div>
        </div>
        <div>
          <div className="nx-hint text-[11px]">运行时长</div>
          <div className="font-mono text-neutral-100">{formatUptime(latest.uptime_s)}</div>
        </div>
      </div>
      <div className="flex items-center gap-2">
        <div className="nx-hint text-[11px]">
          近 24 小时共 {samples.length} 个采样点 · 最新 {formatTime(latest.ts)}
        </div>
        <div className="nx-spacer" />
        {refreshButton}
      </div>
    </div>
  );
}
