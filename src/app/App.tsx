import {
  memo,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useUi,
  useActiveWorkspace,
  cycleActiveWorkspace,
  cycleActiveTab,
  cycleActivePane,
  openTerminalTab,
  nextTabId,
  connectAsset,
  dbKindOf,
  openCredentialsSidebar,
  requestCloseTab,
  requestCloseTabs,
  requestKillTab,
  reconnectRestoredTab,
  restoreClosedTab,
  closeTabHint,
  closeActionHint,
  isSessionReconnectPending,
  reconnectDeadHost,
  reconnectWorkspaceSession,
  reconnectRestoredWorkspace,
  sanitizeRemoteTabTitle,
  sessionStatusText,
  sessionReconnectStatusText,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  type AppTab,
  type Pane,
  type SessionRuntimeInfo,
  type Workspace,
} from "./store";
import {
  clampSplitRatio,
  splitRatioAt,
  splitRatioForKey,
  workspaceViewport,
  type WorkspaceViewport,
} from "../features/terminal/workspaceLayout";
import {
  createFrameCoalescer,
  RESIZE_END_EVENT,
  type FrameCoalescer,
} from "../ui/ResizeHandle";
import {
  finishLayoutRestore,
  layoutBootstrapped,
  layoutRestoreStatus,
  reviewPendingLayoutRestore,
  startLayoutSync,
  subscribeLayoutRestore,
  undoLayoutRestore,
} from "./layout";
import { wireAccountSwitchCleanup } from "./accountSwitch";
import { useClosedTabs } from "./closedTabs";
import { startUnreadTracking } from "./unread";
import { useTabUnread } from "./unreadStore";
import { applyAuthoritativeTabTitle, useTerminalTabTitle } from "./terminalViewStore";
import { useEdgeGestures } from "./edgeGestures";
import {
  formatBinding,
  matchAppKeybinding,
  useKeybindings,
  type KeybindingActionId,
} from "./keybindings";
import { assetApi, sessionApi, syncApi, terminalApi, vaultApi, type Asset, type SessionInfo } from "../ipc/commands";
import { listenEvent } from "../ipc/events";
import { onAssetDataChanged } from "../ipc/assetEvents";
import { describeError } from "../ui/errorText";
import { formatTime } from "../ui/format";
import { TRANSPORT, WEB } from "../ipc/env";
import {
  coarsePointerMedia,
  isCoarsePointer,
  isMac,
  isMobile,
  isWailsDragRegionTarget,
  modHint,
  useLandscapeFullScreen,
  wailsDragRegionStyle,
  wailsNoDragRegionStyle,
} from "./platform";
import {
  closeWindow,
  minimiseWindow,
  toggleMaximiseWindow,
} from "../ipc/wails";
import { mountUnavailableReason } from "./capabilities";
import { AssetTree, AssetEditor } from "../features/explorer/AssetTree";
import { ProjectWorkspaceStrip } from "./ProjectWorkspaceStrip";
import { ALL_PROJECTS, focusedWorkspaces } from "./projectFocus";
import { TerminalPane } from "../features/terminal/TerminalPane";
import { BackgroundSessions } from "../features/terminal/BackgroundSessions";
import { useBackgroundSessionCount, useBackgroundSessionsAccess } from "../features/terminal/backgroundSessionsAccess";
import { WorkspaceRestoreBanner } from "../features/terminal/WorkspaceRestoreBanner";
import { TranscriptHistoryPanel } from "../features/terminal/TranscriptHistoryPanel";
import { FileBrowser } from "../features/files/FileBrowser";
import { FileTree } from "../features/files/FileTree";
import { fileVisual } from "../features/files/fileTypes";
import { MountPanel } from "../features/files/MountPanel";
import { ForwardPanel } from "../features/forward/ForwardPanel";
import { ForwardStartupRecovery } from "../features/forward/ForwardStartupRecovery";
import { FileEditor } from "../features/files/FileEditor";
import { LogViewer } from "../features/files/LogViewer";
import { MultiHostLogTail } from "../features/logs/MultiHostLogTail";
import { isMultiHostLogTabId } from "../features/logs/logTailTab";
import { FleetHealthPanel } from "../features/fleet/FleetHealthPanel";
import { isFleetHealthTabId } from "../features/fleet/fleetHealthTab";
import { DockerPanel } from "../features/docker/DockerPanel";
import { DbPanel } from "../features/db/DbPanel";
import { ExecBatchPanel } from "../features/execbatch/ExecBatchPanel";
import { useAssetProductionClassification } from "../features/production/productionSettings";
import { AiSidebar } from "../features/ai/AiSidebar";
import { SettingsView } from "../features/settings/SettingsView";
import { useDesktopAutoSync, useWebAutoSync } from "../features/settings/useWebAutoSync";
import { WEB_SYNC_ERROR_EVENT, WEB_SYNC_RESULT_EVENT, type WebSyncResult } from "../features/settings/SyncCard";
import { CredentialsPanel } from "../features/credentials/CredentialsPanel";
import { CredentialsSidebar } from "../features/credentials/CredentialsSidebar";
import { AuditView } from "../features/settings/AuditView";
import { CommandPalette } from "./CommandPalette";
import { QuickConnect } from "./QuickConnect";
import { TakeoverBanner } from "./TakeoverBanner";
import { UpdateBanner } from "./UpdateBanner";
import { DekUnlockBanner } from "./DekUnlockBanner";
import { DeviceSessionBoundary } from "../features/fleet/DeviceSessionBoundary";
import { getUpdateState } from "./useUpdate";
import { promptUpdateIfNeeded } from "./updatePrompt";
import { PromptModal } from "../ui/PromptModal";
import { DialogHost, isEditableTarget } from "../ui/DialogHost";
import { ResizeHandle } from "../ui/ResizeHandle";
import { ErrorBoundary } from "../ui/ErrorBoundary";
import { registerPromptHandler, registerDialogHandlers, registerDialogEpochSource, promptText, type ChoiceResult } from "../ui/dialogs";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../ui/ContextMenu";
import {
  IconActivity,
  IconAlert,
  IconBox,
  IconCheckCircle,
  IconChevronLeft,
  IconChevronRight,
  IconChevronDown,
  IconClock,
  IconClose,
  IconEdit,
  IconMaximize,
  IconMinus,
  IconCommand,
  IconDatabase,
  IconDrive,
  IconFolderOpen,
  IconHistory,
  IconInfo,
  IconKey,
  IconLayers,
  IconList,
  IconLoader,
  IconLock,
  IconMonitor,
  IconNetwork,
  IconPlus,
  IconServer,
  IconSettings,
  IconSparkles,
  IconStop,
  IconTerminal,
  IconXCircle,
  Logo,
  assetIcon,
  IconSplitH,
  IconMergeH,
} from "../ui/icons";

const TAB_ICON = {
  terminal: IconTerminal,
  files: IconFolderOpen,
  log: IconList,
  mount: IconDrive,
  forward: IconNetwork,
  docker: IconBox,
  db: IconDatabase,
  credentials: IconKey,
  settings: IconSettings,
  audit: IconHistory,
  devices: IconMonitor,
  background: IconActivity,
  history: IconClock,
  execBatch: IconCommand,
} as const;

function onDragRegionDoubleClick(event: MouseEvent<HTMLElement>): void {
  if (TRANSPORT !== "desktop" || isMobile()) return;
  if (isWailsDragRegionTarget(event.target)) {
    void toggleMaximiseWindow();
  }
}

function workspaceIcon(w: Workspace) {
  if (w.kind === "db") return w.dbKind === "redis" ? IconLayers : IconDatabase;
  if (w.kind === "tools") return IconSettings;
  return assetIcon(w.assetKind ?? "ssh");
}

function WorkspaceStatusGlyph({ status }: { status: SessionInfo["status"] | undefined }) {
  if (status === undefined) return null;
  const tone =
    status === "connected" ? "is-ok" : status === "failed" ? "is-bad" : "is-warn";
  return (
    <span className={`nx-ws-status ${tone}`} role="img" aria-label={sessionStatusText(status)}>
      {status === "connected" ? (
        <IconCheckCircle size={11} />
      ) : status === "failed" ? (
        <IconXCircle size={11} />
      ) : status === "disconnected" ? (
        <IconInfo size={11} />
      ) : (
        <IconLoader size={11} className="animate-spin" />
      )}
    </span>
  );
}

function WorkspaceReconnectStatus({ session }: { session: SessionRuntimeInfo | undefined }) {
  const text = sessionReconnectStatusText(session);
  if (!text) return null;
  return (
    <span
      role="status"
      className="shrink-0 text-[11px] text-amber-300"
      title={session?.error ?? undefined}
    >
      {text}
    </span>
  );
}

function tabIcon(t: AppTab) {
  if ((t.kind === "files" || t.kind === "log") && t.path) return fileVisual(t.path, "file").Icon;
  return TAB_ICON[t.kind] ?? IconTerminal;
}

function tabTooltip(t: AppTab, unread: boolean): string {
  const notes: string[] = [];
  if (t.kind === "terminal" && t.durable === true) notes.push("持久终端：断开后可在「后台会话」接管");
  if (unread) notes.push("有新输出");
  if (t.closeError) notes.push(`上次关闭失败：${t.closeError}`);
  return notes.length > 0 ? `${t.title}（${notes.join(" · ")}）` : t.title;
}

function TabStripItem({
  tab,
  children,
}: {
  tab: AppTab;
  children: (unread: boolean) => ReactNode;
}) {
  const unread = useTabUnread(tab.id);
  return children(unread);
}

// A terminal tab's label prefers the OSC-derived remote title from the per-tab view
// store (narrow subscription: only this tab's label re-renders on a title change);
// renames clear that entry, so the authoritative workspace tab title shows instead.
function TabStripTitle({ tab }: { tab: AppTab }) {
  const remoteTitle = useTerminalTabTitle(tab.id);
  return (
    <span className="truncate">{tab.kind === "terminal" && remoteTitle ? remoteTitle : tab.title}</span>
  );
}

// R-01 workspace-scope warning: a terminal tab whose backend-classified asset
// environment is production carries an amber badge; the environment name is in
// the tooltip, the tab strip stays compact.
function TabProductionBadge({ sessionId }: { sessionId?: string }) {
  const assetId = useUi((s) => (sessionId ? s.sessions.find((x) => x.id === sessionId)?.assetId : undefined));
  const classification = useAssetProductionClassification(assetId);
  if (!classification?.production) return null;
  return (
    <span className="nx-badge nx-badge-amber shrink-0" title={`生产环境：${classification.environment}`}>
      生产
    </span>
  );
}

function handleTablistKeyDown(
  event: ReactKeyboardEvent<HTMLElement>,
  ids: string[],
  currentId: string,
  activate: (id: string) => void,
  moveTab: (fromIndex: number, toIndex: number) => void,
): void {
  if (event.target !== event.currentTarget) return;
  const index = ids.indexOf(currentId);
  if (index < 0) return;
  if ((event.key === "ArrowLeft" || event.key === "ArrowRight") && event.ctrlKey && event.shiftKey) {
    const to = event.key === "ArrowLeft" ? index - 1 : index + 1;
    if (to < 0 || to >= ids.length) return;
    event.preventDefault();
    moveTab(index, to);
    focusTab(event.currentTarget.closest("[role='tablist']"), currentId);
    return;
  }
  let next = -1;
  if (event.key === "ArrowRight") next = (index + 1) % ids.length;
  else if (event.key === "ArrowLeft") next = (index - 1 + ids.length) % ids.length;
  else if (event.key === "Home") next = 0;
  else if (event.key === "End") next = ids.length - 1;
  else if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    activate(currentId);
    return;
  } else return;
  event.preventDefault();
  const nextId = ids[next];
  activate(nextId);
  focusTab(event.currentTarget.closest("[role='tablist']"), nextId);
}

const TAB_DRAG_MIME = "application/x-nexterm-tab";

let tabDragSource: { tabId: string; paneId: string } | null = null;

function focusTab(tablist: Element | null, id: string): void {
  tablist?.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(id)}"]`)?.focus();
}

let bootLocalTried = false;

function readWorkspaceViewport(): WorkspaceViewport {
  return workspaceViewport(window.innerWidth, isCoarsePointer(), window.innerHeight);
}

function sameViewport(a: WorkspaceViewport, b: WorkspaceViewport): boolean {
  return a.width === b.width && a.height === b.height && a.coarsePointer === b.coarsePointer;
}

function useWorkspaceViewport(): WorkspaceViewport {
  const [viewport, setViewport] = useState(readWorkspaceViewport);

  useEffect(() => {
    const frames = createFrameCoalescer<WorkspaceViewport>((next) =>
      setViewport((prev) => (sameViewport(prev, next) ? prev : next)),
    );
    const update = () => frames.schedule(readWorkspaceViewport());
    const coarseQuery = coarsePointerMedia();
    window.addEventListener("resize", update);
    window.visualViewport?.addEventListener("resize", update);
    coarseQuery.addEventListener("change", update);
    update();
    return () => {
      frames.cancel();
      window.removeEventListener("resize", update);
      window.visualViewport?.removeEventListener("resize", update);
      coarseQuery.removeEventListener("change", update);
    };
  }, []);

  return viewport;
}

export function visualViewportInset(
  innerHeight: number,
  vvHeight: number,
  vvOffsetTop: number,
): number {
  if (!Number.isFinite(innerHeight) || !Number.isFinite(vvHeight)) return 0;
  const offset = Number.isFinite(vvOffsetTop) ? vvOffsetTop : 0;
  return Math.max(0, innerHeight - vvHeight - offset);
}

export function virtualKeyboardInset(
  innerHeight: number,
  keyboardTop: number,
  keyboardHeight: number,
): number {
  if (!Number.isFinite(innerHeight) || !Number.isFinite(keyboardTop)) return 0;
  if (!Number.isFinite(keyboardHeight) || keyboardHeight <= 0) return 0;
  return Math.max(0, Math.min(innerHeight, innerHeight - keyboardTop));
}

export function aiToastPlacement(
  width: number,
  overlaySidebars: boolean,
  rightDockOpen: boolean,
  rightWidth: number,
): "dock" | "left" | "overlay" | null {
  if (!rightDockOpen) return null;
  if (overlaySidebars) return "overlay";
  return width - rightWidth - 32 >= 200 ? "dock" : "left";
}

interface VirtualKeyboardLike extends EventTarget {
  overlaysContent: boolean;
  readonly boundingRect: DOMRect;
}

function useKeyboardInset(): void {
  useEffect(() => {
    const root = document.documentElement;
    const vv = window.visualViewport ?? null;
    let frame: number | null = null;
    let vk: VirtualKeyboardLike | undefined;

    const readInset = (): number => {
      if (vk) {
        try {
          return virtualKeyboardInset(
            window.innerHeight,
            vk.boundingRect.top,
            vk.boundingRect.height,
          );
        } catch {
          disableVk();
        }
      }
      if (!vv) return 0;
      return visualViewportInset(window.innerHeight, vv.height, vv.offsetTop);
    };

    const keepFocusedControlVisible = (inset: number) => {
      if (inset <= 0) return;
      const active = document.activeElement;
      if (!(active instanceof HTMLElement) || !isEditableTarget(active)) return;
      const visibleBottom = window.innerHeight - inset;
      for (let node = active.parentElement; node; node = node.parentElement) {
        const overflow = active.getBoundingClientRect().bottom - visibleBottom;
        if (overflow <= 0) return;
        const style = getComputedStyle(node);
        if (/(auto|scroll|overlay)/.test(style.overflowY) && node.scrollHeight > node.clientHeight + 1) {
          node.scrollTop += overflow + 8;
        }
      }
      if (active.getBoundingClientRect().bottom > visibleBottom) {
        active.scrollIntoView({ block: "nearest" });
      }
    };

    const publish = () => {
      frame = null;
      const inset = readInset();
      root.style.setProperty("--nx-kb-inset", `${Math.round(inset)}px`);
      keepFocusedControlVisible(inset);
    };
    const schedule = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(publish);
    };

    function disableVk() {
      if (!vk) return;
      try {
        vk.overlaysContent = false;
      } catch {}
      try {
        vk.removeEventListener("geometrychanged", schedule);
      } catch {}
      vk = undefined;
    }

    try {
      const candidate = (navigator as Navigator & { virtualKeyboard?: VirtualKeyboardLike })
        .virtualKeyboard;
      if (candidate) {
        candidate.overlaysContent = true;
        candidate.addEventListener("geometrychanged", schedule);
        vk = candidate;
      }
    } catch {
      vk = undefined;
    }
    vv?.addEventListener("resize", schedule);
    vv?.addEventListener("scroll", schedule);
    window.addEventListener("resize", schedule);
    document.addEventListener("focusin", schedule, true);
    schedule();
    return () => {
      if (frame !== null) cancelAnimationFrame(frame);
      disableVk();
      vv?.removeEventListener("resize", schedule);
      vv?.removeEventListener("scroll", schedule);
      window.removeEventListener("resize", schedule);
      document.removeEventListener("focusin", schedule, true);
      root.style.removeProperty("--nx-kb-inset");
    };
  }, []);
}

export function dialogLevelForKind(kind?: string): "info" | "warning" {
  return kind === "info" ? "info" : "warning";
}

function useAiToastInset(active: boolean): void {
  useEffect(() => {
    const root = document.documentElement;
    if (!active) {
      root.style.removeProperty("--nx-ai-toast-max");
      return;
    }
    const measure = () => {
      const input = document.querySelector(".nx-right-dock textarea");
      if (!input) return;
      const top = input.getBoundingClientRect().top;
      if (top <= 0) return;
      const stack = parseFloat(getComputedStyle(root).getPropertyValue("--nx-header-stack")) || 36;
      root.style.setProperty("--nx-ai-toast-max", `${Math.max(140, Math.round(top - 12 - stack - 8))}px`);
    };
    measure();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : null;
    const dock = document.querySelector(".nx-right-dock");
    const input = document.querySelector(".nx-right-dock textarea");
    if (dock) observer?.observe(dock);
    if (input) observer?.observe(input);
    window.addEventListener("resize", measure);
    window.visualViewport?.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
      window.visualViewport?.removeEventListener("resize", measure);
      root.style.removeProperty("--nx-ai-toast-max");
    };
  }, [active]);
}

// LazyAuthGate loads the account gate only under WEB, so the desktop does not pull the auth store into env mocks of terminal tests.
function LazyAuthGate() {
  const [gate, setGate] = useState<React.ComponentType | null>(null);
  useEffect(() => {
    if (!WEB) return;
    let alive = true;
    void import("../features/auth/AuthGate").then((m) => {
      if (alive) setGate(() => m.AuthGate);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!gate) return null;
  const Gate = gate;
  return <Gate />;
}

// LazyDevicesView likewise: device management reaches env through the auth store, so lazy loading keeps it out of the desktop test module graph.
function LazyDevicesView() {
  const [view, setView] = useState<React.ComponentType | null>(null);
  useEffect(() => {
    let alive = true;
    void import("../features/fleet/DevicesView").then((m) => {
      if (alive) setView(() => m.DevicesView);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!view) return null;
  const View = view;
  return <View />;
}

// Account-level auth-transition cleanup lives in ./accountSwitch so it can be exercised
// without mounting the shell; see wireAccountSwitchCleanup for the query-key set and ordering.
type SyncStatusWithHealth = import("../ipc/types").SyncStatus & {
  warnings?: string[];
  paused?: boolean;
  vaultLocked?: boolean;
};

type SyncPauseReason = "locked" | "uninitialized";

function syncPauseReason(error: unknown): SyncPauseReason | null {
  if (typeof error === "object" && error !== null) {
    const code = (error as { code?: unknown }).code;
    if (code === "vault_locked") return "locked";
    if (code === "vault_not_init") return "uninitialized";
  }
  const message = describeError(error);
  if (message.includes("凭据库已锁定")) return "locked";
  if (message.includes("凭据库尚未初始化") || message.includes("凭据库未初始化")) return "uninitialized";
  return null;
}

function syncErrorSignature(error: unknown): string {
  const code = typeof error === "object" && error !== null ? (error as { code?: unknown }).code : undefined;
  return `${typeof code === "string" ? code : ""}:${describeError(error)}`;
}

function syncTime(value: string | number | null | undefined): number | null {
  if (value === null || value === undefined || value === "") return null;
  const parsed = typeof value === "number" ? value : Date.parse(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

interface WebSyncHealth {
  error: string | null;
  pauseReason: SyncPauseReason | null;
  warnings: string[];
  lastAttemptAt: number | null;
  lastSuccessAt: number | null;
}

export default function App() {
  return (
    <ErrorBoundary title="应用界面发生错误">
      <AppShell />
    </ErrorBoundary>
  );
}

function AppShell() {
  const {
    workspaces,
    activeWorkspaceId,
    activeProject,
    projectInventory,
    setProjectInventory,
    setActiveWorkspace,
    closeWorkspace,
    splitWorkspace,
    unsplitWorkspace,
    setActivePane,
    setSplitRatio,
    sessions,
    setSessions,
    leftOpen,
    setLeftOpen: setLeftOpenStore,
    leftMode,
    setLeftMode,
    rightOpen,
    setRightOpen: setRightOpenStore,
    leftWidth,
    setLeftWidth,
    rightWidth,
    setRightWidth,
    aiAccountEpoch,
    accountEpoch,
    toasts,
    dismissToast,
    pushToast,
    connectFocusRevision,
    reconcileDeletedTargets,
  } = useUi();
  const closedTabEntries = useClosedTabs((s) => s.stack.entries);

  const visibleWorkspaces = useMemo(
    () => focusedWorkspaces({ workspaces, activeWorkspaceId, activeProject, projectInventory }),
    [workspaces, activeWorkspaceId, activeProject, projectInventory],
  );
  const ws = useActiveWorkspace();
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tabs = pane?.tabs ?? [];
  const activeTabId = pane?.activeTabId ?? null;

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteEditingAsset, setPaletteEditingAsset] = useState<Asset | null>(null);
  const [quickConnectMode, setQuickConnectMode] = useState<"all" | "database" | null>(null);
  const [layoutRestore, setLayoutRestore] = useState(layoutRestoreStatus);
  const [restoringWorkspaceId, setRestoringWorkspaceId] = useState<string | null>(null);

  useEffect(() => subscribeLayoutRestore(() => setLayoutRestore(layoutRestoreStatus())), []);
  const queryClient = useQueryClient();
  // The status bar vault state subscribes directly to the shared ["vault-status"] query, same source as the settings page;
  // no more forked copy maintained via local state + sequence guards.
  const vaultQuery = useQuery({ queryKey: ["vault-status"], queryFn: () => vaultApi.status() });
  const vaultStatus = vaultQuery.data
    ? {
        uninitialized: !vaultQuery.data.initialized,
        passwordless: vaultQuery.data.passwordless,
        text: !vaultQuery.data.initialized
          ? "凭据库未初始化"
          : vaultQuery.data.passwordless
            ? "已启用 · 未设密码"
            : vaultQuery.data.unlocked
              ? "凭据库已解锁"
              : "凭据库已锁定",
      }
    : vaultQuery.isError
      ? { text: "凭据库不可用", uninitialized: false, passwordless: false }
      : { text: "…", uninitialized: false, passwordless: false };
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list() });
  const groupsQuery = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    enabled: activeProject !== ALL_PROJECTS,
  });
  useEffect(() => {
    if (activeProject === ALL_PROJECTS || !assetsQuery.data || !groupsQuery.data) return;
    setProjectInventory({ assets: assetsQuery.data, groups: groupsQuery.data });
  }, [activeProject, assetsQuery.data, groupsQuery.data, setProjectInventory]);
  // The authoritative assets refresh drives deletedTarget: workspaces whose
  // target disappeared are marked and preserved (never auto-disconnected),
  // and the mark clears when the target reappears. The store's epoch guard
  // keeps a snapshot from a previous account from mislabeling.
  useEffect(() => {
    if (!assetsQuery.data) return;
    reconcileDeletedTargets(assetsQuery.data, accountEpoch);
  }, [assetsQuery.data, accountEpoch, reconcileDeletedTargets]);
  const [vaultProtectionSeq, setVaultProtectionSeq] = useState(0);
  const syncLink = useQuery({
    queryKey: ["sync-link"],
    queryFn: () => syncApi.linkGet(),
    enabled: !WEB,
  });
  const syncStatus = useQuery({
    queryKey: ["sync-status"],
    queryFn: async () => (await syncApi.status()) as SyncStatusWithHealth,
    enabled: !WEB,
  });
  const [webSyncHealth, setWebSyncHealth] = useState<WebSyncHealth>({
    error: null, pauseReason: null, warnings: [], lastAttemptAt: null, lastSuccessAt: null,
  });
  const webErrorSignatureRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    if (!WEB) return;
    const onError = (event: Event) => {
      const cause = (event as CustomEvent<unknown>).detail;
      const pauseReason = syncPauseReason(cause);
      const message = describeError(cause);
      const signature = syncErrorSignature(cause);
      setWebSyncHealth((previous) => ({
        error: pauseReason ? null : message,
        pauseReason,
        warnings: [],
        lastAttemptAt: Date.now(),
        lastSuccessAt: previous.lastSuccessAt,
      }));
      if (pauseReason) {
        webErrorSignatureRef.current.clear();
      } else if (!webErrorSignatureRef.current.has(signature)) {
        webErrorSignatureRef.current.add(signature);
        pushToast("error", `同步失败：${message}`);
      }
    };
    const onResult = (event: Event) => {
      const result = (event as CustomEvent<WebSyncResult>).detail;
      if (!result || typeof result !== "object") return;
      const completedAt = syncTime(result.lastSuccessAt) ?? Date.now();
      webErrorSignatureRef.current.clear();
      setWebSyncHealth({
        error: null,
        pauseReason: null,
        warnings: Array.isArray(result.warnings) ? result.warnings : [],
        lastAttemptAt: completedAt,
        lastSuccessAt: completedAt,
      });
    };
    window.addEventListener(WEB_SYNC_ERROR_EVENT, onError);
    window.addEventListener(WEB_SYNC_RESULT_EVENT, onResult);
    return () => {
      window.removeEventListener(WEB_SYNC_ERROR_EVENT, onError);
      window.removeEventListener(WEB_SYNC_RESULT_EVENT, onResult);
    };
  }, [pushToast]);

  const lockVault = useCallback(async () => {
    if (vaultStatus.uninitialized) {
      pushToast("info", "凭据库尚未初始化");
      return;
    }
    if (vaultStatus.passwordless) {
      pushToast("info", "空密码凭据库无需锁定");
      return;
    }
    try {
      await vaultApi.lock();
      void queryClient.invalidateQueries({ queryKey: ["vault-status"] });
      pushToast("info", "凭据库已锁定");
    } catch (e) {
      pushToast("error", describeError(e));
    }
  }, [queryClient, pushToast, vaultStatus.uninitialized, vaultStatus.passwordless]);
  useWebAutoSync();
  useDesktopAutoSync();
  useEffect(() => {
    if (WEB) {
      void import("../features/auth/store").then((m) => m.useAuth.getState().refresh());
    }
  }, []);
  const linkConfigured = !!(syncLink.data?.url && syncLink.data.username && syncLink.data.hasPassword);
  const syncConfigured = linkConfigured || !!syncStatus.data?.configured;
  const vaultLocked = !!vaultQuery.data?.initialized && !vaultQuery.data.unlocked;
  const desktopPauseReason: SyncPauseReason | null = syncStatus.data?.paused || syncStatus.data?.vaultLocked
    ? syncStatus.data.vaultLocked || vaultLocked ? "locked" : "uninitialized"
    : syncConfigured && vaultLocked
      ? "locked"
      : syncPauseReason(syncStatus.error);
  const pauseReason = WEB ? webSyncHealth.pauseReason : desktopPauseReason;
  const lastError = WEB
    ? webSyncHealth.error
    : syncStatus.data?.lastError ?? syncLink.data?.lastError ?? "";
  const lastAttemptAt = WEB ? webSyncHealth.lastAttemptAt : syncTime(syncStatus.data?.lastAttemptAt);
  const lastSuccessAt = WEB ? webSyncHealth.lastSuccessAt : syncTime(syncStatus.data?.lastSuccessAt);
  const syncFailed = !pauseReason && !!lastError &&
    (lastSuccessAt === null || lastAttemptAt === null || lastAttemptAt >= lastSuccessAt);
  const syncStatusText = WEB
    ? pauseReason
      ? `同步已暂停：${pauseReason === "locked" ? "凭据库锁定" : "凭据库未初始化"}`
      : syncFailed
        ? "同步失败"
        : "服务端：浏览器模式"
    : pauseReason && syncConfigured
      ? `同步已暂停：${pauseReason === "locked" ? "凭据库锁定" : "凭据库未初始化"}`
      : syncFailed
        ? "同步失败"
        : syncLink.isError && !syncStatus.data
          ? "同步：不可用"
          : syncLink.data === undefined && !syncStatus.data
            ? "同步：…"
            : syncConfigured
              ? "同步：已配置"
              : "同步：本地模式";
  const syncStatusTitle = syncFailed
    ? `${lastError}${lastAttemptAt ? ` · 上次尝试：${formatTime(lastAttemptAt)}` : ""}`
    : [
        lastSuccessAt ? `上次成功：${formatTime(lastSuccessAt)}` : null,
        lastAttemptAt ? `上次尝试：${formatTime(lastAttemptAt)}` : null,
      ].filter(Boolean).join(" · ") || undefined;
  const syncWarnings = WEB ? webSyncHealth.warnings : syncStatus.data?.warnings ?? [];
  const showSyncWarnings = syncWarnings.length > 0 && !pauseReason;
  const syncStatusClass = syncFailed ? "text-red-300" : pauseReason && (WEB || syncConfigured) ? "text-amber-300" : "";
  const [wsMenu, setWsMenu] = useState<ContextMenuState | null>(null);
  const viewport = useWorkspaceViewport();
  useKeyboardInset();
  const landscapeFull = useLandscapeFullScreen();
  // In landscape fullscreen the width often exceeds the overlay threshold; force sidebars into overlay to avoid squeezing the fullscreen terminal.
  const overlaySidebars = viewport.overlaySidebars || landscapeFull;
  const [overlayDock, setOverlayDock] = useState<"left" | "right" | null>(null);
  const leftDockOpen = overlaySidebars ? overlayDock === "left" && leftOpen : leftOpen;
  const rightDockOpen = overlaySidebars ? overlayDock === "right" && rightOpen : rightOpen;
  useAiToastInset(rightDockOpen);

  const setLeftOpen = useCallback(
    (open: boolean) => {
      setLeftOpenStore(open);
      setOverlayDock(overlaySidebars && open ? "left" : null);
    },
    [setLeftOpenStore, overlaySidebars],
  );
  const setRightOpen = useCallback(
    (open: boolean) => {
      setRightOpenStore(open);
      setOverlayDock(overlaySidebars && open ? "right" : null);
    },
    [setRightOpenStore, overlaySidebars],
  );

  useEffect(() => {
    if (!overlaySidebars) return;
    setOverlayDock(null);
  }, [connectFocusRevision, overlaySidebars]);

  useEdgeGestures({
    overlayDock,
    openLeft: () => setLeftOpen(true),
    openRight: () => setRightOpen(true),
    closeOverlay: () => setOverlayDock(null),
  });

  const active = tabs.find((t) => t.id === activeTabId) ?? null;
  const ActiveWsIcon = ws ? workspaceIcon(ws) : null;

  // On phones, entering a terminal tab auto-collapses the sidebar; switching back to a non-terminal tab restores it. Only the session-level overlayDock is touched:
  // leftOpen/rightOpen are user-persisted and synced across devices; writing them would lose preferences and propagate to the other end.
  const rememberedOverlayDock = useRef<"left" | "right" | null>(null);
  const prevActiveKind = useRef<string | null>(null);
  const activeTabKind = active?.kind ?? null;
  const isTerminalPaneKind = activeTabKind === "terminal";
  useEffect(() => {
    if (!isMobile() || !overlaySidebars) return;
    const prev = prevActiveKind.current;
    prevActiveKind.current = activeTabKind;
    if (activeTabKind === prev) return;
    if (isTerminalPaneKind) {
      rememberedOverlayDock.current = overlayDock;
      if (overlayDock) setOverlayDock(null);
    } else if (rememberedOverlayDock.current) {
      const mem = rememberedOverlayDock.current;
      rememberedOverlayDock.current = null;
      setOverlayDock(mem);
    }
  }, [activeTabKind, isTerminalPaneKind, overlaySidebars, overlayDock]);
  const terminalKeysVisible =
    viewport.terminalKeys &&
    (ws?.panes.some((p) => {
      const current = p.tabs.find((t) => t.id === p.activeTabId) ?? p.tabs[p.tabs.length - 1];
      return current?.kind === "terminal";
    }) ?? false);
  const activeSessionId = ws?.kind === "session" ? ws.sessionId : undefined;
  const activeSession = sessions.find((session) => session.id === activeSessionId);
  const backgroundAvailable = useBackgroundSessionsAccess(ws);
  const backgroundCount = useBackgroundSessionCount(ws, backgroundAvailable);
  const connectedList = sessions.filter((s) => s.status === "connected");
  const connectedSessions = connectedList.length;
  const idleSessions = connectedList.filter(
    (s) => s.kind !== "remote" && (s.tabs?.length ?? 0) === 0,
  ).length;

  const needSession = useCallback(() => {
    pushToast("info", "先连接一台主机（双击左侧资产）");
  }, [pushToast]);

  const openLocalTerminal = useCallback(async () => {
    if (isMobile()) {
      pushToast("info", "移动端不支持本地终端，请双击资产连接远程主机");
      return;
    }
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    try {
      const s = await sessionApi.connectLocal();
      if (useUi.getState().workspaceRuntimeEpoch !== epoch) return;
      const list = useUi.getState().sessions;
      setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  }, [pushToast, setSessions]);

  const openFiles = useCallback(() => {
    const sessionId = ws?.sessionId;
    if (!sessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`files-${sessionId}`),
      kind: "files",
      title: "文件",
      sessionId,
      closable: true,
    });
  }, [ws?.sessionId, needSession]);

  const openMount = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`mount-${activeSessionId}`),
      kind: "mount",
      title: "磁盘挂载",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openForward = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`forward-${activeSessionId}`),
      kind: "forward",
      title: "端口转发",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openDocker = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`docker-${activeSessionId}`),
      kind: "docker",
      title: "容器",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openDatabase = useCallback(async () => {
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    try {
      const assets = (await assetApi.list()).filter((asset) => dbKindOf(asset.kind) !== null);
      if (useUi.getState().workspaceRuntimeEpoch !== epoch) return;
      if (assets.length === 0) {
        pushToast("info", "还没有数据库资产，先在资产树里新建一个");
        return;
      }
      if (assets.length === 1) {
        await connectAsset(assets[0]);
        return;
      }
      setQuickConnectMode("database");
    } catch (e) {
      if (useUi.getState().workspaceRuntimeEpoch === epoch) pushToast("error", describeError(e));
    }
  }, [pushToast]);

  const openAudit = useCallback(() => {
    useUi.getState().addTab({
      id: "audit",
      kind: "audit",
      title: "审计日志",
      closable: true,
    });
  }, []);

  const openDevices = useCallback(() => {
    useUi.getState().addTab({
      id: "devices",
      kind: "devices",
      title: "设备管理",
      closable: true,
    });
  }, []);

  const openSettings = useCallback(() => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  }, []);
  const openSyncSettings = useCallback(() => {
    openSettings();
    window.location.hash = "#settings-account";
    window.setTimeout(() => document.getElementById("settings-account")?.scrollIntoView({ block: "start" }), 0);
  }, [openSettings]);

  useEffect(() => {
    if (window.location.hash === "#settings-account") openSettings();
  }, [openSettings]);

  // The session list auth guard (drop late responses / clear on logout) lives in the store's resyncSessions/resetSessions.
  // Vault state is cleared and refetched by resetQueries on the shared vault-status query above; nothing extra is maintained here.
  const openVaultProtection = useCallback(() => {
    openSettings();
    setVaultProtectionSeq((n) => n + 1);
  }, [openSettings]);

  useEffect(() => {
    if (vaultProtectionSeq === 0) return;
    const wsId = useUi.getState().activeWorkspaceId;
    if (!wsId) return;
    for (const panel of document.querySelectorAll<HTMLElement>(
      `#nx-ws-panel-${CSS.escape(wsId)} [role='tabpanel']:not(.hidden)`,
    )) {
      const card = [...panel.querySelectorAll<HTMLElement>(".nx-card-title")]
        .find((el) => el.textContent?.trim() === "凭据保护")
        ?.closest<HTMLElement>(".nx-card");
      if (!card) continue;
      card.scrollIntoView({ block: "start" });
      break;
    }
  }, [vaultProtectionSeq]);

  const openBackground = useCallback(() => {
    if (ws?.kind !== "session" || !ws.sessionId) return needSession();
    if (!backgroundAvailable) return;
    useUi.getState().addTab({
      id: `background-${ws.id}`,
      kind: "background",
      title: "后台会话",
      sessionId: ws.sessionId,
      closable: true,
    });
  }, [ws?.id, ws?.kind, ws?.sessionId, backgroundAvailable, needSession]);

  const openHistory = useCallback(() => {
    useUi
      .getState()
      .addTab({ id: "history", kind: "history", title: "终端历史", closable: true });
  }, []);

  const openNewTerminal = useCallback(async () => {
    const epoch = useUi.getState().workspaceRuntimeEpoch;
    const workspaceId = ws?.id;
    const workspaceAssetId = ws?.assetId;
    const isCurrent = () => {
      const state = useUi.getState();
      return state.workspaceRuntimeEpoch === epoch &&
        (!workspaceId || state.workspaces.some((workspace) => workspace.id === workspaceId && workspace.assetId === workspaceAssetId));
    };
    const sid = ws?.sessionId;
    const s = sid ? sessions.find((x) => x.id === sid) : undefined;
    if (s && ws) {
      if (isSessionReconnectPending(s.id)) {
        pushToast("info", "正在重连，连接成功后自动新建终端");
        return;
      }
      if (isSessionAlive(s.status)) {
        void openTerminalTab(s);
        return;
      }
      pushToast("info", "正在重连，连接成功后自动新建终端");
      try {
        const fresh = await reconnectWorkspaceSession(ws, s, { isCurrent });
        if (!isCurrent()) return;
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        if (isCurrent()) pushToast("error", `重新连接失败：${describeError(e)}`);
      }
      return;
    }
    if (ws?.assetId && ws.kind !== "db") {
      try {
        const fresh = await reconnectDeadHost(ws, { isCurrent });
        if (!isCurrent()) return;
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        if (isCurrent()) pushToast("error", `重新连接失败：${describeError(e)}`);
      }
      return;
    }
    if (isMobile()) {
      pushToast("info", "移动端不支持本地终端，请双击左侧资产连接远程主机");
      return;
    }
    void openLocalTerminal();
  }, [ws, sessions, setSessions, openLocalTerminal, pushToast]);

  const closeAllWorkspaces = useCallback(async () => {
    const state = useUi.getState();
    const activeId = state.activeWorkspaceId;
    const ids = state.workspaces
      .filter((workspace) => workspace.closable && workspace.id !== activeId)
      .map((workspace) => workspace.id);
    const active = state.workspaces.find((workspace) => workspace.id === activeId);
    if (active?.closable) ids.push(active.id);
    for (const id of ids) {
      if (!(await useUi.getState().closeWorkspace(id))) break;
    }
  }, []);

  const openWorkspaceSwitcher = useCallback(
    (anchor: HTMLElement) => {
      const rect = anchor.getBoundingClientRect();
      const items: MenuItem[] = visibleWorkspaces.map((w) => {
        const Icon = workspaceIcon(w);
        const session = sessions.find((s) => s.id === w.sessionId);
        const tabCount = w.panes.flatMap((p) => p.tabs).length;
        return {
          kind: "item",
          label: w.title,
          icon: <Icon size={12} />,
          hint: [
            w.kind === "tools" ? "工具" : null,
            w.panes.length > 1 ? "已分屏" : null,
            sessionReconnectStatusText(session) || (session ? sessionStatusText(session.status) : null),
            `${tabCount} 个标签`,
          ]
            .filter(Boolean)
            .join(" · "),
          onSelect: () => setActiveWorkspace(w.id),
          trailingAction: {
            label: `关闭工作区 ${w.title}`,
            icon: <IconClose size={12} />,
            disabled: !w.closable,
            onSelect: () => void closeWorkspace(w.id),
          },
        };
      });
      if (workspaces.length > 0) items.push({ kind: "separator" });
      if (workspaces.length > 0) {
        items.push({
          kind: "item",
          label: "关闭所有工作区",
          icon: <IconClose size={12} />,
          hint: `${workspaces.filter((workspace) => workspace.closable).length} 个可关闭`,
          danger: true,
          disabled: !workspaces.some((workspace) => workspace.closable),
          onSelect: () => void closeAllWorkspaces(),
        });
      }
      items.push({ kind: "separator" });
      const latestClosed = closedTabEntries[0];
      items.push({
        kind: "item",
        label: "恢复关闭的标签",
        icon: <IconHistory size={12} />,
        hint: latestClosed?.title,
        accel: `${modHint()}+Shift+T`,
        disabled: !latestClosed,
        onSelect: () => void restoreClosedTab(),
      });
      setWsMenu({ x: rect.left, y: rect.bottom + 4, title: "工作区", items });
    },
    [visibleWorkspaces, workspaces, sessions, setActiveWorkspace, closeWorkspace, closeAllWorkspaces, closedTabEntries],
  );

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const hit = matchAppKeybinding(e);
      if (!hit) {
        // Restore-closed-tab is a fixed Mod+Shift+T (same as the switcher menu accel); it is
        // intentionally outside the rebindable keybinding set.
        if ((e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key.toLowerCase() === "t") {
          e.preventDefault();
          void restoreClosedTab();
        }
        return;
      }
      switch (hit.action) {
        case "commandPalette":
          e.preventDefault();
          setPaletteOpen(true);
          return;
        case "quickConnect":
          e.preventDefault();
          setQuickConnectMode("all");
          return;
        case "toggleSidebar":
          e.preventDefault();
          setLeftOpen(!leftDockOpen);
          return;
        case "toggleAiSidebar":
          e.preventDefault();
          setRightOpen(!rightDockOpen);
          return;
        case "newTerminal":
          e.preventDefault();
          void openNewTerminal();
          return;
        case "nextWorkspace":
          if (cycleActiveWorkspace(1)) e.preventDefault();
          return;
        case "previousWorkspace":
          if (cycleActiveWorkspace(-1)) e.preventDefault();
          return;
        case "nextTab":
          if (cycleActiveTab(1)) e.preventDefault();
          return;
        case "previousTab":
          if (cycleActiveTab(-1)) e.preventDefault();
          return;
        case "focusUpperPane":
          if (cycleActivePane(-1)) e.preventDefault();
          return;
        case "focusLowerPane":
          if (cycleActivePane(1)) e.preventDefault();
          return;
        case "toggleSplit": {
          e.preventDefault();
          const cur = useUi.getState();
          const id = cur.activeWorkspaceId;
          const target = id ? cur.workspaces.find((w) => w.id === id) : undefined;
          if (!target) return;
          if (target.panes.length > 1) void cur.unsplitWorkspace(target.panes[1].id, target.id);
          else if (viewport.splitAllowed) cur.splitWorkspace(target.id);
          return;
        }
        case "closeTab":
          if (!activeTabId) return;
          e.preventDefault();
          void requestCloseTab(activeTabId);
          return;
        case "lockVault": {
          e.preventDefault();
          void lockVault();
          return;
        }
        case "switchTab": {
          if (hit.digit === null) return;
          const st = useUi.getState();
          const current = st.workspaces.find((w) => w.id === st.activeWorkspaceId);
          const currentPane =
            current?.panes.find((p) => p.id === current.activePaneId) ?? current?.panes[0];
          const target = currentPane?.tabs[hit.digit - 1];
          if (!target) return;
          e.preventDefault();
          st.setActiveTab(target.id);
          return;
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    leftDockOpen,
    rightDockOpen,
    activeTabId,
    setLeftOpen,
    setRightOpen,
    openLocalTerminal,
    openNewTerminal,
    lockVault,
    viewport.splitAllowed,
  ]);

  useEffect(() => {
    registerPromptHandler((message, value, options) => {
      const { openTextPrompt } = useUi.getState();
      return new Promise<string | null>((resolve) => {
        openTextPrompt({ message, value, ...options, resolve });
      });
    });
    registerDialogEpochSource(() => useUi.getState().accountEpoch);
  }, []);

  useEffect(() => {
    registerDialogHandlers({
      ask: (message, options) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "ask",
            message,
            title: options?.title,
            level: dialogLevelForKind(options?.kind),
            resolve,
          });
        }),
      confirm: (message) =>
        new Promise<boolean>((resolve) => {
          useUi.getState().openAppDialog({
            kind: "confirm",
            message,
            level: "warning",
            resolve,
          });
        }),
      message: (message) =>
        new Promise<void>((resolve) => {
          useUi
            .getState()
            .openAppDialog({ kind: "message", message, level: "info", resolve: () => resolve() });
        }),
      choose: (message, options) =>
        new Promise<ChoiceResult | null>((resolve) => {
          useUi.getState().openAppChoice({
            title: options?.title ?? "请选择",
            message,
            options: options?.choices ?? [],
            level: options?.level ?? "info",
            rememberLabel: options?.rememberLabel,
            resolve: (key, remember) => resolve(key === null ? null : { key, remember }),
          });
        }),
    });
  }, []);

  useEffect(() => {
    startLayoutSync();
    startUnreadTracking();
  }, []);

  // Check silently at desktop startup and every four hours: no toast; UpdateBanner presents any update.
  // After each check completes, prompt once per version with dedup gating (see updatePrompt).
  useEffect(() => {
    if (TRANSPORT !== "desktop") return;
    const check = () => {
      void getUpdateState()
        .check()
        .then(() => promptUpdateIfNeeded());
    };
    check();
    const interval = window.setInterval(check, 4 * 60 * 60 * 1000);
    return () => window.clearInterval(interval);
  }, []);

  useEffect(() => {
    void useUi.getState().resyncSessions({ silent: true });
  }, []);

  useEffect(() => {
    let disposed = false;
    let inFlight = false;
    let queued = false;
    const refresh = () => {
      if (disposed) return;
      if (inFlight) {
        queued = true;
        return;
      }
      inFlight = true;
      const finish = () => {
        inFlight = false;
        if (!queued || disposed) return;
        queued = false;
        refresh();
      };
      void useUi.getState().resyncSessions({ silent: true }).then(finish, finish);
    };
    const offAssetData = onAssetDataChanged(refresh);
    const subscriptions: (() => void)[] = [];
    for (const topic of ["sync://status", "sync://round-complete"]) {
      try {
        void Promise.resolve(listenEvent(topic, refresh)).then((off) => {
          if (typeof off !== "function") return;
          if (disposed) off();
          else subscriptions.push(off);
        }).catch((error: unknown) => {
          console.warn("[NexTerm] Failed to subscribe asset session refresh", error);
        });
      } catch (error) {
        console.warn("[NexTerm] Failed to subscribe asset session refresh", error);
      }
    }
    return () => {
      disposed = true;
      queued = false;
      offAssetData();
      for (const off of subscriptions) off();
    };
  }, []);

  // With auth=on, when login/register/init brings the gate to ready, account-level requests issued while mounted behind the gate
  // all hit 401; the wiring resets those query keys and clears account-scoped caches on switches. Toasts are not all cleared on ready:
  // the 8s/3.5s auto-dismiss already bounds their lifetime, and terminal/file/AI and non-401 errors remain valid feedback after login.
  useEffect(() => wireAccountSwitchCleanup(queryClient), [queryClient]);

  useEffect(() => {
    if (bootLocalTried) return;
    bootLocalTried = true;
    void (async () => {
      try {
        await layoutBootstrapped;
        if (layoutRestoreStatus().pending || layoutRestoreStatus().preview) return;
        const list = await assetApi.list();
        if (list.some((a) => !a.builtin)) return;
        const builtin = list.find((a) => a.builtin && a.kind === "local");
        if (!builtin) return;
        if (useUi.getState().workspaces.length > 0) return;
        await connectAsset(builtin, { preserveUserFocus: true });
      } catch {
      }
    })();
  }, []);

  const bindings = useKeybindings();
  const kbLabel = (id: KeybindingActionId) => formatBinding(bindings[id]);

  const railPanels: {
    key: string;
    label: string;
    hint: string;
    icon: typeof IconServer;
    onClick: () => void;
  }[] = [
    {
      key: "assets",
      label: "资产",
      hint: "打开资产树：管理主机和数据库，双击资产直接连接；双击本按钮收起侧栏",
      icon: IconServer,
      onClick: () => {
        setLeftMode("assets");
        setLeftOpen(true);
      },
    },
    {
      key: "credentials",
      label: "凭据",
      hint: "打开凭据侧栏：集中保存密码和私钥，连接资产时引用；双击本按钮收起侧栏",
      icon: IconKey,
      onClick: () => {
        openCredentialsSidebar();
        setLeftOpen(true);
      },
    },
    {
      key: "files",
      label: "文件树",
      hint: "打开当前连接的文件树：浏览远程目录，双击文件直接编辑；双击本按钮收起侧栏",
      icon: IconFolderOpen,
      onClick: () => {
        if (!ws?.sessionId) return needSession();
        setLeftMode("files");
        setLeftOpen(true);
      },
    },
  ];

  const railActive = leftMode;

  const hasDbAssets = assetsQuery.data?.some((a) => dbKindOf(a.kind) !== null) ?? true;

  const railItems: {
    key: string;
    label: string;
    hint: string;
    icon: typeof IconServer;
    onClick: () => void;
    unavailableReason?: string;
    badge?: number;
  }[  ] = [
    ...(backgroundAvailable ? [{
      key: "background",
      label: "后台会话",
      hint: backgroundCount
        ? `${backgroundCount} 个终端在后台运行，可恢复到标签页`
        : "查看已转入后台的终端进程，并将其恢复到标签页",
      icon: IconActivity,
      onClick: openBackground,
      badge: backgroundCount ?? undefined,
    }] : []),
    {
      key: "history",
      label: "终端历史",
      hint: "回看和搜索终端的历史输出",
      icon: IconClock,
      onClick: openHistory,
    },
    {
      key: "docker",
      label: "容器",
      hint: "打开 Docker 面板：查看容器状态与日志，进入容器终端",
      icon: IconBox,
      onClick: openDocker,
    },
    ...(hasDbAssets
      ? [
          {
            key: "db",
            label: "数据库",
            hint: "打开数据库工作台：查询 MySQL / PostgreSQL / Redis 资产",
            icon: IconDatabase,
            onClick: () => void openDatabase(),
          },
        ]
      : []),
    ...(mountUnavailableReason()
      ? []
      : [{
          key: "mount",
          label: "磁盘挂载",
          hint: "把 SSH 资产的远程目录挂载到本地，像本地磁盘一样访问",
          icon: IconDrive,
          onClick: openMount,
        }]),
    {
      key: "forward",
      label: "端口转发",
      hint: "把远程端口转发到本地：本地端口映射或 SOCKS5 代理",
      icon: IconNetwork,
      onClick: openForward,
    },
  ];

  const railBottom = [
    { key: "devices", label: "设备管理", hint: "打开设备管理：查看已安装 agent 的设备并远程打开终端", icon: IconMonitor, onClick: openDevices },
    { key: "audit", label: "审计日志", hint: "查看用户和 AI 的操作记录", icon: IconHistory, onClick: openAudit },
    { key: "settings", label: "设置", hint: "打开设置：外观、快捷键、AI、凭据库与账号同步", icon: IconSettings, onClick: openSettings },
  ];

  const aiToast = aiToastPlacement(
    viewport.width,
    overlaySidebars,
    rightDockOpen,
    rightWidth,
  );

  return (
    <div
      className={`nx-app flex h-full flex-col ${viewport.compact ? "is-compact" : ""}`}
      data-nx-keys={terminalKeysVisible ? "true" : undefined}
      data-nx-ai={aiToast ?? undefined}
      data-nx-landscape-full={landscapeFull ? "true" : undefined}
      style={{ "--nx-right-w": `${rightWidth}px` } as CSSProperties}
      onDoubleClick={onDragRegionDoubleClick}
    >
      <a
        href="#nx-main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[100] focus:rounded focus:bg-neutral-800 focus:px-3 focus:py-2 focus:text-xs focus:text-neutral-100"
      >
        跳到主内容
      </a>
      <DeviceSessionBoundary />
      <ForwardStartupRecovery />

      <div className="flex min-h-0 flex-1">
        <nav
          className={`nx-rail ${TRANSPORT === "desktop" && isMac() ? "pt-[28px]" : ""}`}
          aria-label="主导航"
        >
          <div className="mb-2 flex items-center justify-center nx-rail-btn" data-tip="NexTerm">
            <Logo size={26} />
          </div>
          {railPanels.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              data-tip={it.hint}
              aria-label={it.label}
              aria-current={railActive === it.key ? "true" : undefined}
              onClick={it.onClick}
              onDoubleClick={() => setLeftOpen(false)}
            >
              <it.icon size={17} />
            </button>
          ))}
          <div className="nx-rail-sep" />
          {railItems.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${it.unavailableReason ? "is-unavailable" : ""}`}
              data-tip={
                it.unavailableReason ? `${it.hint}（暂不可用：${it.unavailableReason}）` : it.hint
              }
              aria-label={
                it.unavailableReason ? `${it.label}暂不可用：${it.unavailableReason}` : it.label
              }
              onClick={it.onClick}
            >
              <it.icon size={17} />
              {it.unavailableReason ? <span className="nx-rail-off" aria-hidden /> : null}
              {it.badge ? <span className="nx-rail-count" aria-hidden>{it.badge > 99 ? "99+" : it.badge}</span> : null}
            </button>
          ))}
          <div className="nx-spacer" />
          <div className="nx-rail-sep" />
          {railBottom.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              data-tip={it.hint}
              aria-label={it.label}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
        </nav>

        <div className="flex min-w-0 flex-1 flex-col">
          <header
            className={`nx-app-header flex h-[36px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-950 pr-2.5 ${
              TRANSPORT === "desktop" && isMac() ? "pl-[32px]" : "pl-3.5"
            }`}
            data-wails-drag-region style={wailsDragRegionStyle}
          >
            <span
              className="text-[12.5px] font-semibold tracking-tight text-neutral-100"
              data-wails-drag-region style={wailsDragRegionStyle}
            >
              NexTerm
            </span>
            {ws ? (
              <>
                <button
                  type="button"
                  className="nx-ws-switch"
                  style={wailsNoDragRegionStyle}
                  aria-haspopup="menu"
                  aria-label={`切换工作区，当前 ${ws.title}`}
                  title="切换工作区"
                  onClick={(event) => openWorkspaceSwitcher(event.currentTarget)}
                >
                  {ActiveWsIcon ? <ActiveWsIcon size={13} /> : null}
                  <span className="truncate">{ws.title}</span>
                  <WorkspaceStatusGlyph status={sessions.find((s) => s.id === ws.sessionId)?.status} />
                  <IconChevronDown size={12} />
                </button>
                <WorkspaceReconnectStatus session={activeSession} />
                <span
                  className="flex min-w-0 items-center gap-1.5 text-xs text-neutral-500"
                  data-wails-drag-region style={wailsDragRegionStyle}
                >
                  <span className="text-neutral-600" data-wails-drag-region style={wailsDragRegionStyle}>
                    /
                  </span>
                  <span
                    className="truncate font-medium text-neutral-300"
                    data-wails-drag-region style={wailsDragRegionStyle}
                  >
                    {active?.title ?? "工作区"}
                  </span>
                </span>
              </>
            ) : (
              <span
                className="nx-header-hint text-xs text-neutral-500"
                data-wails-drag-region style={wailsDragRegionStyle}
              >
                还没有工作区，双击左侧资产连接一台主机
              </span>
            )}
            <div className="nx-spacer" data-wails-drag-region style={wailsDragRegionStyle} />
            <button
              className={`nx-icon-btn ${rightDockOpen ? "is-active" : ""}`} style={wailsNoDragRegionStyle}
              title="AI 助手"
              aria-label="AI 助手"
              aria-pressed={rightDockOpen}
              onClick={() => setRightOpen(!rightDockOpen)}
            >
              <IconSparkles size={15} />
            </button>
            <button
              className="nx-icon-btn" style={wailsNoDragRegionStyle}
              title={`命令面板 (${kbLabel("commandPalette")})`}
              aria-label={`命令面板 (${kbLabel("commandPalette")})`}
              onClick={() => setPaletteOpen(true)}
            >
              <IconCommand size={15} />
            </button>
            {TRANSPORT === "desktop" && !isMac() && !isMobile() && (
              <div className="flex shrink-0 items-center gap-0.5 border-l border-neutral-800/60 pl-1.5">
                <button
                  className="nx-icon-btn" style={wailsNoDragRegionStyle}
                  title="最小化"
                  aria-label="最小化窗口"
                  onClick={() => void minimiseWindow()}
                >
                  <IconMinus size={13} />
                </button>
                <button
                  className="nx-icon-btn" style={wailsNoDragRegionStyle}
                  title="最大化 / 还原（双击空白处也可）"
                  aria-label="最大化或还原窗口"
                  onClick={() => void toggleMaximiseWindow()}
                >
                  <IconMaximize size={12} />
                </button>
                <button
                  className="nx-icon-btn is-danger" style={wailsNoDragRegionStyle}
                  title="关闭"
                  aria-label="关闭窗口"
                  onClick={() => void closeWindow()}
                >
                  <IconClose size={14} />
                </button>
              </div>
            )}
          </header>

          <ProjectWorkspaceStrip
            workspaces={workspaces}
            activeWorkspaceId={activeWorkspaceId}
            activeProject={activeProject}
            projectInventory={projectInventory}
            groups={groupsQuery.data ?? []}
            sessions={sessions}
            onSelect={setActiveWorkspace}
            onReorder={(dragId, overId, before) => {
              const current = useUi.getState();
              const from = current.workspaces.findIndex((workspace) => workspace.id === dragId);
              const over = current.workspaces.findIndex((workspace) => workspace.id === overId);
              if (from < 0 || over < 0) return;
              const to = before ? (from < over ? over - 1 : over) : from < over ? over : over + 1;
              current.moveWorkspace(from, to);
            }}
          />
          <TakeoverBanner />
          <UpdateBanner onOpenSettings={openSettings} />
          <DekUnlockBanner />
          {(layoutRestore.pending || layoutRestore.preview) && (
            <div role="status" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-blue-500/30 bg-blue-950/30 px-4 py-2 text-xs text-blue-100">
              <span>
                {layoutRestore.preview
                  ? `已预览账号中的 ${layoutRestore.count} 个工作区。请确认目标与标签，然后完成恢复或撤销。`
                  : `账号中有 ${layoutRestore.count} 个工作区待确认恢复；本设备暂不覆盖服务端布局。`}
              </span>
              {layoutRestore.preview ? (
                <>
                  <button type="button" className="nx-btn-ghost" onClick={() => void finishLayoutRestore()}>完成恢复</button>
                  <button type="button" className="nx-btn-ghost" onClick={undoLayoutRestore}>撤销恢复</button>
                </>
              ) : (
                <button type="button" className="nx-btn-ghost" onClick={() => void reviewPendingLayoutRestore()}>
                  查看并恢复
                </button>
              )}
            </div>
          )}
          <WorkspaceRestoreBanner />
          {ws?.deletedTarget && (
            <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-500/30 bg-amber-950/30 px-4 py-2 text-xs text-amber-300">
              <span>「{ws.title}」的资产已删除。现有标签可继续查看，但此工作区无法重新连接。</span>
              <button type="button" className="nx-btn-ghost" onClick={() => { setLeftMode("assets"); setLeftOpen(true); }}>
                选择其他资产
              </button>
              <button type="button" className="nx-btn-ghost" onClick={() => void closeWorkspace(ws.id)}>
                关闭工作区
              </button>
            </div>
          )}
          {ws?.restorePending && (
            <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-500/30 bg-amber-950/30 px-4 py-2 text-xs text-amber-300">
              <span>
                「{ws.title}」的标签已恢复，尚未连接。连接后会新开终端
                {ws.assetKind === "ssh" || ws.assetKind === "device" ? "；之前的持久终端可在「后台会话」接管" : ""}。
              </span>
              <button
                type="button"
                className="nx-btn-ghost"
                disabled={ws.deletedTarget || restoringWorkspaceId === ws.id}
                onClick={() => {
                  const workspaceId = ws.id;
                  setRestoringWorkspaceId(workspaceId);
                  void reconnectRestoredWorkspace(workspaceId).then((result) => {
                    if (!result.ok && result.error) pushToast("error", result.error);
                  }).finally(() => setRestoringWorkspaceId(null));
                }}
              >
                {restoringWorkspaceId === ws.id ? "连接中…" : "重新连接"}
              </button>
            </div>
          )}

        <div className="nx-workspace-body flex min-h-0 flex-1 bg-neutral-900">
          {overlaySidebars && (leftDockOpen || rightDockOpen) && (
            <button
              type="button"
              className="nx-dock-backdrop"
              aria-label="收起侧栏"
              onClick={() => setOverlayDock(null)}
            />
          )}
          {leftDockOpen && (
            <div className="nx-left-dock" role="navigation" aria-label="左栏">
              {leftMode === "credentials" ? (
                <CredentialsSidebar />
              ) : leftMode === "files" && ws?.sessionId ? (
                <FileTree key={ws.sessionId} sessionId={ws.sessionId} />
              ) : (
                // Keyed by account epoch: on account switch the tree remounts so template, runbook,
                // inventory discovery/watch, and bulk panels never show the previous account's state.
                <AssetTree key={accountEpoch} />
              )}
              <ResizeHandle
                side="left"
                width={leftWidth}
                min={LEFT_WIDTH_RANGE.min}
                max={LEFT_WIDTH_RANGE.max}
                defaultWidth={LEFT_WIDTH_RANGE.default}
                onChange={setLeftWidth}
              />
            </div>
          )}

          <main id="nx-main" tabIndex={-1} className="nx-workspace-main min-w-0 flex-1">
            {workspaces.length === 0 ? (
              sessions.length > 0 ? (
                <WorkspaceEmpty onNew={() => void openNewTerminal()} />
              ) : (
                <EmptyState onLocal={() => void openLocalTerminal()} onPalette={() => setPaletteOpen(true)} />
              )
            ) : (
              <>
                {visibleWorkspaces.length === 0 && (
                  <div role="status" className="flex h-full items-center justify-center px-6 text-center text-sm text-neutral-500">
                    当前项目没有打开的工作区。切换项目不会断开其他工作区。
                  </div>
                )}
                {workspaces.map((w) => {
                  const wsActive = w.id === ws?.id;
                  const split = w.panes.length > 1;
                  const groups = w.panes.map((p) => (
                    <PaneGroup
                      key={p.id}
                      pane={p}
                      idPrefix={`${w.id}-${p.id}`}
                      workspaceId={w.id}
                      active={wsActive && p.id === w.activePaneId}
                      visible={wsActive}
                      split={split}
                      canSplit={!split}
                      splitAllowed={viewport.splitAllowed}
                      onToggleSplit={() => {
                        if (split) void unsplitWorkspace(w.panes[1].id, w.id);
                        else if (viewport.splitAllowed) splitWorkspace(w.id);
                      }}
                      onActivate={() => setActivePane(p.id, w.id)}
                      onNewTerminal={() => void openNewTerminal()}
                      leftOpen={leftDockOpen}
                      onToggleLeft={() => setLeftOpen(!leftDockOpen)}
                    />
                  ));
                  return (
                    <div
                      key={w.id}
                      id={`nx-ws-panel-${w.id}`}
                      className={wsActive ? "h-full min-h-0" : "hidden"}
                    >
                      <SplitStack
                        ratio={w.splitRatio}
                        onRatio={(r) => setSplitRatio(r, w.id)}
                        top={groups[0]}
                        bottom={groups[1]}
                      />
                    </div>
                  );
                })}
              </>
            )}
          </main>

          <div className={`nx-right-dock ${rightDockOpen ? "" : "is-hidden"}`}>
            {rightDockOpen && (
              <ResizeHandle
                side="right"
                width={rightWidth}
                min={RIGHT_WIDTH_RANGE.min}
                max={RIGHT_WIDTH_RANGE.max}
                defaultWidth={RIGHT_WIDTH_RANGE.default}
                onChange={setRightWidth}
              />
            )}
            <AiSidebar key={aiAccountEpoch} sessionId={activeSessionId} tabId={active?.kind === "terminal" ? active.tabId : undefined} />
          </div>
        </div>

        <footer className="nx-statusbar flex h-[25px] shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950 px-3 text-[11px] text-neutral-500">
          <div className="nx-statusbar-info">
            <span className="flex items-center gap-1.5">
              <IconServer size={11} />
              <strong className="font-medium text-neutral-300">{sessions.length}</strong> 个会话
              <span className="text-neutral-700">·</span>
              <span
                className={
                  connectedSessions > 0
                    ? "flex items-center gap-1.5 text-green-300"
                    : "flex items-center gap-1.5"
                }
              >
                <span className="nx-dot" />
                <strong className="font-medium">{connectedSessions}</strong> 已连接
                {idleSessions > 0 ? (
                  <span className="text-neutral-500">（{idleSessions} 空闲）</span>
                ) : null}
              </span>
            </span>
            <span className="text-neutral-700">|</span>
            {vaultStatus.uninitialized ? (
              <button
                type="button"
                className="nx-statusbar-truncate flex items-center gap-1.5 text-amber-300 hover:text-[var(--nx-fg-warning)]"
                title="在「设置 → 凭据保护」中开启密码保护"
                onClick={openVaultProtection}
              >
                <IconLock size={11} />
                凭据库未初始化 · 去开启保护
              </button>
            ) : (
              <span className="nx-statusbar-truncate flex items-center gap-1.5" title={vaultStatus.text}>
                <IconLock size={11} />
                {vaultStatus.text}
              </span>
            )}
          </div>
          <div className="nx-statusbar-side">
            <span className={`nx-statusbar-truncate flex items-center gap-1.5 ${syncStatusClass}`} title={syncStatusTitle}>
              <IconNetwork size={11} />
              {syncStatusText}
            </span>
            {showSyncWarnings && (
              <button
                type="button"
                className="nx-badge nx-badge-amber shrink-0"
                title={syncWarnings.join("\n")}
                onClick={openSyncSettings}
              >
                同步警告 {syncWarnings.length}
              </button>
            )}
          </div>
        </footer>
      </div>

      </div>

      <div className="nx-toasts pointer-events-none fixed right-4 flex flex-col gap-2">
        {toasts.map((t) => {
          const Icon = t.kind === "error" ? IconXCircle : t.kind === "success" ? IconCheckCircle : IconInfo;
          const tone =
            t.kind === "error"
              ? "border-red-500/40 bg-red-950/90 text-red-100"
              : t.kind === "success"
                ? "nx-alert-success"
                : "border-neutral-700 bg-neutral-800/95 text-neutral-100";
          return (
            <div
              key={t.id}
              className={`pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[var(--shadow-pop)] backdrop-blur ${tone}`}
            >
              <button
                type="button"
                className="flex min-w-0 flex-1 items-start gap-2.5 text-left"
                onClick={() => dismissToast(t.id)}
              >
                <Icon
                  size={14}
                  className={
                    t.kind === "error"
                      ? "mt-px shrink-0 text-red-300"
                      : t.kind === "success"
                        ? "mt-px shrink-0 text-green-300"
                        : "mt-px shrink-0 text-blue-300"
                  }
                />
                <span className="min-w-0 flex-1">{t.text}</span>
              </button>
              {t.action ? (
                <button
                  type="button"
                  className="nx-btn nx-btn-ghost nx-btn-sm shrink-0"
                  onClick={() => {
                    dismissToast(t.id);
                    t.action?.onSelect();
                  }}
                >
                  {t.action.label}
                </button>
              ) : null}
            </div>
          );
        })}
      </div>

      {paletteOpen && (
        <CommandPalette
          onClose={() => setPaletteOpen(false)}
          onOpenFiles={openFiles}
          onEditAsset={(asset) => setPaletteEditingAsset(asset)}
          onQuickConnect={() => setQuickConnectMode("all")}
        />
      )}
      {quickConnectMode && (
        <QuickConnect
          kindFilter={quickConnectMode === "database" ? "database" : undefined}
          onClose={() => setQuickConnectMode(null)}
        />
      )}
      {paletteEditingAsset && (
        <AssetEditor
          kind="asset"
          initial={paletteEditingAsset}
          onClose={() => setPaletteEditingAsset(null)}
          onSaved={() => {
            setPaletteEditingAsset(null);
            void queryClient.invalidateQueries({ queryKey: ["assets"] });
            void queryClient.invalidateQueries({ queryKey: ["asset-search"] });
            void queryClient.invalidateQueries({ queryKey: ["credentials"] });
          }}
        />
      )}
      <ContextMenu state={wsMenu} onClose={() => setWsMenu(null)} />
      <LazyAuthGate />
      <PromptModal />
      <DialogHost />
    </div>
  );
}

interface PaneGroupProps {
  pane: Pane;
  idPrefix: string;
  workspaceId: string;
  active: boolean;
  visible: boolean;
  split: boolean;
  canSplit: boolean;
  splitAllowed: boolean;
  onToggleSplit: () => void;
  onActivate: () => void;
  onNewTerminal: () => void;
  leftOpen: boolean;
  onToggleLeft: () => void;
}

function PaneGroup({
  pane,
  idPrefix,
  workspaceId,
  active,
  visible,
  split,
  canSplit,
  splitAllowed,
  onToggleSplit,
  onActivate,
  onNewTerminal,
  leftOpen,
  onToggleLeft,
}: PaneGroupProps) {
  const setActiveTab = useUi((s) => s.setActiveTab);
  const moveTabInPane = useUi((s) => s.moveTabInPane);
  const moveTab = (fromIndex: number, toIndex: number) =>
    moveTabInPane(pane.id, fromIndex, toIndex);
  const activeTabId = pane.activeTabId ?? pane.tabs[pane.tabs.length - 1]?.id ?? null;
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropHint, setDropHint] = useState<{ id: string; edge: "before" | "after" } | null>(null);
  const bindings = useKeybindings();
  const kbLabel = (id: KeybindingActionId) => formatBinding(bindings[id]);

  const renameTab = async (id: string, title: string) => {
    let draft = title;
    let failure = "";
    for (;;) {
      const next = await promptText(failure || "重命名标签：", draft);
      if (next === null || !next.trim()) return;
      const trimmed = sanitizeRemoteTabTitle(next);
      if (!trimmed) return;
      const tabId = pane.tabs.find((t) => t.id === id)?.tabId;
      if (!tabId) {
        applyAuthoritativeTabTitle(id, trimmed);
        return;
      }
      try {
        const info = await terminalApi.renameTab(tabId, trimmed);
        applyAuthoritativeTabTitle(id, info.title || trimmed);
        return;
      } catch (error) {
        draft = trimmed;
        failure = `重命名失败：${describeError(error)}`;
      }
    }
  };

  const openTabMenu = (t: (typeof pane.tabs)[number], x: number, y: number) => {
    const items: MenuItem[] = [];
    if (t.kind === "terminal") {
      items.push({
        kind: "item",
        label: "重命名",
        icon: <IconEdit size={12} />,
        onSelect: () => void renameTab(t.id, t.title),
      });
    }
    const running = t.kind === "terminal" && Boolean(t.tabId) && !t.dead && !t.exited;
    items.push({
      kind: "item",
      label: "关闭标签",
      icon: <IconClose size={12} />,
      hint: closeActionHint(t),
      disabled: !t.closable,
      onSelect: () => void requestCloseTab(t.id),
    });
    const tabIndex = pane.tabs.findIndex((candidate) => candidate.id === t.id);
    items.push({
      kind: "item",
      label: "左移标签",
      icon: <IconChevronLeft size={12} />,
      accel: "Ctrl+Shift+←",
      disabled: tabIndex <= 0,
      onSelect: () => moveTab(tabIndex, tabIndex - 1),
    });
    items.push({
      kind: "item",
      label: "右移标签",
      icon: <IconChevronRight size={12} />,
      accel: "Ctrl+Shift+→",
      disabled: tabIndex < 0 || tabIndex >= pane.tabs.length - 1,
      onSelect: () => moveTab(tabIndex, tabIndex + 1),
    });
    const closableLeft = pane.tabs.slice(0, Math.max(0, tabIndex)).filter((candidate) => candidate.closable);
    const closableRight = pane.tabs.slice(tabIndex + 1).filter((candidate) => candidate.closable);
    items.push({
      kind: "item",
      label: "关闭左侧全部标签",
      icon: <IconClose size={12} />,
      disabled: closableLeft.length === 0,
      onSelect: () => void requestCloseTabs(closableLeft.map((candidate) => candidate.id)),
    });
    items.push({
      kind: "item",
      label: "关闭右侧全部标签",
      icon: <IconClose size={12} />,
      disabled: closableRight.length === 0,
      onSelect: () => void requestCloseTabs(closableRight.map((candidate) => candidate.id)),
    });
    if (running) {
      items.push({ kind: "separator" });
      items.push({
        kind: "item",
        label: "结束进程…",
        icon: <IconStop size={12} />,
        hint: "终止进程，无法恢复",
        danger: true,
        onSelect: () => void requestKillTab(t.id),
      });
    }
    setMenu({ x, y, title: t.title, items });
  };

  return (
    <div
      className="flex h-full min-h-0 flex-col"
      onMouseDown={() => {
        if (!active) onActivate();
      }}
    >
      <div
        className={`nx-tabstrip is-sub ${split ? "is-split" : ""} ${
          active ? "is-active-pane" : ""
        }`}
      >
        <div className="nx-tabstrip-scroll">
        {pane.tabs.length === 0 && (
          <span className="px-1 text-xs text-neutral-500">这个面板还没有标签</span>
        )}
        <div role="tablist" aria-label="标签页" className="flex items-center gap-[3px]">
          {pane.tabs.map((t) => {
            const Icon = tabIcon(t);
            const isActive = t.id === activeTabId;
            return (
              <TabStripItem key={t.id} tab={t}>
                {(unread) => (
                  <div
                role="tab"
                data-tab-id={t.id}
                id={`nx-tab-${idPrefix}-${t.id}`}
                aria-selected={isActive}
                aria-controls={`nx-tab-panel-${idPrefix}-${t.id}`}
                tabIndex={isActive ? 0 : -1}
                draggable
                className={`nx-tab ${isActive ? "is-active" : ""}${
                  dropHint?.id === t.id ? ` nx-drop-${dropHint.edge}` : ""
                }${draggingId === t.id ? " nx-drag-source" : ""}`}
                onClick={() => {
                  if (draggingId === t.id) return;
                  setActiveTab(t.id);
                }}
                onDragStart={(e) => {
                  tabDragSource = { tabId: t.id, paneId: pane.id };
                  setDraggingId(t.id);
                  e.dataTransfer.setData(TAB_DRAG_MIME, t.id);
                  e.dataTransfer.effectAllowed = "move";
                }}
                onDragEnd={() => {
                  tabDragSource = null;
                  setDraggingId(null);
                  setDropHint(null);
                }}
                onDragOver={(e) => {
                  if (!e.dataTransfer.types.includes(TAB_DRAG_MIME)) return;
                  if (tabDragSource?.paneId !== pane.id || draggingId === t.id) return;
                  e.preventDefault();
                  e.dataTransfer.dropEffect = "move";
                  const rect = e.currentTarget.getBoundingClientRect();
                  const edge = e.clientX < rect.left + rect.width / 2 ? "before" : "after";
                  setDropHint((hint) => (hint?.id === t.id && hint.edge === edge ? hint : { id: t.id, edge }));
                }}
                onDragLeave={() => {
                  setDropHint((hint) => (hint?.id === t.id ? null : hint));
                }}
                onDrop={(e) => {
                  if (!e.dataTransfer.types.includes(TAB_DRAG_MIME)) return;
                  if (tabDragSource?.paneId !== pane.id) return;
                  e.preventDefault();
                  setDropHint(null);
                  const dragId = e.dataTransfer.getData(TAB_DRAG_MIME);
                  if (!dragId || dragId === t.id) return;
                  const from = pane.tabs.findIndex((x) => x.id === dragId);
                  const over = pane.tabs.findIndex((x) => x.id === t.id);
                  if (from < 0 || over < 0) return;
                  const rect = e.currentTarget.getBoundingClientRect();
                  const before = e.clientX < rect.left + rect.width / 2;
                  moveTab(from, before ? (from < over ? over - 1 : over) : from < over ? over : over + 1);
                }}
                onKeyDown={(e) => {
                  if ((e.key === "F10" && e.shiftKey) || e.key === "ContextMenu") {
                    e.preventDefault();
                    const rect = e.currentTarget.getBoundingClientRect();
                    openTabMenu(t, rect.left, rect.bottom);
                    return;
                  }
                  handleTablistKeyDown(
                    e,
                    pane.tabs.map((x) => x.id),
                    t.id,
                    setActiveTab,
                    moveTab,
                  );
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  openTabMenu(t, e.clientX, e.clientY);
                }}
                title={tabTooltip(t, unread)}
              >
                <span className="relative flex shrink-0">
                  <Icon size={13} />
                  {t.kind === "terminal" && t.durable === true && (
                    <span
                      className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-emerald-400"
                      role="img"
                      aria-label="持久终端"
                    />
                  )}
                </span>
                <TabStripTitle tab={t} />
                {t.kind === "terminal" && <TabProductionBadge sessionId={t.sessionId} />}
                {t.closeError && (
                  <span className="flex shrink-0 items-center text-red-400" role="img" aria-label={`关闭失败：${t.closeError}`}>
                    <IconAlert size={11} />
                  </span>
                )}
                {unread && (
                  <span
                    className="h-1.5 w-1.5 shrink-0 rounded-full bg-sky-400"
                    role="img"
                    aria-label="有新输出"
                  />
                )}
                {t.closable && (
                  <button
                    type="button"
                    className="nx-tab-close"
                    aria-label={`关闭标签 ${t.title}`}
                    title={closeTabHint(t)}
                    onClick={(e) => {
                      e.stopPropagation();
                      void requestCloseTab(t.id);
                    }}
                  >
                    <IconClose size={10} />
                  </button>
                )}
                  </div>
                )}
              </TabStripItem>
            );
          })}
        </div>
        </div>
        <button
          className="nx-tab-new"
          title={`新建终端标签 (${kbLabel("newTerminal")})`}
          aria-label="新建终端标签"
          onClick={onNewTerminal}
        >
          <IconPlus size={13} />
        </button>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          disabled={canSplit && !splitAllowed}
          title={
            canSplit
              ? splitAllowed
                ? `上下分屏 (${kbLabel("toggleSplit")})`
                : "窗口高度不足，无法上下分屏"
              : `取消分屏 (${kbLabel("toggleSplit")})`
          }
          aria-label={canSplit ? "上下分屏" : "取消分屏"}
          onClick={onToggleSplit}
        >
          {canSplit ? <IconSplitH size={14} /> : <IconMergeH size={14} />}
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={leftOpen ? `收起左栏 (${kbLabel("toggleSidebar")})` : `展开左栏 (${kbLabel("toggleSidebar")})`}
          aria-label={leftOpen ? "收起左栏" : "展开左栏"}
          onClick={onToggleLeft}
        >
          <IconChevronLeft size={15} className={leftOpen ? "" : "rotate-180"} />
        </button>
      </div>

      <div className="min-h-0 flex-1">
        {pane.tabs.length === 0 ? (
          <WorkspaceEmpty onNew={onNewTerminal} />
        ) : (
          pane.tabs.map((t) => (
            <div
              key={t.id}
              role="tabpanel"
              id={`nx-tab-panel-${idPrefix}-${t.id}`}
              aria-labelledby={`nx-tab-${idPrefix}-${t.id}`}
              className={t.id === activeTabId ? "h-full min-h-0" : "hidden"}
            >
              <ErrorBoundary
                onClose={t.closable ? () => void requestCloseTab(t.id) : undefined}
              >
                <PaneForTab
                  tab={t}
                  active={t.id === activeTabId && visible}
                  workspaceId={workspaceId}
                  onClose={requestCloseTab}
                />
              </ErrorBoundary>
            </div>
          ))
        )}
      </div>

      <ContextMenu state={menu} onClose={() => setMenu(null)} />
    </div>
  );
}

function SplitStack({
  ratio,
  onRatio,
  top,
  bottom,
}: {
  ratio: number;
  onRatio: (r: number) => void;
  top: ReactNode;
  bottom?: ReactNode;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);
  const [active, setActive] = useState(false);
  const currentRatio = clampSplitRatio(ratio);
  const ratioRef = useRef(currentRatio);
  ratioRef.current = currentRatio;
  const onRatioRef = useRef(onRatio);
  onRatioRef.current = onRatio;
  const framesRef = useRef<FrameCoalescer<number> | null>(null);
  if (!framesRef.current) {
    framesRef.current = createFrameCoalescer<number>((next) => onRatioRef.current(next));
  }

  useEffect(() => () => framesRef.current?.cancel(), []);

  const measure = (clientY: number) => {
    const rect = boxRef.current?.getBoundingClientRect();
    return rect
      ? splitRatioAt(clientY, rect, ratioRef.current)
      : ratioRef.current;
  };
  const finish = (clientY?: number) => {
    if (!dragging.current) return;
    dragging.current = false;
    setActive(false);
    if (clientY === undefined) framesRef.current?.flush();
    else framesRef.current?.flush(measure(clientY));
    window.dispatchEvent(new Event(RESIZE_END_EVENT));
  };
  const split = bottom != null;

  return (
    <div ref={boxRef} className="flex h-full min-h-0 flex-col">
      <div
        className="flex min-h-0 flex-col"
        style={split ? { flex: `0 0 calc(${currentRatio * 100}% - 3px)` } : { flex: "1 1 auto" }}
      >
        {top}
      </div>
      {split && (
        <>
          <div
            role="separator"
            tabIndex={0}
            aria-orientation="horizontal"
            aria-label="调整上下分屏比例"
            aria-valuemin={15}
            aria-valuemax={85}
            aria-valuenow={Math.round(currentRatio * 100)}
            aria-valuetext={`上栏 ${Math.round(currentRatio * 100)}%`}
            className={`nx-split-handle ${active ? "is-dragging" : ""}`}
            title="拖动调整上下比例（双击或回车复位，方向键微调）"
            onPointerDown={(event) => {
              if (event.button !== 0) return;
              event.preventDefault();
              event.currentTarget.setPointerCapture(event.pointerId);
              dragging.current = true;
              setActive(true);
            }}
            onPointerMove={(event) => {
              if (dragging.current) framesRef.current?.schedule(measure(event.clientY));
            }}
            onPointerUp={(event) => {
              finish(event.clientY);
              if (event.currentTarget.hasPointerCapture(event.pointerId)) {
                event.currentTarget.releasePointerCapture(event.pointerId);
              }
            }}
            onPointerCancel={() => finish()}
            onLostPointerCapture={() => finish()}
            onDoubleClick={() => {
              framesRef.current?.cancel();
              onRatioRef.current(0.5);
            }}
            onKeyDown={(event) => {
              const next = splitRatioForKey(ratioRef.current, event.key, event.shiftKey);
              if (next === null) return;
              event.preventDefault();
              onRatioRef.current(next);
            }}
          >
            <span className="nx-split-grip" />
          </div>
          <div className="flex min-h-0 flex-1 flex-col">{bottom}</div>
        </>
      )}
    </div>
  );
}

function isSessionAlive(status: string): boolean {
  return status === "connected" || status === "connecting" || status === "reconnecting";
}

// Shared manual-restore placeholder for restorePending tabs: nothing mounts and no
// connection is dialed until the user explicitly reconnects via reconnectRestoredTab.
function RestoredTabPlaceholder({ tab, workspaceId }: { tab: AppTab; workspaceId: string }) {
  const assetKind = useUi((state) => state.workspaces.find((workspace) => workspace.id === workspaceId)?.assetKind);
  const durableHost = tab.kind === "terminal" && (assetKind === "ssh" || assetKind === "device");
  const reopen = () => {
    void reconnectRestoredTab(tab.id).then((result) => {
      if (!result.ok && result.error) useUi.getState().pushToast("error", result.error);
    });
  };
  const openBackground = () => {
    void reconnectRestoredWorkspace(workspaceId).then((result) => {
      const state = useUi.getState();
      if (!result.ok) {
        if (result.error) state.pushToast("error", result.error);
        return;
      }
      const workspace = state.workspaces.find((item) => item.id === workspaceId);
      if (!workspace?.sessionId) return;
      state.addTab({ id: `background-${workspaceId}`, kind: "background", title: "后台会话", sessionId: workspace.sessionId, closable: true });
    });
  };
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
      <span>
        {tab.kind === "terminal"
          ? durableHost
            ? "原终端未连接。重新连接会新开一个 shell；若原终端是持久终端，可在「后台会话」接管。"
            : "原终端进程已结束，重新连接会新开一个 shell。"
          : "连接后重新打开此标签。"}
      </span>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <button type="button" className="nx-btn nx-btn-outline" onClick={reopen}>
          {tab.kind === "terminal" ? "新开终端" : "连接并打开"}
        </button>
        {durableHost && (
          <button type="button" className="nx-btn nx-btn-ghost" onClick={openBackground}>
            查看后台会话
          </button>
        )}
      </div>
    </div>
  );
}

const PaneForTab = memo(function PaneForTab({
  tab,
  active,
  workspaceId,
  onClose,
}: {
  tab: AppTab;
  active: boolean;
  workspaceId: string;
  onClose: (id: string) => Promise<void>;
}) {
  const workspaceRuntimeEpoch = useUi((state) => state.workspaceRuntimeEpoch);
  const restorePending = useUi((state) => state.workspaces.find((workspace) => workspace.id === workspaceId)?.restorePending);
  const sessions = useUi((state) => state.sessions);
  const close = () => void onClose(tab.id);
  if (restorePending) {
    return (
      <div className="flex h-full items-center justify-center px-6 text-center text-sm text-neutral-400">
        连接主机后继续。
      </div>
    );
  }
  if (tab.kind === "terminal" && tab.containerId && tab.restorePending) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-sm text-neutral-400">
        <span>容器终端不会自动重新执行，请从容器列表重新打开。</span>
        <button type="button" className="nx-btn nx-btn-outline" onClick={close}>关闭标签</button>
      </div>
    );
  }
  if (tab.restorePending && (tab.kind === "terminal" || tab.kind === "files" || tab.kind === "log")) {
    return <RestoredTabPlaceholder tab={tab} workspaceId={workspaceId} />;
  }
  switch (tab.kind) {
    case "terminal":
      return tab.sessionId ? (
        <TerminalPane
          sessionId={tab.sessionId}
          title={tab.title}
          containerId={tab.containerId}
          storeTabId={tab.id}
          resumeTabId={tab.tabId}
          visible={active}
        />
      ) : (
        <EmptyState />
      );
    case "mount":
      return <MountPanel sessionId={tab.sessionId} />;
    case "forward":
      return <ForwardPanel sessionId={tab.sessionId} />;
    case "files":
      if (!tab.sessionId) return <EmptyState />;
      return tab.path ? (
        <FileEditor sessionId={tab.sessionId} path={tab.path} onClose={close} />
      ) : (
        <FileBrowser sessionId={tab.sessionId} />
      );
    case "log":
      if (isMultiHostLogTabId(tab.id)) return <MultiHostLogTail sessions={sessions} />;
      if (isFleetHealthTabId(tab.id)) return <FleetHealthPanel sessions={sessions} visible={active} />;
      if (!tab.sessionId || !tab.path) return <EmptyState />;
      return <LogViewer sessionId={tab.sessionId} path={tab.path} onClose={close} />;
    case "docker":
      return tab.sessionId ? <DockerPanel sessionId={tab.sessionId} visible={active} /> : <EmptyState />;
    case "db":
      return tab.connId ? (
        <DbPanel
          key={workspaceRuntimeEpoch}
          connId={tab.connId}
          kind={tab.dbKind ?? "mysql"}
          workspaceId={workspaceId}
        />
      ) : (
        <EmptyState />
      );
    case "execBatch":
      return <ExecBatchPanel sessions={sessions} />;
    case "settings":
      return <SettingsView />;
    case "audit":
      return <AuditView />;
    case "devices":
      return <LazyDevicesView />;
    case "background":
      return (
        <BackgroundSessions
          visible={active}
          workspaceId={workspaceId}
          sessionId={tab.sessionId}
        />
      );
    case "history":
      return (
        <TranscriptHistoryPanel
          visible={active}
          workspaceId={workspaceId}
          assetId={tab.assetId}
        />
      );
    case "credentials":
      return <CredentialsPanel credId={tab.credId} />;
    default:
      return <EmptyState />;
  }
});

function WorkspaceEmpty({ onNew }: { onNew: () => void }) {
  return (
    <div className="flex h-full items-center justify-center bg-neutral-900 px-6">
      <div className="flex w-[300px] flex-col items-center gap-3.5 rounded-xl border border-neutral-800/70 bg-neutral-950/45 px-6 py-7 text-center">
        <div className="nx-empty-icon">
          <IconTerminal size={18} />
        </div>
        <div>
          <div className="text-[13.5px] font-semibold tracking-tight text-neutral-100">暂无终端</div>
          <div className="mt-1 text-[11.5px] text-neutral-500">创建一个新的终端会话</div>
        </div>
        <button className="nx-btn nx-btn-primary" onClick={onNew}>
          <IconPlus size={14} />
          新建终端
        </button>
        <div className="nx-hint">在左栏文件树中双击文件，可打开编辑器标签。</div>
      </div>
    </div>
  );
}

function EmptyState({ onLocal, onPalette }: { onLocal?: () => void; onPalette?: () => void }) {
  const { setSessions, sessions } = useUi();
  const bindings = useKeybindings();
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 bg-neutral-900 px-6">
      <Logo size={52} />
      <div className="text-center">
        <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
        <div className="mt-1 text-xs text-neutral-500">
          SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台
        </div>
      </div>
      <div className="flex items-center gap-2">
        {!isMobile() && (
          <button
            className="nx-btn nx-btn-primary"
            onClick={() => {
              if (onLocal) {
                onLocal();
                return;
              }
              void sessionApi.connectLocal().then((s) => {
                setSessions([...sessions.filter((x) => x.id !== s.id), s]);
                void openTerminalTab(s);
              });
            }}
          >
            <IconTerminal size={14} />
            打开本地终端
          </button>
        )}
        {onPalette && (
          <button className="nx-btn nx-btn-outline" onClick={onPalette}>
            <IconCommand size={14} />
            命令面板
          </button>
        )}
      </div>
      <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1 text-[11px] text-neutral-500">
        {(
          [
            ["newTerminal", "新建终端"],
            ["quickConnect", "快速连接"],
            ["commandPalette", "命令面板"],
            ["toggleSidebar", "左栏"],
            ["toggleAiSidebar", "AI 侧栏"],
          ] as const
        ).map(([id, label]) => (
          <span key={id} className="inline-flex items-center gap-1">
            <span className="nx-kbd">{formatBinding(bindings[id])}</span>
            {label}
          </span>
        ))}
      </div>
      <div className="nx-hint max-w-md text-center">
        双击左栏「当前设备」可打开本机终端和文件树；SSH / WinRM 资产打开终端标签，容器资产打开容器面板，MySQL / PostgreSQL / Redis 资产打开数据库工作台。
      </div>
    </div>
  );
}
