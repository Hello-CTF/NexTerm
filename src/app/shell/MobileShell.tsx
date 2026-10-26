import { ShellFrame, type ShellParts } from "./ShellFrame";
import { useMobileTerminalDockCollapse } from "./useOverlayDocks";

// MobileShell serves phones and tablets (the "mobile" mode). It currently hosts the shared layout with the
// overlay sidebars and the bottom terminal key bar that narrow and touch viewports already get, and adds the
// mobile-only behaviour: no window chrome, and an overlay sidebar that steps aside while a terminal tab is
// active. It is the place for a mobile-specific home (the status wall) later.
export function MobileShell(parts: ShellParts) {
  useMobileTerminalDockCollapse(parts.body.docks, parts.layout.overlaySidebars, parts.activeTabKind);
  return <ShellFrame {...parts} />;
}
