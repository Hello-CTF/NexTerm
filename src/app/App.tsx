// 应用外壳（§5.1）：左侧图标栏 + 标题栏 + 标签栏 + 三栏主体 + 状态栏。
//
// 相对旧版的布局变化：
//   · 一级导航从「横向标签栏里塞按钮」搬到了最左的 48px 图标栏；
//   · 标题栏（面包屑 + 全局动作）与标签栏拆成两行，各自只干一件事；
//   · 状态栏用色更克制，并显式标出演示模式。
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  useUi,
  useActiveWorkspace,
  openTerminalTab,
  nextTabId,
  connectAsset,
  type AppTab,
  type Pane,
  type Workspace,
} from "./store";
import { assetApi, dbApi, sessionApi, vaultApi } from "../ipc/commands";
import { DEMO } from "../demo";
import { AssetTree } from "../features/explorer/AssetTree";
import { TerminalPane } from "../features/terminal/TerminalPane";
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
import { AuditView } from "../features/settings/AuditView";
import { CommandPalette } from "./CommandPalette";
import { TakeoverBanner } from "./TakeoverBanner";
import { PromptModal } from "../ui/PromptModal";
import { registerPromptHandler } from "../ui/dialogs";
import {
  IconActivity,
  IconBox,
  IconCheckCircle,
  IconChevronLeft,
  IconClose,
  IconCommand,
  IconDatabase,
  IconDrive,
  IconFolderOpen,
  IconHistory,
  IconInfo,
  IconLayers,
  IconLock,
  IconNetwork,
  IconPlus,
  IconSearch,
  IconServer,
  IconSettings,
  IconSparkles,
  IconTerminal,
  IconUnlock,
  IconXCircle,
  Logo,
  assetIcon,
  IconSplitH,
  IconMergeH,
} from "../ui/icons";

/** 标签类型 → 图标；空态与标签栏共用。 */
const TAB_ICON = {
  terminal: IconTerminal,
  files: IconFolderOpen,
  mount: IconDrive,
  forward: IconNetwork,
  docker: IconBox,
  db: IconDatabase,
  settings: IconSettings,
  audit: IconHistory,
} as const;

/** 一级标签（工作区）的图标：会话工作区用资产类型图标，其余按种类给。 */
function workspaceIcon(w: Workspace) {
  if (w.kind === "db") return w.dbKind === "redis" ? IconLayers : IconDatabase;
  if (w.kind === "tools") return IconSettings;
  return assetIcon(w.assetKind ?? "ssh");
}

/** 标签图标：编辑器标签按文件类型取图标（console.log 与 server.ts 长得不一样）。 */
function tabIcon(t: AppTab) {
  if (t.kind === "files" && t.path) return fileVisual(t.path, "file").Icon;
  return TAB_ICON[t.kind] ?? IconTerminal;
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
    closeTab,
    sessions,
    setSessions,
    leftOpen,
    setLeftOpen,
    leftMode,
    setLeftMode,
    rightOpen,
    setRightOpen,
    toasts,
    dismissToast,
    pushToast,
  } = useUi();

  /** 当前工作区（一级标签）；它的面板（二级分屏格）里挂着一排标签。 */
  const ws = useActiveWorkspace();
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tabs = pane?.tabs ?? [];
  const activeTabId = pane?.activeTabId ?? null;

  const [paletteOpen, setPaletteOpen] = useState(false);
  const [vaultStatus, setVaultStatus] = useState<string>("…");
  const [vaultUnlocked, setVaultUnlocked] = useState(false);

  const active = tabs.find((t) => t.id === activeTabId) ?? null;
  // 左栏文件树、AI 侧栏都跟着"当前工作区的机器"走，而不是"最后一个打开过标签的机器"
  const activeSessionId = ws?.sessionId ?? sessions[0]?.id;

  /* ── 面板打开动作（图标栏与命令面板共用） ───────────────────────────── */

  const needSession = useCallback(() => {
    pushToast("info", "先连接一台主机（双击左侧资产）");
  }, [pushToast]);

  const openLocalTerminal = useCallback(async () => {
    try {
      const s = await sessionApi.connectLocal();
      const list = useUi.getState().sessions;
      setSessions([...list.filter((x) => x.id !== s.id), s]);
      await openTerminalTab(s, "本地终端");
    } catch (e) {
      pushToast("error", String(e));
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
    if (!activeSessionId) return needSession();
    useUi.getState().addTab({
      id: nextTabId(`mount-${activeSessionId}`),
      kind: "mount",
      title: "磁盘挂载",
      sessionId: activeSessionId,
      closable: true,
    });
  }, [activeSessionId, needSession]);

  /**
   * 端口转发面板。
   *
   * 和其它会话级面板不同，它**不要求**先有会话：转发表是全局的，
   * 用户经常是"回来看一眼我开过哪些口子 / 关掉一条忘了关的"。
   * 没有会话时面板会提示去连机器，只是"创建"按钮禁用。
   */
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

  /** 数据库：先按资产建连接，再开面板。 */
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
      pushToast("error", String(e));
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

  /** 新标签：在当前工作区再开一个终端（最常用的"再来一个"）。 */
  const openNewTerminal = useCallback(() => {
    const s = sessions.find((x) => x.id === ws?.sessionId);
    if (s) {
      void openTerminalTab(s);
      return;
    }
    void openLocalTerminal();
  }, [ws?.sessionId, sessions, openLocalTerminal]);

  /* ── 全局快捷键 ─────────────────────────────────────────────────────── */

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
        setLeftOpen(!leftOpen);
      } else if (mod && e.key.toLowerCase() === "j") {
        e.preventDefault();
        setRightOpen(!rightOpen);
      } else if (mod && e.key.toLowerCase() === "t") {
        e.preventDefault();
        void openLocalTerminal();
      } else if (mod && e.key === "\\") {
        // 上下分屏 / 取消分屏
        e.preventDefault();
        const cur = useUi.getState();
        const id = cur.activeWorkspaceId;
        const target = cur.workspaces.find((w) => w.id === id);
        if (!target) return;
        if (target.panes.length > 1) void cur.unsplitWorkspace(target.panes[1].id, target.id);
        else cur.splitWorkspace(target.id);
      } else if (mod && e.key.toLowerCase() === "w" && activeTabId) {
        e.preventDefault();
        void closeTab(activeTabId);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    leftOpen,
    rightOpen,
    activeTabId,
    setLeftOpen,
    setRightOpen,
    closeTab,
    openLocalTerminal,
  ]);

  /* ── 全局文本输入弹窗（替代 window.prompt） ─────────────────────────── */

  useEffect(() => {
    registerPromptHandler((message, value, options) => {
      const { openTextPrompt } = useUi.getState();
      return new Promise<string | null>((resolve) => {
        openTextPrompt({ message, value, ...options, resolve });
      });
    });
  }, []);

  /* ── 启动：刷新会话与凭据库状态；演示模式下自动接一台机器 ──────────── */

  useEffect(() => {
    void sessionApi.list().then(setSessions).catch(() => undefined);
    void vaultApi
      .status()
      .then((v) => {
        setVaultUnlocked(v.unlocked);
        setVaultStatus(
          !v.initialized ? "凭据库未初始化" : v.unlocked ? "凭据库已解锁" : "凭据库已锁定",
        );
      })
      .catch(() => setVaultStatus("凭据库不可用"));
  }, [setSessions]);

  useEffect(() => {
    if (!DEMO) return;
    // 演示模式：自动连上 web-01 并开一个终端，让首屏就是"活着"的
    let cancelled = false;
    void (async () => {
      try {
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
        // 演示模式启动失败不影响后续手动操作
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [setSessions]);

  /* ── 面包屑 ─────────────────────────────────────────────────────────── */

  // 面包屑 = 一级标签 / 二级标签，与两级标签栏一一对应
  const crumb = [ws?.title ?? "未连接", active?.title ?? "工作区"];

  /* ── 图标栏 ─────────────────────────────────────────────────────────── */

  /*
   * 图标栏分三层：
   *   1) 左栏形态（资产 / 文件）—— 激活态反映"左栏现在是什么"，
   *      而不是"当前标签是什么"。参考实现里开着终端标签时，
   *      高亮的仍然是资源管理器那一个，这样用户不会误判左栏内容。
   *   2) 快捷动作（终端 / 容器 / 数据库 / 挂载）—— 开对应类型的标签。
   *   3) 底部常驻（AI / 审计 / 设置）。
   */
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

  const railItems: {
    key: string;
    label: string;
    icon: typeof IconServer;
    onClick: () => void;
  }[] = [
    { key: "terminal", label: "新建终端", icon: IconTerminal, onClick: openNewTerminal },
    { key: "docker", label: "容器", icon: IconBox, onClick: openDocker },
    { key: "db", label: "数据库", icon: IconDatabase, onClick: () => void openDatabase() },
    { key: "mount", label: "磁盘挂载", icon: IconDrive, onClick: openMount },
    { key: "forward", label: "端口转发", icon: IconNetwork, onClick: openForward },
  ];

  const railBottom = [
    { key: "ai", label: "AI 助手", icon: IconSparkles, onClick: () => setRightOpen(!rightOpen) },
    { key: "audit", label: "审计日志", icon: IconHistory, onClick: openAudit },
    { key: "settings", label: "设置", icon: IconSettings, onClick: openSettings },
  ];

  return (
    <div className="flex h-full flex-col">
      {/* 接管横幅（§8.6）：置顶占满整行，非空即表示 AI 正在操作某个终端 */}
      <TakeoverBanner />

      <div className="flex min-h-0 flex-1">
        {/* 最左：图标导航栏 */}
        <nav className="nx-rail">
          <div className="mb-2 flex items-center justify-center" title="NexTerm">
            <Logo size={26} />
          </div>
          {railPanels.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              title={it.label}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
          <div className="my-1 h-px w-5 shrink-0 bg-neutral-800" />
          {railItems.map((it) => (
            <button
              key={it.key}
              className="nx-rail-btn"
              title={it.label}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
          <div className="nx-spacer" />
          <div className="my-1 h-px w-5 shrink-0 bg-neutral-800" />
          {railBottom.map((it) => (
            <button
              key={it.key}
              className={`nx-rail-btn ${railActive === it.key ? "is-active" : ""}`}
              title={it.label}
              onClick={it.onClick}
            >
              <it.icon size={17} />
            </button>
          ))}
        </nav>

        {/* 右侧主区 */}
        <div className="flex min-w-0 flex-1 flex-col">
          {/*
            一级标签：工作区。一个工作区 ≈ 一台连上的机器（或一个数据库连接），
            它自己的终端 / 编辑器都挂在下面（二级标签，在 main 里）。
            关闭工作区不会断开连接（断连是另一个明确动作），只回收里面的终端。
          */}
          <div className="nx-tabstrip is-top">
            {workspaces.length === 0 && (
              <span className="px-1 text-xs text-neutral-500">
                还没有工作区 —— 双击左侧资产连接一台机器
              </span>
            )}
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
                <button
                  key={w.id}
                  className={`nx-ws ${isActive ? "is-active" : ""}`}
                  onClick={() => setActiveWorkspace(w.id)}
                  title={`${w.title} · ${w.panes.flatMap((p) => p.tabs).length} 个标签${
                    w.panes.length > 1 ? " · 已分屏" : ""
                  }${
                    status ? ` · ${status}` : ""
                  }${w.sessionId ? ` · ${w.sessionId}` : ""}`}
                >
                  <Icon size={13} />
                  <span className="truncate">{w.title}</span>
                  {dot && <span className={`nx-ws-dot ${dot}`} />}
                  {w.closable && (
                    <span
                      className="nx-tab-close"
                      title="关闭工作区（会关掉里面的终端）"
                      onClick={(e) => {
                        e.stopPropagation();
                        void closeWorkspace(w.id);
                      }}
                    >
                      <IconClose size={10} />
                    </span>
                  )}
                </button>
              );
            })}
            <button
              className="nx-tab-new"
              title="新建工作区：在左侧资产树里双击一台机器"
              onClick={() => {
                setLeftMode("assets");
                setLeftOpen(true);
                pushToast("info", "双击左侧资产，就会为这台机器开一个新的工作区");
              }}
            >
              <IconPlus size={13} />
            </button>
          </div>

          {/* 标题栏：面包屑 + 全局动作 */}
          <header className="flex h-[36px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-950 pr-2.5 pl-3.5">
            <span className="text-[12.5px] font-semibold tracking-tight text-neutral-100">
              NexTerm
            </span>
            <span className="flex min-w-0 items-center gap-1.5 text-xs text-neutral-500">
              <span className="truncate">{crumb[0]}</span>
              <span className="text-neutral-600">/</span>
              <span className="truncate font-medium text-neutral-300">{crumb[1]}</span>
            </span>
            <div className="nx-spacer" />
            <span className={`nx-chip ${vaultUnlocked ? "nx-chip-accent" : ""}`} title={vaultStatus}>
              {vaultUnlocked ? <IconUnlock size={11} /> : <IconLock size={11} />}
              {vaultStatus}
            </span>
            <button
              className="nx-icon-btn"
              title="刷新会话列表与凭据库状态"
              onClick={() => {
                void sessionApi.list().then(setSessions).catch(() => undefined);
                void vaultApi
                  .status()
                  .then((v) => {
                    setVaultUnlocked(v.unlocked);
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
              className="nx-icon-btn"
              title="命令面板 (Ctrl+Shift+P)"
              onClick={() => setPaletteOpen(true)}
            >
              <IconCommand size={15} />
            </button>
            <button className="nx-icon-btn" title="全局搜索 (Ctrl+K)" onClick={() => setPaletteOpen(true)}>
              <IconSearch size={15} />
            </button>
          </header>

        {/* 三栏主体 */}
        <div className="flex min-h-0 flex-1 bg-neutral-900">
          {/*
            左栏两种形态：资产列表（管理）与当前工作区的文件树（干活）。
            连上机器后默认是文件树 —— 这台机器就是接下来一段时间的工作面。
            用 key=sessionId 让每台机器各自保留自己的展开状态与选中项。
          */}
          {!leftOpen ? null : leftMode === "files" && ws?.sessionId ? (
            <FileTree key={ws.sessionId} sessionId={ws.sessionId} />
          ) : (
            <AssetTree />
          )}

          <main className="min-w-0 flex-1">
            {/*
              关键：**所有工作区、所有面板、所有标签都保持挂载**，非激活的用 `hidden` 藏起来。
              以前是 `key={active.id}` + 条件渲染，切标签会卸载/重建组件 →
              新终端实例重新调 terminal_attach → 内核每次都 open_pty 开一个新 shell：
              既"刷新"了界面，又把旧 shell 泄漏在远端。
              分屏之后这条更要紧：一栏收起/切换都不该影响另一栏的 shell。
              挂在 DOM 上还顺带保住了滚动位置、选中内容与命令块。
            */}
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
                    active={wsActive && p.id === w.activePaneId}
                    split={split}
                    canSplit={!split}
                    /* 「取消分屏」始终收掉下面那一栏，符合直觉 */
                    onToggleSplit={() => {
                      if (split) void unsplitWorkspace(w.panes[1].id, w.id);
                      else splitWorkspace(w.id);
                    }}
                    onActivate={() => setActivePane(p.id, w.id)}
                    onNewTerminal={openNewTerminal}
                    leftOpen={leftOpen}
                    onToggleLeft={() => setLeftOpen(!leftOpen)}
                  />
                ));
                return (
                  <div key={w.id} className={wsActive ? "h-full min-h-0" : "hidden"}>
                    {/*
                      永远走 SplitStack（未分屏时 bottom 为 undefined）——
                      别改回「split ? <SplitStack/> : groups[0]」。
                      那会让取消分屏时 div 的子节点类型发生切换，React 卸载重建
                      整棵子树，终端被迫重新 attach（详见 SplitStack 的注释）。
                    */}
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

          <AiSidebar sessionId={activeSessionId} tabId={active?.kind === "terminal" ? active.tabId : undefined} />
        </div>

        {/* 状态栏 */}
        <footer className="flex h-[25px] shrink-0 items-center gap-3 border-t border-neutral-800/60 bg-neutral-950 px-3 text-[11px] text-neutral-500">
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

      {/* Toast */}
      <div className="pointer-events-none fixed right-4 bottom-9 z-50 flex w-[380px] flex-col gap-2">
        {toasts.map((t) => {
          const Icon = t.kind === "error" ? IconXCircle : t.kind === "success" ? IconCheckCircle : IconInfo;
          const tone =
            t.kind === "error"
              ? "border-red-500/40 bg-red-950/90 text-red-100"
              : t.kind === "success"
                ? "border-green-500/40 bg-[#12241b]/95 text-green-100"
                : "border-neutral-700 bg-neutral-800/95 text-neutral-100";
          return (
            <button
              key={t.id}
              className={`pointer-events-auto flex items-start gap-2.5 rounded-lg border px-3.5 py-2.5 text-left text-xs leading-relaxed shadow-[0_18px_48px_-16px_rgba(0,0,0,0.75)] backdrop-blur ${tone}`}
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
      <PromptModal />
    </div>
  );
}

/* ── 面板（分屏格） ───────────────────────────────────────────────────── */

interface PaneGroupProps {
  pane: Pane;
  /** 该面板是激活面板，且它所在的工作区也激活（隐藏时两个都为 false）。 */
  active: boolean;
  /** 所在工作区是否处于分屏状态（决定激活态的视觉提示强弱）。 */
  split: boolean;
  canSplit: boolean;
  onToggleSplit: () => void;
  onActivate: () => void;
  onNewTerminal: () => void;
  leftOpen: boolean;
  onToggleLeft: () => void;
}

/**
 * 一个分屏格：自己的一排二级标签 + 内容区。
 *
 * 不分屏时它就是整个内容区；分屏时上下各一个，各自独立记着自己的标签与激活项。
 */
function PaneGroup({
  pane,
  active,
  split,
  canSplit,
  onToggleSplit,
  onActivate,
  onNewTerminal,
  leftOpen,
  onToggleLeft,
}: PaneGroupProps) {
  const setActiveTab = useUi((s) => s.setActiveTab);
  const closeTab = useUi((s) => s.closeTab);
  const activeTabId = pane.activeTabId ?? pane.tabs[pane.tabs.length - 1]?.id ?? null;

  return (
    <div
      className="flex h-full min-h-0 flex-col"
      // 点这一栏的任意位置（含终端画布）就把焦点切过来；已激活就别再动 store
      onMouseDown={() => {
        if (!active) onActivate();
      }}
    >
      <div
        className={`nx-tabstrip is-sub ${split ? "is-split" : ""} ${
          active ? "is-active-pane" : ""
        }`}
      >
        {pane.tabs.length === 0 && (
          <span className="px-1 text-xs text-neutral-500">这一栏还没有标签</span>
        )}
        {pane.tabs.map((t) => {
          const Icon = tabIcon(t);
          const isActive = t.id === activeTabId;
          return (
            <button
              key={t.id}
              className={`nx-tab ${isActive ? "is-active" : ""}`}
              onClick={() => setActiveTab(t.id)}
              title={t.title}
            >
              <Icon size={13} />
              <span className="truncate">{t.title}</span>
              {t.closable && (
                <span
                  className="nx-tab-close"
                  title="关闭标签"
                  onClick={(e) => {
                    e.stopPropagation();
                    void closeTab(t.id);
                  }}
                >
                  <IconClose size={10} />
                </span>
              )}
            </button>
          );
        })}
        <button className="nx-tab-new" title="新建终端标签 (Ctrl+T)" onClick={onNewTerminal}>
          <IconPlus size={13} />
        </button>
        <div className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={canSplit ? "上下分屏 (Ctrl+\\)" : "取消分屏 (Ctrl+\\)"}
          onClick={onToggleSplit}
        >
          {canSplit ? <IconSplitH size={14} /> : <IconMergeH size={14} />}
        </button>
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={leftOpen ? "收起左栏 (Ctrl+B)" : "展开左栏 (Ctrl+B)"}
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
            <div key={t.id} className={t.id === activeTabId ? "h-full min-h-0" : "hidden"}>
              {/* visible 必须同时满足「标签激活」与「所在栏激活」：
                  隐藏的面板里 fit() 会量到 0 高度，还会连带把远端 PTY 改小 */}
              <PaneForTab
                tab={t}
                active={t.id === activeTabId && active}
                onClose={() => void closeTab(t.id)}
              />
            </div>
          ))
        )}
      </div>
    </div>
  );
}

/**
 * 上下分屏容器：一条可拖拽的分割条，上下各放一个面板。
 *
 * ★ 未分屏时它**依然挂在树上**，只是不渲染第二栏和分割条。
 *
 * 这点是必须的。以前是「分屏时渲 `<SplitStack>`，不分屏时直接渲 `groups[0]`」，
 * 于是取消分屏会让那个 div 的子节点类型从 `SplitStack` 变成 `PaneGroup` ——
 * React 判定类型不同，整棵子树卸载重建，里面所有 XtermView 重新走一遍
 * `terminal_attach`：丢滚动内容、在远端泄漏一个 shell，运气不好还会直接 attach
 * 失败（表现为终端里那句「[attach 失败] [object Object]」，只能重起工作区才恢复）。
 * 保持结构恒定，React 就能按 key 复用同一个 PaneGroup 实例。
 */
function SplitStack({
  ratio,
  onRatio,
  top,
  bottom,
}: {
  /** 上栏高度占比 0.15 ~ 0.85。 */
  ratio: number;
  onRatio: (r: number) => void;
  top: ReactNode;
  /** 为空表示未分屏：此时只渲染上栏。 */
  bottom?: ReactNode;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);
  // 用 ref 存回调，避免每次渲染都重挂 window 监听
  const ratioRef = useRef(onRatio);
  ratioRef.current = onRatio;

  useEffect(() => {
    const move = (e: PointerEvent) => {
      if (!dragging.current) return;
      const box = boxRef.current?.getBoundingClientRect();
      if (!box || box.height < 80) return;
      // 拖拽期间每帧写一次 store：splitRatio 就在工作区上，够便宜
      ratioRef.current((e.clientY - box.top) / box.height);
    };
    const stop = () => {
      if (!dragging.current) return;
      dragging.current = false;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", stop);
    window.addEventListener("pointercancel", stop);
    return () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", stop);
      window.removeEventListener("pointercancel", stop);
    };
  }, []);

  const split = bottom != null;

  return (
    <div ref={boxRef} className="flex h-full min-h-0 flex-col">
      <div
        className="flex min-h-[60px] flex-col"
        style={split ? { flex: `0 0 calc(${ratio * 100}% - 3px)` } : { flex: "1 1 auto" }}
      >
        {top}
      </div>
      {split && (
        <>
          <div
            className="nx-split-handle"
            title="拖动调整上下比例"
            onPointerDown={() => {
              dragging.current = true;
              document.body.style.cursor = "row-resize";
              document.body.style.userSelect = "none";
            }}
          >
            <span className="nx-split-grip" />
          </div>
          <div className="flex min-h-[60px] flex-1 flex-col">{bottom}</div>
        </>
      )}
    </div>
  );
}

/** 单标签内容分发。由 App 的标签映射调用，保持挂载（生命周期与标签一致）。 */
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
          visible={active}
        />
      ) : (
        <EmptyState />
      );
    case "mount":
      return tab.sessionId ? <MountPanel sessionId={tab.sessionId} /> : <EmptyState />;
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
    default:
      return <EmptyState />;
  }
}

/** 已连接但还没有任何标签时的空态：直接给一张"新建终端"的卡片，不铺品牌页。 */
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

/** 空态：品牌 + 快捷入口。 */
function EmptyState({ onLocal, onPalette }: { onLocal?: () => void; onPalette?: () => void }) {
  const { setSessions, sessions } = useUi();
  const shortcuts: [string, string][] = [
    ["Ctrl+T", "本地终端"],
    ["Ctrl+Shift+P", "命令面板"],
    ["Ctrl+B", "资产树"],
    ["Ctrl+J", "AI 侧栏"],
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
              void openTerminalTab(s, "本地终端");
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
        双击左侧资产即可连接；SSH / WinRM / 本地终端会开终端标签，
        容器资产开容器面板，MySQL / Redis 资产开数据库工作台。
      </div>
    </div>
  );
}
