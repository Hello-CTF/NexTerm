// Per-metric trend chart for the device metrics panel: hand-written SVG with a labelled y scale, round-number time
// ticks and a hover readout. Gaps beyond the freshness threshold break the line instead of inventing continuity.

import { useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import { uiTextFactor, useAppearancePrefs } from "../../app/preferences";
import type { FleetMetricsSample } from "../../ipc/fleetApi";
import { formatBytes } from "./format";
import { t as tr } from "../../i18n";

// Samples further apart than this are a gap in reporting, not a line to draw.
export function metricsGapThreshold(sampleIntervalMs?: number): number {
  if (sampleIntervalMs === undefined || !Number.isFinite(sampleIntervalMs) || sampleIntervalMs <= 0) return 180_000;
  return Math.max(3 * sampleIntervalMs, 180_000);
}

// The server keeps raw samples for 48 hours (rawMetricsRetention) and clamps older since_ms values, so 48 hours is the
// longest range the API can honour.
export const METRIC_RANGES = [
  { id: "1h", label: tr("1 小时"), ms: 3600_000 },
  { id: "6h", label: tr("6 小时"), ms: 6 * 3600_000 },
  { id: "24h", label: tr("24 小时"), ms: 24 * 3600_000 },
  { id: "48h", label: tr("48 小时"), ms: 48 * 3600_000 },
] as const;
export type MetricRangeId = (typeof METRIC_RANGES)[number]["id"];
export const DEFAULT_METRIC_RANGE: MetricRangeId = "24h";

export type MetricKind = "cpu" | "mem" | "disk";

// The chart is drawn in CSS pixels: the viewBox follows the measured width so text and strokes keep their size at any
// card width, and only the plot stretches.
const FALLBACK_WIDTH = 400;
const HEIGHT = 120;
const RIGHT = 8;
const TOP = 6;
// Chart text size in pixels before the UI text factor, which scales it like the rest of the interface text.
const FONT_PX = 10;
const MIN_TICK_SPACING = 70;

const TICK_STEPS_MS = [15, 30, 60, 120, 180, 360, 720, 1440].map((m) => m * 60_000);

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

// Round tick times aligned to local-clock multiples of a step, at most maxTicks of them inside [start, end].
export function timeTicks(start: number, end: number, maxTicks = 5): number[] {
  const span = end - start;
  if (!(span > 0)) return [];
  const limit = Math.max(2, Math.floor(maxTicks));
  const step = TICK_STEPS_MS.find((s) => span / s <= limit) ?? TICK_STEPS_MS[TICK_STEPS_MS.length - 1];
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
  if (minutes < 60) return tr("{minutes} 分钟", { minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return tr("{hours} 小时", { hours });
  return tr("{floor} 天", { floor: Math.floor(hours / 24) });
}

// Approximate rendered width of a label: CJK glyphs are about one em wide, Latin digits and letters about 0.6 em.
function textWidth(text: string, fontPx: number): number {
  let em = 0;
  for (const ch of text) em += ch.charCodeAt(0) > 0x2e7f ? 1 : 0.6;
  return em * fontPx;
}

// Tracks an element's width, falling back to a default where layout is unavailable (jsdom, hidden panels).
function useMeasuredWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(FALLBACK_WIDTH);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const apply = (w: number) => {
      if (w > 0) setWidth(Math.round(w));
    };
    apply(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) apply(entry.contentRect.width);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

function metricPct(kind: MetricKind, s: FleetMetricsSample): number {
  if (kind === "cpu") return s.cpu_pct;
  const used = kind === "mem" ? s.mem_used : s.disk_used;
  const total = kind === "mem" ? s.mem_total : s.disk_total;
  return total > 0 ? (used / total) * 100 : 0;
}

const KIND_META: Record<MetricKind, { name: string; stroke: string; fill: string }> = {
  cpu: { name: "CPU", stroke: "stroke-sky-400", fill: "fill-sky-400" },
  mem: { name: tr("内存"), stroke: "stroke-blue-400", fill: "fill-blue-400" },
  disk: { name: tr("磁盘"), stroke: "stroke-amber-400", fill: "fill-amber-400" },
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
  const [wrapRef, width] = useMeasuredWidth<HTMLDivElement>();
  const fontPx = FONT_PX * uiTextFactor(useAppearancePrefs());
  const meta = KIND_META[kind];
  const start = windowStart ?? samples[0].ts;
  const end = windowEnd ?? samples[samples.length - 1].ts;
  const span = Math.max(1, end - start);

  const total = kind === "mem" ? latest.mem_total : kind === "disk" ? latest.disk_total : 0;
  const yLabel = (pct: number) => {
    if (kind === "cpu" || total <= 0) return `${pct}%`;
    return pct === 0 ? "0" : formatBytes((total * pct) / 100);
  };
  const yLabels = [0, 50, 100].map((pct) => ({ pct, text: yLabel(pct) }));
  const left = Math.ceil(Math.max(...yLabels.map((l) => textWidth(l.text, fontPx)))) + 8;
  const bottom = Math.ceil(fontPx * 1.6) + 2;
  const plotW = Math.max(1, width - left - RIGHT);
  const plotH = HEIGHT - TOP - bottom;
  const x = (ts: number) => left + ((Math.min(end, Math.max(start, ts)) - start) / span) * plotW;
  const y = (pct: number) => TOP + plotH - (Math.min(100, Math.max(0, pct)) / 100) * plotH;
  const gap = metricsGapThreshold(sampleIntervalMs);
  const segments: FleetMetricsSample[][] = [[]];
  for (const sample of samples) {
    const seg = segments[segments.length - 1];
    const prev = seg[seg.length - 1];
    if (prev && sample.ts - prev.ts > gap) segments.push([]);
    segments[segments.length - 1].push(sample);
  }

  const tickSpacing = Math.max(MIN_TICK_SPACING, textWidth(formatTick(start, span), fontPx) + 12);
  const ticks = timeTicks(start, end, Math.floor(plotW / tickSpacing));

  const onMove = (event: MouseEvent<SVGRectElement>) => {
    const rect = svgRef.current?.getBoundingClientRect();
    if (!rect || rect.width <= 0) return;
    const ts = start + ((event.clientX - rect.left - left) / plotW) * span;
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
  const lineH = Math.ceil(fontPx * 1.3);
  const tipW = tip ? Math.min(plotW, Math.ceil(Math.max(...tip.lines.map((l) => textWidth(l, fontPx)))) + 12) : 0;
  const tipH = 2 * lineH + 6;
  const tipX = tip ? Math.min(left + plotW - tipW, Math.max(left, tip.px - tipW / 2)) : 0;

  return (
    // The SVG is positioned out of flow so its pixel width never props the card open; the wrapper's width drives it.
    <div ref={wrapRef} className="relative mt-1 w-full min-w-0 overflow-hidden" style={{ height: HEIGHT }}>
      <svg
        ref={svgRef}
        role="img"
        aria-label={tr("{name} 趋势，最新 {toFixed}%", { name: meta.name, toFixed: metricPct(kind, latest).toFixed(1) })}
        width={width}
        height={HEIGHT}
        viewBox={`0 0 ${width} ${HEIGHT}`}
        className="absolute inset-0 block"
        onMouseLeave={() => setHover(null)}
      >
        {yLabels.map(({ pct, text }) => (
          <g key={pct}>
            <line
              x1={left}
              x2={left + plotW}
              y1={y(pct)}
              y2={y(pct)}
              className="stroke-neutral-800"
              strokeWidth="1"
              strokeDasharray={pct === 0 ? undefined : "3 3"}
            />
            <text x={left - 4} y={y(pct)} dy="0.35em" textAnchor="end" className="fill-neutral-500" fontSize={fontPx} data-axis="y">
              {text}
            </text>
          </g>
        ))}
        {ticks.map((t) => (
          <text key={t} x={x(t)} y={HEIGHT - 4} textAnchor="middle" className="fill-neutral-500" fontSize={fontPx} data-axis="x">
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
            <rect x={tipX} y={TOP} width={tipW} height={tipH} rx="3" className="fill-neutral-900 stroke-neutral-700" />
            <text x={tipX + 6} y={TOP + 3 + lineH} dy="-0.25em" className="fill-neutral-400" fontSize={fontPx}>
              {tip.lines[0]}
            </text>
            <text x={tipX + 6} y={TOP + 3 + 2 * lineH} dy="-0.25em" className="fill-neutral-100" fontSize={fontPx}>
              {tip.lines[1]}
            </text>
          </g>
        )}
        <rect x={left} y={TOP} width={plotW} height={plotH} fill="transparent" onMouseMove={onMove} />
      </svg>
    </div>
  );
}

const SPARK_W = 120;
const SPARK_H = 24;

export const SPARKLINE_STROKES = {
  cpu: KIND_META.cpu.stroke,
  mem: KIND_META.mem.stroke,
  disk: KIND_META.disk.stroke,
  rx: "stroke-green-400",
  tx: "stroke-violet-400",
} as const;

// Compact unlabelled line for a live metric, newest value on the right. Values are scaled against max (100 for
// percentages) or, without it, the largest value shown; null breaks the line.
export function MetricSparkline({
  values,
  max,
  label,
  stroke = SPARKLINE_STROKES.cpu,
}: {
  values: readonly (number | null)[];
  max?: number;
  label: string;
  stroke?: string;
}) {
  const top = max ?? Math.max(0, ...values.map((v) => v ?? 0));
  const step = values.length > 1 ? SPARK_W / (values.length - 1) : 0;
  const y = (v: number) => SPARK_H - 1 - (top > 0 ? Math.min(1, Math.max(0, v / top)) : 0) * (SPARK_H - 2);
  const segments: string[][] = [[]];
  values.forEach((v, i) => {
    if (v === null || !Number.isFinite(v)) {
      if (segments[segments.length - 1].length > 0) segments.push([]);
      return;
    }
    segments[segments.length - 1].push(`${(i * step).toFixed(1)},${y(v).toFixed(1)}`);
  });
  return (
    <svg role="img" aria-label={label} viewBox={`0 0 ${SPARK_W} ${SPARK_H}`} preserveAspectRatio="none" className="h-6 w-full">
      <line x1={0} x2={SPARK_W} y1={SPARK_H - 0.5} y2={SPARK_H - 0.5} className="stroke-neutral-800" strokeWidth="1" />
      {segments
        .filter((seg) => seg.length > 1)
        .map((seg) => (
          <polyline
            key={seg[0]}
            points={seg.join(" ")}
            fill="none"
            className={stroke}
            strokeWidth="1.5"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
    </svg>
  );
}
