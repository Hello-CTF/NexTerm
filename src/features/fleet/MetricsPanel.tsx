// Device metrics panel: shared by device management and the asset tree. New responses use an independent latest sample
// and actual window metadata; legacy samples-only responses remain readable but are marked as having an unverified window.

import { useCallback, useEffect, useRef, useState } from "react";
import { type FleetMetricsResponse } from "../../ipc/fleetApi";
import { fleetClient } from "./clients";
import { describeError } from "../../ui/errorText";
import { IconRefresh } from "../../ui/icons";
import { formatBytes, formatTime, formatUptime } from "./format";
import { MetricsChart, metricsGapThreshold } from "./MetricsChart";

interface MetricsResult {
  deviceId: string;
  data: FleetMetricsResponse;
}

interface MetricsRequestError {
  deviceId: string;
  message: string;
}

export function MetricsPanel({ deviceId, online }: { deviceId: string; online: boolean }) {
  const [result, setResult] = useState<MetricsResult | null>(null);
  const [requestError, setRequestError] = useState<MetricsRequestError | null>(null);
  const [loading, setLoading] = useState(false);
  const loadSeq = useRef(0);
  const activeDevice = useRef(deviceId);
  if (activeDevice.current !== deviceId) {
    activeDevice.current = deviceId;
    loadSeq.current++;
  }

  const load = useCallback(() => {
    const seq = ++loadSeq.current;
    const requestedDevice = deviceId;
    setLoading(true);
    setRequestError(null);
    fleetClient()
      .metrics(requestedDevice)
      .then((data) => {
        if (seq === loadSeq.current && activeDevice.current === requestedDevice) {
          setResult({ deviceId: requestedDevice, data });
        }
      })
      .catch((e: unknown) => {
        if (seq === loadSeq.current && activeDevice.current === requestedDevice) {
          setRequestError({ deviceId: requestedDevice, message: describeError(e) });
        }
      })
      .finally(() => {
        if (seq === loadSeq.current && activeDevice.current === requestedDevice) setLoading(false);
      });
  }, [deviceId]);

  useEffect(() => {
    load();
  }, [load]);

  const data = result?.deviceId === deviceId ? result.data : null;
  const error = requestError?.deviceId === deviceId ? requestError.message : null;
  const samples = data?.samples ?? [];
  const latest = data?.latest ?? samples[samples.length - 1];
  const gapThreshold = metricsGapThreshold(data?.sampleIntervalMs);
  const [, refreshFreshness] = useState(0);
  useEffect(() => {
    if (!latest) return;
    const remaining = gapThreshold - (Date.now() - latest.ts);
    if (remaining <= 0) return;
    const timeout = setTimeout(() => refreshFreshness((value) => value + 1), remaining + 1);
    return () => clearTimeout(timeout);
  }, [latest, gapThreshold]);
  const loadingView = loading || (result !== null && result.deviceId !== deviceId);
  const refreshButton = (
    <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" aria-label="刷新指标" onClick={load} disabled={loading}>
      <IconRefresh size={11} className={loading ? "animate-spin" : ""} />
      {error || data?.seriesError ? "重试" : "刷新"}
    </button>
  );

  if (loadingView && !latest) {
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
  if (!latest) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        {data?.seriesError ? (
          <span className="text-red-300">趋势加载失败 · {data.seriesError}</span>
        ) : (
          <span className="nx-hint py-1">{online ? "设备还没有上报指标（默认每 60 秒一次）。" : "设备离线，没有近期指标。"}</span>
        )}
        {refreshButton}
      </div>
    );
  }

  const memPct = latest.mem_total > 0 ? Math.min(100, (latest.mem_used / latest.mem_total) * 100) : 0;
  const diskPct = latest.disk_total > 0 ? Math.min(100, (latest.disk_used / latest.disk_total) * 100) : 0;
  const windowStart = data?.windowStart;
  const windowEnd = data?.windowEnd;
  const hasWindow = windowStart !== undefined && windowEnd !== undefined;
  const sampleCount = data?.sampleCount ?? samples.length;
  const referenceTime = Date.now();
  const stale = referenceTime - latest.ts > gapThreshold;
  const staleText = `${online ? "指标过期" : "设备离线，指标过期"} · 过期阈值 ${formatUptime(gapThreshold / 1000)}`;

  return (
    <div className="flex flex-col gap-2">
      {data?.seriesError ? (
        <div className="text-[11.5px] text-red-300">趋势加载失败 · {data.seriesError}</div>
      ) : samples.length > 0 ? (
        <MetricsChart
          samples={samples}
          latest={latest}
          windowStart={windowStart}
          windowEnd={windowEnd}
          sampleIntervalMs={data?.sampleIntervalMs}
        />
      ) : (
        <div className="nx-hint text-[11.5px]">当前时间窗口没有采样点。</div>
      )}
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
          {hasWindow ? (
            <>
              趋势窗口 {formatTime(windowStart)} - {formatTime(windowEnd)} · {data?.seriesError ? "采样数未知" : `共 ${sampleCount} 个采样点${data?.downsampled ? ` · 显示 ${samples.length} 个原始点` : ""}`}
            </>
          ) : (
            <>旧接口返回 {samples.length} 个采样点，窗口完整性未知</>
          )}
          {" · 最新 "}{formatTime(latest.ts)}{stale ? ` · ${staleText}` : ""}
        </div>
        <div className="nx-spacer" />
        {refreshButton}
      </div>
    </div>
  );
}
