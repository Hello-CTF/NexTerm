import { useUi, isHostWorkspace, showLeftPanel, type Workspace } from "../store";
import { resolveLeftView } from "./leftView";
import { useBackgroundSessionCount } from "../../features/terminal/backgroundSessionsAccess";
import { TRANSPORT } from "../../ipc/env";
import { isMac } from "../platform";
import { mountUnavailableReason } from "../capabilities";
import {
  IconActivity,
  IconBox,
  IconClock,
  IconDatabase,
  IconDrive,
  IconHistory,
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
  const leftMode = useUi((s) => s.leftMode);
  const leftOverride = useUi((s) => s.leftOverride);
  // Host tools act on the active workspace's session; without one they cannot run, so they are not offered.
  const hasHost = isHostWorkspace(workspace);
  // The sidebar shows the file tree in a host workspace, so neither panel button is highlighted there.
  const railActive = resolveLeftView(workspace, leftMode, leftOverride);
  const { openBackground, openHistory, openDocker, openDatabase, openMount, openForward, openLogs } = actions;

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
        ? "临时切换到资产树，双击资产连接后自动回到文件树；再次单击回到文件树；双击本按钮收起侧栏"
        : "打开资产树：管理主机和数据库，双击资产直接连接；双击本按钮收起侧栏",
      icon: IconServer,
      onClick: () => {
        if (showLeftPanel("assets", leftDockOpen)) setLeftOpen(true);
      },
    },
    {
      key: "credentials",
      label: "凭据",
      hint: "打开凭据侧栏：集中保存密码和私钥，连接资产时引用；双击本按钮收起侧栏",
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
        ? `${backgroundCount} 个终端在该主机后台运行，单击查看`
        : "查看该主机上在后台运行的终端，并重新接管到标签页",
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
    ...(hasHost
      ? [
          {
            key: "docker",
            label: "容器",
            hint: "打开 Docker 面板：查看容器状态与日志，进入容器终端",
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
            hint: "打开数据库工作台：查询 MySQL / PostgreSQL / Redis 资产",
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
            hint: "实时查看日志文件、journalctl 或容器日志：默认选中当前主机，可合并多台主机（仅 SSH 与非 Windows 受管设备）",
            icon: IconList,
            onClick: openLogs,
          },
        ]
      : []),
  ];

  const railBottom = [
    { key: "devices", label: "设备管理", hint: "打开设备管理：查看已安装设备代理的设备并远程打开终端", icon: IconMonitor, onClick: actions.openDevices },
    { key: "audit", label: "审计日志", hint: "查看用户和 AI 的操作记录", icon: IconHistory, onClick: actions.openAudit },
    { key: "settings", label: "设置", hint: "打开设置：外观、快捷键、AI、凭据库与账号同步", icon: IconSettings, onClick: actions.openSettings },
  ];

  return (
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
