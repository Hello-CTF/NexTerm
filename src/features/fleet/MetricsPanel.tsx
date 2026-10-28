// Device metrics panel: shared by device management and the asset tree. Current responses carry an independent
// latest sample and actual window metadata.

import { useCallback, useEffect, useRef, useState } from "react";
import { type FleetMetricsResponse } from "../../ipc/fleetApi";
import { fleetClient } from "./clients";
import { describeError } from "../../ui/errorText";
import { IconRefresh } from "../../ui/icons";
import { formatBytes, formatTime, formatUptime } from "./format";
import {
  DEFAULT_METRIC_RANGE,
  formatAge,
  METRIC_RANGES,
  metricsGapThreshold,
  MetricTrendChart,
  type MetricKind,
  type MetricRangeId,
} from "./MetricsTrend";

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
  const [range, setRange] = useState<MetricRangeId>(DEFAULT_METRIC_RANGE);
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
    // The default 24 hour range is the server default, so it keeps the argument-less request; the API honours
    // since_ms up to its 48 hour raw retention.
    const rangeMs = METRIC_RANGES.find((r) => r.id === range)?.ms ?? 0;
    const client = fleetClient();
    (range === DEFAULT_METRIC_RANGE ? client.metrics(requestedDevice) : client.metrics(requestedDevice, Date.now() - rangeMs))
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
  }, [deviceId, range]);

  useEffect(() => {
    load();
  }, [load]);

  const data = result?.deviceId === deviceId ? result.data : null;
  const error = requestError?.deviceId === deviceId ? requestError.message : null;
  const samples = data?.samples ?? [];
  const latest = data?.latest;
  const gapThreshold = metricsGapThreshold(data?.sampleIntervalMs);
  const windowStart = data?.windowStart;
  const windowEnd = data?.windowEnd;
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
        <span className="nx-hint py-1">正在加载指标…</span>
        {refreshButton}
      </div>
    );
  }
  if (error) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <span className="text-red-300">未能加载指标：{error}</span>
        {refreshButton}
      </div>
    );
  }
  if (!latest) {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        {data?.seriesError ? (
          <span className="text-red-300">未能加载趋势：{data.seriesError}</span>
        ) : (
          <span className="nx-hint py-1">{online ? "设备尚未上报指标（默认每 60 秒一次）。" : "设备离线，暂无近期指标。"}</span>
        )}
        {refreshButton}
      </div>
    );
  }

  const referenceTime = Date.now();
  const age = referenceTime - latest.ts;
  const stale = age > gapThreshold;
  const staleText = `${online ? "" : "设备离线，"}数据已 ${formatAge(age)}未更新`;
  const hasTrend = !data?.seriesError && samples.length > 0;
  const metricBlocks: { kind: MetricKind; label: string; value: string }[] = [
    { kind: "cpu", label: "CPU", value: `${latest.cpu_pct.toFixed(1)}%` },
    { kind: "mem", label: "内存", value: `${formatBytes(latest.mem_used)} / ${formatBytes(latest.mem_total)}` },
    { kind: "disk", label: "磁盘", value: `${formatBytes(latest.disk_used)} / ${formatBytes(latest.disk_total)}` },
  ];

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1" role="group" aria-label="时间范围">
        {METRIC_RANGES.map((r) => (
          <button
            key={r.id}
            type="button"
            className={`nx-btn nx-btn-xs ${range === r.id ? "nx-btn-primary" : "nx-btn-ghost"}`}
            aria-pressed={range === r.id}
            onClick={() => setRange(r.id)}
          >
            {r.label}
          </button>
        ))}
      </div>
      {data?.seriesError ? (
        <div className="text-[11.5px] text-red-300">未能加载趋势：{data.seriesError}</div>
      ) : !hasTrend ? (
        <div className="nx-hint text-[11.5px]">所选时间范围内暂无数据。</div>
      ) : null}
      <div className={`grid grid-cols-1 gap-x-4 gap-y-3 text-[12px] sm:grid-cols-3 ${loading ? "opacity-60" : ""}`} aria-busy={loading}>
        {metricBlocks.map((m) => (
          <div key={m.kind}>
            <div className="nx-hint text-[11px]">{m.label}</div>
            <div className="font-mono text-neutral-100">{m.value}</div>
            {hasTrend && (
              <MetricTrendChart
                kind={m.kind}
                samples={samples}
                latest={latest}
                windowStart={windowStart}
                windowEnd={windowEnd}
                sampleIntervalMs={data?.sampleIntervalMs}
              />
            )}
          </div>
        ))}
      </div>
      <div className="text-[12px]">
        <span className="nx-hint text-[11px]">运行时长 </span>
        <span className="font-mono text-neutral-100">{formatUptime(latest.uptime_s)}</span>
      </div>
      <div className="flex items-center gap-2">
        <div className="nx-hint text-[11px]">
          最新 {formatTime(latest.ts)}
          {stale ? <span className="text-amber-300"> · {staleText}</span> : null}
        </div>
        <div className="nx-spacer" />
        {refreshButton}
      </div>
    </div>
  );
}
