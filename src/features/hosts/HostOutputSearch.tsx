import { useRef, useState, type ReactNode } from "react";
import { COMMAND_RANGE_OPTIONS, rangeStart, type CommandRange } from "../../app/jump/commandHistorySearch";
import { transcriptHostSearchApi, type TranscriptHostSearchResult } from "../../ipc/searchApi";
import { describeError } from "../../ui/errorText";
import { IconAlert, IconHistory, IconLoader, IconSearch } from "../../ui/icons";
import { Select } from "../../ui/Select";
import { openHostHistoryTab, openTranscriptFocus } from "../terminal/transcriptFocus";
import { formatTranscriptBytes, formatTranscriptTime, stripAnsi } from "../terminal/transcriptText";
import { HostSection } from "./HostSections";

const MAX_QUERY_BYTES = 256;
const MATCH_CAP = 50;

const encoder = new TextEncoder();

function byteLength(text: string): number {
  return encoder.encode(text).length;
}

export type HostDetailTabId = "overview" | "search";

// HostDetailTabs adds the 概览 | 搜索输出 switch to a host detail page; the overview content is rendered unchanged.
export function HostDetailTabs({ assetId, overview }: { assetId: string; overview: ReactNode }) {
  const [tab, setTab] = useState<HostDetailTabId>("overview");
  const tabs: { id: HostDetailTabId; label: string }[] = [
    { id: "overview", label: "概览" },
    { id: "search", label: "搜索输出" },
  ];
  return (
    <>
      <div className="flex gap-1 border-b border-neutral-800/60" role="tablist" aria-label="主机详情">
        {tabs.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={tab === item.id}
            className={`px-3 py-1.5 text-[12.5px] pointer-coarse:min-h-11 ${
              tab === item.id
                ? "border-b-2 border-blue-400 text-neutral-100"
                : "text-neutral-400 hover:text-neutral-200"
            }`}
            onClick={() => setTab(item.id)}
          >
            {item.label}
          </button>
        ))}
      </div>
      {tab === "overview" ? overview : <HostOutputSearch assetId={assetId} />}
    </>
  );
}

function statusBadge(transcript: { active: boolean; endedAt: number | null }): { className: string; text: string } {
  if (transcript.active) return { className: "nx-badge nx-badge-green", text: "进行中" };
  if (transcript.endedAt === null) return { className: "nx-badge nx-badge-amber", text: "异常结束" };
  return { className: "nx-badge nx-badge-blue", text: "已结束" };
}

export function HostOutputSearch({ assetId }: { assetId: string }) {
  const [query, setQuery] = useState("");
  const [range, setRange] = useState<CommandRange>("all");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<TranscriptHostSearchResult | null>(null);
  const generationRef = useRef(0);

  const runSearch = () => {
    const text = query.trim();
    if (!text) return;
    const generation = ++generationRef.current;
    setLoading(true);
    setError(null);
    void transcriptHostSearchApi
      .search({ assetId, query: text, startedFrom: rangeStart(range), startedTo: null })
      .then((found) => {
        if (generation !== generationRef.current) return;
        setResult(found);
        setLoading(false);
      })
      .catch((cause: unknown) => {
        if (generation !== generationRef.current) return;
        setResult(null);
        setError(describeError(cause));
        setLoading(false);
      });
  };

  return (
    <div className="flex flex-col gap-3" data-testid="host-output-search">
      <div className="flex flex-wrap items-center gap-2 rounded border border-neutral-800/60 px-3 py-2 text-[12px] text-neutral-400">
        <IconHistory size={13} />
        <span className="min-w-0 flex-1">终端记录以明文保存在本机数据库，可在终端历史中逐条删除。</span>
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => openHostHistoryTab(assetId)}>
          打开终端历史
        </button>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[200px] flex-1">
          <IconSearch size={12} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-neutral-500" />
          <input
            className="nx-input nx-input-sm w-full pl-7 pointer-coarse:min-h-11 pointer-coarse:text-[16px]"
            placeholder="搜索这台主机的历史终端输出，回车开始"
            aria-label="搜索终端输出"
            enterKeyHint="search"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            value={query}
            onChange={(event) => {
              // The backend rejects queries over 256 bytes, so the box refuses further input instead.
              if (byteLength(event.target.value) <= MAX_QUERY_BYTES) setQuery(event.target.value);
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                event.preventDefault();
                runSearch();
              }
            }}
          />
        </div>
        <Select
          className="w-[150px] max-w-full"
          ariaLabel="输出搜索时间范围"
          value={range}
          options={COMMAND_RANGE_OPTIONS}
          onChange={(value) => setRange(value as CommandRange)}
        />
        <button
          type="button"
          className="nx-btn nx-btn-primary nx-btn-sm pointer-coarse:min-h-11"
          disabled={loading || !query.trim()}
          onClick={runSearch}
        >
          {loading ? <IconLoader size={12} className="animate-spin" /> : <IconSearch size={12} />}
          搜索
        </button>
      </div>

      {error && (
        <div role="alert" className="flex items-center gap-2 text-[12px] text-red-300">
          <IconAlert size={13} />
          搜索失败：{error}
        </div>
      )}
      {result?.truncated && (
        <div
          role="alert"
          data-testid="output-search-truncated"
          className="flex items-center gap-2 rounded border border-amber-700/60 bg-amber-950/40 px-3 py-2 text-[12.5px] text-amber-300"
        >
          <IconAlert size={14} />
          已扫描 {result.scannedSessions} 个会话 / {formatTranscriptBytes(result.scannedBytes)}，结果已截断，请缩小时间范围
        </div>
      )}
      {loading && !result && <div role="status" className="text-[12px] text-neutral-500">正在搜索…</div>}
      {result && result.sessions.length === 0 && !loading && (
        <div role="status" className="text-[12px] text-neutral-500">没有匹配的输出</div>
      )}
      {result?.sessions.map((session) => {
        const badge = statusBadge(session.transcript);
        return (
          <HostSection
            key={session.transcript.id}
            title={formatTranscriptTime(session.transcript.startedAt)}
            icon={<IconHistory size={14} />}
            testId="output-search-session"
          >
            <div className="mb-1.5 flex flex-wrap items-center gap-2 text-[11.5px] text-neutral-500">
              <span className={badge.className}>{badge.text}</span>
              <span>{session.matches.length} 条匹配</span>
              {session.moreMatches && <span className="text-amber-300">仅显示前 {MATCH_CAP} 条</span>}
            </div>
            <div className="flex flex-col">
              {session.matches.map((match) => (
                <button
                  key={`${match.seq}-${match.ts}`}
                  type="button"
                  data-testid="output-search-match"
                  className="block w-full truncate rounded px-2 py-1.5 text-left text-[12px] text-neutral-300 hover:bg-neutral-800/60 pointer-coarse:min-h-11"
                  title={stripAnsi(match.preview)}
                  onClick={() =>
                    openTranscriptFocus({
                      assetId,
                      transcriptId: session.transcript.id,
                      ts: match.ts,
                      seq: match.seq,
                    })
                  }
                >
                  <span className="font-mono text-neutral-500">#{match.seq + 1}</span> {stripAnsi(match.preview).trim()}
                </button>
              ))}
            </div>
          </HostSection>
        );
      })}
    </div>
  );
}
