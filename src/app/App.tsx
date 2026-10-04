import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
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
import { assetApi, dbApi, sessionApi, vaultApi } from "../ipc/commands";
import { describeError } from "../ui/errorText";
import { DEMO, TRANSPORT } from "../demo";
import {
  isMac,
  isWailsDragRegionTarget,
  modHint,
  wailsDragRegionStyle,
  wailsNoDragRegionStyle,
} from "./platform";
import {
  closeWindow,
  minimiseWindow,
  toggleMaximiseWindow,
} from "../ipc/wails";
import { mountUnavailableReason } from "./capabilities";
import { AssetTree } from "../features/explorer/AssetTree";
import { TerminalPane } from "../features/terminal/TerminalPane";
import { BackgroundSessions } from "../features/terminal/BackgroundSessions";
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
  IconLock,
  IconNetwork,
  IconPlus,
  IconSearch,
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
  background: IconActivity,
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
  const coarse = window.matchMedia?.("(pointer: coarse)").matches ?? false;
  return workspaceViewport(window.innerWidth, coarse, window.innerHeight);
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
    const coarseQuery = window.matchMedia?.("(pointer: coarse)");
    window.addEventListener("resize", update);
    window.visualViewport?.addEventListener("resize", update);
    coarseQuery?.addEventListener("change", update);
    update();
    return () => {
      frames.cancel();
      window.removeEventListener("resize", update);
      window.visualViewport?.removeEventListener("resize", update);
      coarseQuery?.removeEventListener("change", update);
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
  } = useUi();

  const ws = useActiveWorkspace();
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tabs = pane?.tabs ?? [];
  const activeTabId = pane?.activeTabId ?? null;

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [vaultStatus, setVaultStatus] = useState<string>("…");
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

  const openSettings = useCallback(() => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
  }, []);

  const openBackground = useCallback(() => {
    useUi
      .getState()
      .addTab({ id: "background", kind: "background", title: "后台会话", closable: true });
  }, []);

  const openNewTerminal = useCallback(async () => {
    const sid = ws?.sessionId;
    const s = sid ? sessions.find((x) => x.id === sid) : undefined;
    if (s && isSessionAlive(s.status)) {
      void openTerminalTab(s);
      return;
    }
    if (s) {
      try {
        const started = await sessionApi.reconnect(s.id);
        pushToast(
          started ? "info" : "error",
          started
            ? "连接已断开，正在重连…连上之后再点一次「新建终端」"
            : "重连未能启动",
        );
      } catch (e) {
        pushToast("error", `重连失败：${describeError(e)}`);
      }
      return;
    }
    if (ws?.assetId) {
      try {
        const fresh = await sessionApi.connect(ws.assetId);
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

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.shiftKey && e.key.toLowerCase() === "p") {
        e.preventDefault();
        setPaletteOpen(true);
      } else if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setPaletteOpen(true);
      } else if (mod && e.key.toLowerCase() === "b") {
        e.preventDefault();
        setLeftOpen(!leftDockOpen);
      } else if (mod && e.key.toLowerCase() === "j") {
        e.preventDefault();
        setRightOpen(!rightDockOpen);
      } else if (mod && e.key.toLowerCase() === "t") {
        e.preventDefault();
        void openLocalTerminal();
      } else if (mod && e.key === "\\") {
        e.preventDefault();
        const cur = useUi.getState();
        const id = cur.activeWorkspaceId;
        const target = cur.workspaces.find((w) => w.id === id);
        if (!target) return;
        if (target.panes.length > 1) void cur.unsplitWorkspace(target.panes[1].id, target.id);
        else if (viewport.splitAllowed) cur.splitWorkspace(target.id);
      } else if (mod && e.key.toLowerCase() === "w" && activeTabId) {
        e.preventDefault();
        void requestCloseTab(activeTabId);
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
    void sessionApi.list().then(setSessions).catch(() => undefined);
    void vaultApi
      .status()
      .then((v) => {
        setVaultStatus(
          !v.initialized ? "凭据库未初始化" : v.unlocked ? "凭据库已解锁" : "凭据库已锁定",
        );
      })
      .catch(() => setVaultStatus("凭据库不可用"));
  }, [setSessions]);

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
          .pushToast("info", "演示模式：数据都是假的，随便点 —— 终端里输入 help 看可用命令");
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

  const railPanels: {
    key: string;
    label: string;
    icon: typeof IconServer;
    onClick: () => void;
  }[] = [
    {
      key: "assets",
      label: "资产",
      icon: IconServer,
      onClick: () => {
        setLeftMode("assets");
        setLeftOpen(true);
      },
    },
    {
      key: "credentials",
      label: "凭据",
      icon: IconKey,
      onClick: () => {
        openCredentialsSidebar();
        setLeftOpen(true);
      },
    },
    {
      key: "files",
      label: "文件树",
      icon: IconFolderOpen,
      onClick: () => {
        if (!ws?.sessionId) return needSession();
        setLeftMode("files");
        setLeftOpen(true);
      },
    },
  ];

  const railActive = leftMode;
  const mod = modHint();

  const railItems: {
    key: string;
    label: string;
    icon: typeof IconServer;
    onClick: () => void;
    unavailableReason?: string;
  }[  ] = [
    { key: "terminal", label: "新建终端", icon: IconTerminal, onClick: openNewTerminal },
    {
      key: "background",
      label: "后台会话",
      icon: IconActivity,
      onClick: openBackground,
    },
    { key: "docker", label: "容器", icon: IconBox, onClick: openDocker },
    { key: "db", label: "数据库", icon: IconDatabase, onClick: () => void openDatabase() },
    {
      key: "mount",
      label: "磁盘挂载",
      icon: IconDrive,
      onClick: openMount,
      unavailableReason: mountUnavailableReason() ?? undefined,
    },
    { key: "forward", label: "端口转发", icon: IconNetwork, onClick: openForward },
  ];

  const railBottom = [
    { key: "ai", label: "AI 助手", icon: IconSparkles, onClick: () => setRightOpen(!rightDockOpen) },
    { key: "audit", label: "审计日志", icon: IconHistory, onClick: openAudit },
    { key: "settings", label: "设置", icon: IconSettings, onClick: openSettings },
  ];

  return (
    <div
      className={`nx-app flex h-full flex-col ${viewport.compact ? "is-compact" : ""}`}
      data-nx-keys={terminalKeysVisible ? "true" : undefined}
      onDoubleClick={onDragRegionDoubleClick}
    >
      <TakeoverBanner />

      <div className="flex min-h-0 flex-1">
        <nav className={`nx-rail ${TRANSPORT === "desktop" && isMac() ? "pt-[28px]" : ""}`}>
          <div className="mb-2 flex items-center justify-center" title="NexTerm">
            <Logo size={26} />
          </div>
          {railPanels.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              title={it.label}
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
                it.unavailableReason ? `${it.label}暂不可用：${it.unavailableReason}` : it.label
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
              title={it.label}
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
                还没有工作区 —— 双击左侧资产连接一台主机
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
                const dot =
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
                    }${
                      status ? ` · ${sessionStatusText(status)}` : ""
                    }${w.sessionId ? ` · ${w.sessionId}` : ""}`}
                  >
                    <Icon size={13} />
                    <span className="truncate">{w.title}</span>
                    {dot && <span className={`nx-ws-dot ${dot}`} />}
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
                void sessionApi.list().then(setSessions).catch(() => undefined);
                void vaultApi
                  .status()
                  .then((v) => {
                    setVaultStatus(
                      !v.initialized
                        ? "凭据库未初始化"
                        : v.unlocked
                          ? "凭据库已解锁"
                          : "凭据库已锁定",
                    );
                  })
                  .catch(() => undefined);
              }}
            >
              <IconActivity size={15} />
            </button>
            <button
              className="nx-icon-btn" style={wailsNoDragRegionStyle}
              title={`命令面板 (${mod}+Shift+P)`}
              aria-label={`命令面板 (${mod}+Shift+P)`}
              onClick={() => setPaletteOpen(true)}
            >
              <IconCommand size={15} />
            </button>
            <button
              className="nx-icon-btn" style={wailsNoDragRegionStyle}
              title={`全局搜索 (${mod}+K)`}
              aria-label={`全局搜索 (${mod}+K)`}
              onClick={() => setPaletteOpen(true)}
            >
              <IconSearch size={15} />
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
            <div className="nx-left-dock">
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

          <main className="nx-workspace-main min-w-0 flex-1">
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
          <span className="flex items-center gap-1.5">
            <IconLock size={11} />
            {vaultStatus}
          </span>
          <div className="nx-spacer" />
          {DEMO && (
            <span className="nx-badge nx-badge-amber" title="数据来自内置假数据，未连接真实服务器">
              演示模式
            </span>
          )}
          <span className="flex items-center gap-1.5">
            <IconNetwork size={11} />
            同步：本地模式
          </span>
          <span className="text-neutral-700">|</span>
          <button className="nx-link" onClick={openAudit}>
            审计
          </button>
          <button className="nx-link" onClick={openSettings}>
            设置
          </button>
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
        <CommandPalette onClose={() => setPaletteOpen(false)} onOpenFiles={openFiles} />
      )}
      <ContextMenu state={wsMenu} onClose={() => setWsMenu(null)} />
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
  const mod = modHint();

  const renameTab = async (id: string, title: string) => {
    const next = await promptText("重命名标签：", title);
    if (next === null || !next.trim()) return;
    updateTab(id, { title: next.trim() });
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
          title={`新建终端标签 (${mod}+T)`}
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
                ? `上下分屏 (${mod}+\\)`
                : "窗口高度不足，无法上下分屏"
              : `取消分屏 (${mod}+\\)`
          }
          aria-label={canSplit ? "上下分屏" : "取消分屏"}
          onClick={onToggleSplit}
        >
          {canSplit ? <IconSplitH size={14} /> : <IconMergeH size={14} />}
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={leftOpen ? `收起左栏 (${mod}+B)` : `展开左栏 (${mod}+B)`}
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
    case "background":
      return <BackgroundSessions visible={active} />;
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
  const mod = modHint();
  const shortcuts: [string, string][] = [
    [`${mod}+T`, "本地终端"],
    [`${mod}+Shift+P`, "命令面板"],
    [`${mod}+B`, "资产树"],
    [`${mod}+J`, "AI 侧栏"],
  ];
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 bg-neutral-900 px-6">
      <Logo size={52} />
      <div className="text-center">
        <div className="text-[17px] font-semibold tracking-tight text-neutral-100">NexTerm</div>
        <div className="mt-1 text-xs text-neutral-500">
          一体化开发运维终端 · 资产 / 终端 / 文件 / 容器 / 数据库 / AI
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
