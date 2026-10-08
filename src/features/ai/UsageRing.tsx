import { useCallback, useEffect, useRef, useState } from "react";
import type { AiRunDto, AiUsageSummaryRow } from "../../ipc/types";

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
  runs?: AiRunDto[];
  loadSummary?: () => Promise<AiUsageSummaryRow[]>;
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

export interface SceneUsage {
  runs: number;
  tokensIn: number;
  tokensOut: number;
  cacheCreationTokens: number;
  averageLatencyMs: number;
}

function emptySceneUsage(): SceneUsage {
  return { runs: 0, tokensIn: 0, tokensOut: 0, cacheCreationTokens: 0, averageLatencyMs: 0 };
}

function safeCount(value: number): number {
  return Number.isFinite(value) && value > 0 ? value : 0;
}

export function aggregateUsageRows(rows: AiUsageSummaryRow[]): {
  chat: SceneUsage;
  title: SceneUsage;
  cron: SceneUsage;
  subagent: SceneUsage;
} {
  const chat = emptySceneUsage();
  const title = emptySceneUsage();
  const cron = emptySceneUsage();
  const subagent = emptySceneUsage();
  for (const row of rows) {
    const target =
      row.source === "title" ? title : row.source === "cron" ? cron : row.source === "subagent" ? subagent : chat;
    const runs = safeCount(row.runs);
    const weighted = target.averageLatencyMs * target.runs + safeCount(row.averageLatencyMs) * runs;
    target.runs += runs;
    target.tokensIn += safeCount(row.tokensIn);
    target.tokensOut += safeCount(row.tokensOut);
    target.cacheCreationTokens += safeCount(row.cacheCreationTokens);
    target.averageLatencyMs = target.runs > 0 ? Math.round(weighted / target.runs) : 0;
  }
  return { chat, title, cron, subagent };
}

export interface RunErrorStats {
  runs: number;
  retries: number;
  failures: number;
  retryRate: number;
  failureRate: number;
}

export function runErrorStats(runs: AiRunDto[]): RunErrorStats {
  const finished = runs.filter((run) => run.finishedAt != null && run.source !== "title");
  let retries = 0;
  let failures = 0;
  for (const run of finished) {
    retries += safeCount(run.retries);
    failures += safeCount(run.failures);
  }
  const total = finished.length;
  return {
    runs: total,
    retries,
    failures,
    retryRate: total > 0 ? Math.min(1, retries / total) : 0,
    failureRate: total > 0 ? Math.min(1, failures / total) : 0,
  };
}

function percentText(rate: number): string {
  return `${(rate * 100).toFixed(0)}%`;
}

export function UsageRing({ usage, size = 18, className = "", runs, loadSummary }: UsageRingProps) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [summaryRows, setSummaryRows] = useState<AiUsageSummaryRow[] | null>(null);
  const [summaryFailed, setSummaryFailed] = useState(false);
  const containerRef = useRef<HTMLSpanElement>(null);
  const detailsAvailable = runs !== undefined || loadSummary !== undefined;

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

  useEffect(() => {
    if (!detailsOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setDetailsOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [detailsOpen]);

  const loadSummaryRows = useCallback(() => {
    if (!loadSummary) return;
    setSummaryFailed(false);
    loadSummary().then(setSummaryRows).catch(() => setSummaryFailed(true));
  }, [loadSummary]);

  const toggleDetails = () => {
    setDetailsOpen((open) => !open);
    if (!detailsOpen && loadSummary && summaryRows === null && !summaryFailed) {
      loadSummaryRows();
    }
  };

  const ring = (
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
  );

  return (
    <span
      ref={containerRef}
      className={`group relative inline-flex shrink-0 items-center pointer-coarse:min-h-6 pointer-coarse:min-w-6 ${className}`}
    >
      {detailsAvailable ? (
        <button
          type="button"
          aria-expanded={detailsOpen}
          title="查看用量明细"
          onClick={toggleDetails}
          className="pointer-coarse:min-h-6 pointer-coarse:min-w-6 inline-flex items-center justify-center"
        >
          <span role="img" aria-label={label} className="inline-flex items-center">
            {ring}
          </span>
        </button>
      ) : (
        <span role="img" aria-label={label} tabIndex={0} className="inline-flex items-center">
          {ring}
        </span>
      )}

      <span className="pointer-events-none absolute bottom-full right-0 z-20 mb-1.5 hidden w-max group-hover:block group-focus-within:block">
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

      {detailsAvailable && detailsOpen ? (
        <UsageDetails runs={runs} summaryRows={summaryRows} summaryFailed={summaryFailed} onRetrySummary={loadSummaryRows} />
      ) : null}
    </span>
  );
}

function UsageDetails({
  runs,
  summaryRows,
  summaryFailed,
  onRetrySummary,
}: {
  runs?: AiRunDto[];
  summaryRows: AiUsageSummaryRow[] | null;
  summaryFailed: boolean;
  onRetrySummary: () => void;
}) {
  const errorStats = runs ? runErrorStats(runs) : null;
  const scenes = summaryRows ? aggregateUsageRows(summaryRows) : null;
  return (
    <span className="absolute top-full right-0 z-20 mt-1.5 block w-64 rounded-lg border border-neutral-700 bg-neutral-900 px-2.5 py-2 text-left text-[11px] leading-relaxed shadow-xl">
      <span className="mb-0.5 block font-medium text-neutral-100">用量明细</span>
      {errorStats && errorStats.runs > 0 ? (
        <span className="block text-neutral-300">
          本会话 {errorStats.runs} 轮 · 重试 {errorStats.retries}（{percentText(errorStats.retryRate)}）
          · 失败 {errorStats.failures}（{percentText(errorStats.failureRate)}）
        </span>
      ) : null}
      {scenes ? (
        <>
          <span className="block text-neutral-300">
            对话 {scenes.chat.runs} 次 · 输入 {formatTokens(scenes.chat.tokensIn)} · 输出{" "}
            {formatTokens(scenes.chat.tokensOut)}（全部会话）
          </span>
          {scenes.cron.runs > 0 ? (
            <span className="block text-neutral-300">
              定时任务 {scenes.cron.runs} 次 · 输入 {formatTokens(scenes.cron.tokensIn)} · 输出{" "}
              {formatTokens(scenes.cron.tokensOut)}（全部会话）
            </span>
          ) : null}
          {scenes.subagent.runs > 0 ? (
            <span className="block text-neutral-300">
              子任务 {scenes.subagent.runs} 次 · 输入 {formatTokens(scenes.subagent.tokensIn)} · 输出{" "}
              {formatTokens(scenes.subagent.tokensOut)}（全部会话）
            </span>
          ) : null}
          <span className="block text-neutral-500">
            标题 {scenes.title.runs} 次 · 输入 {formatTokens(scenes.title.tokensIn)} · 输出{" "}
            {formatTokens(scenes.title.tokensOut)}（单独计，不入对话总量）
          </span>
        </>
      ) : null}
      {summaryFailed ? (
        <span className="flex items-center gap-1.5">
          <span className="text-red-300">用量汇总加载失败</span>
          <button type="button" className="nx-btn nx-btn-outline nx-btn-xs" onClick={onRetrySummary}>
            重试
          </button>
        </span>
      ) : null}
      {!errorStats && !scenes && !summaryFailed ? (
        <span className="block text-neutral-500">加载中…</span>
      ) : null}
    </span>
  );
}
