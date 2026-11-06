import { useUi, isHostWorkspace, showLeftPanel, type Workspace } from "../store";
import { resolveLeftView } from "./leftView";
import { useAwarenessStore } from "../sessionAwarenessStore";
import { hideMobileHome, showMobileHome, useMobileHome } from "./mobileHome";
import { useBackgroundSessionCount } from "../../features/terminal/backgroundSessionsAccess";
import { mountUnavailableReason } from "../capabilities";
import { deviceIdOfHost, isHostAssetKind, openHostDetail } from "../../features/hosts/hostDetail";
import {
  IconActivity,
  IconBox,
  IconClock,
  IconDashboard,
  IconDatabase,
  IconDrive,
  IconHistory,
  IconInfo,
  IconKey,
  IconList,
  IconMonitor,
  IconNetwork,
  IconServer,
  IconSettings,
  Logo,
} from "../../ui/icons";
import { type OverlayDock } from "./useOverlayDocks";
import { type WorkspaceActions } from "./useWorkspaceActions";

export interface AppRailProps {
  overlaySidebars: boolean;
  setOverlayDock: (dock: OverlayDock) => void;
  setLeftOpen: (open: boolean) => void;
  leftDockOpen: boolean;
  backgroundAvailable: boolean;
  // workspace is the active workspace; the rail counts its background terminals itself, so a host status
  // change re-renders the rail rather than the whole app shell.
  workspace: Workspace | null | undefined;
  hasDbAssets: boolean;
  actions: WorkspaceActions;
}

export function AppRail({
  overlaySidebars,
  setOverlayDock,
  setLeftOpen,
  leftDockOpen,
  backgroundAvailable,
  workspace,
  hasDbAssets,
  actions,
}: AppRailProps) {
  const backgroundCount = useBackgroundSessionCount(workspace, backgroundAvailable);
  const homeOpen = useMobileHome((s) => s.open);
  // Sessions waiting on the user, counted from this client's own awareness so the badge works without the listing.
  const needsMe = useAwarenessStore((s) => {
    let count = 0;
    for (const awareness of s.byTabId.values()) if (awareness.state === "needs_me") count += 1;
    return count;
  });
  const leftMode = useUi((s) => s.leftMode);
  const leftOverride = useUi((s) => s.leftOverride);
  // Host tools act on the active workspace's session; without one they cannot run, so they are not offered.
  const hasHost = isHostWorkspace(workspace);
  // The sidebar shows the file tree in a host workspace, so neither panel button is highlighted there.
  const railActive = resolveLeftView(workspace, leftMode, leftOverride);
  const { openBackground, openHistory, openDocker, openDatabase, openMount, openForward, openLogs } = actions;
  const hostAssetId = workspace?.assetId ?? "";
  const hasHostDetail = hasHost && hostAssetId !== "" &&
    (isHostAssetKind(workspace?.assetKind ?? "") || deviceIdOfHost(hostAssetId) !== null);

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
      hint: hasHost
        ? "临时切换到资产树；双击收起左侧栏"
        : "管理主机和数据库；双击收起左侧栏",
      icon: IconServer,
      onClick: () => {
        if (showLeftPanel("assets", leftDockOpen)) setLeftOpen(true);
      },
    },
    {
      key: "credentials",
      label: "凭据",
      hint: "管理密码和私钥；双击收起左侧栏",
      icon: IconKey,
      onClick: () => {
        if (showLeftPanel("credentials", leftDockOpen)) setLeftOpen(true);
      },
    },
  ];

  const railItems: {
    key: string;
    label: string;
    hint: string;
    icon: typeof IconServer;
    onClick: () => void;
    badge?: number;
  }[  ] = [
    ...(backgroundAvailable ? [{
      key: "background",
      label: "后台会话",
      hint: backgroundCount
        ? `该主机有 ${backgroundCount} 个后台会话`
        : "查看并接管该主机的后台会话",
      icon: IconActivity,
      onClick: openBackground,
      badge: backgroundCount ?? undefined,
    }] : []),
    ...(hasHostDetail
      ? [
          {
            key: "host-detail",
            label: "主机详情",
            hint: "当前主机的会话、容器、端口、进程和指标",
            icon: IconInfo,
            onClick: () => openHostDetail(hostAssetId),
          },
        ]
      : []),
    {
      key: "history",
      label: "终端历史",
      hint: hasHost && workspace?.assetId ? "回看和搜索当前主机的终端历史输出" : "回看和搜索终端的历史输出",
      icon: IconClock,
      onClick: openHistory,
    },
    ...(hasHost
      ? [
          {
            key: "docker",
            label: "容器",
            hint: "查看容器状态与日志，进入容器终端",
            icon: IconBox,
            onClick: openDocker,
          },
        ]
      : []),
    ...(hasDbAssets
      ? [
          {
            key: "db",
            label: "数据库",
            hint: "查询 MySQL / PostgreSQL / Redis 资产",
            icon: IconDatabase,
            onClick: () => void openDatabase(),
          },
        ]
      : []),
    ...(!hasHost || mountUnavailableReason()
      ? []
      : [{
          key: "mount",
          label: "磁盘挂载",
          hint: "将 SSH 资产的远程目录挂载到本地，作为本地磁盘访问",
          icon: IconDrive,
          onClick: openMount,
        }]),
    ...(hasHost
      ? [
          {
            key: "forward",
            label: "端口转发",
            hint: "将远程端口转发到本地：本地端口映射或 SOCKS5 代理",
            icon: IconNetwork,
            onClick: openForward,
          },
          {
            key: "logs",
            label: "日志",
            hint: "实时查看日志文件、journalctl 或容器日志",
            icon: IconList,
            onClick: openLogs,
          },
        ]
      : []),
  ];

  const railBottom = [
    { key: "devices", label: "设备管理", hint: "管理已接入的设备并打开远程终端", icon: IconMonitor, onClick: actions.openDevices },
    { key: "audit", label: "审计日志", hint: "查看用户和 AI 的操作记录", icon: IconHistory, onClick: actions.openAudit },
    { key: "settings", label: "设置", hint: "外观、快捷键、AI、凭据库与账号同步", icon: IconSettings, onClick: actions.openSettings },
  ];

  return (
    <nav className="nx-rail" aria-label="主导航">
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
      <button
        className={`nx-rail-btn ${homeOpen ? "is-active" : ""}`}
        data-tip={needsMe > 0 ? `所有终端的状态一览（${needsMe} 个需要处理）` : "所有终端的状态一览"}
        aria-label="会话总览"
        aria-pressed={homeOpen}
        onClick={() => {
          if (homeOpen) {
            hideMobileHome();
            return;
          }
          // An overlay drawer would otherwise stay on top of the wall it was asked to show.
          if (overlaySidebars) setOverlayDock(null);
          showMobileHome();
        }}
      >
        <IconDashboard size={17} />
        {needsMe > 0 ? <span className="nx-rail-count is-attention" aria-hidden>{needsMe > 99 ? "99+" : needsMe}</span> : null}
      </button>
      <div className="nx-rail-sep" />
      {railItems.map((it) => (
        <button
          key={it.key}
          className="nx-rail-btn"
          data-tip={it.hint}
          aria-label={it.label}
          onClick={() => {
            // Overlay drawers would otherwise stay on top of the page the rail item opens.
            if (overlaySidebars) setOverlayDock(null);
            it.onClick();
          }}
        >
          <it.icon size={17} />
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
          onClick={() => {
            if (overlaySidebars) setOverlayDock(null);
            it.onClick();
          }}
        >
          <it.icon size={17} />
        </button>
      ))}
    </nav>
  );
}
