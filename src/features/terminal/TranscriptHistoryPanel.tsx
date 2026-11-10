import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent as ReactMouseEvent } from "react";
import {
  assetApi,
  transcriptApi,
  type TranscriptChunk,
  type TranscriptMatch,
  type TranscriptSummary,
} from "../../ipc/commands";
import { useUi } from "../../app/store";
import { startPolling } from "../../app/usePolling";
import { ask, finishSave, pickSavePath } from "../../ui/dialogs";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import {
  IconAlert,
  IconArrowLeft,
  IconClock,
  IconCopy,
  IconHistory,
  IconRefresh,
  IconSearch,
  IconStar,
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
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { WEB } from "../../ipc/env";
import { useTranscriptFocus } from "./transcriptFocus";
import { t } from "../../i18n";

const POLL_MS = 5000;
const PAGE_BYTES = 512 * 1024;
const TERMINAL_ASSET_KINDS = new Set(["ssh", "local", "docker", "winrm", "telnet"]);

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
  const suffix = host.deleted ? t("（已删除）") : "";
  return `${host.name}${suffix}`;
}

// The browser-mode backend runs elsewhere, so timestamps need the viewer's zone; the desktop backend's local zone is the viewer's.
function viewerTimeZone(): { timeZone?: string; utcOffsetMinutes?: number } {
  if (!WEB) return {};
  let timeZone: string | undefined;
  try {
    timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    timeZone = undefined;
  }
  return { timeZone, utcOffsetMinutes: -new Date().getTimezoneOffset() };
}

function exportFileName(summary: TranscriptSummary, extension: string): string {
  const day = new Date(summary.startedAt).toISOString().slice(0, 10);
  return `${summary.assetName}-${day}.${extension}`.replace(/[\\/:*?"<>|]/g, "_");
}

function badgeClass(summary: TranscriptSummary): string {
  if (summary.active) return "nx-badge nx-badge-green";
  if (summary.endedAt === null) return "nx-badge nx-badge-amber";
  return "nx-badge nx-badge-blue";
}

function badgeText(summary: TranscriptSummary): string {
  if (summary.active) return t("进行中");
  if (summary.endedAt === null) return t("异常结束");
  return t("已结束");
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

export function TranscriptHistoryPanel({
  visible = true,
  assetId: sourceAssetId,
  fixedHost = false,
}: {
  visible?: boolean;
  workspaceId?: string;
  assetId?: string;
  // Scoped to one host (a host workspace tab): no host selector is shown.
  fixedHost?: boolean;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [hosts, setHosts] = useState<HostOption[] | null>(null);
  const [hostsError, setHostsError] = useState<string | null>(null);
  const [assetId, setAssetId] = useState<string | null>(sourceAssetId ?? null);
  const appliedSourceAssetIdRef = useRef(sourceAssetId);
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
  const [exportMenu, setExportMenu] = useState<ContextMenuState | null>(null);
  // Lists every starred transcript of the host instead of the most recent ones.
  const [starredOnly, setStarredOnly] = useState(false);
  const [viewMode, setViewMode] = useState<"text" | "replay">("text");
  const [listWidth, setListWidth] = useState(300);
  const [loadedId, setLoadedId] = useState<string | null>(null);
  // A focus request moves through "select" (wait for the session list) and "load" (wait for the text reader).
  const [pendingFocus, setPendingFocus] = useState<{
    assetId: string;
    transcriptId: string;
    ts: number;
    seq?: number;
    stage: "select" | "load";
  } | null>(null);
  const [focusTs, setFocusTs] = useState<{ transcriptId: string; ts: number } | null>(null);
  const focusRequest = useTranscriptFocus((s) => s.request);
  const focusRetriedRef = useRef(false);
  const readerRef = useRef<HTMLDivElement | null>(null);
  const decoderRef = useRef<TranscriptDecoder | null>(null);
  const hostsGenerationRef = useRef(0);
  const sessionsGenerationRef = useRef(0);
  const readerGenerationRef = useRef(0);
  const searchGenerationRef = useRef(0);

  const loadHosts = useCallback(async () => {
    const generation = ++hostsGenerationRef.current;
    try {
      const [assets, transcriptHosts] = await Promise.all([
        assetApi.list(),
        transcriptApi.hosts(),
      ]);
      if (generation !== hostsGenerationRef.current) return;
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
      if (generation === hostsGenerationRef.current) setHostsError(describeError(error));
    }
  }, []);

  useEffect(() => {
    if (!fixedHost) void loadHosts();
    return () => {
      hostsGenerationRef.current += 1;
      sessionsGenerationRef.current += 1;
      readerGenerationRef.current += 1;
      searchGenerationRef.current += 1;
    };
  }, [loadHosts, fixedHost]);

  useEffect(() => {
    if (sourceAssetId === undefined || sourceAssetId === appliedSourceAssetIdRef.current) return;
    appliedSourceAssetIdRef.current = sourceAssetId;
    sessionsGenerationRef.current += 1;
    readerGenerationRef.current += 1;
    searchGenerationRef.current += 1;
    setAssetId(sourceAssetId);
    setSelectedId(null);
    setSessions(null);
  }, [sourceAssetId]);

  const loadSessions = useCallback(async () => {
    if (!assetId) return;
    const generation = ++sessionsGenerationRef.current;
    try {
      const list = await (starredOnly ? transcriptApi.list(assetId, true) : transcriptApi.list(assetId));
      if (generation !== sessionsGenerationRef.current) return;
      setSessions(Array.isArray(list) ? list : []);
      setSessionsError(null);
    } catch (error) {
      if (generation === sessionsGenerationRef.current) setSessionsError(describeError(error));
    }
  }, [assetId, starredOnly]);

  useEffect(() => {
    if (!assetId) return;
    void loadSessions();
  }, [assetId, loadSessions]);

  // The initial load above already ran; the poll only keeps the list fresh while the panel is shown and the page is visible.
  useEffect(() => {
    if (!visible || !assetId) return;
    return startPolling(() => loadSessions(), { intervalMs: POLL_MS, immediate: false });
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
    async (id: string, afterSeq: number, generation: number) => {
      const result = normalizeRead(await transcriptApi.read(id, afterSeq, PAGE_BYTES));
      if (generation !== readerGenerationRef.current) return null;
      decodeChunks(result.chunks, result.done);
      setLoadedId(id);
      setNextSeq(result.nextSeq);
      setDone(result.done);
      return result;
    },
    [decodeChunks],
  );

  const reloadReader = useCallback(
    async (id: string) => {
      const generation = ++readerGenerationRef.current;
      searchGenerationRef.current += 1;
      decoderRef.current = createTranscriptDecoder();
      setReaderLoading(true);
      setLoadedId(null);
      setLoadingMore(false);
      setReaderError(null);
      setChunks([]);
      setNextSeq(0);
      setDone(false);
      setMatches(null);
      setSearching(false);
      setSearchError(null);
      try {
        await loadReader(id, 0, generation);
      } catch (error) {
        if (generation === readerGenerationRef.current) {
          setReaderError(describeError(error));
        }
      } finally {
        if (generation === readerGenerationRef.current) setReaderLoading(false);
      }
    },
    [loadReader],
  );

  useEffect(() => {
    searchGenerationRef.current += 1;
    setSearching(false);
    if (!selectedId) {
      readerGenerationRef.current += 1;
      decoderRef.current = null;
      setReaderLoading(false);
      setLoadingMore(false);
      setChunks([]);
      setNextSeq(0);
      setDone(true);
      setReaderError(null);
      setMatches(null);
      setSearchError(null);
      setQuery("");
    } else if (selected?.contentOmitted) {
      readerGenerationRef.current += 1;
      decoderRef.current = null;
      setReaderLoading(false);
      setLoadingMore(false);
      setReaderError(null);
      setChunks([]);
      setNextSeq(0);
      setDone(true);
      setMatches(null);
      setSearchError(null);
    } else {
      void reloadReader(selectedId);
    }
    return () => {
      readerGenerationRef.current += 1;
      searchGenerationRef.current += 1;
    };
  }, [selectedId, selected?.contentOmitted, reloadReader]);

  const loadMore = async () => {
    if (!selectedId || loadingMore || done) return;
    const generation = readerGenerationRef.current;
    const id = selectedId;
    setLoadingMore(true);
    try {
      await loadReader(id, nextSeq, generation);
    } catch (error) {
      if (generation === readerGenerationRef.current) {
        setReaderError(describeError(error));
      }
    } finally {
      if (generation === readerGenerationRef.current) setLoadingMore(false);
    }
  };

  const runSearch = async () => {
    if (!selectedId || selected?.contentOmitted) return;
    const id = selectedId;
    const trimmed = query.trim();
    const generation = ++searchGenerationRef.current;
    if (!trimmed) {
      setMatches(null);
      setSearching(false);
      setSearchError(null);
      return;
    }
    setSearching(true);
    setSearchError(null);
    try {
      const found = await transcriptApi.search(id, trimmed);
      if (generation !== searchGenerationRef.current) return;
      setMatches(Array.isArray(found) ? found : []);
    } catch (error) {
      if (generation !== searchGenerationRef.current) return;
      setMatches(null);
      setSearchError(describeError(error));
    } finally {
      if (generation === searchGenerationRef.current) setSearching(false);
    }
  };

  const jumpToMatch = async (match: TranscriptMatch) => {
    if (!selectedId) return;
    const id = selectedId;
    const generation = readerGenerationRef.current;
    try {
      let cursor = nextSeq;
      let reachedDone = done;
      while (cursor <= match.seq && !reachedDone) {
        const result = await loadReader(id, cursor, generation);
        if (!result) return;
        cursor = result.nextSeq;
        reachedDone = result.done;
      }
      window.requestAnimationFrame(() => {
        if (generation !== readerGenerationRef.current) return;
        const target = readerRef.current?.querySelector(`[data-chunk-seq="${match.seq}"]`);
        target?.scrollIntoView({ block: "center" });
      });
    } catch (error) {
      if (generation === readerGenerationRef.current) {
        setReaderError(describeError(error));
      }
    }
  };

  const copyLoaded = async () => {
    try {
      await copyText(chunks.map((chunk) => chunk.text).join(""));
      pushToast("success", done ? t("已复制输出") : t("已复制已加载的输出（尚未加载全部）"));
    } catch (error) {
      pushToast("error", t("复制失败：{value}", { value: describeError(error) }));
    }
  };

  const refresh = () => {
    void loadSessions();
    if (selectedId && !selected?.contentOmitted) void reloadReader(selectedId);
  };

  const removeTranscript = async (summary: TranscriptSummary) => {
    const ok = await ask(
      t("删除「{assetName}」在 {value} 的终端记录？\n\n删除后无法恢复。", { assetName: summary.assetName, value: formatTranscriptTime(summary.startedAt) }),
      { title: t("删除终端记录"), kind: "warning" },
    );
    if (!ok) return;
    try {
      await transcriptApi.remove(summary.id);
      pushToast("success", t("已删除终端记录"));
      if (selectedId === summary.id) setSelectedId(null);
      await loadSessions();
    } catch (error) {
      pushToast("error", t("删除失败：{value}", { value: describeError(error) }));
    }
  };

  const exportCast = async (summary: TranscriptSummary) => {
    if (summary.contentOmitted) return;
    const name = exportFileName(summary, "cast");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      const result = await transcriptApi.exportCast(summary.id, path);
      const where = await finishSave(path, name);
      const estimated = result.timingEstimated ? t("。部分时间戳缺失，已按固定间隔估算") : "";
      pushToast(where ? "success" : "info", where ? t("已导出到 {where}{estimated}", { where, estimated }) : t("已取消保存"));
    } catch (error) {
      pushToast("error", t("导出失败：{value}。请更换保存位置后重试", { value: describeError(error) }));
    }
  };

  const exportText = async (summary: TranscriptSummary, timestamps: boolean) => {
    if (summary.contentOmitted) return;
    const name = exportFileName(summary, "txt");
    const path = await pickSavePath(name);
    if (!path) return;
    try {
      const result = await transcriptApi.exportText(summary.id, path, { timestamps, ...viewerTimeZone() });
      const where = await finishSave(path, name);
      if (!where) {
        pushToast("info", t("已取消保存"));
        return;
      }
      const omitted = result.fullScreenSegments > 0
        ? t("。{count} 段全屏程序（如 vim、top）的画面不在文本中", { count: result.fullScreenSegments })
        : "";
      pushToast("success", t("已导出到 {where}{omitted}", { where, omitted }));
    } catch (error) {
      pushToast("error", t("导出失败：{value}。请更换保存位置后重试", { value: describeError(error) }));
    }
  };

  const openExportTextMenu = (event: ReactMouseEvent<HTMLElement>, summary: TranscriptSummary) => {
    const rect = event.currentTarget.getBoundingClientRect();
    setExportMenu({
      x: rect.left,
      y: rect.bottom,
      items: [
        { kind: "item", label: t("纯文本"), onSelect: () => void exportText(summary, false) },
        { kind: "item", label: t("纯文本，每行带时间戳"), onSelect: () => void exportText(summary, true) },
      ],
    });
  };

  const toggleTranscriptSync = async (summary: TranscriptSummary) => {
    if (summary.syncOptIn) {
      const ok = await ask(
        t("关闭该记录的账号同步？\n\n其他设备上的同步副本将被删除，本机记录保留。"),
        { title: t("关闭账号同步"), kind: "warning" },
      );
      if (!ok) return;
    }
    const optIn = !summary.syncOptIn;
    try {
      await transcriptApi.syncOptIn(summary.id, optIn);
      setSyncToggleError(null);
      pushToast("success", optIn ? t("已开启该记录的同步") : t("已关闭该记录的同步"));
      await loadSessions();
    } catch (error) {
      setSyncToggleError(describeError(error));
    }
  };

  const toggleStar = async (summary: TranscriptSummary) => {
    const starred = !summary.starred;
    try {
      await transcriptApi.star(summary.id, starred);
      await loadSessions();
    } catch (error) {
      pushToast("error", t("无法更新星标：{error}", { error: describeError(error) }));
    }
  };

  const switchAsset = (nextAssetId: string | null) => {
    appliedSourceAssetIdRef.current = nextAssetId ?? undefined;
    sessionsGenerationRef.current += 1;
    readerGenerationRef.current += 1;
    searchGenerationRef.current += 1;
    setAssetId(nextAssetId);
    setSelectedId(null);
    setSessions(null);
    setPendingFocus(null);
    useUi.getState().updateTab("history", { assetId: nextAssetId ?? undefined });
  };

  // Consume a focus request (search hit or command history entry) once this panel is the visible one for its host.
  useEffect(() => {
    if (!focusRequest || !visible) return;
    if (fixedHost && focusRequest.assetId !== sourceAssetId) return;
    const { nonce, assetId: targetAsset, transcriptId, ts, seq } = focusRequest;
    useTranscriptFocus.getState().clear(nonce);
    focusRetriedRef.current = false;
    setPendingFocus({ assetId: targetAsset, transcriptId, ts, seq, stage: "select" });
    setFocusTs(null);
    if (targetAsset !== assetId) {
      appliedSourceAssetIdRef.current = targetAsset;
      sessionsGenerationRef.current += 1;
      readerGenerationRef.current += 1;
      searchGenerationRef.current += 1;
      setAssetId(targetAsset);
      setSelectedId(null);
      setSessions(null);
      if (!fixedHost) useUi.getState().updateTab("history", { assetId: targetAsset });
    }
  }, [focusRequest, visible, fixedHost, sourceAssetId, assetId]);

  useEffect(() => {
    if (!pendingFocus || pendingFocus.stage !== "select") return;
    if (assetId !== pendingFocus.assetId || sessions === null) return;
    const target = sessions.find((summary) => summary.id === pendingFocus.transcriptId);
    if (!target) {
      if (!focusRetriedRef.current) {
        focusRetriedRef.current = true;
        void loadSessions();
        return;
      }
      setPendingFocus(null);
      pushToast("info", t("未找到对应的终端记录，可能已被删除"));
      return;
    }
    setSelectedId(target.id);
    if (pendingFocus.seq === undefined || target.contentOmitted) {
      setViewMode(target.contentOmitted ? "text" : "replay");
      setFocusTs({ transcriptId: target.id, ts: pendingFocus.ts });
      setPendingFocus(null);
      return;
    }
    setViewMode("text");
    setPendingFocus({ ...pendingFocus, stage: "load" });
  }, [pendingFocus, assetId, sessions, loadSessions, pushToast]);

  useEffect(() => {
    if (!pendingFocus || pendingFocus.stage !== "load") return;
    if (readerError) {
      setPendingFocus(null);
      return;
    }
    if (selectedId !== pendingFocus.transcriptId || loadedId !== pendingFocus.transcriptId) return;
    if (viewMode !== "text" || pendingFocus.seq === undefined) return;
    const { seq, ts } = pendingFocus;
    setPendingFocus(null);
    void jumpToMatch({ seq, ts, preview: "" });
    // jumpToMatch reads the current render's reader state, which is what this effect was gated on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pendingFocus, selectedId, loadedId, viewMode, readerError]);

  const hostOptions = hosts ?? [];
  const sessionList = sessions ?? [];

  return (
    <div className="nx-pane" data-testid="transcript-history">
      <div className="nx-toolbar nx-wrap-toolbar">
        <IconHistory size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">{t("终端历史")}</span>
        {!fixedHost && <Select
          className="w-[280px] max-w-full max-[640px]:order-9 max-[640px]:w-auto max-[640px]:basis-full"
          ariaLabel={t("选择主机")}
          value={assetId ?? ""}
          disabled={hostOptions.length === 0}
          placeholder={hostOptions.length === 0 ? t("无可用主机") : t("请选择主机")}
          options={hostOptions.map((host) => ({ value: host.assetId, label: hostLabel(host) }))}
          onChange={(value) => switchAsset(value || null)}
        />}
        <div className="nx-spacer" />
        <button
          className={`nx-btn nx-btn-ghost ${starredOnly ? "text-amber-300" : ""}`}
          aria-pressed={starredOnly}
          disabled={!assetId}
          title={t("只显示加了星标的记录。星标记录不会被自动或手动清理")}
          onClick={() => {
            sessionsGenerationRef.current += 1;
            setSessions(null);
            setStarredOnly((current) => !current);
          }}
        >
          <IconStar size={13} fill={starredOnly ? "currentColor" : "none"} />
          {t("只看星标")}
        </button>
        <button
          className="nx-btn nx-btn-ghost"
          disabled={!assetId}
          onClick={refresh}
        >
          <IconRefresh size={13} />
          {t("刷新")}
        </button>
      </div>

      {hostsError && (
        <div className="flex items-center gap-2 border-b border-red-900/50 bg-red-950/40 px-3 py-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          {t("未能读取主机列表：{hostsError}", { hostsError })}
          <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => void loadHosts()}>
            {t("重试")}
          </button>
        </div>
      )}

      {syncToggleError && (
        <div className="flex items-center gap-2 border-b border-red-900/50 bg-red-950/40 px-3 py-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          {t("切换同步失败：{syncToggleError}", { syncToggleError })}
          <button className="nx-btn nx-btn-ghost nx-btn-xs" onClick={() => setSyncToggleError(null)}>
            {t("关闭")}
          </button>
        </div>
      )}

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <div
          className={`flex min-h-0 flex-col border-b border-neutral-800/60 max-md:max-h-none max-md:flex-1 md:max-h-none md:w-[var(--nx-transcript-list-w)] md:shrink-0 md:border-b-0 md:border-r ${
            selectedId ? "max-h-[38%] max-md:hidden" : "max-h-[38%]"
          }`}
          style={{ "--nx-transcript-list-w": `${listWidth}px` } as CSSProperties}
        >
          <div className="min-h-0 flex-1 overflow-auto">
            <table className="nx-table nx-table-cards">
              <thead>
                <tr>
                  <th>{t("终端记录")}</th>
                  <th style={{ width: 132 }} />
                </tr>
              </thead>
              <tbody>
                {sessionList.map((summary) => (
                  <tr
                    key={summary.id}
                    className={`cursor-pointer pointer-coarse:min-h-11 ${summary.id === selectedId ? "bg-neutral-800/60" : ""}`}
                    onClick={() => {
                      setPendingFocus(null);
                      setSelectedId(summary.id);
                    }}
                  >
                    <td>
                      <div className="text-[12.5px] font-medium text-neutral-200">
                        {formatTranscriptTime(summary.startedAt)}
                      </div>
                      <div className="mt-0.5 text-[11px] text-neutral-500">
                        {formatTranscriptDuration(summary.startedAt, summary.endedAt)} ·{" "}
                        {formatTranscriptBytes(summary.storedBytes || summary.bytes)}
                        {summary.truncated && t(" · 已截断")}
                        {summary.contentOmitted && t(" · 仅保留记录信息，没有输出内容")}
                        {summary.assetDeleted && t(" · 主机已删除")}
                      </div>
                    </td>
                    <td className="nx-right">
                      <div className="flex flex-wrap items-center justify-end gap-1.5 max-[640px]:justify-start">
                        <button
                          className={`nx-btn nx-btn-ghost nx-btn-xs ${summary.starred ? "text-amber-300" : ""}`}
                          aria-pressed={Boolean(summary.starred)}
                          title={summary.starred ? t("取消星标") : t("加星标：不会被自动或手动清理")}
                          aria-label={summary.starred ? t("取消该记录的星标") : t("为该记录加星标")}
                          onClick={(event) => {
                            event.stopPropagation();
                            void toggleStar(summary);
                          }}
                        >
                          <IconStar size={11} fill={summary.starred ? "currentColor" : "none"} />
                        </button>
                        {summary.endedAt !== null ? (
                          <button
                            className={`nx-btn nx-btn-ghost nx-btn-xs ${summary.syncOptIn ? "text-green-400" : ""}`}
                            title={
                              summary.syncOptIn
                                ? t("此记录已开启账号同步。关闭后，其他设备上的同步副本将被删除，本机记录保留")
                                : t("通过账号将此记录同步到其他设备（客户端加密，服务端只保存密文）")
                            }
                            aria-label={summary.syncOptIn ? t("关闭该记录的同步") : t("开启该记录的同步")}
                            onClick={(event) => {
                              event.stopPropagation();
                              void toggleTranscriptSync(summary);
                            }}
                          >
                            {summary.syncOptIn ? t("已开启同步") : t("同步")}
                          </button>
                        ) : (
                          <button
                            className="nx-btn nx-btn-ghost nx-btn-xs"
                            disabled
                            title={
                              summary.active
                                ? t("进行中的记录在结束后才能开启同步")
                                : t("该记录没有结束时间，不能开启同步")
                            }
                            aria-label={t("同步不可用")}
                          >
                            {t("同步")}
                          </button>
                        )}
                        <span className={badgeClass(summary)}>
                          <span className="nx-dot" />
                          {badgeText(summary)}
                        </span>
                        <button
                          className="nx-btn nx-btn-ghost nx-btn-xs"
                          title={t("删除该记录")}
                          aria-label={t("删除该记录")}
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
                      {t("未能读取终端历史：{sessionsError}", { sessionsError })}
                    </td>
                  </tr>
                )}
                {!sessionsError && sessions !== null && sessionList.length === 0 && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      {starredOnly ? t("该主机没有加星标的记录") : t("该主机暂无终端历史")}
                      <div className="mt-1.5 text-[11px] text-neutral-500">
                        {t("连接该主机并产生终端输出后，记录会自动保存在这里。")}
                      </div>
                    </td>
                  </tr>
                )}
                {!sessionsError && sessions === null && assetId && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      {t("正在加载…")}
                    </td>
                  </tr>
                )}
                {!sessionsError && !assetId && hosts !== null && (
                  <tr>
                    <td colSpan={2} className="nx-table-empty">
                      {hostOptions.length === 0 ? t("暂无可查看的主机") : t("请选择主机查看终端历史")}
                      <div className="mt-1.5 text-[11px] text-neutral-500">
                        {hostOptions.length === 0
                          ? t("请先添加主机并连接终端。")
                          : t("请从上方列表选择要查看的主机，以免查看到其他主机的记录。")}
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

        <div
          className={`min-h-0 min-w-0 flex-1 flex-col ${selectedId ? "flex" : "flex max-md:hidden"}`}
          data-testid="transcript-detail"
        >
          <div className="flex items-center gap-2 border-b border-neutral-800/60 px-3 py-1.5 md:hidden">
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-11"
              aria-label={t("返回记录列表")}
              onClick={() => setSelectedId(null)}
            >
              <IconArrowLeft size={13} />
              {t("记录列表")}
            </button>
            {selected && (
              <span className="min-w-0 flex-1 truncate text-right text-[11px] text-neutral-500">
                {formatTranscriptTime(selected.startedAt)}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/60 px-3 py-2">
            {viewMode === "text" ? (
              <>
                <div className="relative min-w-[160px] flex-1">
                  <IconSearch size={12} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-neutral-500" />
                  <input
                    className="nx-input nx-input-sm w-full pl-7 pointer-coarse:min-h-11 pointer-coarse:text-[16px]"
                    placeholder={t("在选中的记录里搜索…")}
                    aria-label={t("搜索终端记录")}
                    enterKeyHint="search"
                    autoCapitalize="off"
                    autoCorrect="off"
                    spellCheck={false}
                    onFocus={(event) => event.currentTarget.scrollIntoView({ block: "nearest" })}
                    value={query}
                    disabled={!selected || selected.contentOmitted}
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
                  className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-11"
                  disabled={!selected || selected.contentOmitted || searching}
                  onClick={() => void runSearch()}
                >
                  {searching ? <IconRefresh size={12} className="animate-spin" /> : <IconSearch size={12} />}
                  {t("搜索")}
                </button>
              </>
            ) : (
              <span className="flex-1 text-[11px] text-neutral-500">
                {t("按录制时间轴回放终端输出，期间不可输入")}
              </span>
            )}
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-11"
              aria-label={t("导出 asciicast")}
              disabled={!selected || selected.contentOmitted}
              title={
                selected?.contentOmitted
                  ? t("该记录只同步了元数据，没有输出内容，无法导出")
                  : t("只导出终端输出，不含键盘输入；可用 asciinema 播放")
              }
              onClick={() => selected && void exportCast(selected)}
            >
              {t("导出 asciicast")}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm pointer-coarse:min-h-11"
              aria-label={t("导出文本")}
              aria-haspopup="menu"
              disabled={!selected || selected.contentOmitted}
              title={
                selected?.contentOmitted
                  ? t("该记录只同步了元数据，没有输出内容，无法导出")
                  : t("按终端里看到的样子导出纯文本：去掉颜色和控制序列，不含键盘输入和全屏程序画面")
              }
              onClick={(event) => selected && openExportTextMenu(event, selected)}
            >
              {t("导出文本")}
            </button>
            <div className="flex overflow-hidden rounded border border-neutral-700/60" role="tablist" aria-label={t("记录视图")}>
              {(["text", "replay"] as const).map((mode) => (
                <button
                  key={mode}
                  role="tab"
                  aria-selected={viewMode === mode}
                  className={`px-2 py-1 text-[11px] pointer-coarse:min-h-11 pointer-coarse:px-4 ${
                    viewMode === mode
                      ? "bg-neutral-700/70 text-neutral-100"
                      : "text-neutral-400 hover:bg-neutral-800/60"
                  }`}
                  onClick={() => {
                    setFocusTs(null);
                    setViewMode(mode);
                  }}
                >
                  {mode === "text" ? t("文本") : t("回放")}
                </button>
              ))}
            </div>
          </div>

          {viewMode === "replay" && selected ? (
            <TranscriptReplayView
              transcriptId={selected.id}
              contentOmitted={selected.contentOmitted}
              initialTs={focusTs?.transcriptId === selected.id ? focusTs.ts : undefined}
            />
          ) : (
            <>

          {(matches !== null || searchError) && (
            <div className="max-h-[140px] shrink-0 overflow-auto overscroll-contain border-b max-[560px]:max-h-[35%] border-neutral-800/60">
              {searchError && (
                <div className="px-3 py-2 text-[12px] text-red-300">{t("搜索失败：{searchError}", { searchError })}</div>
              )}
              {!searchError && matches !== null && matches.length === 0 && (
                <div className="px-3 py-2 text-[12px] text-neutral-500">{t("没有匹配的内容")}</div>
              )}
              {!searchError &&
                matches !== null &&
                matches.map((match, index) => (
                  <button
                    key={`${match.seq}-${index}`}
                    className="block w-full truncate px-3 py-1.5 text-left text-[12px] text-neutral-300 hover:bg-neutral-800/60 pointer-coarse:min-h-11 pointer-coarse:py-2.5 max-[560px]:line-clamp-2 max-[560px]:whitespace-normal max-[560px]:break-all"
                    title={stripAnsi(match.preview)}
                    onClick={() => void jumpToMatch(match)}
                  >
                    <span className="font-mono text-neutral-500">#{match.seq + 1}</span>{" "}
                    {stripAnsi(match.preview).trim()}
                  </button>
                ))}
            </div>
          )}

          <div ref={readerRef} className="min-h-0 flex-1 overflow-auto overscroll-contain px-3 py-2">
            {!selected && !readerLoading && (
              <div className="flex h-full items-center justify-center text-[12px] text-neutral-500">
                {t("选择左侧的终端记录查看输出")}
              </div>
            )}
            {readerLoading && (
              <div className="flex h-full items-center justify-center text-[12px] text-neutral-500">
                {t("正在加载…")}
              </div>
            )}
            {readerError && (
              <div className="flex h-full items-center justify-center gap-2 text-[12px] text-red-300">
                <IconAlert size={13} />
                {t("未能读取终端记录：{readerError}", { readerError })}
              </div>
            )}
            {selected?.contentOmitted && !readerLoading && !readerError && (
              <div className="flex h-full items-center justify-center text-[12px] text-neutral-500">
                {t("仅保留记录信息，没有输出内容")}
              </div>
            )}
            {selected && !selected.contentOmitted && !readerLoading && !readerError && (
              <pre className="font-mono whitespace-pre-wrap break-all text-[12px] leading-relaxed text-neutral-300 max-[560px]:text-[11.5px]">
                {chunks.map((chunk) => (
                  <span key={chunk.seq} data-chunk-seq={chunk.seq}>
                    {chunk.text}
                  </span>
                ))}
                {chunks.length === 0 && done && t("（该记录没有任何输出）")}
              </pre>
            )}
          </div>
            </>
          )}

          <div className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-1 border-t border-neutral-800/60 px-3 py-2 pb-[max(0.5rem,var(--nx-safe-bottom,0px))] text-[11px] text-neutral-500">
            <IconClock size={11} />
            {selected ? (
              <>
                {t("{value} 开始 ·{value2}{value3} · {chunks} 段输出", { value: formatTranscriptTime(selected.startedAt), value2: " ", value3: formatTranscriptBytes(selected.storedBytes || selected.bytes), chunks: selected.chunks })}
                {selected.truncated && t(" · 已达到大小上限，后续输出未记录")}
              </>
            ) : (
              t("终端会话会自动记录（含键盘输入），按会话留存")
            )}
            <div className="nx-spacer" />
            {selected && !selected.contentOmitted && viewMode === "text" && chunks.length > 0 && (
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs pointer-coarse:min-h-11"
                aria-label={t("复制已加载的输出")}
                onClick={() => void copyLoaded()}
              >
                <IconCopy size={11} />
                {t("复制")}
              </button>
            )}
            {selected && !done && viewMode === "text" && (
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                disabled={loadingMore}
                onClick={() => void loadMore()}
              >
                {loadingMore ? <IconRefresh size={11} className="animate-spin" /> : null}
                {t("加载更多")}
              </button>
            )}
          </div>
        </div>
      </div>
      <ContextMenu state={exportMenu} onClose={() => setExportMenu(null)} />
    </div>
  );
}
