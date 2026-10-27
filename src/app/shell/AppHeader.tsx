import { type ReactNode } from "react";
import {
  sessionStatusText,
  sessionReconnectStatusText,
  useSessionById,
  useSessionStatus,
  type AppTab,
  type Workspace,
} from "../store";
import { type SessionInfo } from "../../ipc/commands";
import { TRANSPORT } from "../../ipc/env";
import { isMac, wailsDragRegionStyle, wailsNoDragRegionStyle } from "../platform";
import {
  IconCheckCircle,
  IconChevronDown,
  IconCommand,
  IconDatabase,
  IconInfo,
  IconLayers,
  IconLoader,
  IconPanelRight,
  IconSettings,
  IconSparkles,
  IconXCircle,
  assetIcon,
} from "../../ui/icons";

export function workspaceIcon(w: Workspace) {
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

function WorkspaceReconnectStatus({ sessionId }: { sessionId: string | undefined }) {
  const session = useSessionById(sessionId);
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

export interface AppHeaderProps {
  ws: Workspace | null | undefined;
  // activeSessionId is the session of the active host workspace; the header follows only that session.
  activeSessionId: string | undefined;
  activeTab: AppTab | null;
  rightDockOpen: boolean;
  sessionPanelOpen: boolean;
  sessionPanelLabel: string;
  commandPaletteLabel: string;
  onOpenWorkspaceSwitcher: (anchor: HTMLElement) => void;
  onToggleRight: () => void;
  onToggleSessionPanel: () => void;
  onOpenPalette: () => void;
  // windowControls is the desktop shell's minimise/maximise/close group; the mobile shell has none.
  windowControls?: ReactNode;
}

export function AppHeader({
  ws,
  activeSessionId,
  activeTab,
  rightDockOpen,
  sessionPanelOpen,
  sessionPanelLabel,
  commandPaletteLabel,
  onOpenWorkspaceSwitcher,
  onToggleRight,
  onToggleSessionPanel,
  onOpenPalette,
  windowControls,
}: AppHeaderProps) {
  const ActiveWsIcon = ws ? workspaceIcon(ws) : null;
  const wsStatus = useSessionStatus(ws?.sessionId);
  return (
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
            onClick={(event) => onOpenWorkspaceSwitcher(event.currentTarget)}
          >
            {ActiveWsIcon ? <ActiveWsIcon size={13} /> : null}
            <span className="truncate">{ws.title}</span>
            <WorkspaceStatusGlyph status={wsStatus} />
            <IconChevronDown size={12} />
          </button>
          <WorkspaceReconnectStatus sessionId={activeSessionId} />
          <span
            className="nx-header-crumb flex min-w-0 items-center gap-1.5 text-xs text-neutral-500"
            data-wails-drag-region style={wailsDragRegionStyle}
          >
            <span className="text-neutral-600" data-wails-drag-region style={wailsDragRegionStyle}>
              /
            </span>
            <span
              className="truncate font-medium text-neutral-300"
              data-wails-drag-region style={wailsDragRegionStyle}
            >
              {activeTab?.title ?? "工作区"}
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
        className={`nx-icon-btn ${sessionPanelOpen ? "is-active" : ""}`} style={wailsNoDragRegionStyle}
        title={`会话侧栏：当前终端所在主机的文件、容器、端口与日志${sessionPanelLabel ? ` (${sessionPanelLabel})` : ""}`}
        aria-label="会话侧栏"
        aria-pressed={sessionPanelOpen}
        onClick={onToggleSessionPanel}
      >
        <IconPanelRight size={15} />
      </button>
      <button
        className={`nx-icon-btn ${rightDockOpen ? "is-active" : ""}`} style={wailsNoDragRegionStyle}
        title="AI 助手"
        aria-label="AI 助手"
        aria-pressed={rightDockOpen}
        onClick={onToggleRight}
      >
        <IconSparkles size={15} />
      </button>
      <button
        className="nx-icon-btn" style={wailsNoDragRegionStyle}
        title={`跳转面板 (${commandPaletteLabel})`}
        aria-label={`跳转面板 (${commandPaletteLabel})`}
        onClick={onOpenPalette}
      >
        <IconCommand size={15} />
      </button>
      {windowControls}
    </header>
  );
}
