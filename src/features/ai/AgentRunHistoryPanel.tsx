import { useCallback, useEffect, useRef, useState } from "react";
import { describeError } from "../../ui/errorText";
import { IconLoader, IconRefresh, IconTerminal } from "../../ui/icons";
import type { AgentRunHistoryApi, AgentRunHistoryScope } from "./agentApi";
import { formatEvidence, runStateView, type AgentStateTone } from "./agentState";
import type {
  AgentRecoverySummary,
  AgentRunHistoryEntry,
  AgentRunStatusFilter,
} from "./agentTypes";

const TONE_BADGE: Record<AgentStateTone, string> = {
  ok: "nx-badge nx-badge-green",
  warn: "nx-badge nx-badge-amber",
  error: "nx-badge nx-badge-red",
  muted: "nx-count",
  accent: "nx-badge nx-badge-blue",
};

const FILTER_OPTIONS: { value: AgentRunStatusFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "running", label: "运行中" },
  { value: "idle", label: "空闲" },
  { value: "done", label: "已完成" },
  { value: "error", label: "失败" },
  { value: "canceled", label: "已取消" },
];

// Pages may overlap at cursor boundaries; the run id is the identity.
function dedupeEntries(entries: AgentRunHistoryEntry[]): AgentRunHistoryEntry[] {
  const seen = new Set<string>();
  const out: AgentRunHistoryEntry[] = [];
  for (const entry of entries) {
    if (seen.has(entry.runId)) continue;
    seen.add(entry.runId);
    out.push(entry);
  }
  return out;
}

export function AgentRunHistoryPanel({
  api,
  scope,
  pageSize = 50,
}: {
  api: AgentRunHistoryApi;
  scope: AgentRunHistoryScope;
  pageSize?: number;
}) {
  const { ownerId, conversationId } = scope;
  const [entries, setEntries] = useState<AgentRunHistoryEntry[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [filter, setFilter] = useState<AgentRunStatusFilter>("all");
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [errorPhase, setErrorPhase] = useState<"initial" | "more" | null>(null);
  const [summary, setSummary] = useState<AgentRecoverySummary | null>(null);

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const invalidatePendingLoads = useCallback(() => {
    aliveRef.current = false;
    loadGenRef.current++;
  }, []);
  useEffect(() => {
    aliveRef.current = true;
    return invalidatePendingLoads;
  }, [invalidatePendingLoads]);

  // Generation + liveness guard: a response from an older reload, an older
  // scope/filter, or an unmounted panel never touches the visible list.
  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setEntries(null);
    setCursor(null);
    setLoadingMore(false);
    setLoadError(null);
    setErrorPhase(null);
    setSummary(null);
    try {
      const [first, recovery] = await Promise.all([
        api.listRuns({ ownerId, conversationId }, filter, null, pageSize),
        api.recoverySummary({ ownerId, conversationId }).catch(() => null),
      ]);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries(dedupeEntries(first.entries));
      setCursor(first.nextCursor);
      setSummary(recovery);
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries([]);
      setLoadError(describeError(e));
      setErrorPhase("initial");
    }
  }, [api, ownerId, conversationId, filter, pageSize]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const loadMore = useCallback(async () => {
    if (cursor === null || loadingMore) return;
    const gen = ++loadGenRef.current;
    setLoadingMore(true);
    setLoadError(null);
    setErrorPhase(null);
    try {
      const next = await api.listRuns({ ownerId, conversationId }, filter, cursor, pageSize);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries((prev) => dedupeEntries([...(prev ?? []), ...next.entries]));
      setCursor(next.nextCursor);
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setLoadError(describeError(e));
      setErrorPhase("more");
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoadingMore(false);
    }
  }, [api, ownerId, conversationId, filter, cursor, loadingMore, pageSize]);

  return (
    <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        <IconTerminal size={12} />
        Agent Run 历史
        <span className="nx-count">{entries ? `${entries.length} 条` : "加载中"}</span>
        <span className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="刷新 run 历史"
          aria-label="刷新 run 历史"
          onClick={() => void reload()}
        >
          <IconRefresh size={11} />
        </button>
      </div>

      <div className="mb-1.5 flex flex-wrap items-center gap-1">
        {FILTER_OPTIONS.map((opt) => (
          <button
            key={opt.value}
            className={`nx-btn nx-btn-outline nx-btn-xs ${filter === opt.value ? "border-blue-500/50 text-blue-300" : ""}`}
            aria-pressed={filter === opt.value}
            onClick={() => setFilter(opt.value)}
          >
            {opt.label}
          </button>
        ))}
      </div>

      {summary && (
        <div className="mb-1.5 rounded-lg border border-neutral-800/60 px-2 py-1.5 text-[10.5px] leading-relaxed text-neutral-400">
          恢复摘要：已恢复 {summary.restoredRuns} 个 run
          {summary.resumableRunId ? ` · 可继续运行 ${summary.resumableRunId}` : ""}
          {summary.interruptedRunIds.length > 0
            ? ` · ${summary.interruptedRunIds.length} 个 run 中断`
            : ""}
          {summary.truncated ? " · 恢复已截断，列表外可能还有更多 run" : ""}
        </div>
      )}

      {loadError && (
        <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-red-400">
          <span className="min-w-0 flex-1 truncate">加载失败：{loadError}</span>
          <button
            className="nx-btn nx-btn-outline nx-btn-xs"
            onClick={() => void (errorPhase === "more" ? loadMore() : reload())}
          >
            重试
          </button>
        </div>
      )}

      {!loadError && entries === null && (
        <div className="flex items-center gap-1.5 py-1 text-[11px] text-neutral-500">
          <IconLoader size={12} className="animate-spin" />
          加载 run 历史…
        </div>
      )}

      {entries?.map((entry) => {
        const state = runStateView(entry.state, entry.evidence);
        return (
          <div
            key={entry.runId}
            className="mb-1 rounded-lg border border-neutral-800/60 px-2 py-1.5"
            data-run-id={entry.runId}
          >
            <div className="flex items-center gap-1.5">
              <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-neutral-200" title={entry.runId}>
                {entry.runId}
              </span>
              <span
                className={`shrink-0 ${TONE_BADGE[state.tone]}`}
                title={state.evidence ? formatEvidence(state.evidence) : "无证据"}
              >
                {state.label}
              </span>
            </div>
            <div className="mt-0.5 flex items-center gap-1.5 text-[10.5px] text-neutral-500">
              <span className="min-w-0 flex-1 truncate">{entry.summary}</span>
              <span className="shrink-0">{new Date(entry.startedAt).toLocaleString()}</span>
            </div>
          </div>
        );
      })}

      {!loadError && entries !== null && entries.length === 0 && (
        <div className="py-1 text-[11px] text-neutral-500">暂无 run 记录</div>
      )}

      {!loadError && cursor !== null && (
        <button
          className="nx-btn nx-btn-outline nx-btn-xs"
          disabled={loadingMore}
          onClick={() => void loadMore()}
        >
          {loadingMore ? "加载中…" : "加载更早"}
        </button>
      )}
    </div>
  );
}
