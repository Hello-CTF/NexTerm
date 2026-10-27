import { useEffect, useLayoutEffect, useRef, type ReactNode } from "react";
import type { WallCard } from "../../features/mobile/statusWall";
import { SessionPromptChoices } from "../../features/terminal/ChoiceButtons";
import { IconHome } from "../../ui/icons";
import { Lazy } from "../../ui/LazyBoundary";
import { StatusWall } from "../lazyPanels";
import { wailsNoDragRegionStyle } from "../platform";
import { useUi } from "../store";
import { hideMobileHome, showMobileHome, showMobileHomeOnLaunch, useMobileHome } from "./mobileHome";
import { ShellFrame, type ShellParts } from "./ShellFrame";
import { useMobileTerminalDockCollapse } from "./useOverlayDocks";

// useLeaveHomeOnConnectFocus closes the home when a connect the user started takes the focus. Other ways to a session
// (a new terminal, the palette, the inbox, the workspace switcher, a takeover, the wall's own cards) close it where
// the user asks for them; restoring the saved workspaces at startup does neither, so the home stays.
function useLeaveHomeOnConnectFocus(): void {
  const focusRevision = useUi((state) => state.connectFocusRevision);
  const lastRevision = useRef(focusRevision);
  useEffect(() => {
    if (focusRevision === lastRevision.current) return;
    lastRevision.current = focusRevision;
    hideMobileHome();
  }, [focusRevision]);
}

// A card that needs the user answers its detected prompt in place; one this client holds no tab for is opened first.
function wallPromptActions(card: WallCard): ReactNode {
  if (card.state !== "needs_me" || !card.awareness?.prompt) return null;
  return <SessionPromptChoices tabId={card.tabId} awareness={card.awareness} attachedHere={card.uiTabId !== undefined} />;
}

// MobileShell serves phones and tablets (the "mobile" mode). It hosts the shared layout with the overlay sidebars and
// the bottom terminal key bar, without window chrome, with an overlay sidebar that steps aside while a terminal tab
// is active, and with the status wall as its home: shown at launch in the workspace area, until the user opens a
// session or goes to one. The docks stay usable beside or over it, and the header's home button toggles it.
export function MobileShell(parts: ShellParts) {
  useMobileTerminalDockCollapse(parts.body.docks, parts.layout.overlaySidebars, parts.activeTabKind);
  useLayoutEffect(() => showMobileHomeOnLaunch(), []);
  useLeaveHomeOnConnectFocus();
  const homeOpen = useMobileHome((state) => state.open);
  const { setLeftOpen } = parts.body.docks;
  const homeButton = (
    <button
      type="button"
      className={`nx-icon-btn ${homeOpen ? "is-active" : ""}`}
      style={wailsNoDragRegionStyle}
      aria-label="会话状态首页"
      aria-pressed={homeOpen}
      title="会话状态首页"
      onClick={() => (homeOpen ? hideMobileHome() : showMobileHome())}
    >
      <IconHome size={15} />
    </button>
  );
  const home = homeOpen ? (
    <Lazy>
      <StatusWall onOpened={hideMobileHome} onOpenAssets={() => setLeftOpen(true)} cardActions={wallPromptActions} />
    </Lazy>
  ) : null;
  return <ShellFrame {...parts} header={{ ...parts.header, leading: homeButton }} body={{ ...parts.body, home }} />;
}
