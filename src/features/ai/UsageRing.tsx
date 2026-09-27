// 上下文用量圆环（P0-5）：AI 侧栏功能行右侧的占用指示 + hover 明细浮层。
//
// 为什么用圆环而不是进度条：功能行只有一行高（30px 圆钮）、右半边已经挤着
// 权限盾牌 / 接管 / 发送四个控件，圆环用同样的横向占地把"上下文还剩多少"
// 压成一个一眼可辨的形状；明细收进 hover，平时不打扰 —— 与这条功能行
// "高频操作常驻、次要信息按需展开"的密度一致。
//
// 不引第三方图表库、不碰 styles.css：全部 Tailwind 内联类 + 内联 SVG，
// 沿用 icons.tsx 那套 `stroke="currentColor"` + `text-*` 上色的写法。

/**
 * 与 Rust `ai::usage::Usage` 对应的 TS 形状（camelCase）。
 *
 * 内核侧的 `Usage` 序列化后就是这四个字段；`src/ipc/types.ts` 由 ts-rs 生成，
 * 生成出 `UsageDto` 后可直接用它替换本接口（字段一致），或保留本接口做前端本地类型。
 */
export interface AiUsage {
  /** 输入侧 token（含命中缓存的部分）。 */
  promptTokens: number;
  /** 输出侧 token。 */
  completionTokens: number;
  /** 命中 prompt cache 的输入 token（promptTokens 的子集）。 */
  cachedTokens: number;
  /** 模型上下文窗口上限；未知时 0。 */
  contextWindow: number;
}

export interface UsageRingProps {
  /** 最近一轮的用量快照；`null`/`undefined` = 还没对话过。 */
  usage?: AiUsage | null;
  /** 圆环直径（px），默认 18 —— 与相邻 `nx-icon-btn-sm` 的视觉重量相当。 */
  size?: number;
  className?: string;
}

/** 环宽；相对 18px 直径取值，细一点免得小尺寸下显得发闷。 */
const STROKE = 2.5;

function clampPercent(x: number): number {
  if (!Number.isFinite(x)) return 0;
  return Math.max(0, Math.min(100, x));
}

/** 输入侧占窗口的百分比；窗口为 0（未知）时返回 0，绝不返回 NaN。 */
export function usedPercent(promptTokens: number, contextWindow: number): number {
  if (contextWindow <= 0) return 0;
  return clampPercent((promptTokens / contextWindow) * 100);
}

/** 缓存命中率；输入为 0 时返回 0，命中数超过输入按 100% 计。 */
export function cacheHitPercent(promptTokens: number, cachedTokens: number): number {
  if (promptTokens <= 0) return 0;
  return clampPercent((Math.min(cachedTokens, promptTokens) / promptTokens) * 100);
}

/** 1234 → "1.2k"，1_200_000 → "1.2M"；不足千原样返回。 */
export function formatTokens(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(Math.round(n));
}

/** 占用越高越危险：<60 绿、<85 琥珀、>=85 红。 */
function ringTone(percent: number): string {
  if (percent >= 85) return "text-red-400";
  if (percent >= 60) return "text-amber-400";
  return "text-green-400";
}

export function UsageRing({ usage, size = 18, className = "" }: UsageRingProps) {
  const prompt = usage?.promptTokens ?? 0;
  const cached = usage?.cachedTokens ?? 0;
  const window = usage?.contextWindow ?? 0;
  // 窗口未知就不画进度：没有分母的百分比只会误导。
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
        {/* 底轨 */}
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
          // 空态：虚线空环，表示"还没开始"，同时占住布局不让功能行跳动。
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

      {/* hover 明细浮层：往上弹，右对齐到圆环，避免超出侧栏右边界。 */}
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
