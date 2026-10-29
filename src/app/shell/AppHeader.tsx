import { type ReactNode } from "react";
import {
  sessionStatusText,
  isSessionSuspect,
  useSessionLiveness,
  useSessionStatus,
  type AppTab,
  type Workspace,
} from "../store";
import { type SessionInfo } from "../../ipc/commands";
import { TRANSPORT } from "../../ipc/env";
import { isMac, wailsDragRegionStyle, wailsNoDragRegionStyle } from "../platform";
import { InboxButton } from "../../features/inbox/InboxButton";
import {
  IconCheckCircle,
  IconChevronDown,
  IconCommand,
  IconDatabase,
  IconInfo,
  IconLayers,
  IconLoader,
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

function WorkspaceStatusGlyph({ status, liveness }: { status: SessionInfo["status"] | undefined; liveness: string | null | undefined }) {
  if (status === undefined) return null;
  const suspect = isSessionSuspect(status, liveness);
  const tone =
    status === "connected" && !suspect ? "is-ok" : status === "failed" ? "is-bad" : "is-warn";
  return (
    <span className={`nx-ws-status ${tone}`} role="img" aria-label={sessionStatusText(status, liveness)} title={sessionStatusText(status, liveness)}>
      {suspect ? (
        <IconInfo size={11} />
      ) : status === "connected" ? (
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

export interface AppHeaderProps {
  ws: Workspace | null | undefined;
  activeTab: AppTab | null;
  rightDockOpen: boolean;
  commandPaletteLabel: string;
  onOpenWorkspaceSwitcher: (anchor: HTMLElement) => void;
  onToggleRight: () => void;
  onOpenPalette: () => void;
  // windowControls is the desktop shell's minimise/maximise/close group; the mobile shell has none.
  windowControls?: ReactNode;
  // leading sits before the app name; the mobile shell puts its home button there.
  leading?: ReactNode;
}

export function AppHeader({
  ws,
  activeTab,
  rightDockOpen,
  commandPaletteLabel,
  onOpenWorkspaceSwitcher,
  onToggleRight,
  onOpenPalette,
  windowControls,
  leading,
}: AppHeaderProps) {
  const ActiveWsIcon = ws ? workspaceIcon(ws) : null;
  const wsStatus = useSessionStatus(ws?.sessionId);
  const wsLiveness = useSessionLiveness(ws?.sessionId);
  return (
    <header
      className={`nx-app-header flex h-[36px] shrink-0 items-center gap-2 border-b border-neutral-800/60 bg-neutral-950 pr-2.5 ${
        TRANSPORT === "desktop" && isMac() ? "pl-[22px]" : "pl-2.5"
      }`}
      data-wails-drag-region style={wailsDragRegionStyle}
    >
      {leading}
      <span
        className="nx-app-name text-[12.5px] font-semibold tracking-tight text-neutral-100"
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
            <WorkspaceStatusGlyph status={wsStatus} liveness={wsLiveness} />
            <IconChevronDown size={12} />
          </button>
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
          暂无工作区，双击左侧资产连接主机
        </span>
      )}
      <div className="nx-spacer" data-wails-drag-region style={wailsDragRegionStyle} />
      <InboxButton />
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
