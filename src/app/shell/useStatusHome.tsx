import { useEffect, useRef, type ReactNode } from "react";
import { Lazy } from "../../ui/LazyBoundary";
import { StatusWall } from "../lazyPanels";
import { showLeftPanel, useUi } from "../store";
import { hideMobileHome, useMobileHome } from "./mobileHome";
import { type OverlayDocks } from "./useOverlayDocks";

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

// useStatusHome is the home both shells pass to the workspace body: the status wall while it is open, null otherwise.
export function useStatusHome(docks: OverlayDocks): ReactNode {
  useLeaveHomeOnConnectFocus();
  const homeOpen = useMobileHome((state) => state.open);
  if (!homeOpen) return null;
  return (
    <Lazy>
      <StatusWall
        onOpened={hideMobileHome}
        onClose={hideMobileHome}
        onOpenAssets={() => {
          if (showLeftPanel("assets", docks.leftDockOpen)) docks.setLeftOpen(true);
        }}
      />
    </Lazy>
  );
}
