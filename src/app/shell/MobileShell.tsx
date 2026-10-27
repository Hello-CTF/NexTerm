import { useEffect, useLayoutEffect, useRef } from "react";
import { StatusWall } from "../../features/mobile/StatusWall";
import { IconHome } from "../../ui/icons";
import { wailsNoDragRegionStyle } from "../platform";
import { useUi } from "../store";
import { hideMobileHome, showMobileHome, showMobileHomeOnLaunch, useMobileHome } from "./mobileHome";
import { ShellFrame, type ShellParts } from "./ShellFrame";
import { useMobileTerminalDockCollapse } from "./useOverlayDocks";

// activeTabKey names the tab the workspaces show: the active workspace and its active pane's tab.
function useActiveTabKey(): string {
  return useUi((state) => {
    const workspace = state.workspaces.find((candidate) => candidate.id === state.activeWorkspaceId);
    const pane = workspace?.panes.find((candidate) => candidate.id === workspace.activePaneId);
    return `${state.activeWorkspaceId ?? ""}\n${pane?.activeTabId ?? ""}`;
  });
}

// useLeaveHomeOnNavigation closes the home when the user goes to a workspace: a connect they started (which takes
// the focus) or a tab that did not exist before becoming active, such as a new terminal. Restoring the saved
// workspaces at startup neither takes the focus nor adds tabs, so the home stays.
function useLeaveHomeOnNavigation(): void {
  const focusRevision = useUi((state) => state.connectFocusRevision);
  const activeKey = useActiveTabKey();
  const lastRevision = useRef(focusRevision);
  const knownTabs = useRef<Set<string> | null>(null);
  useEffect(() => {
    if (focusRevision === lastRevision.current) return;
    lastRevision.current = focusRevision;
    hideMobileHome();
  }, [focusRevision]);
  useEffect(() => {
    const ids = new Set(useUi.getState().workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id))));
    const previous = knownTabs.current;
    knownTabs.current = ids;
    const activeTabId = activeKey.split("\n")[1];
    if (previous && activeTabId && !previous.has(activeTabId)) hideMobileHome();
  }, [activeKey]);
}

// MobileShell serves phones and tablets (the "mobile" mode). It hosts the shared layout with the overlay sidebars and
// the bottom terminal key bar, without window chrome, with an overlay sidebar that steps aside while a terminal tab
// is active, and with the status wall as its home: shown at launch, over the workspaces, until the user opens a
// session or goes to a workspace. The header's home button brings it back.
export function MobileShell(parts: ShellParts) {
  useMobileTerminalDockCollapse(parts.body.docks, parts.layout.overlaySidebars, parts.activeTabKind);
  useLayoutEffect(() => showMobileHomeOnLaunch(), []);
  useLeaveHomeOnNavigation();
  // The docks (the asset drawer, the AI and session sheets) live in the workspace body, so opening one leaves the home.
  const { leftDockOpen, rightDockOpen, sessionDockOpen } = parts.body.docks;
  const anyDockOpen = leftDockOpen || rightDockOpen || sessionDockOpen;
  const dockWasOpen = useRef(anyDockOpen);
  useEffect(() => {
    if (anyDockOpen && !dockWasOpen.current) hideMobileHome();
    dockWasOpen.current = anyDockOpen;
  }, [anyDockOpen]);
  const homeOpen = useMobileHome((state) => state.open);
  const { setLeftOpen } = parts.body.docks;
  const homeButton = (
    <button
      type="button"
      className={`nx-icon-btn ${homeOpen ? "is-active" : ""}`}
      style={wailsNoDragRegionStyle}
      aria-label={homeOpen ? "返回工作区" : "会话状态首页"}
      aria-pressed={homeOpen}
      title={homeOpen ? "返回工作区" : "会话状态首页"}
      onClick={() => (homeOpen ? hideMobileHome() : showMobileHome())}
    >
      <IconHome size={15} />
    </button>
  );
  const header = {
    ...parts.header,
    leading: homeButton,
    onOpenWorkspaceSwitcher: (anchor: HTMLElement) => {
      hideMobileHome();
      parts.header.onOpenWorkspaceSwitcher(anchor);
    },
  };
  const home = homeOpen ? (
    <StatusWall
      onOpened={hideMobileHome}
      onOpenAssets={() => {
        hideMobileHome();
        setLeftOpen(true);
      }}
    />
  ) : null;
  return <ShellFrame {...parts} header={header} home={home} />;
}
