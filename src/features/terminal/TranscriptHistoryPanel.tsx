import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  assetApi,
  transcriptApi,
  type Asset,
  type TranscriptChunk,
  type TranscriptMatch,
  type TranscriptReadResult,
  type TranscriptSummary,
} from "../../ipc/commands";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconAlert,
  IconClock,
  IconHistory,
  IconRefresh,
  IconSearch,
  IconTrash,
} from "../../ui/icons";
import {
  chunkToText,
  formatTranscriptBytes,
  formatTranscriptDuration,
  formatTranscriptTime,
  stripAnsi,
} from "./transcriptText";

const POLL_MS = 5000;
const PAGE_BYTES = 512 * 1024;
const TERMINAL_ASSET_KINDS = new Set(["ssh", "local", "docker", "winrm"]);

interface LoadedChunk {
  seq: number;
  ts: number;
  tabId: string;
  text: string;
}

function assetLabel(asset: Asset): string {
  const host = asset.host ? ` (${asset.host})` : "";
  return `${asset.name}${host}`;
}

function badgeClass(summary: TranscriptSummary): string {
  if (summary.active) return "nx-badge nx-badge-green";
  if (summary.endedAt === null) return "nx-badge nx-badge-amber";
  return "nx-badge nx-badge-blue";
}

function badgeText(summary: TranscriptSummary): string {
  if (summary.active) return "进行中";
  if (summary.endedAt === null) return "异常结束";
  return "已结束";
}

function normalizeRead(result: TranscriptReadResult | null): TranscriptReadResult {
  if (!result || !Array.isArray(result.chunks)) {
    return { chunks: [], nextSeq: 0, done: true, totalBytes: 0 };
  }
  return result;
}

export function TranscriptHistoryPanel({ visible = true }: { visible?: boolean }) {
  const pushToast = useUi((s) => s.pushToast);
  const [assets, setAssets] = useState<Asset[] | null>(null);
  const [assetsError, setAssetsError] = useState<string | null>(null);
  const [assetId, setAssetId] = useState<string | null>(null);
  const [sessions, setSessions] = useState<TranscriptSummary[] | null>(null);
  const [sessionsError, setSessionsError] = useState<string | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [chunks, setChunks] = useState<LoadedChunk[]>([]);
  const [nextSeq, setNextSeq] = useState(0);
  const [done, setDone] = useState(true);
  const [readerLoading, setReaderLoading] = useState(false);
  const [readerError, setReaderError] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState<TranscriptMatch[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);
  const readerRef = useRef<HTMLDivElement | null>(null);

  const loadAssets = useCallback(async () => {
    try {
      const list = await assetApi.list();
      setAssets(list.filter((asset) => TERMINAL_ASSET_KINDS.has(asset.kind)));
      setAssetsError(null);
    } catch (error) {
      setAssetsError(describeError(error));
    }
  }, []);

  useEffect(() => {
    void loadAssets();
  }, [loadAssets]);

  useEffect(() => {
    if (assets === null || assetId !== null) return;
    const first = assets[0];
    if (first) setAssetId(first.id);
  }, [assets, assetId]);

  const loadSessions = useCallback(async () => {
    if (!assetId) return;
    try {
      const list = await transcriptApi.list(assetId);
      setSessions(Array.isArray(list) ? list : []);
      setSessionsError(null);
    } catch (error) {
      setSessionsError(describeError(error));
    }
  }, [assetId]);

  useEffect(() => {
    if (!visible || !assetId) return;
    void loadSessions();
    const timer = window.setInterval(() => void loadSessions(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [visible, assetId, loadSessions]);

  const selected = useMemo(
    () => sessions?.find((summary) => summary.id === selectedId) ?? null,
    [sessions, selectedId],
  );

  const appendChunks = useCallback((incoming: TranscriptChunk[]) => {
    setChunks((prev) => {
      const seen = new Set(prev.map((chunk) => chunk.seq));
      const merged = [...prev];
      for (const chunk of incoming) {
        if (seen.has(chunk.seq)) continue;
        seen.add(chunk.seq);
        merged.push({
          seq: chunk.seq,
          ts: chunk.ts,
          tabId: chunk.tabId,
          text: stripAnsi(chunkToText(chunk.dataBase64)),
        });
      }
      merged.sort((a, b) => a.seq - b.seq);
      return merged;
    });
  }, []);

  const loadReader = useCallback(
    async (id: string, afterSeq: number) => {
      const result = normalizeRead(await transcriptApi.read(id, afterSeq, PAGE_BYTES));
      appendChunks(result.chunks);
      setNextSeq(result.nextSeq);
      setDone(result.done);
    },
    [appendChunks],
  );

  useEffect(() => {
    if (!selectedId) {
      setChunks([]);
      setNextSeq(0);
      setDone(true);
      setReaderError(null);
      setMatches(null);
      setSearchError(null);
      setQuery("");
      return;
    }
    let cancelled = false;
    setReaderLoading(true);
    setReaderError(null);
    setChunks([]);
    setNextSeq(0);
    setDone(false);
    setMatches(null);
    setSearchError(null);
    void (async () => {
      try {
        const result = normalizeRead(await transcriptApi.read(selectedId, 0, PAGE_BYTES));
        if (cancelled) return;
        appendChunks(result.chunks);
        setNextSeq(result.nextSeq);
        setDone(result.done);
      } catch (error) {
        if (!cancelled) setReaderError(describeError(error));
      } finally {
        if (!cancelled) setReaderLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [selectedId, appendChunks]);

  const loadMore = async () => {
    if (!selectedId || loadingMore || done) return;
    setLoadingMore(true);
    try {
      await loadReader(selectedId, nextSeq);
    } catch (error) {
      setReaderError(describeError(error));
    } finally {
      setLoadingMore(false);
    }
  };

  const runSearch = async () => {
    if (!selectedId) return;
    const trimmed = query.trim();
    if (!trimmed) {
      setMatches(null);
      setSearchError(null);
      return;
    }
    setSearching(true);
    setSearchError(null);
    try {
      const found = await transcriptApi.search(selectedId, trimmed);
      setMatches(Array.isArray(found) ? found : []);
    } catch (error) {
      setMatches(null);
      setSearchError(describeError(error));
    } finally {
      setSearching(false);
    }
  };

  const jumpToMatch = async (match: TranscriptMatch) => {
    if (!selectedId) return;
    try {
      let cursor = nextSeq;
      let reachedDone = done;
      while (cursor <= match.seq && !reachedDone) {
        const result = await transcriptApi.read(selectedId, cursor, PAGE_BYTES);
        appendChunks(result.chunks);
        cursor = result.nextSeq;
        setNextSeq(result.nextSeq);
        setDone(result.done);
        reachedDone = result.done;
      }
      window.requestAnimationFrame(() => {
        const target = readerRef.current?.querySelector(`[data-chunk-seq="${match.seq}"]`);
        target?.scrollIntoView({ block: "center" });
      });
    } catch (error) {
      setReaderError(describeError(error));
    }
  };

  const removeTranscript = async (summary: TranscriptSummary) => {
    const ok = await ask(
      `删除「${summary.assetName}」在 ${formatTranscriptTime(summary.startedAt)} 的终端记录？\n\n记录内容将从数据库中移除，无法恢复。`,
      { kind: "warning" },
    );
    if (!ok) return;
    try {
      await transcriptApi.remove(summary.id);
      pushToast("success", "已删除终端记录");
      if (selectedId === summary.id) setSelectedId(null);
      await loadSessions();
    } catch (error) {
      pushToast("error", `删除失败：${describeError(error)}`);
    }
  };

  const assetOptions = assets ?? [];
  const sessionList = sessions ?? [];

  return (
    <div className="nx-pane" data-testid="transcript-history">
      <div className="nx-toolbar">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">终端历史</span>
        <select
          className="nx-select max-w-[280px]"
          aria-label="选择主机"
          value={assetId ?? ""}
          disabled={assetOptions.length === 0}
          onChange={(event) => {
            setAssetId(event.target.value || null);
            setSelectedId(null);
            setSessions(null);
          }}
        >
          {assetOptions.length === 0 && <option value="">无可用主机</option>}
          {assetOptions.map((asset) => (
            <option key={asset.id} value={asset.id}>
              {assetLabel(asset)}
            </option>
          ))}
        </select>
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          disabled={!assetId}
          onClick={() => void loadSessions()}
        >
          <IconRefresh size={13} />
          刷新
        </button>
      </div>

      {assetsError && (
        <div className="flex items-center gap-2 border-b border-red-900/50 bg-red-950/40 px-3 py-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          读取主机列表失败:{assetsError}
          <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void loadAssets()}>
            重试
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <div className="flex max-h-[38%] min-h-0 flex-col border-b border-neutral-800/60 md:max-h-none md:w-[300px] md:shrink-0 md:border-b-0 md:border-r">
          <div className="min-h-0 flex-1 overflow-auto">
            <table className="nx-table">
              <thead>
                <tr>
                  <th>会话记录</th>
                  <th style={{ width: 132 }} />
                </tr>
              </thead>
              <tbody>
                {sessionList.map((summary) => (
                  <tr
                    key={summary.id}
                    className={`cursor-pointer ${summary.id === selectedId ? "bg-neutral-800/60" : ""}`}
                    onClick={() => setSelectedId(summary.id)}
                  >
                    <td>
                      <div className="text-[12.5px] font-medium text-neutral-200">
                        {formatTranscriptTime(summary.startedAt)}
                      </div>
                      <div className="mt-0.5 text-[11px] text-neutral-500">
                        {formatTranscriptDuration(summary.startedAt, summary.endedAt)} ·{" "}
                        {formatTranscriptBytes(summary.bytes)}
                        {summary.truncated && " · 已截断"}
                        {summary.assetDeleted && " · 主机已删除"}
                      </div>
                    </td>
                    <td className="nx-right">
                      <div className="flex items-center justify-end gap-1.5">
                        <span className={badgeClass(summary)}>
                          <span className="nx-dot" />
                          {badgeText(summary)}
                        </span>
                        <button
                          className="nx-btn nx-btn-ghost nx-btn-xs"
                          title="删除这条记录"
                          aria-label="删除这条记录"
                          onClick={(event) => {
                            event.stopPropagation();
                            void removeTranscript(summary);
                          }}
                        >
                          <IconTrash size={11} />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {sessionsError && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty text-red-300">
                      读取终端历史失败:{sessionsError}
                    </td>
                  </tr>
                )}
                {!sessionsError && sessions !== null && sessionList.length === 0 && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      该主机还没有终端历史
                      <div className="mt-1.5 text-[11px] text-neutral-500">
                        连接这台主机并产生终端输出后，记录会自动保存在这里。
                      </div>
                    </td>
                  </tr>
                )}
                {!sessionsError && sessions === null && assetId && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      加载中…
                    </td>
                  </tr>
                )}
                {!sessionsError && !assetId && assets !== null && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      还没有可查看的主机
                      <div className="mt-1.5 text-[11px] text-neutral-500">
                        先在资产里添加一台主机并连接终端。
                      </div>
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </div>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
            <div className="relative flex-1">
              <IconSearch size={12} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-neutral-500" />
              <input
                className="nx-input nx-input-sm w-full pl-7"
                placeholder="在选中的记录里搜索…"
                aria-label="搜索终端记录"
                value={query}
                disabled={!selected}
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    void runSearch();
                  }
                }}
              />
            </div>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={!selected || searching}
              onClick={() => void runSearch()}
            >
              {searching ? <IconRefresh size={12} className="animate-spin" /> : <IconSearch size={12} />}
              搜索
            </button>
          </div>

          {(matches !== null || searchError) && (
            <div className="max-h-[140px] shrink-0 overflow-auto border-b border-neutral-800/60">
              {searchError && (
                <div className="px-3 py-2 text-[12px] text-red-300">搜索失败:{searchError}</div>
              )}
              {!searchError && matches !== null && matches.length === 0 && (
                <div className="px-3 py-2 text-[12px] text-neutral-500">没有匹配的内容</div>
              )}
              {!searchError &&
                matches !== null &&
                matches.map((match, index) => (
                  <button
                    key={`${match.seq}-${index}`}
                    className="block w-full truncate px-3 py-1.5 text-left text-[12px] text-neutral-300 hover:bg-neutral-800/60"
                    title={stripAnsi(match.preview)}
                    onClick={() => void jumpToMatch(match)}
                  >
                    <span className="font-mono text-neutral-500">#{match.seq + 1}</span>{" "}
                    {stripAnsi(match.preview).trim()}
                  </button>
                ))}
            </div>
          )}

          <div ref={readerRef} className="min-h-0 flex-1 overflow-auto px-3 py-2">
            {!selected && !readerLoading && (
              <div className="flex h-full items-center justify-center text-[12px] text-neutral-500">
                选择左侧的一条会话记录查看输出
              </div>
            )}
            {readerLoading && (
              <div className="flex h-full items-center justify-center text-[12px] text-neutral-500">
                加载中…
              </div>
            )}
            {readerError && (
              <div className="flex h-full items-center justify-center gap-2 text-[12px] text-red-300">
                <IconAlert size={13} />
                读取终端记录失败:{readerError}
              </div>
            )}
            {selected && !readerLoading && !readerError && (
              <pre className="font-mono whitespace-pre-wrap break-all text-[12px] leading-relaxed text-neutral-300">
                {chunks.map((chunk) => (
                  <span key={chunk.seq} data-chunk-seq={chunk.seq}>
                    {chunk.text}
                  </span>
                ))}
                {chunks.length === 0 && done && "（这条记录没有任何输出）"}
              </pre>
            )}
          </div>

          <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 px-3 py-2 text-[11px] text-neutral-500">
            <IconClock size={11} />
            {selected ? (
              <>
                {formatTranscriptTime(selected.startedAt)} 开始 ·{" "}
                {formatTranscriptBytes(selected.bytes)} · {selected.chunks} 个分块
                {selected.truncated && " · 已达到大小上限，后续输出未记录"}
              </>
            ) : (
              "终端输出会自动记录，按会话留存"
            )}
            <div className="nx-spacer" />
            {selected && !done && (
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                disabled={loadingMore}
                onClick={() => void loadMore()}
              >
                {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
                加载更多
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
