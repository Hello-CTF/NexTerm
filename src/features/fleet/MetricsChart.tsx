// CPU trend line: hand-written SVG, no chart dependency; the x axis maps linearly by sample time or the supplied window,
// and gaps beyond the freshness threshold break the polyline instead of inventing continuity.

import type { FleetMetricsSample } from "../../ipc/fleetApi";

const WIDTH = 320;
const HEIGHT = 64;
const PAD_X = 4;
const PAD_Y = 6;

export function metricsGapThreshold(sampleIntervalMs?: number): number {
  if (sampleIntervalMs === undefined || !Number.isFinite(sampleIntervalMs) || sampleIntervalMs <= 0) return 180_000;
  return Math.max(3 * sampleIntervalMs, 180_000);
}

interface MetricsChartProps {
  samples: FleetMetricsSample[];
  latest?: FleetMetricsSample;
  windowStart?: number;
  windowEnd?: number;
  sampleIntervalMs?: number;
}

export function MetricsChart({ samples, latest, windowStart, windowEnd, sampleIntervalMs }: MetricsChartProps) {
  if (samples.length === 0) return null;
  const last = samples[samples.length - 1];
  const start = windowStart ?? samples[0].ts;
  const end = windowEnd ?? last.ts;
  const span = Math.max(1, end - start);
  const x = (ts: number) => PAD_X + ((ts - start) / span) * (WIDTH - PAD_X * 2);
  const y = (pct: number) => HEIGHT - PAD_Y - (Math.min(100, Math.max(0, pct)) / 100) * (HEIGHT - PAD_Y * 2);
  const gapThreshold = metricsGapThreshold(sampleIntervalMs);
  const segments: FleetMetricsSample[][] = [[]];
  for (const sample of samples) {
    const segment = segments[segments.length - 1];
    const previous = segment[segment.length - 1];
    if (previous && sample.ts - previous.ts > gapThreshold) segments.push([]);
    segments[segments.length - 1].push(sample);
  }
  const latestPoint = latest ?? last;

  return (
    <svg
      role="img"
      aria-label={`CPU 趋势，最新 ${latestPoint.cpu_pct.toFixed(1)}%`}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      preserveAspectRatio="none"
      className="h-16 w-full"
    >
      <line
        x1={PAD_X}
        x2={WIDTH - PAD_X}
        y1={y(100)}
        y2={y(100)}
        className="stroke-neutral-800"
        strokeWidth="1"
        strokeDasharray="3 3"
        vectorEffect="non-scaling-stroke"
      />
      <line
        x1={PAD_X}
        x2={WIDTH - PAD_X}
        y1={y(0)}
        y2={y(0)}
        className="stroke-neutral-800"
        strokeWidth="1"
        strokeDasharray="3 3"
        vectorEffect="non-scaling-stroke"
      />
      {segments.map((segment) => {
        const first = segment[0];
        if (segment.length === 1) {
          return <circle key={first.ts} cx={x(first.ts)} cy={y(first.cpu_pct)} r="2.5" className="fill-sky-400" />;
        }
        const points = segment.map((sample) => `${x(sample.ts).toFixed(1)},${y(sample.cpu_pct).toFixed(1)}`).join(" ");
        return (
          <polyline
            key={first.ts}
            points={points}
            fill="none"
            className="stroke-sky-400"
            strokeWidth="1.5"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        );
      })}
    </svg>
  );
}
