import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type RefObject } from "react";
import { useUi } from "../../../app/store";
import type { AiConversationFilter, AiConversationStatus, AiConversationSummaryDto } from "../../../ipc/types";
import { Select } from "../../../ui/Select";
import { IconDownload, IconRefresh, IconSearch } from "../../../ui/icons";
import { ResizeHandle } from "../../../ui/ResizeHandle";
import { usePaneWidth } from "../../../ui/usePaneWidth";
import { useHostLabeler } from "../../hosts/hostLabel";
import { ConversationArchiveDialog } from "./ConversationArchiveDialog";
import { ConversationDetails } from "./ConversationDetails";
import { ConversationList } from "./ConversationList";
import { ConversationPane } from "./ConversationPane";
import { useConversationPages } from "./useConversationPages";
import { useSelectedConversation } from "./useSelectedConversation";
import { RANGE_PRESETS, WORKBENCH_STATUSES, conversationStatusLabel, presetDates, rangeBounds, type RangePreset } from "./workbenchShared";
import { t } from "../../../i18n";

export { HostAiHistoryPanel } from "./HostAiHistoryPanel";

// The host filter value for conversations whose scope names no host.
export const UNLINKED_HOST = "__unlinked__";

// Below this width the three columns stack.
const STACK_WIDTH = 900;
const WORKBENCH_LIST_WIDTH = { min: 220, max: 560, default: 290 };

const SEARCH_DEBOUNCE_MS = 300;

function useNarrow<T extends HTMLElement>(): [RefObject<T | null>, boolean] {
  const ref = useRef<T>(null);
  const [narrow, setNarrow] = useState(false);
  useEffect(() => {
    const element = ref.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? element.clientWidth;
      if (width > 0) setNarrow(width < STACK_WIDTH);
    });
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  return [ref, narrow];
}

export function useWorkbenchHostLabel(): (assetId: string) => string {
  const label = useHostLabeler();
  return useCallback((assetId: string) => (assetId ? label(assetId) || assetId : t("未关联主机")), [label]);
}

// AiWorkbenchView is the global AI workbench in the tools workspace: every conversation, filtered by host, status,
// time and text, with the selected one shown read-only next to its run details. Cron results, memory and usage
// would join it as further sections; this version has the conversations only.
export function AiWorkbenchView({ visible, assetId }: { visible: boolean; assetId?: string }) {
  const [rootRef, narrow] = useNarrow<HTMLDivElement>();
  const hostLabel = useWorkbenchHostLabel();
  const [host, setHost] = useState<string>(assetId ?? "");
  const [status, setStatus] = useState<AiConversationStatus | "">("");
  const [preset, setPreset] = useState<RangePreset>("all");
  const [dates, setDates] = useState({ from: "", to: "" });
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [archiveOpen, setArchiveOpen] = useState(false);
  const [listWidth, setListWidth] = usePaneWidth("ai-workbench-list", WORKBENCH_LIST_WIDTH);

  // "去到全局" re-opens this tab with a host; the tab's assetId follows the picker, so a new value is a new request.
  useEffect(() => {
    if (assetId) setHost(assetId);
  }, [assetId]);

  useEffect(() => {
    const timer = setTimeout(() => setQuery(queryInput.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [queryInput]);

  const chooseHost = (value: string) => {
    setHost(value);
    const realHost = value && value !== UNLINKED_HOST ? value : undefined;
    useUi.getState().updateTab("aiWorkbench", { assetId: realHost });
  };

  const bounds = rangeBounds(dates.from, dates.to);
  const filter = useMemo<AiConversationFilter | null>(() => {
    if (!bounds.valid) return null;
    return {
      ...(host === UNLINKED_HOST ? { unlinked: true } : host ? { assetId: host } : {}),
      ...(status ? { status } : {}),
      ...(query ? { query } : {}),
      ...(bounds.updatedFrom ? { updatedFrom: bounds.updatedFrom } : {}),
      ...(bounds.updatedBefore ? { updatedBefore: bounds.updatedBefore } : {}),
    };
  }, [host, status, query, bounds.valid, bounds.updatedFrom, bounds.updatedBefore]);
  const pages = useConversationPages(filter, visible);
  const selection = useSelectedConversation(pages, visible);

  const hostOptions = useMemo(() => {
    const counts = new Map(pages.hosts.map((entry) => [entry.assetId, entry.count]));
    const ids = pages.hosts.map((entry) => entry.assetId).filter((id) => id !== "");
    if (host && host !== UNLINKED_HOST && !ids.includes(host)) ids.unshift(host);
    return [
      { value: "", label: t("全部主机") },
      ...ids.map((id) => ({ value: id, label: counts.has(id) ? `${hostLabel(id)} (${counts.get(id)})` : hostLabel(id) })),
      { value: UNLINKED_HOST, label: counts.has("") ? `${t("未关联主机")} (${counts.get("")})` : t("未关联主机") },
    ];
  }, [pages.hosts, host, hostLabel]);

  const hostNames = useMemo(() => {
    const names: Record<string, string> = {};
    for (const entry of pages.hosts) if (entry.assetId) names[entry.assetId] = hostLabel(entry.assetId);
    return names;
  }, [pages.hosts, hostLabel]);

  const choosePreset = (value: RangePreset) => {
    setPreset(value);
    if (value !== "custom") setDates(presetDates(value, new Date()));
  };

  const listColumn = (
    <aside
      className={`flex min-h-0 flex-col border-neutral-800/60 ${narrow ? "max-h-[45vh] border-b" : "w-[var(--nx-ai-workbench-list-w)] shrink-0 border-r"}`}
      style={narrow ? undefined : ({ "--nx-ai-workbench-list-w": `${listWidth}px` } as CSSProperties)}
      aria-label={t("会话筛选和列表")}
    >
      <div className="flex shrink-0 flex-col gap-1.5 border-b border-neutral-800/60 p-2">
        <div className="flex items-center gap-1">
          <IconSearch size={12} className="shrink-0 text-neutral-500" />
          <input
            className="nx-input nx-input-sm min-w-0 flex-1"
            placeholder={t("搜索标题或对话内容")}
            aria-label={t("搜索 AI 会话")}
            value={queryInput}
            onChange={(event) => setQueryInput(event.target.value)}
          />
        </div>
        <Select ariaLabel={t("按主机筛选")} value={host} options={hostOptions} onChange={chooseHost} />
        <div className="flex gap-1.5">
          <div className="min-w-0 flex-1">
            <Select
              ariaLabel={t("按状态筛选")}
              value={status}
              options={[{ value: "", label: t("全部状态") }, ...WORKBENCH_STATUSES.map((value) => ({ value, label: conversationStatusLabel(value) }))]}
              onChange={(value) => setStatus(value as AiConversationStatus | "")}
            />
          </div>
          <div className="min-w-0 flex-1">
            <Select
              ariaLabel={t("按时间筛选")}
              value={preset}
              options={[...RANGE_PRESETS.map((option) => ({ value: option.value, label: option.label() })), { value: "custom", label: t("自定义") }]}
              onChange={(value) => choosePreset(value as RangePreset)}
            />
          </div>
        </div>
        {preset === "custom" ? (
          <div className="flex items-center gap-1">
            <input
              type="date"
              className="nx-input nx-input-sm min-w-0 flex-1"
              aria-label={t("开始日期")}
              value={dates.from}
              onChange={(event) => setDates((current) => ({ ...current, from: event.target.value }))}
            />
            <span className="nx-hint">-</span>
            <input
              type="date"
              className="nx-input nx-input-sm min-w-0 flex-1"
              aria-label={t("结束日期")}
              value={dates.to}
              onChange={(event) => setDates((current) => ({ ...current, to: event.target.value }))}
            />
          </div>
        ) : null}
        {!bounds.valid ? <div className="text-[11.5px] text-red-300" role="alert">{t("结束日期不能早于开始日期")}</div> : null}
        <div className="flex items-center gap-1">
          <span className="nx-hint min-w-0 flex-1 truncate" aria-live="polite">{t("共 {count} 个会话", { count: pages.total })}</span>
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" title={t("刷新")} aria-label={t("刷新会话列表")} onClick={pages.reload}>
            <IconRefresh size={12} />
          </button>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-xs" title={t("按当前筛选批量导出会话")} onClick={() => setArchiveOpen(true)}>
            <IconDownload size={12} />
            {t("批量导出")}
          </button>
        </div>
      </div>
      <ConversationList
        pages={pages}
        selectedId={selection.summary?.id ?? null}
        onSelect={(item: AiConversationSummaryDto) => selection.select(item)}
        hostLabel={(item) => hostLabel(item.assetId)}
        emptyText={filter && Object.keys(filter).length > 0 ? t("没有符合筛选条件的会话") : t("尚无 AI 会话")}
      />
    </aside>
  );

  const selectedHost = selection.summary ? hostLabel(selection.summary.assetId) : "";
  return (
    <div
      ref={rootRef}
      className={`flex h-full min-h-0 ${narrow ? "flex-col overflow-y-auto" : "flex-row"}`}
      data-testid="ai-workbench"
      data-layout={narrow ? "stacked" : "columns"}
    >
      {listColumn}
      {narrow ? null : (
        <ResizeHandle
          side="left"
          width={listWidth}
          min={WORKBENCH_LIST_WIDTH.min}
          max={WORKBENCH_LIST_WIDTH.max}
          defaultWidth={WORKBENCH_LIST_WIDTH.default}
          onChange={setListWidth}
          label={t("调整会话列表宽度")}
        />
      )}
      <div className={`flex min-w-0 flex-col ${narrow ? "min-h-[60vh]" : "min-h-0 flex-1"}`}>
        <ConversationPane summary={selection.summary} hostLabel={selectedHost} record={selection.record} onDeleted={selection.deleted} />
      </div>
      {selection.summary ? (
        <aside
          className={`border-neutral-800/60 ${narrow ? "border-t" : "w-[290px] shrink-0 overflow-y-auto border-l"}`}
          aria-label={t("会话详情")}
        >
          <ConversationDetails summary={selection.summary} hostLabel={selectedHost} items={selection.record.items} />
        </aside>
      ) : null}
      {archiveOpen ? (
        <ConversationArchiveDialog
          assetId={host && host !== UNLINKED_HOST ? host : null}
          hostName={host && host !== UNLINKED_HOST ? hostLabel(host) : ""}
          allowAllHosts
          defaults={{
            unlinked: host === UNLINKED_HOST,
            ...(status ? { status } : {}),
            query,
            preset,
            dates,
          }}
          hostNames={hostNames}
          onClose={() => setArchiveOpen(false)}
        />
      ) : null}
    </div>
  );
}
