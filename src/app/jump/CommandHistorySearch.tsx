import { useCallback, useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type MutableRefObject } from "react";
import type { Asset } from "../../ipc/commands";
import { terminalApi } from "../../ipc/commands";
import { commandSearchApi, type CommandSearchItem } from "../../ipc/searchApi";
import { insertRisk, terminalTarget } from "../../features/explorer/snippetInsert";
import { openTranscriptFocus } from "../../features/terminal/transcriptFocus";
import { formatTranscriptTime } from "../../features/terminal/transcriptText";
import { isEditableTarget } from "../../ui/DialogHost";
import { copyText } from "../../ui/clipboard";
import { describeError } from "../../ui/errorText";
import { IconLoader } from "../../ui/icons";
import { Select } from "../../ui/Select";
import { useUi } from "../store";
import {
  COMMAND_RANGE_OPTIONS,
  COMMAND_SEARCH_DEBOUNCE_MS,
  COMMAND_SEARCH_PAGE,
  hostLabelFor,
  rangeStart,
  type CommandRange,
} from "./commandHistorySearch";
import { t } from "../../i18n";

const TERMINAL_ASSET_KINDS = new Set(["ssh", "local", "docker", "winrm", "telnet"]);

export type CommandHistoryKeyHandler = (event: ReactKeyboardEvent<HTMLElement>) => void;

// CommandHistorySearch is the body of the jump palette in command history mode (input starts with ">").
// The palette keeps the input and forwards keys through keyRef.
export function CommandHistorySearch({
  text,
  assets,
  listId,
  helpId,
  keyRef,
  onActiveChange,
  onClose,
}: {
  text: string;
  assets: Asset[];
  listId: string;
  helpId: string;
  keyRef: MutableRefObject<CommandHistoryKeyHandler | null>;
  onActiveChange: (optionId: string | undefined) => void;
  onClose: () => void;
}) {
  const [hostId, setHostId] = useState("");
  const [range, setRange] = useState<CommandRange>("all");
  const [items, setItems] = useState<CommandSearchItem[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [cursor, setCursor] = useState(0);
  const generationRef = useRef(0);
  const listRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const generation = ++generationRef.current;
    setLoading(true);
    // A page request from the previous query is ignored by the generation guard, so its finally never clears this.
    setLoadingMore(false);
    setError(null);
    const timer = window.setTimeout(() => {
      void commandSearchApi
        .search({
          text,
          assetId: hostId || null,
          startedFrom: rangeStart(range),
          startedTo: null,
          limit: COMMAND_SEARCH_PAGE,
          offset: 0,
        })
        .then((result) => {
          if (generation !== generationRef.current) return;
          setItems(result.items);
          setHasMore(result.hasMore);
          setCursor(0);
          setLoading(false);
        })
        .catch((cause: unknown) => {
          if (generation !== generationRef.current) return;
          setItems([]);
          setHasMore(false);
          setError(describeError(cause));
          setLoading(false);
        });
    }, COMMAND_SEARCH_DEBOUNCE_MS);
    return () => {
      window.clearTimeout(timer);
      generationRef.current += 1;
    };
  }, [text, hostId, range]);

  const loadMore = useCallback(() => {
    if (loading || loadingMore || !hasMore) return;
    const generation = generationRef.current;
    setLoadingMore(true);
    void commandSearchApi
      .search({
        text,
        assetId: hostId || null,
        startedFrom: rangeStart(range),
        startedTo: null,
        limit: COMMAND_SEARCH_PAGE,
        offset: items.length,
      })
      .then((result) => {
        if (generation !== generationRef.current) return;
        setItems((prev) => {
          const seen = new Set(prev.map((entry) => entry.id));
          return [...prev, ...result.items.filter((entry) => !seen.has(entry.id))];
        });
        setHasMore(result.hasMore);
      })
      .catch((cause: unknown) => {
        if (generation === generationRef.current) setError(describeError(cause));
      })
      .finally(() => {
        if (generation === generationRef.current) setLoadingMore(false);
      });
  }, [loading, loadingMore, hasMore, text, hostId, range, items.length]);

  const openReplay = (entry: CommandSearchItem) => {
    if (!entry.transcriptId) {
      useUi.getState().pushToast("info", t("该命令没有对应的终端记录"));
      return;
    }
    openTranscriptFocus({ assetId: entry.assetId, transcriptId: entry.transcriptId, ts: entry.startedAt });
    onClose();
  };

  const fillTerminal = (entry: CommandSearchItem) => {
    const pushToast = useUi.getState().pushToast;
    const target = terminalTarget();
    if ("blocked" in target) {
      pushToast("info", t("{blocked}，才能填入命令", { blocked: target.blocked }));
      return;
    }
    if (insertRisk(entry.command) !== "none") {
      pushToast("info", t("命令包含换行或控制字符，未填入终端"), {
        label: t("复制命令"),
        onSelect: () => {
          void copyText(entry.command).catch((cause: unknown) => {
            useUi.getState().pushToast("error", t("复制失败：{value}", { value: describeError(cause) }));
          });
        },
      });
      return;
    }
    void terminalApi.write(target.tabId, new TextEncoder().encode(entry.command)).catch((cause: unknown) => {
      useUi.getState().pushToast("error", t("填入终端失败：{value}", { value: describeError(cause) }));
    });
    onClose();
  };

  const activeIndex = items.length === 0 ? -1 : Math.min(cursor, items.length - 1);
  const optionId = (index: number) => `${listId}-option-${index}`;

  useEffect(() => {
    onActiveChange(activeIndex >= 0 ? optionId(activeIndex) : undefined);
    const node = listRef.current?.querySelector<HTMLElement>(`[data-row-index="${activeIndex}"]`);
    node?.scrollIntoView?.({ block: "nearest" });
    // optionId only depends on listId.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeIndex, items, onActiveChange]);

  useEffect(() => () => onActiveChange(undefined), [onActiveChange]);

  keyRef.current = (event) => {
    const move = (next: number) => {
      if (items.length > 0) setCursor((next + items.length) % items.length);
    };
    if (event.key === "ArrowDown") {
      event.preventDefault();
      move(activeIndex + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      move(activeIndex - 1);
    } else if (event.key === "Enter" && !event.repeat && isEditableTarget(event.target) && items[activeIndex]) {
      event.preventDefault();
      if (event.shiftKey) fillTerminal(items[activeIndex]);
      else openReplay(items[activeIndex]);
    }
  };

  const hostOptions = [
    { value: "", label: t("全部主机") },
    ...assets
      .filter((asset) => TERMINAL_ASSET_KINDS.has(asset.kind))
      .map((asset) => ({ value: asset.id, label: asset.name })),
  ];

  return (
    <>
      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
        <Select
          className="w-[180px] max-w-full"
          ariaLabel={t("命令历史主机")}
          value={hostId}
          options={hostOptions}
          onChange={setHostId}
        />
        <Select
          className="w-[150px] max-w-full"
          ariaLabel={t("命令历史时间范围")}
          value={range}
          options={COMMAND_RANGE_OPTIONS}
          onChange={(value) => setRange(value as CommandRange)}
        />
      </div>
      <div ref={listRef} id={listId} className="max-h-[340px] overflow-y-auto p-1.5" role="listbox" aria-label={t("命令历史结果")} tabIndex={-1}>
        {items.map((entry, index) => (
          <button
            key={entry.id}
            id={optionId(index)}
            type="button"
            role="option"
            aria-selected={index === activeIndex}
            tabIndex={-1}
            data-testid="command-history-row"
            data-row-index={index}
            className={`nx-command-item ${index === activeIndex ? "is-active" : ""}`}
            onMouseEnter={() => setCursor(index)}
            onMouseDown={(event) => event.preventDefault()}
            onClick={(event) => (event.shiftKey ? fillTerminal(entry) : openReplay(entry))}
          >
            <span className="min-w-0 flex-1 truncate font-mono text-[12px]" title={entry.command}>
              {entry.command}
            </span>
            <span className="nx-command-hint max-w-[120px] shrink-0 truncate text-[11px]">
              {hostLabelFor(entry.assetId, assets)}
            </span>
            <span className="nx-command-hint shrink-0 text-[11px]">{formatTranscriptTime(entry.startedAt)}</span>
            {entry.exitCode === null ? (
              <span className="nx-command-hint w-6 shrink-0 text-center text-[11px]" title={t("没有记录退出码")}>—</span>
            ) : (
              <span
                className={`shrink-0 text-center text-[11px] ${entry.exitCode === 0 ? "nx-command-hint w-6" : "nx-badge nx-badge-red"}`}
                title={t("退出码 {exitCode}", { exitCode: entry.exitCode })}
              >
                {entry.exitCode}
              </span>
            )}
          </button>
        ))}
        {loading && (
          <div role="status" className="nx-command-empty flex items-center justify-center gap-2 px-4 py-6 text-xs">
            <IconLoader size={12} className="animate-spin" />
            {t("正在搜索命令历史…")}
          </div>
        )}
        {!loading && error && (
          <div role="alert" className="px-4 py-6 text-center text-xs text-red-300">
            {t("命令历史搜索失败：{error}", { error })}
          </div>
        )}
        {!loading && !error && items.length === 0 && (
          <div role="status" className="nx-command-empty px-4 py-6 text-center text-xs">
            {text ? t("没有匹配的命令") : t("暂无命令历史")}
          </div>
        )}
        {!loading && hasMore && (
          <div className="flex justify-center py-2">
            <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={loadingMore} onClick={loadMore}>
              {loadingMore ? t("加载中…") : t("加载更多")}
            </button>
          </div>
        )}
      </div>
      <div id={helpId} className="nx-command-help flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
        <span className="flex items-center gap-1.5">
          <span className="nx-kbd">↑</span>
          <span className="nx-kbd">↓</span> {t("选择")}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="nx-kbd">Enter</span> {t("打开回放")}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="nx-kbd">Shift</span>
          <span className="nx-kbd">Enter</span> {t("填入终端")}
        </span>
        <span className="flex items-center gap-1.5">
          <span className="nx-kbd">Esc</span> {t("关闭")}
        </span>
      </div>
    </>
  );
}
