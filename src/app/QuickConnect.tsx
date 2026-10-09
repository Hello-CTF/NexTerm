import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { connectAsset, connectQuickTarget, saveQuickConnectAsset, useUi } from "./store";
import { assetApi, type Asset } from "../ipc/commands";
import { WEB } from "../ipc/env";
import { describeError } from "../ui/errorText";
import { isEditableTarget, isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../ui/DialogHost";
import { useAssetVisibility } from "../features/explorer/assetVisibility";
import { orderQuickConnectAssets, useConnectHistory } from "../features/explorer/connectHistory";
import { useAssetReachability } from "../features/explorer/assetReachability";
import { ReachabilityDot } from "../features/explorer/ReachabilityDot";
import { IconLoader, IconSave, IconSearch, IconTerminal, assetIcon } from "../ui/icons";
import {
  formatQuickConnectTarget,
  parseQuickConnectTarget,
  type QuickConnectTarget,
} from "./quickConnectTarget";

const PROBE_DEBOUNCE_MS = 250;
const PROBE_VISIBLE_LIMIT = 24;

type QuickConnectRow =
  | { kind: "quick"; mode: "temporary" | "save"; target: QuickConnectTarget; label: string }
  | { kind: "asset"; asset: Asset };

export function QuickConnect({ onClose }: { onClose: () => void }) {
  const assetsQuery = useQuery({
    queryKey: ["assets"],
    queryFn: () => assetApi.list(),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const { hiddenIds, showHidden } = useAssetVisibility();
  const historyEntries = useConnectHistory((s) => s.entries);
  const connectingIds = useUi((s) => s.connectingAssetIds);
  const reachEntries = useAssetReachability((s) => s.entries);
  const batchError = useAssetReachability((s) => s.batchError);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [connectError, setConnectError] = useState<{
    label: string;
    message: string;
    retry: () => void;
  } | null>(null);
  const [pendingQuick, setPendingQuick] = useState<"temporary" | "save" | null>(null);
  const quickPendingRef = useRef(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const modalRef = useRef<HTMLDivElement>(null);
  const closedRef = useRef(false);
  const probeSeq = useRef(0);
  const queryClient = useQueryClient();
  const listId = useId();
  const titleId = useId();
  const helpId = useId();
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => inputRef.current });

  const assets = assetsQuery.data ?? [];
  const visible = useMemo(
    () =>
      orderQuickConnectAssets(
        assets.filter((a) => showHidden || !hiddenIds.includes(a.id)),
        historyEntries,
        query,
      ),
    [assets, hiddenIds, showHidden, historyEntries, query],
  );

  const probeTargets = useMemo(
    () =>
      visible
        .filter((a) => Boolean(a.host))
        .slice(0, PROBE_VISIBLE_LIMIT)
        .map((a) => a.id),
    [visible],
  );

  useEffect(() => {
    if (probeTargets.length === 0) return;
    const seq = ++probeSeq.current;
    const timer = window.setTimeout(() => {
      if (seq === probeSeq.current) void useAssetReachability.getState().probe(probeTargets);
    }, PROBE_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [probeTargets]);

  const quickTarget = useMemo(() => parseQuickConnectTarget(query), [query]);
  useEffect(() => {
    setCursor(0);
  }, [quickTarget]);
  const rows = useMemo<QuickConnectRow[]>(() => {
    const list: QuickConnectRow[] = [];
    if (quickTarget) {
      const label = formatQuickConnectTarget(quickTarget);
      // 服务端装配禁用了 session_connect_quick (防借服务器 pivot), web 模式只提供"保存为资产"。
      if (!WEB) list.push({ kind: "quick", mode: "temporary", target: quickTarget, label });
      list.push({ kind: "quick", mode: "save", target: quickTarget, label });
    }
    for (const asset of visible) list.push({ kind: "asset", asset });
    return list;
  }, [quickTarget, visible]);

  const activeIndex = rows.length > 0 ? Math.min(cursor, rows.length - 1) : 0;
  const activeOptionId = rows[activeIndex] ? `${listId}-option-${activeIndex}` : undefined;
  useEffect(() => {
    if (!activeOptionId) return;
    const option = document.getElementById(activeOptionId);
    if (typeof option?.scrollIntoView === "function") option.scrollIntoView({ block: "nearest" });
  }, [activeOptionId]);

  const closeOverlay = () => {
    if (closedRef.current) return;
    closedRef.current = true;
    onClose();
  };

  const refocusInput = () => {
    const active = document.activeElement;
    if (
      active instanceof HTMLElement &&
      active !== inputRef.current &&
      modalRef.current?.contains(active)
    ) {
      inputRef.current?.focus({ preventScroll: true });
    }
  };

  const connect = async (asset: Asset) => {
    if (useUi.getState().connectingAssetIds.includes(asset.id)) return;
    setConnectError(null);
    refocusInput();
    const outcome = await connectAsset(asset, { silent: true });
    if (closedRef.current) return;
    if (outcome.ok) {
      closeOverlay();
      return;
    }
    if (outcome.canceled) return;
    setConnectError({
      label: asset.name,
      message: outcome.error ?? "连接失败",
      retry: () => void connect(asset),
    });
  };

  const runQuick = async (row: QuickConnectRow & { kind: "quick" }) => {
    if (quickPendingRef.current) return;
    quickPendingRef.current = true;
    setConnectError(null);
    refocusInput();
    setPendingQuick(row.mode);
    try {
      const outcome =
        row.mode === "temporary"
          ? await connectQuickTarget(row.target, { silent: true })
          : await saveQuickConnectAsset(row.target, { silent: true });
      if (closedRef.current) return;
      if (outcome.ok) {
        if (row.mode === "save") void queryClient.invalidateQueries({ queryKey: ["assets"] });
        closeOverlay();
        return;
      }
      if (outcome.canceled) return;
      setConnectError({
        label: row.label,
        message: outcome.error ?? "连接失败",
        retry: () => void runQuick(row),
      });
    } finally {
      quickPendingRef.current = false;
      if (!closedRef.current) setPendingQuick(null);
    }
  };

  const activate = (row: QuickConnectRow) => {
    if (row.kind === "asset") void connect(row.asset);
    else void runQuick(row);
  };

  const moveCursor = (next: number) => {
    if (rows.length > 0) setCursor((next + rows.length) % rows.length);
  };
  const onOverlayKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Tab") {
      trapOverlayTab(event, modalRef.current);
      return;
    }
    if (isImeKeyEvent(event)) return;
    if (event.key === "Escape" && !event.repeat) {
      event.preventDefault();
      closeOverlay();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      moveCursor(activeIndex + 1);
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      moveCursor(activeIndex - 1);
    } else if (event.key === "Home") {
      event.preventDefault();
      moveCursor(0);
    } else if (event.key === "End") {
      event.preventDefault();
      moveCursor(rows.length - 1);
    } else if (event.key === "Enter" && !event.repeat && rows[activeIndex]) {
      if (isEditableTarget(event.target)) {
        event.preventDefault();
        activate(rows[activeIndex]);
      }
    }
  };

  return (
    <div className="nx-overlay items-start justify-center pt-24" onClick={closeOverlay}>
      <div
        ref={modalRef}
        className="nx-modal nx-command-modal w-[560px] overflow-hidden p-0"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={helpId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onOverlayKeyDown}
      >
        <h2 id={titleId} className="nx-sr-only">快速连接</h2>
        <div className="flex items-center gap-2.5 border-b border-neutral-800/70 px-4">
          <IconSearch size={15} className="shrink-0 text-neutral-400" />
          <input
            ref={inputRef}
            className="nx-command-input border-none px-0"
            placeholder="搜索资产，或输入 user@host:port 快速连接"
            aria-label="资产搜索"
            role="combobox"
            aria-autocomplete="list"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={activeOptionId}
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setCursor(0);
            }}
          />
        </div>
        <div id={listId} className="max-h-[340px] overflow-y-auto p-1.5" role="listbox" aria-label="资产连接" tabIndex={-1}>
          {rows.map((row, index) => {
            const optionId = `${listId}-option-${index}`;
            if (row.kind === "quick") {
              const Icon = row.mode === "temporary" ? IconTerminal : IconSave;
              const pending = pendingQuick === row.mode;
              return (
                <button
                  key={`quick-${row.mode}`}
                  id={optionId}
                  type="button"
                  role="option"
                  aria-selected={index === activeIndex}
                  tabIndex={-1}
                  className={`nx-command-item ${index === activeIndex ? "is-active" : ""}`}
                  onMouseEnter={() => setCursor(index)}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => activate(row)}
                >
                  <Icon size={15} className="shrink-0 text-neutral-400" />
                  <span className="min-w-0 flex-1 truncate">
                    {row.mode === "temporary" ? `临时连接 ${row.label}` : `保存为资产并连接 ${row.label}`}
                  </span>
                  {pending ? (
                    <span className="flex shrink-0 items-center gap-1 text-[11px] text-neutral-400">
                      <IconLoader size={11} className="animate-spin" />
                      连接中…
                    </span>
                  ) : (
                    <span className="nx-command-hint shrink-0 text-[11px]">
                      {row.mode === "temporary" ? "不保存资产" : "存入资产树"}
                    </span>
                  )}
                </button>
              );
            }
            const asset = row.asset;
            const connecting = connectingIds.includes(asset.id);
            const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
            const Icon = assetIcon(asset.kind);
            return (
              <button
                key={asset.id}
                id={optionId}
                type="button"
                role="option"
                aria-selected={index === activeIndex}
                tabIndex={-1}
                className={`nx-command-item ${index === activeIndex ? "is-active" : ""}`}
                onMouseEnter={() => setCursor(index)}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => activate(row)}
              >
                <Icon size={15} className="shrink-0 text-neutral-400" />
                <span className="min-w-0 flex-1 truncate">{asset.name}</span>
                {asset.kind === "local" || asset.builtin ? (
                  <span className="nx-badge nx-badge-blue shrink-0">本机</span>
                ) : (
                  <ReachabilityDot entry={reachEntries[asset.id]} />
                )}
                {connecting ? (
                  <span className="flex shrink-0 items-center gap-1 text-[11px] text-neutral-400">
                    <IconLoader size={11} className="animate-spin" />
                    连接中…
                  </span>
                ) : (
                  target && <span className="nx-command-hint shrink-0 text-[11px]">{target}</span>
                )}
              </button>
            );
          })}
          {rows.length === 0 && (
            <div className="nx-command-empty px-4 py-8 text-center text-xs" role="status">
              {assets.length === 0 ? "还没有资产，先在左侧资产树新建一个" : "没有匹配的资产"}
            </div>
          )}
        </div>
        {connectError && (
          <div className="nx-command-error" role="alert">
            <span className="min-w-0 flex-1 truncate" title={connectError.message}>
              「{connectError.label}」连接失败：{connectError.message}
            </span>
            <button
              type="button"
              className="nx-link shrink-0"
              onClick={() => void connectError.retry()}
            >
              重试
            </button>
          </div>
        )}
        {!connectError && batchError && (
          <div className="nx-command-error" role="alert">
            <span className="min-w-0 flex-1 truncate" title={batchError}>
              可达性检测失败：{batchError}
            </span>
            <button
              type="button"
              className="nx-link shrink-0"
              onClick={() => {
                refocusInput();
                void useAssetReachability.getState().probe(probeTargets);
              }}
            >
              重试
            </button>
          </div>
        )}
        {assetsQuery.isError && (
          <div className="nx-command-error" role="alert">
            <span className="min-w-0 flex-1 truncate" title={describeError(assetsQuery.error)}>
              资产列表加载失败：{describeError(assetsQuery.error)}
            </span>
            <button
              type="button"
              className="nx-link shrink-0"
              onClick={() => {
                refocusInput();
                void assetsQuery.refetch();
              }}
            >
              重试
            </button>
          </div>
        )}
        <div id={helpId} className="nx-command-help flex items-center gap-3 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">↑</span>
            <span className="nx-kbd">↓</span> 选择
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Enter</span> 连接
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Esc</span> 关闭
          </span>
        </div>
      </div>
    </div>
  );
}
