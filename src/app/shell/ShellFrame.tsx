import { type ComponentProps, type CSSProperties, type MouseEvent } from "react";
import { DeviceSessionBoundary } from "../../features/fleet/DeviceSessionBoundary";
import { Lazy } from "../../ui/LazyBoundary";
import { AutoSync, ForwardStartupRecovery } from "../lazyPanels";
import { ProjectWorkspaceStrip } from "../ProjectWorkspaceStrip";
import { AppBanners, type AppBannersProps } from "./AppBanners";
import { AppDialogs, type AppDialogsProps } from "./AppDialogs";
import { AppHeader, type AppHeaderProps } from "./AppHeader";
import { AppRail, type AppRailProps } from "./AppRail";
import { type aiToastPlacement } from "./chromeInsets";
import { StatusBar, type StatusBarProps } from "./StatusBar";
import { ToastStack } from "./ToastStack";
import { type ShellLayout } from "./useShell";
import { WorkspaceBody, type WorkspaceBodyProps } from "./WorkspaceBody";

// ShellParts is everything the app controller hands to a shell; each shell decides the chrome around it.
export interface ShellParts {
  layout: ShellLayout;
  terminalKeysVisible: boolean;
  aiToast: ReturnType<typeof aiToastPlacement>;
  rightWidth: number;
  activeTabKind: string | null;
  rail: AppRailProps;
  header: AppHeaderProps;
  strip: ComponentProps<typeof ProjectWorkspaceStrip>;
  banners: AppBannersProps;
  body: WorkspaceBodyProps;
  statusBar: StatusBarProps;
  dialogs: AppDialogsProps;
}

export interface ShellFrameProps extends ShellParts {
  onDoubleClick?: (event: MouseEvent<HTMLElement>) => void;
}

// ShellFrame is the layout both shells share: rail, header, notices, workspace body, status bar and overlays.
export function ShellFrame({
  layout,
  terminalKeysVisible,
  aiToast,
  rightWidth,
  rail,
  header,
  strip,
  banners,
  body,
  statusBar,
  dialogs,
  onDoubleClick,
}: ShellFrameProps) {
  return (
    <div
      className={`nx-app flex h-full flex-col ${layout.viewport.compact ? "is-compact" : ""}`}
      data-nx-keys={terminalKeysVisible ? "true" : undefined}
      data-nx-ai={aiToast ?? undefined}
      data-nx-landscape-full={layout.landscapeFull ? "true" : undefined}
      style={{ "--nx-right-w": `${rightWidth}px` } as CSSProperties}
      onDoubleClick={onDoubleClick}
    >
      <a
        href="#nx-main"
        className="sr-only focus:not-sr-only focus:fixed focus:left-2 focus:top-2 focus:z-[100] focus:rounded focus:bg-neutral-800 focus:px-3 focus:py-2 focus:text-xs focus:text-neutral-100"
      >
        跳转到主内容
      </a>
      <DeviceSessionBoundary />
      <Lazy fallback="none" silent>
        <ForwardStartupRecovery />
      </Lazy>
      <Lazy fallback="none" silent>
        <AutoSync />
      </Lazy>

      <div className="flex min-h-0 flex-1">
        <AppRail {...rail} />

        <div className="flex min-w-0 flex-1 flex-col">
          <AppHeader {...header} />
          <ProjectWorkspaceStrip {...strip} />
          <AppBanners {...banners} />
          <WorkspaceBody {...body} />
          <StatusBar {...statusBar} />
        </div>
      </div>

      <ToastStack />
      <AppDialogs {...dialogs} />
    </div>
  );
}
