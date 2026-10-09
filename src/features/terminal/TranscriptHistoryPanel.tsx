import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import {
  assetApi,
  transcriptApi,
  type TranscriptChunk,
  type TranscriptMatch,
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
  createTranscriptDecoder,
  formatTranscriptBytes,
  formatTranscriptDuration,
  formatTranscriptTime,
  stripAnsi,
  type TranscriptDecoder,
} from "./transcriptText";
import { TranscriptReplayView } from "./TranscriptReplayView";
import { ResizeHandle } from "../../ui/ResizeHandle";

const POLL_MS = 5000;
const PAGE_BYTES = 512 * 1024;
const TERMINAL_ASSET_KINDS = new Set(["ssh", "local", "docker", "winrm"]);

interface LoadedChunk {
  seq: number;
  ts: number;
  tabId: string;
  text: string;
}

interface HostOption {
  assetId: string;
  name: string;
  kind: string;
  deleted: boolean;
}

function hostLabel(host: HostOption): string {
  const suffix = host.deleted ? "（已删除）" : "";
  return `${host.name}${suffix}`;
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

function normalizeRead(result: {
  chunks: TranscriptChunk[];
  nextSeq: number;
  done: boolean;
  totalBytes: number;
} | null): { chunks: TranscriptChunk[]; nextSeq: number; done: boolean; totalBytes: number } {
  if (!result || !Array.isArray(result.chunks)) {
    return { chunks: [], nextSeq: 0, done: true, totalBytes: 0 };
  }
  return result;
}

export function TranscriptHistoryPanel({ visible = true }: { visible?: boolean }) {
  const pushToast = useUi((s) => s.pushToast);
  const [hosts, setHosts] = useState<HostOption[] | null>(null);
  const [hostsError, setHostsError] = useState<string | null>(null);
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
  const [syncToggleError, setSyncToggleError] = useState<string | null>(null);
  const [viewMode, setViewMode] = useState<"text" | "replay">("text");
  const [listWidth, setListWidth] = useState(300);
  const readerRef = useRef<HTMLDivElement | null>(null);
  const decoderRef = useRef<TranscriptDecoder | null>(null);

  const loadHosts = useCallback(async () => {
    try {
      const [assets, transcriptHosts] = await Promise.all([
        assetApi.list(),
        transcriptApi.hosts(),
      ]);
      const merged: HostOption[] = [];
      const seen = new Set<string>();
      for (const asset of assets.filter((candidate) => TERMINAL_ASSET_KINDS.has(candidate.kind))) {
        merged.push({ assetId: asset.id, name: asset.name, kind: asset.kind, deleted: false });
        seen.add(asset.id);
      }
      for (const host of Array.isArray(transcriptHosts) ? transcriptHosts : []) {
        if (seen.has(host.assetId)) continue;
        merged.push({
          assetId: host.assetId,
          name: host.assetName || host.assetId,
          kind: host.assetKind,
          deleted: host.assetDeleted,
        });
        seen.add(host.assetId);
      }
      setHosts(merged);
      setHostsError(null);
    } catch (error) {
      setHostsError(describeError(error));
    }
  }, []);

  useEffect(() => {
    void loadHosts();
  }, [loadHosts]);

  useEffect(() => {
    if (hosts === null || assetId !== null) return;
    const first = hosts[0];
    if (first) setAssetId(first.assetId);
  }, [hosts, assetId]);

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
    if (!assetId) return;
    void loadSessions();
  }, [assetId, loadSessions]);

  useEffect(() => {
    if (!visible || !assetId) return;
    const timer = window.setInterval(() => void loadSessions(), POLL_MS);
    return () => window.clearInterval(timer);
  }, [visible, assetId, loadSessions]);

  const selected = useMemo(
    () => sessions?.find((summary) => summary.id === selectedId) ?? null,
    [sessions, selectedId],
  );

  const decodeChunks = useCallback((incoming: TranscriptChunk[], reachedDone: boolean) => {
    const decoder = decoderRef.current;
    if (!decoder) return;
    const sorted = incoming.filter((chunk) => (chunk.kind ?? 0) === 0).sort((a, b) => a.seq - b.seq);
    const decoded = sorted.map((chunk) => ({
      seq: chunk.seq,
      ts: chunk.ts,
      tabId: chunk.tabId,
      text: decoder.push(chunk.dataBase64),
    }));
    const rest = reachedDone ? decoder.flush() : "";
    setChunks((prev) => {
      const seen = new Set(prev.map((chunk) => chunk.seq));
      const merged = [...prev];
      for (const chunk of decoded) {
        if (seen.has(chunk.seq)) continue;
        seen.add(chunk.seq);
        merged.push(chunk);
      }
      merged.sort((a, b) => a.seq - b.seq);
      if (rest && merged.length > 0) {
        const last = merged[merged.length - 1];
        merged[merged.length - 1] = { ...last, text: last.text + rest };
      }
      return merged;
    });
  }, []);

  const loadReader = useCallback(
    async (id: string, afterSeq: number) => {
      const result = normalizeRead(await transcriptApi.read(id, afterSeq, PAGE_BYTES));
      decodeChunks(result.chunks, result.done);
      setNextSeq(result.nextSeq);
      setDone(result.done);
    },
    [decodeChunks],
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
      decoderRef.current = null;
      return;
    }
    let cancelled = false;
    decoderRef.current = createTranscriptDecoder();
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
        decodeChunks(result.chunks, result.done);
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
  }, [selectedId, decodeChunks]);

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
        const result = normalizeRead(await transcriptApi.read(selectedId, cursor, PAGE_BYTES));
        decodeChunks(result.chunks, result.done);
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
      { title: "删除终端记录", kind: "warning" },
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

  const toggleTranscriptSync = async (summary: TranscriptSummary) => {
    const optIn = !summary.syncOptIn;
    try {
      await transcriptApi.syncOptIn(summary.id, optIn);
      setSyncToggleError(null);
      pushToast("success", optIn ? "已开启这条记录的同步" : "已关闭这条记录的同步");
      await loadSessions();
    } catch (error) {
      setSyncToggleError(describeError(error));
    }
  };

  const hostOptions = hosts ?? [];
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
          disabled={hostOptions.length === 0}
          onChange={(event) => {
            setAssetId(event.target.value || null);
            setSelectedId(null);
            setSessions(null);
          }}
        >
          {hostOptions.length === 0 && <option value="">无可用主机</option>}
          {hostOptions.map((host) => (
            <option key={host.assetId} value={host.assetId}>
              {hostLabel(host)}
            </option>
          ))}
        </select>
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-ghost"
          disabled={!assetId}
          onClick={() => void loadSessions()}
        >
          <IconRefresh size={13} />
          刷新
        </button>
      </div>

      {hostsError && (
        <div className="flex items-center gap-2 border-b border-red-900/50 bg-red-950/40 px-3 py-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          读取主机列表失败：{hostsError}
          <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void loadHosts()}>
            重试
          </button>
        </div>
      )}

      {syncToggleError && (
        <div className="flex items-center gap-2 border-b border-red-900/50 bg-red-950/40 px-3 py-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          切换同步失败：{syncToggleError}
          <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => setSyncToggleError(null)}>
            关闭
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <div
          className="flex max-h-[38%] min-h-0 flex-col border-b border-neutral-800/60 md:max-h-none md:w-[var(--nx-transcript-list-w)] md:shrink-0 md:border-b-0 md:border-r"
          style={{ "--nx-transcript-list-w": `${listWidth}px` } as CSSProperties}
        >
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
                        {summary.endedAt !== null ? (
                          <button
                            className={`nx-btn nx-btn-ghost nx-btn-xs ${summary.syncOptIn ? "text-green-400" : ""}`}
                            title={
                              summary.syncOptIn
                                ? "这条记录已开启同步（经账号同步到你的其他设备），点击关闭"
                                : "经账号把这条记录同步到你的其他设备（端到端加密）"
                            }
                            aria-label={summary.syncOptIn ? "关闭这条记录的同步" : "开启这条记录的同步"}
                            onClick={(event) => {
                              event.stopPropagation();
                              void toggleTranscriptSync(summary);
                            }}
                          >
                            {summary.syncOptIn ? "已同步" : "同步"}
                          </button>
                        ) : (
                          <button
                            className="nx-btn nx-btn-ghost nx-btn-xs"
                            disabled
                            title={
                              summary.active
                                ? "进行中的记录在结束后才能开启同步"
                                : "这条记录没有结束时间，不能开启同步"
                            }
                            aria-label="同步不可用"
                          >
                            同步
                          </button>
                        )}
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
                      读取终端历史失败：{sessionsError}
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
                {!sessionsError && !assetId && hosts !== null && (
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

        <div className="hidden md:block">
          <ResizeHandle
            side="left"
            width={listWidth}
            min={200}
            max={520}
            defaultWidth={300}
            onChange={setListWidth}
          />
        </div>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <div className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
            {viewMode === "text" ? (
              <>
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
              </>
            ) : (
              <span className="flex-1 text-[11px] text-neutral-500">
                回放模式：按录制时间轴重放终端输出，输入不会在回放中显示
              </span>
            )}
            <div className="flex overflow-hidden rounded border border-neutral-700/60" role="tablist" aria-label="记录视图">
              {(["text", "replay"] as const).map((mode) => (
                <button
                  key={mode}
                  role="tab"
                  aria-selected={viewMode === mode}
                  className={`px-2 py-1 text-[11px] ${
                    viewMode === mode
                      ? "bg-neutral-700/70 text-neutral-100"
                      : "text-neutral-400 hover:bg-neutral-800/60"
                  }`}
                  onClick={() => setViewMode(mode)}
                >
                  {mode === "text" ? "文本" : "回放"}
                </button>
              ))}
            </div>
          </div>

          {viewMode === "replay" && selected ? (
            <TranscriptReplayView transcriptId={selected.id} />
          ) : (
            <>

          {(matches !== null || searchError) && (
            <div className="max-h-[140px] shrink-0 overflow-auto border-b border-neutral-800/60">
              {searchError && (
                <div className="px-3 py-2 text-[12px] text-red-300">搜索失败：{searchError}</div>
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
                读取终端记录失败：{readerError}
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
            </>
          )}

          <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 px-3 py-2 text-[11px] text-neutral-500">
            <IconClock size={11} />
            {selected ? (
              <>
                {formatTranscriptTime(selected.startedAt)} 开始 ·{" "}
                {formatTranscriptBytes(selected.bytes)} · {selected.chunks} 个分块
                {selected.truncated && " · 已达到大小上限，后续输出未记录"}
              </>
            ) : (
              "终端会话会自动记录（含键盘输入），按会话留存"
            )}
            <div className="nx-spacer" />
            {selected && !done && viewMode === "text" && (
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
