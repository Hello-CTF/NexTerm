import {
  useCallback,
  useEffect,
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
  openTerminalTab,
  nextTabId,
  connectAsset,
  openCredentialsSidebar,
  openCredentialsViewTab,
  requestCloseTab,
  requestKillTab,
  requestKillWorkspaceTerminals,
  closeTabHint,
  closeActionHint,
  countBlockedTerminals,
  isSessionReconnectPending,
  reconnectSessionAndWait,
  sessionStatusText,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  type AppTab,
  type Pane,
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
import { layoutBootstrapped, startLayoutSync } from "./layout";
import {
  formatBinding,
  matchAppKeybinding,
  useKeybindings,
  type KeybindingActionId,
} from "./keybindings";
import { connectWithHostKeyConfirm } from "./hostKeys";
import { assetApi, dbApi, sessionApi, syncApi, vaultApi, type Asset } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { DEMO, TRANSPORT, WEB } from "../demo";
import {
  coarsePointerMedia,
  isCoarsePointer,
  isMac,
  isWailsDragRegionTarget,
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
import { TerminalPane } from "../features/terminal/TerminalPane";
import { BackgroundSessions } from "../features/terminal/BackgroundSessions";
import { TranscriptHistoryPanel } from "../features/terminal/TranscriptHistoryPanel";
import { FileBrowser } from "../features/files/FileBrowser";
import { FileTree } from "../features/files/FileTree";
import { fileVisual } from "../features/files/fileTypes";
import { MountPanel } from "../features/files/MountPanel";
import { ForwardPanel } from "../features/forward/ForwardPanel";
import { FileEditor } from "../features/files/FileEditor";
import { DockerPanel } from "../features/docker/DockerPanel";
import { DbPanel } from "../features/db/DbPanel";
import { AiSidebar } from "../features/ai/AiSidebar";
import { SettingsView } from "../features/settings/SettingsView";
import { CredentialsPanel } from "../features/credentials/CredentialsPanel";
import { CredentialsSidebar } from "../features/credentials/CredentialsSidebar";
import { CredentialsView } from "../features/credentials/CredentialsView";
import { AuditView } from "../features/settings/AuditView";
import { CommandPalette } from "./CommandPalette";
import { QuickConnect } from "./QuickConnect";
import { TakeoverBanner } from "./TakeoverBanner";
import { PromptModal } from "../ui/PromptModal";
import { DialogHost, isEditableTarget } from "../ui/DialogHost";
import { ResizeHandle } from "../ui/ResizeHandle";
import { registerPromptHandler, registerDialogHandlers, promptText } from "../ui/dialogs";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../ui/ContextMenu";
import {
  IconActivity,
  IconBox,
  IconCheckCircle,
  IconChevronLeft,
  IconClock,
  IconClose,
  IconCode,
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
  mount: IconDrive,
  forward: IconNetwork,
  docker: IconBox,
  db: IconDatabase,
  credentials: IconKey,
  credentialsText: IconCode,
  settings: IconSettings,
  audit: IconHistory,
  devices: IconMonitor,
  deviceTerminal: IconTerminal,
  background: IconActivity,
  history: IconClock,
} as const;

function onDragRegionDoubleClick(event: MouseEvent<HTMLElement>): void {
  if (TRANSPORT !== "desktop") return;
  if (isWailsDragRegionTarget(event.target)) {
    void toggleMaximiseWindow();
  }
}

function workspaceIcon(w: Workspace) {
  if (w.kind === "db") return w.dbKind === "redis" ? IconLayers : IconDatabase;
  if (w.kind === "tools") return IconSettings;
  return assetIcon(w.assetKind ?? "ssh");
}

function tabIcon(t: AppTab) {
  if (t.kind === "files" && t.path) return fileVisual(t.path, "file").Icon;
  return TAB_ICON[t.kind] ?? IconTerminal;
}

function handleTablistKeyDown(
  event: ReactKeyboardEvent<HTMLElement>,
  ids: string[],
  currentId: string,
  activate: (id: string) => void,
): void {
  if (event.target !== event.currentTarget) return;
  const index = ids.indexOf(currentId);
  if (index < 0) return;
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
  const tab = event.currentTarget
    .closest("[role='tablist']")
    ?.querySelector<HTMLElement>(`[data-tab-id="${CSS.escape(nextId)}"]`);
  tab?.focus();
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
      const stack = parseFloat(getComputedStyle(root).getPropertyValue("--nx-header-stack")) || 70;
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

// LazyAuthGate 仅在 WEB/DEMO 下加载账号门,避免桌面端把 auth store(经 demo 引 env)拉进终端等测试的 env mock。
function LazyAuthGate() {
  const [gate, setGate] = useState<React.ComponentType | null>(null);
  useEffect(() => {
    if (!WEB && !DEMO) return;
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

// LazyDeviceTerminalBoundary 与 LazyAuthGate 同理懒加载; 它常驻 App 根部,
// 在 logout/401/账号切换时关闭全部设备终端标签 (FLEET156 账号隔离)。
function LazyDeviceTerminalBoundary() {
  const [view, setView] = useState<React.ComponentType | null>(null);
  useEffect(() => {
    if (!WEB) return;
    let alive = true;
    void import("../features/fleet/DeviceTerminalBoundary").then((m) => {
      if (alive) setView(() => m.DeviceTerminalBoundary);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!view) return null;
  const View = view;
  return <View />;
}

// LazyDevicesView 同理:设备管理经 auth store 引 demo/env,懒加载避免拖进桌面端测试的模块图。
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

// LazyDeviceTerminalView 与 LazyDevicesView 同一理由懒加载 (xterm + fleet API 只服务 WEB)。
function LazyDeviceTerminalView({ deviceId, visible }: { deviceId: string; visible: boolean }) {
  const [view, setView] = useState<React.ComponentType<{ deviceId: string; visible?: boolean }> | null>(null);
  useEffect(() => {
    let alive = true;
    void import("../features/fleet/DeviceTerminalView").then((m) => {
      if (alive) setView(() => m.DeviceTerminalView);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!view) return null;
  const View = view;
  return <View deviceId={deviceId} visible={visible} />;
}

// 账号级查询键, 与 AssetTree/CredentialsSidebar/CredentialsPanel/CredentialsView 的
// queryKey 对齐(资产/分组/凭据/片段/搜索/凭据库状态)。auth 转换时统一重置: 登录前
// 这些请求全部吃到 401(分组资产不显示), 换账号则不能把上一个会话的缓存漏给下一个。
const ACCOUNT_QUERY_KEYS = [
  ["assets"],
  ["groups"],
  ["credentials"],
  ["snippets"],
  ["asset-search"],
  ["vault-status"],
] as const;

export default function App() {
  const {
    workspaces,
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
    toasts,
    dismissToast,
    pushToast,
    connectFocusRevision,
  } = useUi();

  const ws = useActiveWorkspace();
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tabs = pane?.tabs ?? [];
  const activeTabId = pane?.activeTabId ?? null;

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteEditingAsset, setPaletteEditingAsset] = useState<Asset | null>(null);
  const [quickConnectOpen, setQuickConnectOpen] = useState(false);
  const queryClient = useQueryClient();
  const [vaultStatus, setVaultStatus] = useState<{ text: string; uninitialized: boolean }>({
    text: "…",
    uninitialized: false,
  });
  const [vaultProtectionSeq, setVaultProtectionSeq] = useState(0);
  const syncLink = useQuery({
    queryKey: ["sync-link"],
    queryFn: () => syncApi.linkGet(),
    enabled: !WEB,
  });
  useEffect(() => {
    if (WEB || DEMO) {
      void import("../features/auth/store").then((m) => m.useAuth.getState().refresh());
    }
  }, []);
  const syncStatusText = WEB
    ? "服务端：浏览器模式"
    : syncLink.isError
      ? "同步：不可用"
      : syncLink.data === undefined
        ? "同步：…"
        : syncLink.data?.url
          ? "同步：已配置"
          : "同步：本地模式";
  const [wsMenu, setWsMenu] = useState<ContextMenuState | null>(null);
  const viewport = useWorkspaceViewport();
  useKeyboardInset();
  const [overlayDock, setOverlayDock] = useState<"left" | "right" | null>(null);
  const leftDockOpen = viewport.overlaySidebars
    ? overlayDock === "left" && leftOpen
    : leftOpen;
  const rightDockOpen = viewport.overlaySidebars
    ? overlayDock === "right" && rightOpen
    : rightOpen;
  useAiToastInset(rightDockOpen);

  const setLeftOpen = useCallback(
    (open: boolean) => {
      setLeftOpenStore(open);
      setOverlayDock(viewport.overlaySidebars && open ? "left" : null);
    },
    [setLeftOpenStore, viewport.overlaySidebars],
  );
  const setRightOpen = useCallback(
    (open: boolean) => {
      setRightOpenStore(open);
      setOverlayDock(viewport.overlaySidebars && open ? "right" : null);
    },
    [setRightOpenStore, viewport.overlaySidebars],
  );

  useEffect(() => {
    if (!viewport.overlaySidebars) return;
    setOverlayDock(null);
  }, [connectFocusRevision, viewport.overlaySidebars]);

  const active = tabs.find((t) => t.id === activeTabId) ?? null;
  const terminalKeysVisible =
    viewport.terminalKeys &&
    (ws?.panes.some((p) => {
      const current = p.tabs.find((t) => t.id === p.activeTabId) ?? p.tabs[p.tabs.length - 1];
      return current?.kind === "terminal";
    }) ?? false);
  const activeSessionId = ws?.sessionId ?? sessions[0]?.id;

  const needSession = useCallback(() => {
    pushToast("info", "先连接一台主机（双击左侧资产）");
  }, [pushToast]);

  const openLocalTerminal = useCallback(async () => {
    try {
      const s = await sessionApi.connectLocal();
      const list = useUi.getState().sessions;
      setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s);
    } catch (e) {
      pushToast("error", describeError(e));
    }
  }, [pushToast, setSessions]);

  const openFiles = useCallback(() => {
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`files-${activeSessionId}`),
      kind: "files",
      title: "文件",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openMount = useCallback(() => {
    const unavailable = mountUnavailableReason() !== null;
    if (!unavailable && !activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`mount-${activeSessionId ?? "none"}`),
      kind: "mount",
      title: "磁盘挂载",
      sessionId: activeSessionId ?? undefined,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  const openForward = useCallback(() => {
    useUi.getState().addTab({
      id: nextTabId(`forward-${activeSessionId ?? "none"}`),
      kind: "forward",
      title: "端口转发",
      sessionId: activeSessionId ?? undefined,
      closable: true,
    });
  }, [activeSessionId]);

  const openDocker = useCallback(() => {
    if (!activeSessionId) return needSession();
    const name = sessions.find((s) => s.id === activeSessionId)?.name ?? "容器";
    useUi.getState().addTab({
      id: nextTabId(`docker-${activeSessionId}`),
      kind: "docker",
      title: `容器 · ${name}`,
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession, sessions]);

  const openDatabase = useCallback(async () => {
    try {
      const list = await assetApi.list();
      const asset = list.find((a) => a.kind === "mysql") ?? list.find((a) => a.kind === "redis");
      if (!asset) {
        pushToast("info", "还没有数据库资产，先在资产树里新建一个");
        return;
      }
      const { connId } = await dbApi.connect(asset.id);
      const kind = asset.kind === "redis" ? "redis" : "mysql";
      useUi.getState().addTab({
        id: `db-${connId}`,
        kind: "db",
        title: `${asset.name} · ${kind === "mysql" ? "SQL" : "Redis"}`,
        connId,
        dbKind: kind,
        closable: true,
      });
    } catch (e) {
      pushToast("error", describeError(e));
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

  const vaultStatusSeq = useRef(0);
  const refreshVaultStatus = useCallback(() => {
    const seq = ++vaultStatusSeq.current;
    void vaultApi
      .status()
      .then((v) => {
        if (seq !== vaultStatusSeq.current) return;
        setVaultStatus({
          uninitialized: !v.initialized,
          text: !v.initialized
            ? "凭据库未初始化"
            : v.unlocked
              ? "凭据库已解锁"
              : "凭据库已锁定",
        });
      })
      .catch(() => {
        if (seq !== vaultStatusSeq.current) return;
        setVaultStatus({ text: "凭据库不可用", uninitialized: false });
      });
  }, []);

  // 序号守卫: auth 转换(登录/登出)时在途旧请求可能迟到, 不允许覆盖新会话拉到的数据。
  const sessionsSeq = useRef(0);
  const refreshSessions = useCallback(() => {
    const seq = ++sessionsSeq.current;
    void sessionApi
      .list()
      .then((list) => {
        if (seq === sessionsSeq.current) setSessions(list);
      })
      .catch(() => undefined);
  }, [setSessions]);

  // 登出/会话过期时先递增两个序号使在途响应全部失效, 再清空状态: 旧账号的慢请求
  // 迟到后不得回写(否则 seq 未变, 迟到的旧数据会覆盖清空结果)。
  const clearSessionScopedState = useCallback(() => {
    sessionsSeq.current += 1;
    vaultStatusSeq.current += 1;
    setSessions([]);
    setVaultStatus({ text: "…", uninitialized: false });
  }, [setSessions]);

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
    useUi
      .getState()
      .addTab({ id: "background", kind: "background", title: "后台会话", closable: true });
  }, []);

  const openHistory = useCallback(() => {
    useUi
      .getState()
      .addTab({ id: "history", kind: "history", title: "终端历史", closable: true });
  }, []);

  const openNewTerminal = useCallback(async () => {
    const sid = ws?.sessionId;
    const s = sid ? sessions.find((x) => x.id === sid) : undefined;
    if (s) {
      if (isSessionReconnectPending(s.id)) {
        pushToast("info", "正在重连，连上后会自动新建终端");
        return;
      }
      if (isSessionAlive(s.status)) {
        void openTerminalTab(s);
        return;
      }
      pushToast("info", "正在重连，连上后自动新建终端…");
      try {
        const fresh = await reconnectSessionAndWait(s);
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        pushToast("error", `重连失败：${describeError(e)}`);
      }
      return;
    }
    if (ws?.assetId) {
      try {
        const fresh = await connectWithHostKeyConfirm(() => sessionApi.connect(ws.assetId as string));
        if (!fresh) {
          pushToast("info", "已取消重连");
          return;
        }
        const list = useUi.getState().sessions;
        setSessions([...list.filter((x) => x.id !== fresh.id), fresh]);
        await openTerminalTab(fresh);
      } catch (e) {
        pushToast("error", `重新连接失败：${describeError(e)}`);
      }
      return;
    }
    void openLocalTerminal();
  }, [ws?.sessionId, ws?.assetId, sessions, setSessions, openLocalTerminal, pushToast]);

  const openWsMenu = useCallback(
    (w: Workspace, x: number, y: number) => {
      const tabs = w.panes.flatMap((p) => p.tabs);
      const running = tabs.filter((t) => t.kind === "terminal" && t.tabId && !t.dead && !t.exited).length;
      const blocked = countBlockedTerminals(tabs, w.assetKind);
      const items: MenuItem[] = [
        {
          kind: "item",
          label: "关闭工作区",
          icon: <IconClose size={12} />,
          hint:
            blocked > 0
              ? `${running} 个运行中 · ${blocked} 个将结束进程`
              : "运行中的终端转入后台",
          onSelect: () => void closeWorkspace(w.id),
        },
        {
          kind: "item",
          label: "结束全部终端进程…",
          icon: <IconStop size={12} />,
          hint: running > 0 ? `${running} 个正在运行` : "没有运行中的终端",
          danger: true,
          disabled: running === 0,
          onSelect: () => void requestKillWorkspaceTerminals(w.id),
        },
      ];
      setWsMenu({ x, y, title: w.title, items });
    },
    [closeWorkspace],
  );

  const runSyncNow = useCallback(async () => {
    const { pushToast: toast } = useUi.getState();
    if (WEB || DEMO) {
      toast("info", "浏览器模式的同步在「设置 → 账号同步」里进行");
      return;
    }
    // types.ts 的 SyncLink 是 v1 残留;线上形状含 username/hasPassword,与设置页「立即同步」按钮同一 configured 判定。
    const link = syncLink.data as unknown as { url: string; username: string; hasPassword: boolean } | undefined;
    if (!link || link.url === "" || link.username === "" || !link.hasPassword) {
      toast("info", "同步还没配置：先到「设置 → 账号同步」里登录");
      return;
    }
    try {
      const r = (await syncApi.syncNow()) as unknown as { applied: number; pushed: number };
      const moved = r.applied + r.pushed;
      toast(moved > 0 ? "success" : "info", moved > 0 ? `同步完成：应用 ${r.applied} · 推送 ${r.pushed}` : "两边已经一致");
      void queryClient.invalidateQueries();
    } catch (e) {
      toast("error", `同步失败：${describeError(e)}`);
    }
  }, [queryClient, syncLink.data]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const hit = matchAppKeybinding(e);
      if (!hit) return;
      switch (hit.action) {
        case "commandPalette":
          e.preventDefault();
          setPaletteOpen(true);
          return;
        case "quickConnect":
          e.preventDefault();
          setQuickConnectOpen(true);
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
          void openLocalTerminal();
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
        case "syncNow": {
          e.preventDefault();
          void runSyncNow();
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
    runSyncNow,
    viewport.splitAllowed,
  ]);

  useEffect(() => {
    registerPromptHandler((message, value, options) => {
      const { openTextPrompt } = useUi.getState();
      return new Promise<string | null>((resolve) => {
        openTextPrompt({ message, value, ...options, resolve });
      });
    });
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
        new Promise<string | null>((resolve) => {
          useUi.getState().openAppChoice({
            title: options?.title ?? "请选择",
            message,
            options: options?.choices ?? [],
            level: options?.level ?? "info",
            resolve,
          });
        }),
    });
  }, []);

  useEffect(() => {
    startLayoutSync();
  }, []);

  useEffect(() => {
    refreshSessions();
    refreshVaultStatus();
  }, [refreshSessions, refreshVaultStatus]);

  // auth=on 登录/注册/初始化使 gate 进入 ready 时, 门后 mounted 期间发出的账号级请求全部
  // 吃到 401; resetQueries 会取消在途的旧 fetch 再重取(invalidateQueries 在 data 为空时会
  // 并入旧 fetch, 401 照样落账)。反方向(登出/会话过期回 login)重置同一组键并清空会话
  // 列表与凭据库状态, 避免上一个会话的数据漏给下一个账号。toast 不做 ready 全清:
  // 8s/3.5s 自动消失已是有限生命周期, 终端/文件/AI 与非 401 错误在登录后仍是有效反馈。
  useEffect(() => {
    if (!WEB || DEMO) return;
    let cancelled = false;
    let unsubscribe: (() => void) | undefined;
    void import("../features/auth/store").then((m) => {
      if (cancelled) return;
      unsubscribe = m.useAuth.subscribe((s, prev) => {
        if (s.gate === prev.gate) return;
        if (s.gate === "ready" && prev.gate !== "loading") {
          for (const key of ACCOUNT_QUERY_KEYS) void queryClient.resetQueries({ queryKey: key });
          refreshSessions();
          refreshVaultStatus();
        } else if (prev.gate === "ready" && s.gate === "login") {
          for (const key of ACCOUNT_QUERY_KEYS) void queryClient.resetQueries({ queryKey: key });
          clearSessionScopedState();
        }
      });
    });
    return () => {
      cancelled = true;
      unsubscribe?.();
    };
  }, [queryClient, refreshSessions, refreshVaultStatus, clearSessionScopedState]);

  useEffect(() => {
    if (!DEMO) return;
    let cancelled = false;
    void (async () => {
      try {
        await layoutBootstrapped;
        const list = await assetApi.list();
        const web = list.find((a) => a.name === "web-01");
        if (!web || cancelled) return;
        if (useUi.getState().workspaces.length > 0) return;
        await connectAsset(web);
        const s = await sessionApi.list();
        if (!cancelled) setSessions(s);
        useUi
          .getState()
          .pushToast("info", "演示模式：数据都是假的，随便点。终端里输入 help 看可用命令");
      } catch {
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [setSessions]);

  useEffect(() => {
    if (DEMO) return;
    if (bootLocalTried) return;
    bootLocalTried = true;
    void (async () => {
      try {
        await layoutBootstrapped;
        const list = await assetApi.list();
        if (list.some((a) => !a.builtin)) return;
        const builtin = list.find((a) => a.builtin && a.kind === "local");
        if (!builtin) return;
        if (useUi.getState().workspaces.length > 0) return;
        await connectAsset(builtin);
      } catch {
      }
    })();
  }, []);

  const crumb = [ws?.title ?? "未连接", active?.title ?? "工作区"];

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
      hint: "打开资产树：管理主机和数据库，双击资产直接连接",
      icon: IconServer,
      onClick: () => {
        setLeftMode("assets");
        setLeftOpen(true);
      },
    },
    {
      key: "credentials",
      label: "凭据",
      hint: "打开凭据侧栏：集中保存密码和私钥，连接资产时引用",
      icon: IconKey,
      onClick: () => {
        openCredentialsSidebar();
        setLeftOpen(true);
      },
    },
    {
      key: "files",
      label: "文件树",
      hint: "打开当前连接的文件树：浏览远程目录，双击文件直接编辑",
      icon: IconFolderOpen,
      onClick: () => {
        if (!ws?.sessionId) return needSession();
        setLeftMode("files");
        setLeftOpen(true);
      },
    },
  ];

  const railActive = leftMode;

  const railItems: {
    key: string;
    label: string;
    hint: string;
    icon: typeof IconServer;
    onClick: () => void;
    unavailableReason?: string;
  }[  ] = [
    {
      key: "terminal",
      label: "新建终端",
      hint: "新建终端标签：已连接就直接打开，断线会先重连；没有远程工作区时打开本机终端",
      icon: IconTerminal,
      onClick: openNewTerminal,
    },
    {
      key: "background",
      label: "后台会话",
      hint: "查看已转入后台的终端进程，随时接管回标签页",
      icon: IconActivity,
      onClick: openBackground,
    },
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
    {
      key: "db",
      label: "数据库",
      hint: "打开数据库工作台：查询 MySQL / Redis 资产",
      icon: IconDatabase,
      onClick: () => void openDatabase(),
    },
    {
      key: "mount",
      label: "磁盘挂载",
      hint: "把 SSH 资产的远程目录挂载到本地，像本地磁盘一样访问",
      icon: IconDrive,
      onClick: openMount,
      unavailableReason: mountUnavailableReason() ?? undefined,
    },
    {
      key: "forward",
      label: "端口转发",
      hint: "把远程端口转发到本地：本地端口映射或 SOCKS5 代理",
      icon: IconNetwork,
      onClick: openForward,
    },
  ];

  const railBottom = [
    { key: "ai", label: "AI 助手", hint: "打开 AI 侧栏：就当前终端向 AI 提问", icon: IconSparkles, onClick: () => setRightOpen(!rightDockOpen) },
    { key: "devices", label: "设备管理", hint: "打开设备管理：查看装了 agent 的设备，远程打开设备终端", icon: IconMonitor, onClick: openDevices },
    { key: "audit", label: "审计日志", hint: "查看用户和 AI 的操作记录", icon: IconHistory, onClick: openAudit },
    { key: "settings", label: "设置", hint: "打开设置：外观、快捷键、AI、凭据库与账号同步", icon: IconSettings, onClick: openSettings },
  ];

  const aiToast = aiToastPlacement(
    viewport.width,
    viewport.overlaySidebars,
    rightDockOpen,
    rightWidth,
  );

  return (
    <div
      className={`nx-app flex h-full flex-col ${viewport.compact ? "is-compact" : ""}`}
      data-nx-keys={terminalKeysVisible ? "true" : undefined}
      data-nx-ai={aiToast ?? undefined}
      style={{ "--nx-right-w": `${rightWidth}px` } as CSSProperties}
      onDoubleClick={onDragRegionDoubleClick}
    >
      <a
        href="#nx-main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[100] focus:rounded focus:bg-neutral-800 focus:px-3 focus:py-2 focus:text-xs focus:text-neutral-100"
      >
        跳到主内容
      </a>
      <TakeoverBanner />

      <div className="flex min-h-0 flex-1">
        <nav
          className={`nx-rail ${TRANSPORT === "desktop" && isMac() ? "pt-[28px]" : ""}`}
          aria-label="主导航"
        >
          <div className="mb-2 flex items-center justify-center" title="NexTerm">
            <Logo size={26} />
          </div>
          {railPanels.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              title={it.hint}
              aria-label={it.label}
              aria-current={railActive === it.key ? "true" : undefined}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
          <div className="my-1 h-px w-5 shrink-0 bg-neutral-800" />
          {railItems.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${it.unavailableReason ? "is-unavailable" : ""}`}
              title={
                it.unavailableReason ? `${it.hint}（暂不可用：${it.unavailableReason}）` : it.hint
              }
              aria-label={
                it.unavailableReason ? `${it.label}暂不可用：${it.unavailableReason}` : it.label
              }
              onClick={it.onClick}
            >
              <it.icon size={17} />
              {it.unavailableReason ? <span className="nx-rail-off" aria-hidden /> : null}
            </button>
          ))}
          <div className="nx-spacer" />
          <div className="my-1 h-px w-5 shrink-0 bg-neutral-800" />
          {railBottom.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              title={it.hint}
              aria-label={it.label}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
        </nav>

        <div className="flex min-w-0 flex-1 flex-col">
          <div
            className={`nx-tabstrip is-top ${TRANSPORT === "desktop" && isMac() ? "pl-[32px]" : ""}`}
            data-wails-drag-region style={wailsDragRegionStyle}
          >
            {workspaces.length === 0 && (
              <span className="px-1 text-xs text-neutral-500" data-wails-drag-region style={wailsDragRegionStyle}>
                还没有工作区，双击左侧资产连接一台主机
              </span>
            )}
            <div className="nx-tabstrip-scroll">
            <div
              role="tablist"
              aria-label="工作区"
              className="flex items-center gap-[3px]"
              style={wailsNoDragRegionStyle}
            >
              {workspaces.map((w) => {
                const Icon = workspaceIcon(w);
                const isActive = w.id === ws?.id;
                const status = sessions.find((s) => s.id === w.sessionId)?.status;
                const tone =
                  status === undefined
                    ? null
                    : status === "connected"
                      ? "is-ok"
                      : status === "failed"
                        ? "is-bad"
                        : "is-warn";
                return (
                  <div
                    key={w.id}
                    role="tab"
                    data-tab-id={w.id}
                    id={`nx-ws-tab-${w.id}`}
                    aria-selected={isActive}
                    aria-controls={`nx-ws-panel-${w.id}`}
                    tabIndex={isActive ? 0 : -1}
                    className={`nx-ws ${isActive ? "is-active" : ""}`}
                    style={wailsNoDragRegionStyle}
                    onClick={() => setActiveWorkspace(w.id)}
                    onContextMenu={(e) => {
                      e.preventDefault();
                      openWsMenu(w, e.clientX, e.clientY);
                    }}
                    onKeyDown={(e) =>
                      handleTablistKeyDown(
                        e,
                        workspaces.map((x) => x.id),
                        w.id,
                        setActiveWorkspace,
                      )
                    }
                    title={`${w.title} · ${w.panes.flatMap((p) => p.tabs).length} 个标签${
                      w.panes.length > 1 ? " · 已分屏" : ""
                    }${status ? ` · ${sessionStatusText(status)}` : ""}`}
                  >
                    <Icon size={13} />
                    <span className="truncate">{w.title}</span>
                    {tone && status && (
                      <span
                        className={`nx-ws-status ${tone}`}
                        role="img"
                        aria-label={sessionStatusText(status)}
                      >
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
                    )}
                    {w.closable && (
                      <button
                        type="button"
                        className="nx-tab-close"
                        style={wailsNoDragRegionStyle}
                        aria-label={`关闭工作区 ${w.title}`}
                        title={`关闭工作区（${
                          countBlockedTerminals(w.panes.flatMap((p) => p.tabs), w.assetKind) > 0
                            ? "部分终端将结束进程"
                            : "运行中的终端转入后台"
                        }）`}
                        onClick={(e) => {
                          e.stopPropagation();
                          void closeWorkspace(w.id);
                        }}
                      >
                        <IconClose size={10} />
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
            </div>
            <button
              className="nx-tab-new"
              style={wailsNoDragRegionStyle}
              title="新建工作区：在左侧资产树里双击一台主机"
              aria-label="新建工作区"
              onClick={() => {
                setLeftMode("assets");
                setLeftOpen(true);
                pushToast("info", "双击左侧资产，就会为这台主机开一个新的工作区");
              }}
            >
              <IconPlus size={13} />
            </button>
            <div className="nx-spacer min-w-0" data-wails-drag-region style={wailsDragRegionStyle} />
            {TRANSPORT === "desktop" && !isMac() && (
              <div className="sticky right-0 z-10 flex shrink-0 items-center gap-0.5 border-l border-neutral-800/60 bg-neutral-950 pl-1.5 pr-1.5">
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
                  className="nx-icon-btn is-danger"
                  style={wailsNoDragRegionStyle}
                  title="关闭"
                  aria-label="关闭窗口"
                  onClick={() => void closeWindow()}
                >
                  <IconClose size={14} />
                </button>
              </div>
            )}
          </div>

          <header
            className="flex h-[36px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-950 pr-2.5 pl-3.5"
            data-wails-drag-region style={wailsDragRegionStyle}
          >
            <span
              className="text-[12.5px] font-semibold tracking-tight text-neutral-100"
              data-wails-drag-region style={wailsDragRegionStyle}
            >
              NexTerm
            </span>
            <span
              className="flex min-w-0 items-center gap-1.5 text-xs text-neutral-500"
              data-wails-drag-region style={wailsDragRegionStyle}
            >
              <span className="truncate" data-wails-drag-region style={wailsDragRegionStyle}>
                {crumb[0]}
              </span>
              <span className="text-neutral-600" data-wails-drag-region style={wailsDragRegionStyle}>
                /
              </span>
              <span className="truncate font-medium text-neutral-300" data-wails-drag-region style={wailsDragRegionStyle}>
                {crumb[1]}
              </span>
            </span>
            <div className="nx-spacer" data-wails-drag-region style={wailsDragRegionStyle} />
            <button
              className="nx-icon-btn" style={wailsNoDragRegionStyle}
              title="刷新会话列表与凭据库状态"
              aria-label="刷新会话列表与凭据库状态"
              onClick={() => {
                refreshSessions();
                refreshVaultStatus();
              }}
            >
              <IconActivity size={15} />
            </button>
            <button
              className="nx-icon-btn" style={wailsNoDragRegionStyle}
              title={`命令面板 (${kbLabel("commandPalette")})`}
              aria-label={`命令面板 (${kbLabel("commandPalette")})`}
              onClick={() => setPaletteOpen(true)}
            >
              <IconCommand size={15} />
            </button>
          </header>

        <div className="nx-workspace-body flex min-h-0 flex-1 bg-neutral-900">
          {viewport.overlaySidebars && (leftDockOpen || rightDockOpen) && (
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
                <AssetTree />
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
                <WorkspaceEmpty onNew={openNewTerminal} />
              ) : (
                <EmptyState onLocal={openLocalTerminal} onPalette={() => setPaletteOpen(true)} />
              )
            ) : (
              workspaces.map((w) => {
                const wsActive = w.id === ws?.id;
                const split = w.panes.length > 1;
                const groups = w.panes.map((p) => (
                  <PaneGroup
                    key={p.id}
                    pane={p}
                    idPrefix={`${w.id}-${p.id}`}
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
                    onNewTerminal={openNewTerminal}
                    leftOpen={leftDockOpen}
                    onToggleLeft={() => setLeftOpen(!leftDockOpen)}
                  />
                ));
                return (
                  <div
                    key={w.id}
                    role="tabpanel"
                    id={`nx-ws-panel-${w.id}`}
                    aria-labelledby={`nx-ws-tab-${w.id}`}
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
              })
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
            <AiSidebar sessionId={activeSessionId} tabId={active?.kind === "terminal" ? active.tabId : undefined} />
          </div>
        </div>

        <footer className="nx-statusbar flex h-[25px] shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950 px-3 text-[11px] text-neutral-500">
          <div className="nx-statusbar-info">
            <span className="flex items-center gap-1.5">
              <IconServer size={11} />
              <strong className="font-medium text-neutral-300">{sessions.length}</strong> 个会话
            </span>
            <span className="text-neutral-700">|</span>
            <span
              className={
                sessions.some((s) => s.status === "connected")
                  ? "flex items-center gap-1.5 text-green-300"
                  : "flex items-center gap-1.5"
              }
            >
              <span className="nx-dot" />
              <strong className="font-medium">{sessions.filter((s) => s.status === "connected").length}</strong> 已连接
            </span>
            <span className="text-neutral-700">|</span>
            {vaultStatus.uninitialized ? (
              <button
                type="button"
                className="nx-statusbar-truncate flex items-center gap-1.5 text-amber-300 hover:text-amber-200"
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
            {DEMO && (
              <span className="nx-badge nx-badge-amber" title="数据来自内置假数据，未连接真实服务器">
                演示模式
              </span>
            )}
            <span className="nx-statusbar-truncate flex items-center gap-1.5">
              <IconNetwork size={11} />
              {syncStatusText}
            </span>
            <span className="text-neutral-700">|</span>
            <button className="nx-link" onClick={openAudit}>
              审计
            </button>
            <button className="nx-link" onClick={openSettings}>
              设置
            </button>
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
                ? "border-green-500/40 bg-[color-mix(in_srgb,var(--color-green-500)_18%,var(--nx-bg-pane))] text-green-300"
                : "border-neutral-700 bg-neutral-800/95 text-neutral-100";
          return (
            <button
              key={t.id}
              className={`pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[var(--shadow-pop)] backdrop-blur ${tone}`}
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
          );
        })}
      </div>

      {paletteOpen && (
        <CommandPalette
          onClose={() => setPaletteOpen(false)}
          onOpenFiles={openFiles}
          onEditAsset={(asset) => setPaletteEditingAsset(asset)}
          onQuickConnect={() => setQuickConnectOpen(true)}
        />
      )}
      {quickConnectOpen && <QuickConnect onClose={() => setQuickConnectOpen(false)} />}
      {paletteEditingAsset && (
        <AssetEditor
          kind="asset"
          initial={paletteEditingAsset}
          onClose={() => setPaletteEditingAsset(null)}
          onSaved={() => {
            setPaletteEditingAsset(null);
            void queryClient.invalidateQueries({ queryKey: ["assets"] });
            void queryClient.invalidateQueries({ queryKey: ["credentials"] });
          }}
        />
      )}
      <ContextMenu state={wsMenu} onClose={() => setWsMenu(null)} />
      <LazyAuthGate />
      <LazyDeviceTerminalBoundary />
      <PromptModal />
      <DialogHost />
    </div>
  );
}

interface PaneGroupProps {
  pane: Pane;
  idPrefix: string;
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
  const updateTab = useUi((s) => s.updateTab);
  const activeTabId = pane.activeTabId ?? pane.tabs[pane.tabs.length - 1]?.id ?? null;
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const bindings = useKeybindings();
  const kbLabel = (id: KeybindingActionId) => formatBinding(bindings[id]);

  const renameTab = async (id: string, title: string) => {
    const next = await promptText("重命名标签：", title);
    if (next === null || !next.trim()) return;
    updateTab(id, { title: next.trim(), renamedByUser: true });
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
              <div
                key={t.id}
                role="tab"
                data-tab-id={t.id}
                id={`nx-tab-${idPrefix}-${t.id}`}
                aria-selected={isActive}
                aria-controls={`nx-tab-panel-${idPrefix}-${t.id}`}
                tabIndex={isActive ? 0 : -1}
                className={`nx-tab ${isActive ? "is-active" : ""}`}
                onClick={() => setActiveTab(t.id)}
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
                  );
                }}
                onContextMenu={(e) => {
                  e.preventDefault();
                  openTabMenu(t, e.clientX, e.clientY);
                }}
                title={t.title}
              >
                <Icon size={13} />
                <span className="truncate">{t.title}</span>
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
              <PaneForTab
                tab={t}
                active={t.id === activeTabId && visible}
                onClose={() => void requestCloseTab(t.id)}
              />
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

function PaneForTab({
  tab,
  active,
  onClose,
}: {
  tab: AppTab;
  active: boolean;
  onClose: () => void;
}) {
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
        <FileEditor sessionId={tab.sessionId} path={tab.path} onClose={onClose} />
      ) : (
        <FileBrowser sessionId={tab.sessionId} />
      );
    case "docker":
      return tab.sessionId ? <DockerPanel sessionId={tab.sessionId} visible={active} /> : <EmptyState />;
    case "db":
      return tab.connId ? (
        <DbPanel connId={tab.connId} kind={tab.dbKind ?? "mysql"} />
      ) : (
        <EmptyState />
      );
    case "settings":
      return <SettingsView />;
    case "audit":
      return <AuditView />;
    case "devices":
      return <LazyDevicesView />;
    case "deviceTerminal":
      return tab.deviceId ? (
        <LazyDeviceTerminalView deviceId={tab.deviceId} visible={active} />
      ) : (
        <EmptyState />
      );
    case "background":
      return <BackgroundSessions visible={active} />;
    case "history":
      return <TranscriptHistoryPanel visible={active} />;
    case "credentials":
      return <CredentialsPanel credId={tab.credId} />;
    case "credentialsText":
      return (
        <CredentialsView view={tab.credView ?? "text"} onChange={openCredentialsViewTab} />
      );
    default:
      return <EmptyState />;
  }
}

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
        <div className="nx-hint">左栏文件树里双击文件，可以直接开编辑器标签</div>
      </div>
    </div>
  );
}

function EmptyState({ onLocal, onPalette }: { onLocal?: () => void; onPalette?: () => void }) {
  const { setSessions, sessions } = useUi();
  const bindings = useKeybindings();
  const shortcuts: [string, string][] = [
    [formatBinding(bindings.newTerminal), "本地终端"],
    [formatBinding(bindings.quickConnect), "快速连接"],
    [formatBinding(bindings.commandPalette), "命令面板"],
    [formatBinding(bindings.toggleSidebar), "资产树"],
    [formatBinding(bindings.toggleAiSidebar), "AI 侧栏"],
  ];
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 bg-neutral-900 px-6">
      <Logo size={52} />
      <div className="text-center">
        <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
        <div className="mt-1 text-xs text-neutral-500">
          SSH、WinRM、文件、Docker、数据库和 AI，集中在一个工作台
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-center gap-2">
        {shortcuts.map(([k, label]) => (
          <span key={k} className="nx-chip">
            <span className="nx-kbd">{k}</span>
            {label}
          </span>
        ))}
      </div>
      <div className="flex items-center gap-2">
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
        <button className="nx-btn nx-btn-outline" onClick={onPalette}>
          <IconCommand size={14} />
          命令面板
        </button>
      </div>
      <div className="nx-hint max-w-md text-center">
        左栏的「当前设备」双击即连（本机终端 + 文件树）；SSH / WinRM 资产开终端标签，
        容器资产开容器面板，MySQL / Redis 资产开数据库工作台。
      </div>
    </div>
  );
}
