// CPU 24 小时趋势线: 手写 SVG, 无图表依赖; x 轴按采样时间线性映射,
// y 轴固定 0-100%。单点采样画圆点, 空数据由调用方走空态, 不在此分支。

import type { FleetMetricsSample } from "../../ipc/fleetApi";

const WIDTH = 320;
const HEIGHT = 64;
const PAD_X = 4;
const PAD_Y = 6;

export function MetricsChart({ samples }: { samples: FleetMetricsSample[] }) {
  if (samples.length === 0) return null;
  const first = samples[0];
  const last = samples[samples.length - 1];
  const span = Math.max(1, last.ts - first.ts);
  const x = (ts: number) => PAD_X + ((ts - first.ts) / span) * (WIDTH - PAD_X * 2);
  const y = (pct: number) => HEIGHT - PAD_Y - (Math.min(100, Math.max(0, pct)) / 100) * (HEIGHT - PAD_Y * 2);

  const points = samples.map((s) => `${x(s.ts).toFixed(1)},${y(s.cpu_pct).toFixed(1)}`).join(" ");
  const latest = samples[samples.length - 1];

  return (
    <svg
      role="img"
      aria-label={`CPU 24 小时趋势，最新 ${latest.cpu_pct.toFixed(1)}%`}
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
        vectorEffect="non-scaling-stroke"
      />
      {samples.length > 1 ? (
        <polyline
          points={points}
          fill="none"
          className="stroke-sky-400"
          strokeWidth="1.5"
          strokeLinejoin="round"
          strokeLinecap="round"
          vectorEffect="non-scaling-stroke"
        />
      ) : (
        <circle cx={x(latest.ts)} cy={y(latest.cpu_pct)} r="2.5" className="fill-sky-400" />
      )}
    </svg>
  );
}
