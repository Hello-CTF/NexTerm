import { useCallback, useEffect, useRef, useState } from "react";
import { assetApi } from "../../ipc/commands";
import type { AuditEntryDto } from "../../ipc/types";
import { describeError } from "../../ui/errorText";
import { IconHistory, IconRefresh } from "../../ui/icons";

interface AuditEntry {
  id: number;
  ts: number;
  sessionId: string | null;
  assetId: string | null;
  source: string;
  kind: string;
  payload: unknown;
  exitCode: number | null;
  durationMs: number | null;
}

type SourceFilter = "" | "user" | "ai";

const FILTERS: { value: SourceFilter; label: string }[] = [
  { value: "", label: "全部" },
  { value: "user", label: "用户" },
  { value: "ai", label: "AI" },
];

const PAGE_SIZE = 100;
const SKELETON_ROWS = 8;

function toEntry(r: AuditEntryDto): AuditEntry {
  return {
    id: r.id,
    ts: r.ts,
    sessionId: r.sessionId,
    assetId: r.assetId,
    source: r.source,
    kind: r.kind,
    payload: r.payload,
    exitCode: r.exitCode,
    durationMs: r.durationMs,
  };
}

function mergeEntries(prev: AuditEntry[], rows: AuditEntryDto[]): AuditEntry[] {
  if (prev.length === 0) return rows.map(toEntry);
  const seen = new Set(prev.map((e) => e.id));
  const added: AuditEntry[] = [];
  for (const r of rows) {
    if (seen.has(r.id)) continue;
    seen.add(r.id);
    added.push(toEntry(r));
  }
  return [...prev, ...added];
}

function AuditSkeletonRows() {
  return (
    <>
      {Array.from({ length: SKELETON_ROWS }, (_, i) => (
        <tr key={i} aria-hidden="true">
          <td>
            <span className="nx-skeleton w-4/5" />
          </td>
          <td>
            <span className="nx-skeleton nx-skeleton-chip w-3/5" />
          </td>
          <td>
            <span className="nx-skeleton w-3/5" />
          </td>
          <td>
            <span className="nx-skeleton w-3/4" />
          </td>
          <td>
            <span className="nx-skeleton ml-auto w-1/3" />
          </td>
          <td>
            <span className="nx-skeleton ml-auto w-1/2" />
          </td>
        </tr>
      ))}
    </>
  );
}

export function AuditView() {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [source, setSource] = useState<SourceFilter>("");
  const [total, setTotal] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [lastPageFull, setLastPageFull] = useState(false);
  const loadSeq = useRef(0);
  const fetchedRef = useRef(0);

  const loadFirstPage = useCallback(async (src: SourceFilter) => {
    const seq = ++loadSeq.current;
    fetchedRef.current = 0;
    setEntries([]);
    setTotal(null);
    setError(null);
    setMoreError(null);
    setLastPageFull(false);
    setLoadingMore(false);
    setLoading(true);
    void (async () => {
      try {
        const count = await assetApi.auditCount({ source: src || undefined });
        if (seq === loadSeq.current) setTotal(count.total);
      } catch {
        if (seq === loadSeq.current) setTotal(null);
      }
    })();
    try {
      const rows = await assetApi.auditQuery({ source: src || undefined, limit: PAGE_SIZE, offset: 0 });
      if (seq !== loadSeq.current) return;
      fetchedRef.current = rows.length;
      setLastPageFull(rows.length === PAGE_SIZE);
      setEntries(mergeEntries([], rows));
      setError(null);
    } catch (e) {
      if (seq === loadSeq.current) setError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadFirstPage(source);
  }, [source, loadFirstPage]);

  const knownTotal = total !== null ? total : lastPageFull ? null : entries.length;
  const hasMore = lastPageFull && (knownTotal === null || entries.length < knownTotal);

  const loadMore = async () => {
    if (loading || loadingMore || !hasMore) return;
    const seq = loadSeq.current;
    const offset = fetchedRef.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const rows = await assetApi.auditQuery({ source: source || undefined, limit: PAGE_SIZE, offset });
      if (seq !== loadSeq.current) return;
      fetchedRef.current += rows.length;
      setLastPageFull(rows.length === PAGE_SIZE);
      setEntries((prev) => mergeEntries(prev, rows));
    } catch (e) {
      if (seq === loadSeq.current) setMoreError(describeError(e));
    } finally {
      if (seq === loadSeq.current) setLoadingMore(false);
    }
  };

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">审计日志</span>
        <span className="nx-hint">
          {error && entries.length === 0
            ? "审计记录加载失败"
            : knownTotal !== null
              ? `共 ${knownTotal} 条`
              : `已加载 ${entries.length} 条`}
        </span>
        {error && entries.length > 0 && (
          <span className="nx-hint text-red-300">刷新失败 · {error}</span>
        )}
        <div className="nx-spacer" />
        <div className="nx-segment">
          {FILTERS.map((f) => (
            <button
              key={f.value}
              className={`nx-segment-item ${source === f.value ? "is-active" : ""}`}
              onClick={() => setSource(f.value)}
            >
              {f.label}
            </button>
          ))}
        </div>
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => void loadFirstPage(source)}
          disabled={loading}
        >
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table nx-table-fixed">
          <thead>
            <tr>
              <th style={{ width: 168 }}>时间</th>
              <th style={{ width: 72 }}>来源</th>
              <th style={{ width: 140 }}>动作</th>
              <th>详情</th>
              <th style={{ width: 84 }} className="nx-right">
                退出码
              </th>
              <th style={{ width: 84 }} className="nx-right">
                耗时
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id}>
                <td className="nx-mono whitespace-nowrap">{new Date(e.ts).toLocaleString()}</td>
                <td>
                  <span className={`nx-badge ${e.source === "ai" ? "nx-badge-purple" : "nx-badge-blue"}`}>
                    {e.source === "ai" ? "AI" : "用户"}
                  </span>
                </td>
                <td className="font-mono text-[11.5px] text-neutral-200">{e.kind}</td>
                <td className="nx-mono max-w-[520px] truncate" title={JSON.stringify(e.payload)}>
                  {JSON.stringify(e.payload)}
                </td>
                <td className="nx-right">
                  {e.exitCode === null ? (
                    <span className="text-neutral-600">—</span>
                  ) : e.exitCode === 0 ? (
                    <span className="text-green-300" title="成功">
                      ✓ 0
                    </span>
                  ) : (
                    <span className="text-red-300" title="失败">
                      ✗ {e.exitCode}
                    </span>
                  )}
                </td>
                <td className="nx-right nx-mono">
                  {e.durationMs === null ? <span className="text-neutral-600">—</span> : `${e.durationMs}ms`}
                </td>
              </tr>
            ))}
            {entries.length === 0 && loading ? (
              <AuditSkeletonRows />
            ) : entries.length === 0 && error ? (
              <tr>
                <td colSpan={6} className="nx-table-empty">
                  <span className="text-red-300">审计记录加载失败 · {error}</span>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm ml-2"
                    onClick={() => void loadFirstPage(source)}
                    disabled={loading}
                  >
                    <IconRefresh size={12} />
                    重试
                  </button>
                </td>
              </tr>
            ) : entries.length === 0 ? (
              <tr>
                <td colSpan={6} className="nx-table-empty">
                  暂无记录：连接主机、执行命令或让 AI 动手之后，这里会逐条记下来。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {entries.length > 0 && (
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
          <span className="nx-hint min-w-0 truncate">
            {knownTotal !== null ? `已显示 ${entries.length} / ${knownTotal} 条` : `已显示 ${entries.length} 条`}
          </span>
          <div className="nx-spacer" />
          {moreError ? (
            <>
              <span className="min-w-0 break-words text-red-300">加载更多失败 · {moreError}</span>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                onClick={() => void loadMore()}
                disabled={loadingMore}
              >
                {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
                重试
              </button>
            </>
          ) : hasMore ? (
            <button
              className="nx-btn nx-btn-ghost nx-btn-xs"
              onClick={() => void loadMore()}
              disabled={loadingMore}
            >
              {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
              加载更多
            </button>
          ) : (
            <span>已加载全部</span>
          )}
        </div>
      )}

      <div className="flex shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
        <span>所有会话命令、AI 动作、文件写操作都会落库，逐条标注来源；有退出码的记录会一并展示。</span>
      </div>
    </div>
  );
}
