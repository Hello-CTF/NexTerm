import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
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
import { workspaceLayoutTree } from "./paneLayout";
import { resolveActivePane } from "./splitTree";
import { TabAttentionBadge } from "./workspace/tabStrip";
import { awarenessBadge, useTabAwareness } from "./sessionAwarenessStore";
import { hideMobileHome } from "./shell/mobileHome";
import { IconDownload, IconLoader, IconPlus, IconSave, IconSearch, IconTerminal, assetIcon } from "../ui/icons";
import {
  formatQuickConnectTarget,
  parseQuickConnectTarget,
  type QuickConnectTarget,
} from "./quickConnectTarget";
import { freezeOrder, hostCandidates, rankJumpItems, type JumpAttention, type JumpCandidate } from "./jump/jumpRanking";
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

// SessionAttentionBadge follows one session row's awareness state live; the row order stays frozen.
function SessionAttentionBadge({ terminalId }: { terminalId: string | null }) {
  const awareness = useTabAwareness(terminalId ?? undefined);
  return <TabAttentionBadge badge={awarenessBadge(awareness)} />;
}

// JumpPalette is the single keyboard-first palette for open and background sessions, hosts and commands.
// The hosts scope is the former quick connect; Tab switches between it and the full list.
export function JumpPalette({
  onClose,
  scope: initialScope = "all",
  onEditAsset,
  attentionOf,
}: {
  onClose: () => void;
  scope?: JumpScope;
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
  const orderRef = useRef<{ token: string; keys: string[] } | null>(null);
  const [announcement, setAnnouncement] = useState("");
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

  const changeScope = useCallback((next: JumpScope) => {
    setScope(next);
    setConnectError(null);
    setAnnouncement(next === "hosts" ? "只看主机" : "显示全部会话、主机和命令");
  }, []);
  const showHosts = useCallback(() => {
    changeScope("hosts");
    setQuery("");
    setCursor(0);
  }, [changeScope]);
  const commands = useJumpCommands({ assets, snippets, onEditAsset, onShowHosts: showHosts });

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

  // The order is snapshot when the palette opens and when the query or scope changes; later updates such as a
  // renamed tab or a newly listed background terminal change rows in place or append them, never reorder.
  const orderToken = `${scope}\n${query}`;
  const grouped = !query.trim();
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
    const fresh = rankJumpItems(items, query, {
      attentionOf: attentionOf
        ? (item) => (item.row.kind === "session" ? attentionOf(item.row.entry) : undefined)
        : undefined,
    }).map((item) => item.row);
    const previous = orderRef.current?.token === orderToken ? orderRef.current.keys : null;
    const byKey = new Map(fresh.map((row) => [rowKey(row), row]));
    // Without a query each section freezes on its own, so rows that arrive late stay in their section.
    const groups = grouped
      ? (["session", "asset", "command"] as const).map((kind) => fresh.filter((row) => row.kind === kind))
      : [fresh];
    return groups.flatMap((group) =>
      freezeOrder(previous, group.map(rowKey)).map((key) => byKey.get(key)!),
    );
  }, [showAll, sessions, hostAssets, commands, historyEntries, query, attentionOf, orderToken, grouped]);
  useLayoutEffect(() => {
    orderRef.current = { token: orderToken, keys: ranked.map(rowKey) };
  }, [orderToken, ranked]);

  // Probe targets depend only on the hosts and the query, so unrelated updates do not probe again.
  const probeKey = useMemo(
    () =>
      rankJumpItems(hostCandidates(hostAssets, () => 0), query)
        .flatMap((candidate) => (candidate.asset.host ? [candidate.asset.id] : []))
        .slice(0, PROBE_VISIBLE_LIMIT)
        .join("\n"),
    [hostAssets, query],
  );

  useEffect(() => {
    if (!probeKey) return;
    const seq = ++probeSeq.current;
    const timer = window.setTimeout(() => {
      if (seq === probeSeq.current) void useAssetReachability.getState().probe(probeKey.split("\n"));
    }, PROBE_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [probeKey]);

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

  // Shift+Enter: put the session or host to the right of the current pane. Panes belong to one host, so only the
  // active workspace's own sessions and host can split; anything else opens normally and says so.
  const openInSplit = (row: JumpRow) => {
    if (closedRef.current) return;
    const st = useUi.getState();
    const current = st.workspaces.find((item) => item.id === st.activeWorkspaceId);
    const tree = current ? workspaceLayoutTree(current) : null;
    const paneId = current && tree ? resolveActivePane(tree, current.activePaneId) : null;
    if (row.kind === "session" && row.entry.target.source === "tab" && current && paneId && row.entry.target.workspaceId === current.id) {
      closeOverlay();
      st.splitPaneWithTab(row.entry.target.tabId, paneId, "right", current.id);
      return;
    }
    if (row.kind === "asset" && current && paneId && current.kind === "session" && current.assetId === row.asset.id && current.sessionId) {
      closeOverlay();
      st.splitPane("right", paneId, current.id);
      return;
    }
    if (row.kind === "session" || row.kind === "asset") {
      st.pushToast("info", "只能把当前工作区的会话或同一主机拆到右侧，已直接打开");
    }
    activate(row);
  };

  const activate = (row: JumpRow, split = false) => {
    if (split && (row.kind === "session" || row.kind === "asset")) {
      openInSplit(row);
      return;
    }
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
        hideMobileHome();
        void jumpToSession(row.entry.target);
        return;
      case "command":
        runCommand(row.command);
        return;
    }
  };

  // Tab switches scope only while the panel has nothing else to reach; otherwise it moves between controls.
  const panelHasControls =
    Boolean(connectError) ||
    Boolean(!databaseOnly && connectionProblem) ||
    Boolean(!connectError && batchError) ||
    assetsQuery.isError ||
    (showAll && snippetsQuery.isError) ||
    showConnectionActions;
  const tabSwitchesScope = !databaseOnly && !panelHasControls;

  const moveCursor = (next: number) => {
    if (rows.length > 0) setCursor((next + rows.length) % rows.length);
  };
  const onOverlayKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Tab") {
      // Tab in the search field switches between all results and hosts; elsewhere it keeps focus in the dialog.
      const plainTab = !event.shiftKey && !event.altKey && !event.ctrlKey && !event.metaKey;
      if (plainTab && tabSwitchesScope && event.target === inputRef.current && !isImeKeyEvent(event)) {
        event.preventDefault();
        changeScope(scope === "all" ? "hosts" : "all");
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
        activate(rows[activeIndex], event.shiftKey);
      }
    } else if (event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.repeat) {
      // Alt+1..9 opens the Nth row as shown, whether or not the search box has text; plain digits stay text so
      // host names and addresses such as 10.0.0.8 can still be typed. event.code, since Alt changes event.key on macOS.
      const digit = /^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1];
      const row = digit ? rows[Number(digit) - 1] : undefined;
      if (row) {
        event.preventDefault();
        activate(row);
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
      onClick: (event: { shiftKey: boolean }) => activate(row, event.shiftKey),
      "aria-keyshortcuts": index < 9 ? `Alt+${index + 1}` : undefined,
    };
    const numberHint = index < 9 ? <span aria-hidden="true" className="nx-command-hint shrink-0 text-[10.5px]">⌥{index + 1}</span> : null;
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
          {numberHint}
        </button>
      );
    }
    if (row.kind === "session") {
      return (
        <button {...optionProps}>
          <IconTerminal size={15} className="shrink-0 text-neutral-400" />
          <span className="min-w-0 flex-1 truncate">{row.entry.label}</span>
          <SessionAttentionBadge terminalId={row.entry.terminalId} />
          {row.entry.hint && <span className="nx-command-hint min-w-0 shrink truncate text-[11px]">{row.entry.hint}</span>}
          {numberHint}
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
          {numberHint}
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
        {numberHint}
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
                  className={`nx-btn ${mobile ? "nx-btn-sm min-h-[44px] min-w-[44px]" : "nx-btn-xs"} ${scope === value ? "nx-btn-primary" : "nx-btn-ghost"}`}
                  aria-pressed={scope === value}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => {
                    if (value !== scope) changeScope(value);
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
                  <div role="presentation" className="px-3 pb-1 pt-2 text-[10.5px] font-medium text-neutral-500" data-command-section={section}>
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
          errorBanner(`可达性检测失败：${batchError}`, () => void useAssetReachability.getState().probe(probeKey.split("\n")))}
        {assetsQuery.isError &&
          errorBanner(`资产列表加载失败：${describeError(assetsQuery.error)}`, () => void assetsQuery.refetch())}
        {showAll && snippetsQuery.isError &&
          errorBanner(`片段列表加载失败：${describeError(snippetsQuery.error)}`, () => void snippetsQuery.refetch())}
        {showAll && background.unlisted > 0 && (
          <div className="nx-command-error" role="status">
            <span className="min-w-0 flex-1 truncate">
              有 {background.unlisted} 处后台会话未能列出，原因见「后台会话」页
            </span>
          </div>
        )}
        <div className="nx-sr-only" aria-live="polite" aria-atomic="true">{announcement}</div>
        <div id={helpId} className="nx-command-help flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-neutral-800/70 bg-neutral-950/40 px-4 py-2 text-[10.5px]">
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">↑</span>
            <span className="nx-kbd">↓</span> 选择
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Enter</span> {showAll ? "打开" : "连接"}
          </span>
          {tabSwitchesScope && (
            <span className="flex items-center gap-1.5">
              <span className="nx-kbd">Tab</span> {showAll ? "只看主机" : "全部"}
            </span>
          )}
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Alt</span>
            <span className="nx-kbd">1-9</span> 直达
          </span>
          <span className="flex items-center gap-1.5">
            <span className="nx-kbd">Shift</span>
            <span className="nx-kbd">Enter</span> 右侧分屏
          </span>
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
