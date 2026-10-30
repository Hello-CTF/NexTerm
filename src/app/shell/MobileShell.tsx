import { useLayoutEffect } from "react";
import { ShellFrame, type ShellParts } from "./ShellFrame";
import { showMobileHomeOnLaunch } from "./mobileHome";
import { useStatusHome } from "./useStatusHome";
import { useMobileTerminalDockCollapse } from "./useOverlayDocks";

// MobileShell serves phones and tablets (the "mobile" mode). It hosts the shared layout with the overlay sidebars and
// the bottom terminal key bar, without window chrome, with an overlay sidebar that steps aside while a terminal tab
// is active, and with the status wall as its home: shown at launch in the workspace area, until the user opens a
// session or goes to one. The docks stay usable beside or over it, and the rail's overview button toggles it.
export function MobileShell(parts: ShellParts) {
  useMobileTerminalDockCollapse(parts.body.docks, parts.layout.overlaySidebars, parts.activeTabKind);
  useLayoutEffect(() => showMobileHomeOnLaunch(), []);
  const home = useStatusHome(parts.body.docks);
  return <ShellFrame {...parts} body={{ ...parts.body, home }} />;
}
