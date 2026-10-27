import { Fragment, useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  assetConnectionProblemKey,
  connectAsset,
  connectDevice,
  connectQuickTarget,
  dbKindOf,
  quickConnectionProblemKey,
  saveQuickConnectAsset,
  useConnectionProblem,
  useUi,
  useWorkspaceList,
  type ConnectAssetInput,
  type ConnectOutcome,
  type WorkspaceProblem,
} from "./store";
import { ConnectionProblemNotice } from "../features/problems/WorkspaceProblems";
import { assetApi, type Asset } from "../ipc/commands";
import { WEB } from "../ipc/env";
import { describeError } from "../ui/errorText";
import { isEditableTarget, isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../ui/DialogHost";
import { useAssetVisibility } from "../features/explorer/assetVisibility";
import { quickConnectScore, useConnectHistory } from "../features/explorer/connectHistory";
import { useAssetReachability } from "../features/explorer/assetReachability";
import { ReachabilityDot } from "../features/explorer/ReachabilityDot";
import { isInventoryOnlyKind } from "../features/explorer/inventory";
import { AssetEditor } from "../features/explorer/AssetTree";
import { SshImportDialog } from "../features/explorer/SshImportDialog";
import { useShell } from "./shell";
import { IconDownload, IconLoader, IconPlus, IconSave, IconSearch, IconTerminal, assetIcon } from "../ui/icons";
import {
  formatQuickConnectTarget,
  parseQuickConnectTarget,
  type QuickConnectTarget,
} from "./quickConnectTarget";
import { hostCandidates, rankJumpItems, type JumpAttention, type JumpCandidate } from "./jump/jumpRanking";
import {
  backgroundSessionEntries,
  jumpToSession,
  openTabSessions,
  useBackgroundJumpSessions,
  type JumpSessionEntry,
} from "./jump/jumpSessions";
import { useJumpCommands, type JumpCommand } from "./jump/useJumpCommands";

const PROBE_DEBOUNCE_MS = 250;
const PROBE_VISIBLE_LIMIT = 24;

// JumpScope picks what the palette lists: everything, hosts only (quick connect) or database assets only.
export type JumpScope = "all" | "hosts" | "database";

type QuickRow = { kind: "quick"; mode: "temporary" | "save"; target: QuickConnectTarget; label: string };
type JumpRow =
  | QuickRow
  | { kind: "asset"; asset: Asset }
  | { kind: "session"; entry: JumpSessionEntry }
  | { kind: "command"; command: JumpCommand };

type RankedRow = JumpCandidate & { row: Exclude<JumpRow, QuickRow> };

function rowKey(row: JumpRow): string {
  switch (row.kind) {
    case "quick":
      return `quick-${row.mode}`;
    case "asset":
      return `asset-${row.asset.id}`;
    case "session":
      return row.entry.key;
    case "command":
      return `command-${row.command.id}`;
  }
}

function sectionLabel(row: JumpRow): string {
  switch (row.kind) {
    case "quick":
    case "asset":
      return "主机";
    case "session":
      return "会话";
    case "command":
      return row.command.section ?? "命令";
  }
}

const SCOPE_TEXT: Record<JumpScope, { title: string; input: string; placeholder: string; list: string }> = {
  all: {
    title: "跳转面板",
    input: "搜索会话、主机或命令",
    placeholder: "搜索会话、主机或命令，或输入 user@host:port",
    list: "跳转结果",
  },
  hosts: {
    title: "快速连接",
    input: "资产搜索",
    placeholder: "搜索资产，或输入 user@host:port 快速连接",
    list: "资产连接",
  },
  database: {
    title: "选择数据库",
    input: "数据库资产搜索",
    placeholder: "搜索数据库资产",
    list: "数据库资产连接",
  },
};

// JumpPalette is the single keyboard-first palette for open and background sessions, hosts and commands.
// The hosts scope is the former quick connect; Tab switches between it and the full list.
export function JumpPalette({
  onClose,
  scope: initialScope = "all",
  onOpenFiles,
  onEditAsset,
  attentionOf,
}: {
  onClose: () => void;
  scope?: JumpScope;
  onOpenFiles?: () => void;
  onEditAsset?: (asset: Asset) => void;
  // attentionOf ranks sessions by attention state once the session awareness store provides it.
  attentionOf?: (session: JumpSessionEntry) => JumpAttention | undefined;
}) {
  const [scope, setScope] = useState<JumpScope>(initialScope);
  const showAll = scope === "all";
  const databaseOnly = scope === "database";
  const assetsQuery = useQuery({
    queryKey: ["assets"],
    queryFn: () => assetApi.list(),
    refetchOnWindowFocus: false,
    retry: false,
  });
  const snippetsQuery = useQuery({
    queryKey: ["snippets"],
    queryFn: () => assetApi.snippetList(),
    retry: false,
    enabled: showAll,
  });
  const { hiddenIds, showHidden, setShowHidden } = useAssetVisibility();
  const historyEntries = useConnectHistory((s) => s.entries);
  const connectingIds = useUi((s) => s.connectingAssetIds);
  const workspaces = useWorkspaceList();
  const activeWorkspaceId = useUi((s) => s.activeWorkspaceId);
  const reachEntries = useAssetReachability((s) => s.entries);
  const batchError = useAssetReachability((s) => s.batchError);
  const background = useBackgroundJumpSessions(showAll);
  const [query, setQuery] = useState("");
  const [cursor, setCursor] = useState(0);
  const [connectError, setConnectError] = useState<{
    label: string;
    message: string;
    retry: () => void;
  } | null>(null);
  const [pendingQuick, setPendingQuick] = useState<"temporary" | "save" | null>(null);
  const [problemKey, setProblemKey] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [importing, setImporting] = useState(false);
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

  const assets = useMemo(() => assetsQuery.data ?? [], [assetsQuery.data]);
  const snippets = useMemo(() => snippetsQuery.data ?? [], [snippetsQuery.data]);
  const hiddenDatabaseCount = assets.filter(
    (asset) => dbKindOf(asset.kind) !== null && hiddenIds.includes(asset.id),
  ).length;
  const mobile = useShell() === "mobile";
  const text = SCOPE_TEXT[scope];

  const showHosts = useCallback(() => setScope("hosts"), []);
  const commands = useJumpCommands({ assets, snippets, onOpenFiles, onEditAsset, onShowHosts: showHosts });

  const hostAssets = useMemo(
    () =>
      assets
        .filter(
          (a) =>
            !isInventoryOnlyKind(a.kind) &&
            (!databaseOnly || dbKindOf(a.kind) !== null) &&
            (!mobile || a.kind !== "local") &&
            (showHidden || !hiddenIds.includes(a.id)),
        ),
    [assets, hiddenIds, showHidden, mobile, databaseOnly],
  );

  const sessions = useMemo(
    () => [...openTabSessions(workspaces, activeWorkspaceId), ...backgroundSessionEntries(background.tabs)],
    [workspaces, activeWorkspaceId, background.tabs],
  );

  const ranked = useMemo(() => {
    const now = Date.now();
    const items: RankedRow[] = [];
    if (showAll) {
      for (const entry of sessions) {
        items.push({
          section: "session",
          label: entry.label,
          keywords: entry.keywords,
          recency: entry.recency,
          row: { kind: "session", entry },
        });
      }
    }
    for (const { asset, ...candidate } of hostCandidates(hostAssets, (item) => quickConnectScore(historyEntries[item.id], now))) {
      items.push({ ...candidate, row: { kind: "asset", asset } });
    }
    if (showAll) {
      for (const command of commands) {
        items.push({
          section: "command",
          label: command.label,
          keywords: [command.section, command.hint],
          row: { kind: "command", command },
        });
      }
    }
    return rankJumpItems(items, query, {
      attentionOf: attentionOf
        ? (item) => (item.row.kind === "session" ? attentionOf(item.row.entry) : undefined)
        : undefined,
    }).map((item) => item.row);
  }, [showAll, sessions, hostAssets, commands, historyEntries, query, attentionOf]);

  const probeTargets = useMemo(
    () =>
      ranked
        .flatMap((row) => (row.kind === "asset" && row.asset.host ? [row.asset.id] : []))
        .slice(0, PROBE_VISIBLE_LIMIT),
    [ranked],
  );

  useEffect(() => {
    if (probeTargets.length === 0) return;
    const seq = ++probeSeq.current;
    const timer = window.setTimeout(() => {
      if (seq === probeSeq.current) void useAssetReachability.getState().probe(probeTargets);
    }, PROBE_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [probeTargets]);

  const quickTarget = useMemo(
    () => (databaseOnly ? null : parseQuickConnectTarget(query)),
    [query, databaseOnly],
  );
  useEffect(() => {
    setCursor(0);
  }, [quickTarget, scope]);
  const firstConnect =
    scope === "hosts" &&
    assetsQuery.isSuccess &&
    !query.trim() &&
    !assets.some((asset) => asset.kind !== "local");
  const rows = useMemo<JumpRow[]>(() => {
    const list: JumpRow[] = [];
    if (firstConnect) return list;
    if (quickTarget) {
      const label = formatQuickConnectTarget(quickTarget);
      // The server assembly disabled session_connect_quick (prevent pivoting through the server); web mode only offers "save as asset".
      if (!WEB) list.push({ kind: "quick", mode: "temporary", target: quickTarget, label });
      list.push({ kind: "quick", mode: "save", target: quickTarget, label });
    }
    list.push(...ranked);
    return list;
  }, [quickTarget, ranked, firstConnect]);
  const localAsset =
    assets.find(
      (asset) =>
        asset.kind === "local" &&
        !mobile &&
        (showHidden || !hiddenIds.includes(asset.id)),
    ) ?? null;
  const showConnectionActions = assetsQuery.isSuccess && rows.length === 0;
  const grouped = !query.trim();

  const activeIndex = rows.length > 0 ? Math.min(cursor, rows.length - 1) : 0;
  const activeRow = rows[activeIndex];
  const targetProblemKey = quickTarget
    ? quickConnectionProblemKey(quickTarget)
    : query.trim() && activeRow?.kind === "asset"
      ? assetConnectionProblemKey(activeRow.asset.id)
      : null;
  const connectionProblem = useConnectionProblem(
    problemKey ?? targetProblemKey,
    !problemKey && !query.trim(),
  );
  const activeOptionId = activeRow ? `${listId}-option-${activeIndex}` : undefined;
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

  const connect = async (asset: ConnectAssetInput) => {
    if (databaseOnly && dbKindOf(asset.kind) === null) {
      useUi.getState().pushToast("info", "这里只能连接数据库资产");
      return;
    }
    if (mobile && asset.kind === "local") {
      useUi.getState().pushToast("info", "移动端不支持本地终端，请双击资产连接远程主机");
      return;
    }
    if (useUi.getState().connectingAssetIds.includes(asset.id)) return;
    setProblemKey(assetConnectionProblemKey(asset.id));
    setConnectError(null);
    refocusInput();
    const outcome = asset.kind === "device" && asset.id.startsWith("device/")
      ? await connectDevice({ id: asset.id.slice("device/".length), name: asset.name }, { silent: true })
      : await connectAsset(asset, { silent: true });
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

  const onAssetSaved = (asset?: Asset) => {
    setCreating(false);
    void queryClient.invalidateQueries({ queryKey: ["assets"] });
    void queryClient.invalidateQueries({ queryKey: ["asset-search"] });
    void queryClient.invalidateQueries({ queryKey: ["groups"] });
    void queryClient.invalidateQueries({ queryKey: ["credentials"] });
    if (!asset) return;
    setQuery("");
    void connect(asset);
  };

  const runQuick = async (row: QuickRow) => {
    if (databaseOnly) return;
    if (quickPendingRef.current) return;
    quickPendingRef.current = true;
    setProblemKey(quickConnectionProblemKey(row.target));
    setConnectError(null);
    refocusInput();
    setPendingQuick(row.mode);
    try {
      let outcome: ConnectOutcome;
      let retryAsset: Asset | undefined;
      if (row.mode === "temporary") {
        outcome = await connectQuickTarget(row.target, { silent: true });
      } else {
        const saved = await saveQuickConnectAsset(row.target, { silent: true });
        outcome = saved;
        retryAsset = saved.asset;
        if (saved.asset) {
          void queryClient.invalidateQueries({ queryKey: ["assets"] });
          void queryClient.invalidateQueries({ queryKey: ["asset-search"] });
        }
        if (!closedRef.current) setProblemKey(saved.problemKey);
      }
      if (closedRef.current) return;
      if (outcome.ok) {
        closeOverlay();
        return;
      }
      if (outcome.canceled) return;
      setConnectError({
        label: row.label,
        message: outcome.error ?? "连接失败",
        retry: () => retryAsset ? void connect(retryAsset) : void runQuick(row),
      });
    } finally {
      quickPendingRef.current = false;
      if (!closedRef.current) setPendingQuick(null);
    }
  };

  const reconnectProblem = (problem: WorkspaceProblem) => {
    const connection = problem.connection;
    if (!connection) return;
    if (connection.kind === "asset") {
      if (!problemTargetAvailable(problem)) return;
      void connect(connection.asset);
    } else {
      void runQuick({
        kind: "quick",
        mode: connection.mode,
        target: connection.target,
        label: connection.label,
      });
    }
  };

  const problemTargetAvailable = (problem: WorkspaceProblem): boolean => {
    const connection = problem.connection;
    if (connection?.kind !== "asset") return true;
    if (connection.asset.kind === "device" && connection.asset.id.startsWith("device/")) return true;
    return assets.some((asset) => asset.id === connection.asset.id);
  };

  const runCommand = (command: JumpCommand) => {
    if (closedRef.current) return;
    if (command.keepOpen) {
      command.run();
      refocusInput();
      return;
    }
    closedRef.current = true;
    try {
      command.run();
    } finally {
      onClose();
    }
  };

  const activate = (row: JumpRow) => {
    switch (row.kind) {
      case "asset":
        void connect(row.asset);
        return;
      case "quick":
        void runQuick(row);
        return;
      case "session":
        if (closedRef.current) return;
        closeOverlay();
        void jumpToSession(row.entry.target);
        return;
      case "command":
        runCommand(row.command);
        return;
    }
  };

  const toggleScope = () => {
    setScope((current) => (current === "all" ? "hosts" : "all"));
    setConnectError(null);
  };

  const moveCursor = (next: number) => {
    if (rows.length > 0) setCursor((next + rows.length) % rows.length);
  };
  const onOverlayKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Tab") {
      // Tab in the search field switches between all results and hosts; elsewhere it keeps focus in the dialog.
      const plainTab = !event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey;
      if (plainTab && !databaseOnly && event.target === inputRef.current && !isImeKeyEvent(event)) {
        event.preventDefault();
        toggleScope();
        return;
      }
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

  const renderRow = (row: JumpRow, index: number) => {
    const optionId = `${listId}-option-${index}`;
    const optionProps = {
      id: optionId,
      type: "button" as const,
      role: "option",
      "aria-selected": index === activeIndex,
      tabIndex: -1,
      className: `nx-command-item ${index === activeIndex ? "is-active" : ""}`,
      onMouseEnter: () => setCursor(index),
      onMouseDown: (event: { preventDefault: () => void }) => event.preventDefault(),
      onClick: () => activate(row),
    };
    if (row.kind === "quick") {
      const Icon = row.mode === "temporary" ? IconTerminal : IconSave;
      const pending = pendingQuick === row.mode;
      return (
        <button {...optionProps}>
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
    if (row.kind === "session") {
      return (
        <button {...optionProps}>
          <IconTerminal size={15} className="shrink-0 text-neutral-400" />
          <span className="min-w-0 flex-1 truncate">{row.entry.label}</span>
          {row.entry.hint && <span className="nx-command-hint min-w-0 shrink truncate text-[11px]">{row.entry.hint}</span>}
        </button>
      );
    }
    if (row.kind === "command") {
      const Icon = row.command.icon;
      return (
        <button {...optionProps}>
          <Icon size={15} className="shrink-0 text-neutral-400" />
          <span className="min-w-0 flex-1 truncate">{row.command.label}</span>
          {row.command.hint && <span className="nx-command-hint shrink-0 text-[11px]">{row.command.hint}</span>}
        </button>
      );
    }
    const asset = row.asset;
    const connecting = connectingIds.includes(asset.id);
    const target = asset.host ? `${asset.host}${asset.port ? `:${asset.port}` : ""}` : "";
    const identity = target
      ? `${asset.username?.trim() ? `${asset.username.trim()}@` : ""}${target}`
      : "";
    const Icon = assetIcon(asset.kind, typeof asset.options?.icon === "string" ? asset.options.icon : undefined);
    return (
      <button {...optionProps}>
        <Icon size={15} className="shrink-0 text-neutral-400" />
        <span className="min-w-0 flex-1">
          <span className="block truncate">{asset.name}</span>
          {identity && <span className="block break-all text-[11px] leading-tight text-neutral-400">{identity}</span>}
        </span>
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
        ) : null}
      </button>
    );
  };

  const errorBanner = (message: string, onRetry: () => void, key?: string) => (
    <div key={key} className="nx-command-error" role="alert">
      <span className="min-w-0 flex-1 truncate" title={message}>{message}</span>
      <button
        type="button"
        className="nx-link shrink-0"
        onClick={() => {
          refocusInput();
          onRetry();
        }}
      >
        重试
      </button>
    </div>
  );

  return (
    <>
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
        <h2 id={titleId} className="nx-sr-only">{text.title}</h2>
        <div className="flex items-center gap-2.5 border-b border-neutral-800/70 px-4">
          <IconSearch size={15} className="shrink-0 text-neutral-400" />
          <input
            ref={inputRef}
            className="nx-command-input border-none px-0"
            placeholder={text.placeholder}
            aria-label={text.input}
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
          {!databaseOnly && (
            <div className="flex shrink-0 items-center gap-1" role="group" aria-label="跳转范围">
              {(["all", "hosts"] as const).map((value) => (
                <button
                  key={value}
                  type="button"
                  tabIndex={-1}
                  className={`nx-btn nx-btn-xs ${scope === value ? "nx-btn-primary" : "nx-btn-ghost"}`}
                  aria-pressed={scope === value}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => {
                    setScope(value);
                    setConnectError(null);
                    inputRef.current?.focus({ preventScroll: true });
                  }}
                >
                  {value === "all" ? "全部" : "主机"}
                </button>
              ))}
            </div>
          )}
        </div>
        <div id={listId} className="max-h-[340px] overflow-y-auto p-1.5" role="listbox" aria-label={text.list} tabIndex={-1}>
          {rows.map((row, index) => {
            const section = sectionLabel(row);
            const header = showAll && grouped && (index === 0 || sectionLabel(rows[index - 1]) !== section);
            return (
              <Fragment key={rowKey(row)}>
                {header && (
                  <div className="px-3 pb-1 pt-2 text-[10.5px] font-medium text-neutral-500" data-command-section={section}>
                    {section}
                  </div>
                )}
                {renderRow(row, index)}
              </Fragment>
            );
          })}
          {showConnectionActions && (
            <div className="nx-command-empty px-4 py-8 text-center text-xs">
              <div role="status">
                {firstConnect
                  ? "还没有远程资产，选择一种方式开始"
                  : databaseOnly
                    ? query.trim()
                      ? "没有匹配的数据库资产"
                      : hiddenDatabaseCount > 0 && !showHidden
                        ? "数据库资产已隐藏"
                        : "还没有数据库资产，请先在资产树中新建一个"
                  : showAll
                    ? "没有匹配的会话、主机或命令"
                  : query.trim()
                    ? "没有匹配的资产"
                    : "还没有可连接的资产"}
              </div>
              {databaseOnly && hiddenDatabaseCount > 0 && (
                <button
                  type="button"
                  className="nx-btn nx-btn-outline nx-btn-sm mt-4"
                  onClick={() => setShowHidden(!showHidden)}
                >
                  {showHidden ? "隐藏已隐藏资产" : `显示隐藏资产（${hiddenDatabaseCount}）`}
                </button>
              )}
              {scope === "hosts" && (
              <div className="mt-4 flex flex-wrap items-center justify-center gap-2" role="group" aria-label="连接操作">
                <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={() => setCreating(true)}>
                  <IconPlus size={13} />
                  新建连接
                </button>
                <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => setImporting(true)}>
                  <IconDownload size={13} />
                  导入 SSH 配置
                </button>
                {localAsset && (
                  <button
                    type="button"
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    disabled={connectingIds.includes(localAsset.id)}
                    onClick={() => void connect(localAsset)}
                  >
                    {connectingIds.includes(localAsset.id) ? (
                      <IconLoader size={13} className="animate-spin" />
                    ) : (
                      <IconTerminal size={13} />
                    )}
                    {connectingIds.includes(localAsset.id) ? "连接中…" : "打开本机终端"}
                  </button>
                )}
              </div>
              )}
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
        {!databaseOnly && connectionProblem && (
          <ConnectionProblemNotice
            problem={connectionProblem}
            onReconnect={reconnectProblem}
            targetAvailable={problemTargetAvailable}
          />
        )}
        {!connectError && batchError &&
          errorBanner(`可达性检测失败：${batchError}`, () => void useAssetReachability.getState().probe(probeTargets))}
        {assetsQuery.isError &&
          errorBanner(`资产列表加载失败：${describeError(assetsQuery.error)}`, () => void assetsQuery.refetch())}
        {showAll && snippetsQuery.isError &&
          errorBanner(`片段列表加载失败：${describeError(snippetsQuery.error)}`, () => void snippetsQuery.refetch())}
        {showAll && background.error && (
          <div className="nx-command-error" role="status">
            <span className="min-w-0 flex-1 truncate" title={background.error}>
              部分后台会话未列出：{background.error}
            </span>
          </div>
        )}
        <div id={helpId} className="nx-command-help flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">↑</span>
            <span className="nx-kbd">↓</span> 选择
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Enter</span> {showAll ? "打开" : "连接"}
          </span>
          {!databaseOnly && (
            <span className="flex items-center gap-1.5">
              <span className="nx-kbd">Tab</span> {showAll ? "只看主机" : "全部"}
            </span>
          )}
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Esc</span> 关闭
          </span>
        </div>
      </div>
    </div>
    {creating && (
      <AssetEditor
        kind="asset"
        onClose={() => setCreating(false)}
        onSaved={onAssetSaved}
      />
    )}
    {importing && <SshImportDialog onClose={() => setImporting(false)} />}
    </>
  );
}
