
export interface AiUsage {
  promptTokens: number;
  completionTokens: number;
  cachedTokens: number;
  contextWindow: number;
}

export interface UsageRingProps {
  usage?: AiUsage | null;
  size?: number;
  className?: string;
}

const STROKE = 2.5;

function clampPercent(x: number): number {
  if (!Number.isFinite(x)) return 0;
  return Math.max(0, Math.min(100, x));
}

export function usedPercent(promptTokens: number, contextWindow: number): number {
  if (contextWindow <= 0) return 0;
  return clampPercent((promptTokens / contextWindow) * 100);
}

export function cacheHitPercent(promptTokens: number, cachedTokens: number): number {
  if (promptTokens <= 0) return 0;
  return clampPercent((Math.min(cachedTokens, promptTokens) / promptTokens) * 100);
}

export function formatTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(Math.round(n));
}

function ringTone(percent: number): string {
  if (percent >= 85) return "text-red-400";
  if (percent >= 60) return "text-amber-400";
  return "text-green-400";
}

export function UsageRing({ usage, size = 18, className = "" }: UsageRingProps) {
  const prompt = usage?.promptTokens ?? 0;
  const cached = usage?.cachedTokens ?? 0;
  const window = usage?.contextWindow ?? 0;
  const hasData = usage != null && window > 0;

  const used = usedPercent(prompt, window);
  const hit = cacheHitPercent(prompt, cached);

  const center = size / 2;
  const radius = (size - STROKE) / 2;
  const circumference = 2 * Math.PI * radius;
  const dash = (used / 100) * circumference;

  const label = hasData
    ? `上下文占用 ${used.toFixed(0)}%（${formatTokens(prompt)} / ${formatTokens(window)}），缓存命中 ${hit.toFixed(0)}%`
    : "尚未产生用量";

  return (
    <span
      role="img"
      aria-label={label}
      className={`group relative inline-flex shrink-0 items-center ${className}`}
    >
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="block">
        <circle
          cx={center}
          cy={center}
          r={radius}
          fill="none"
          stroke="currentColor"
          strokeWidth={STROKE}
          className="text-neutral-800"
        />
        {hasData ? (
          <circle
            cx={center}
            cy={center}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={STROKE}
            strokeLinecap="round"
            strokeDasharray={`${dash} ${circumference - dash}`}
            transform={`rotate(-90 ${center} ${center})`}
            className={ringTone(used)}
          />
        ) : (
          <circle
            cx={center}
            cy={center}
            r={radius}
            fill="none"
            stroke="currentColor"
            strokeWidth={STROKE}
            strokeLinecap="round"
            strokeDasharray="2 3"
            className="text-neutral-600"
          />
        )}
      </svg>

      <span className="pointer-events-none absolute bottom-full right-0 z-20 mb-1.5 hidden w-max group-hover:block">
        <span className="block rounded-lg border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-[11px] leading-relaxed text-neutral-300 shadow-xl">
          {hasData ? (
            <>
              <span className="block font-medium text-neutral-100">
                上下文占用 {used.toFixed(0)}%
              </span>
              <span className="block font-mono text-[10.5px] text-neutral-400">
                {formatTokens(prompt)} / {formatTokens(window)} tokens
              </span>
              <span
                className={`block ${
                  hit > 0 ? "text-green-400" : "text-neutral-500"
                }`}
              >
                缓存命中 {hit.toFixed(0)}%
              </span>
            </>
          ) : (
            <span className="block text-neutral-500">尚未产生用量</span>
          )}
        </span>
      </span>
    </span>
  );
}
