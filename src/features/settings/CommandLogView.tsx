import { useCallback, useEffect, useRef, useState } from "react";
import { assetApi, type Asset } from "../../ipc/commands";
import type { CommandEntryDto } from "../../ipc/types";
import { WEB } from "../../ipc/env";
import { useAuth } from "../auth/store";
import { describeError } from "../../ui/errorText";
import { IconRefresh, IconTerminal } from "../../ui/icons";
import { useCompactWidth } from "./useCompactWidth";

interface CommandEntry {
  id: number;
  sessionId: string;
  tabId: string;
  assetId: string;
  userId: string | null;
  command: string;
  source: string;
  exitCode: number | null;
  startedAt: number;
  finishedAt: number;
}

const PAGE_SIZE = 100;
const SKELETON_ROWS = 8;

const SOURCE_LABELS: Record<string, string> = {
  terminal: "终端",
  exec: "执行",
};

function toEntry(r: CommandEntryDto): CommandEntry {
  return {
    id: r.id,
    sessionId: r.sessionId,
    tabId: r.tabId,
    assetId: r.assetId,
    userId: r.userId,
    command: r.command,
    source: r.source,
    exitCode: r.exitCode,
    startedAt: r.startedAt,
    finishedAt: r.finishedAt,
  };
}

function mergeEntries(prev: CommandEntry[], rows: CommandEntryDto[]): CommandEntry[] {
  if (prev.length === 0) return rows.map(toEntry);
  const seen = new Set(prev.map((e) => e.id));
  const added: CommandEntry[] = [];
  for (const r of rows) {
    if (seen.has(r.id)) continue;
    seen.add(r.id);
    added.push(toEntry(r));
  }
  return [...prev, ...added];
}

function CommandSkeletonRows() {
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
            <span className="nx-skeleton w-full" />
          </td>
          <td>
            <span className="nx-skeleton nx-skeleton-chip w-2/5" />
          </td>
          <td>
            <span className="nx-skeleton ml-auto w-1/3" />
          </td>
        </tr>
      ))}
    </>
  );
}

// Command audit track: structured records of commands executed in sessions (device/user/session/exit code);
// data comes from OSC 133 shell integration observation; plain output and other sources are not included.
export function CommandLogView({ onShowAudit }: { onShowAudit: () => void }) {
  const [bodyRef, compact] = useCompactWidth(640);
  // In server multi-user mode, non-superadmins can only read their own command records (server-enforced), so the user filter entry is not offered.
  const account = useAuth((s) => s.user);
  const userFilterLocked = WEB && account !== null && account.role !== "superadmin";
  const [entries, setEntries] = useState<CommandEntry[]>([]);
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [assetId, setAssetId] = useState("");
  const [userId, setUserId] = useState("");
  const [sessionId, setSessionId] = useState("");
  const [total, setTotal] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [lastPageFull, setLastPageFull] = useState(false);
  const loadSeq = useRef(0);
  const fetchedRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    assetApi
      .list()
      .then((rows) => {
        if (!cancelled) setAssets(rows.filter((a) => a.deletedAt === null));
      })
      .catch(() => {
        if (!cancelled) setAssets(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const queryArgs = useCallback(
    (device: string, user: string, session: string): Record<string, unknown> => ({
      assetId: device || undefined,
      userId: userFilterLocked ? undefined : user || undefined,
      sessionId: session || undefined,
    }),
    [userFilterLocked],
  );

  const loadFirstPage = useCallback(
    async (device: string, user: string, session: string) => {
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
          const count = await assetApi.commandCount(queryArgs(device, user, session));
          if (seq === loadSeq.current) setTotal(count.total);
        } catch {
          if (seq === loadSeq.current) setTotal(null);
        }
      })();
      try {
        const rows = await assetApi.commandQuery({ ...queryArgs(device, user, session), limit: PAGE_SIZE, offset: 0 });
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
    },
    [queryArgs],
  );

  useEffect(() => {
    void loadFirstPage(assetId, userId, sessionId);
  }, [assetId, userId, sessionId, loadFirstPage]);

  const knownTotal = total !== null ? total : lastPageFull ? null : entries.length;
  const hasMore = lastPageFull && (knownTotal === null || entries.length < knownTotal);

  const loadMore = async () => {
    if (loading || loadingMore || !hasMore) return;
    const seq = loadSeq.current;
    const offset = fetchedRef.current;
    setLoadingMore(true);
    setMoreError(null);
    try {
      const rows = await assetApi.commandQuery({ ...queryArgs(assetId, userId, sessionId), limit: PAGE_SIZE, offset });
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

  const assetName = (id: string) => assets?.find((a) => a.id === id)?.name ?? id;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar flex-wrap">
        <IconTerminal size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">命令记录</span>
        <div className="nx-segment">
          <button className="nx-segment-item" onClick={onShowAudit}>
            审计
          </button>
          <button className="nx-segment-item is-active" disabled>
            命令
          </button>
        </div>
        <span className="nx-hint">
          {error && entries.length === 0
            ? "命令记录加载失败"
            : knownTotal !== null
              ? `共 ${knownTotal} 条`
              : `已加载 ${entries.length} 条`}
        </span>
        {error && entries.length > 0 && (
          <span className="nx-hint text-red-300">刷新失败：{error}</span>
        )}
        <div className="nx-spacer" />
        <select
          className="nx-select shrink-0"
          style={{ width: 140 }}
          aria-label="按设备过滤"
          value={assetId}
          onChange={(e) => setAssetId(e.target.value)}
        >
          <option value="">全部设备</option>
          {(assets ?? []).map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        {!userFilterLocked && (
          <input
            className="nx-input shrink-0"
            style={{ width: 120 }}
            aria-label="按用户过滤"
            placeholder="用户 ID"
            value={userId}
            onChange={(e) => setUserId(e.target.value.trim())}
          />
        )}
        <input
          className="nx-input shrink-0"
          style={{ width: 140 }}
          aria-label="按会话过滤"
          placeholder="会话 ID"
          value={sessionId}
          onChange={(e) => setSessionId(e.target.value.trim())}
        />
        <button
          className="nx-btn nx-btn-ghost"
          onClick={() => void loadFirstPage(assetId, userId, sessionId)}
          disabled={loading}
        >
          <IconRefresh size={13} className={loading ? "animate-spin" : ""} />
          刷新
        </button>
      </div>

      <div ref={bodyRef} className="min-h-0 flex-1 overflow-auto">
        {compact ? (
        <ul className="m-0 flex list-none flex-col p-0" aria-label="命令记录" data-testid="command-cards">
          {entries.map((e) => (
            <li key={e.id} className="flex flex-col gap-1 border-b border-neutral-800/60 px-3 py-2.5">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className={`nx-badge ${e.source === "exec" ? "nx-badge-green" : "nx-badge-blue"}`}>
                  {SOURCE_LABELS[e.source] ?? e.source}
                </span>
                <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-neutral-200" title={assetName(e.assetId)}>
                  {assetName(e.assetId)}
                </span>
                {e.exitCode !== null && (
                  <span className={`text-[12px] ${e.exitCode === 0 ? "text-green-300" : "text-red-300"}`} title={e.exitCode === 0 ? "成功" : "失败"}>
                    {e.exitCode === 0 ? "✓ 0" : `✗ ${e.exitCode}`}
                  </span>
                )}
              </div>
              <div className="nx-mono line-clamp-3 break-all text-[11.5px] text-neutral-300" title={e.command}>
                {e.command}
              </div>
              <div className="nx-mono break-all text-[11px] text-neutral-500">
                {new Date(e.finishedAt).toLocaleString()} · {e.sessionId}
                {e.userId ? ` · ${e.userId}` : ""}
              </div>
            </li>
          ))}
          {entries.length === 0 && loading && <li className="nx-hint px-3 py-4 text-center">正在加载…</li>}
          {entries.length === 0 && !loading && error && (
            <li className="flex flex-wrap items-center justify-center gap-2 px-3 py-4 text-center text-[12px]">
              <span className="min-w-0 break-words text-red-300">未能加载命令记录：{error}</span>
              <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void loadFirstPage(assetId, userId, sessionId)} disabled={loading}>
                <IconRefresh size={12} />
                重试
              </button>
            </li>
          )}
          {entries.length === 0 && !loading && !error && (
            <li className="nx-hint px-3 py-4 text-center">暂无记录。仅记录启用了 OSC 133 集成的 shell 中执行的命令。</li>
          )}
        </ul>
        ) : (
        <table className="nx-table nx-table-fixed">
          <thead>
            <tr>
              <th style={{ width: 168 }}>时间</th>
              <th style={{ width: 110 }}>设备</th>
              <th style={{ width: 90 }}>用户</th>
              <th style={{ width: 110 }}>会话</th>
              <th>命令</th>
              <th style={{ width: 72 }}>来源</th>
              <th style={{ width: 84 }} className="nx-right">
                退出码
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e) => (
              <tr key={e.id}>
                <td className="nx-mono whitespace-nowrap">{new Date(e.finishedAt).toLocaleString()}</td>
                <td className="max-w-[110px] truncate" title={assetName(e.assetId)}>
                  {assetName(e.assetId)}
                </td>
                <td className="nx-mono max-w-[90px] truncate" title={e.userId ?? ""}>
                  {e.userId ?? <span className="text-neutral-600">—</span>}
                </td>
                <td className="nx-mono max-w-[110px] truncate" title={e.sessionId}>
                  {e.sessionId}
                </td>
                <td className="nx-mono max-w-[520px] truncate" title={e.command}>
                  {e.command}
                </td>
                <td>
                  <span className={`nx-badge ${e.source === "exec" ? "nx-badge-green" : "nx-badge-blue"}`}>
                    {SOURCE_LABELS[e.source] ?? e.source}
                  </span>
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
              </tr>
            ))}
            {entries.length === 0 && loading ? (
              <CommandSkeletonRows />
            ) : entries.length === 0 && error ? (
              <tr>
                <td colSpan={7} className="nx-table-empty">
                  <span className="text-red-300">未能加载命令记录：{error}</span>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm ml-2"
                    onClick={() => void loadFirstPage(assetId, userId, sessionId)}
                    disabled={loading}
                  >
                    <IconRefresh size={12} />
                    重试
                  </button>
                </td>
              </tr>
            ) : entries.length === 0 ? (
              <tr>
                <td colSpan={7} className="nx-table-empty">
                  暂无记录。仅记录启用了 OSC 133 集成的 shell 中执行的命令。
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
        )}
      </div>

      {entries.length > 0 && (
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/60 bg-neutral-950/40 px-3 py-1.5 text-[11px] text-neutral-500">
          <span className="nx-hint min-w-0 truncate">
            {knownTotal !== null ? `已显示 ${entries.length} / ${knownTotal} 条` : `已显示 ${entries.length} 条`}
          </span>
          <div className="nx-spacer" />
          {moreError ? (
            <>
              <span className="min-w-0 break-words text-red-300">未能加载更多：{moreError}</span>
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
        <span>
          仅覆盖启用 OSC 133 shell 集成的会话（本地注入或远端支持）；命令文本取自执行时的屏幕行，可能包含提示符。纯输出和其他来源将不被记录。
        </span>
      </div>
    </div>
  );
}
