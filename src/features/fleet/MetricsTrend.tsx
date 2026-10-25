// Per-metric trend chart for the device metrics panel: hand-written SVG with a labelled y scale, round-number time
// ticks and a hover readout. Gaps beyond the freshness threshold break the line instead of inventing continuity.

import { useRef, useState, type MouseEvent } from "react";
import type { FleetMetricsSample } from "../../ipc/fleetApi";
import { formatBytes } from "./format";

// Samples further apart than this are a gap in reporting, not a line to draw.
export function metricsGapThreshold(sampleIntervalMs?: number): number {
  if (sampleIntervalMs === undefined || !Number.isFinite(sampleIntervalMs) || sampleIntervalMs <= 0) return 180_000;
  return Math.max(3 * sampleIntervalMs, 180_000);
}

// The server keeps raw samples for 48 hours (rawMetricsRetention) and clamps older since_ms values, so 48 hours is the
// longest range the API can honour.
export const METRIC_RANGES = [
  { id: "1h", label: "1 小时", ms: 3600_000 },
  { id: "6h", label: "6 小时", ms: 6 * 3600_000 },
  { id: "24h", label: "24 小时", ms: 24 * 3600_000 },
  { id: "48h", label: "48 小时", ms: 48 * 3600_000 },
] as const;
export type MetricRangeId = (typeof METRIC_RANGES)[number]["id"];
export const DEFAULT_METRIC_RANGE: MetricRangeId = "24h";

export type MetricKind = "cpu" | "mem" | "disk";

const WIDTH = 240;
const HEIGHT = 100;
const LEFT = 40;
const RIGHT = 8;
const TOP = 8;
const BOTTOM = 18;

const TICK_STEPS_MS = [15, 30, 60, 120, 180, 360, 720, 1440].map((m) => m * 60_000);

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

// Round tick times aligned to local-clock multiples of a step, at most five of them inside [start, end].
export function timeTicks(start: number, end: number): number[] {
  const span = end - start;
  if (!(span > 0)) return [];
  const step = TICK_STEPS_MS.find((s) => span / s <= 5) ?? TICK_STEPS_MS[TICK_STEPS_MS.length - 1];
  const offset = new Date(start).getTimezoneOffset() * 60_000;
  const ticks: number[] = [];
  for (let t = Math.ceil((start - offset) / step) * step + offset; t <= end; t += step) ticks.push(t);
  return ticks;
}

export function formatTick(ts: number, spanMs: number): string {
  const d = new Date(ts);
  const clock = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  return spanMs > 24 * 3600_000 ? `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${clock}` : clock;
}

export function formatAge(ms: number): string {
  const minutes = Math.max(1, Math.floor(ms / 60_000));
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours} 小时`;
  return `${Math.floor(hours / 24)} 天`;
}

function metricPct(kind: MetricKind, s: FleetMetricsSample): number {
  if (kind === "cpu") return s.cpu_pct;
  const used = kind === "mem" ? s.mem_used : s.disk_used;
  const total = kind === "mem" ? s.mem_total : s.disk_total;
  return total > 0 ? (used / total) * 100 : 0;
}

const KIND_META: Record<MetricKind, { name: string; stroke: string; fill: string }> = {
  cpu: { name: "CPU", stroke: "stroke-sky-400", fill: "fill-sky-400" },
  mem: { name: "内存", stroke: "stroke-blue-400", fill: "fill-blue-400" },
  disk: { name: "磁盘", stroke: "stroke-amber-400", fill: "fill-amber-400" },
};

interface Props {
  kind: MetricKind;
  samples: FleetMetricsSample[];
  latest: FleetMetricsSample;
  windowStart?: number;
  windowEnd?: number;
  sampleIntervalMs?: number;
}

export function MetricTrendChart({ kind, samples, latest, windowStart, windowEnd, sampleIntervalMs }: Props) {
  const [hover, setHover] = useState<number | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  const meta = KIND_META[kind];
  const start = windowStart ?? samples[0].ts;
  const end = windowEnd ?? samples[samples.length - 1].ts;
  const span = Math.max(1, end - start);
  const plotW = WIDTH - LEFT - RIGHT;
  const plotH = HEIGHT - TOP - BOTTOM;
  const x = (ts: number) => LEFT + ((Math.min(end, Math.max(start, ts)) - start) / span) * plotW;
  const y = (pct: number) => TOP + plotH - (Math.min(100, Math.max(0, pct)) / 100) * plotH;
  const gap = metricsGapThreshold(sampleIntervalMs);
  const segments: FleetMetricsSample[][] = [[]];
  for (const sample of samples) {
    const seg = segments[segments.length - 1];
    const prev = seg[seg.length - 1];
    if (prev && sample.ts - prev.ts > gap) segments.push([]);
    segments[segments.length - 1].push(sample);
  }

  const total = kind === "mem" ? latest.mem_total : kind === "disk" ? latest.disk_total : 0;
  const yLabel = (pct: number) => {
    if (kind === "cpu" || total <= 0) return `${pct}%`;
    return pct === 0 ? "0" : formatBytes((total * pct) / 100);
  };
  const ticks = timeTicks(start, end);

  const onMove = (event: MouseEvent<SVGRectElement>) => {
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect || rect.width <= 0) return;
    const vx = ((event.clientX - rect.left) / rect.width) * WIDTH;
    const ts = start + ((vx - LEFT) / plotW) * span;
    let best = 0;
    for (let i = 1; i < samples.length; i++) {
      if (Math.abs(samples[i].ts - ts) < Math.abs(samples[best].ts - ts)) best = i;
    }
    setHover(best);
  };

  const point = hover !== null ? samples[hover] : undefined;
  let tip: { px: number; lines: [string, string] } | undefined;
  if (point) {
    const pct = metricPct(kind, point);
    const d = new Date(point.ts);
    const when = `${span > 24 * 3600_000 ? `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ` : ""}${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
    const value =
      kind === "cpu"
        ? `${pct.toFixed(1)}%`
        : `${formatBytes(kind === "mem" ? point.mem_used : point.disk_used)} (${pct.toFixed(1)}%)`;
    tip = { px: x(point.ts), lines: [when, `${meta.name} ${value}`] };
  }
  const tipW = 104;
  const tipX = tip ? Math.min(WIDTH - RIGHT - tipW, Math.max(LEFT, tip.px - tipW / 2)) : 0;

  return (
    <svg
      ref={svgRef}
      role="img"
      aria-label={`${meta.name} 趋势，最新 ${metricPct(kind, latest).toFixed(1)}%`}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      className="mt-1 h-auto w-full"
      onMouseLeave={() => setHover(null)}
    >
      {[0, 50, 100].map((pct) => (
        <g key={pct}>
          <line
            x1={LEFT}
            x2={WIDTH - RIGHT}
            y1={y(pct)}
            y2={y(pct)}
            className="stroke-neutral-800"
            strokeWidth="1"
            strokeDasharray={pct === 0 ? undefined : "3 3"}
          />
          <text x={LEFT - 4} y={y(pct) + 3} textAnchor="end" fontSize="8" className="fill-neutral-500" data-axis="y">
            {yLabel(pct)}
          </text>
        </g>
      ))}
      {ticks.map((t) => (
        <text key={t} x={x(t)} y={HEIGHT - 5} textAnchor="middle" fontSize="8" className="fill-neutral-500" data-axis="x">
          {formatTick(t, span)}
        </text>
      ))}
      {segments.map((seg) => {
        const first = seg[0];
        if (seg.length === 1) {
          return <circle key={first.ts} cx={x(first.ts)} cy={y(metricPct(kind, first))} r="2" className={meta.fill} />;
        }
        const pts = seg.map((s) => `${x(s.ts).toFixed(1)},${y(metricPct(kind, s)).toFixed(1)}`).join(" ");
        return (
          <polyline
            key={first.ts}
            points={pts}
            fill="none"
            className={meta.stroke}
            strokeWidth="1.5"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        );
      })}
      {point && tip && (
        <g pointerEvents="none" data-testid="metric-tooltip">
          <line x1={tip.px} x2={tip.px} y1={TOP} y2={TOP + plotH} className="stroke-neutral-600" strokeWidth="1" />
          <circle cx={tip.px} cy={y(metricPct(kind, point))} r="3" className={meta.fill} />
          <rect x={tipX} y={TOP} width={tipW} height={24} rx="3" className="fill-neutral-900 stroke-neutral-700" />
          <text x={tipX + 5} y={TOP + 9} fontSize="8" className="fill-neutral-400">
            {tip.lines[0]}
          </text>
          <text x={tipX + 5} y={TOP + 19} fontSize="8" className="fill-neutral-100">
            {tip.lines[1]}
          </text>
        </g>
      )}
      <rect x={LEFT} y={TOP} width={plotW} height={plotH} fill="transparent" onMouseMove={onMove} />
    </svg>
  );
}
