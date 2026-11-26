import { createContext, useContext } from "react";
import { useUi } from "../store";
import { paneShowsTabStrip } from "./paneChrome";

export interface LeftDockToggle {
  open: boolean;
  toggle: () => void;
}

// The workspace body provides the shell's left dock toggle so a pane without a tab strip can offer it itself.
export const LeftDockToggleContext = createContext<LeftDockToggle | null>(null);

// useStriplessLeftDockToggle returns the left dock toggle when the pane holding tabId shows no tab strip, where the
// strip's own toggle is missing; otherwise null.
export function useStriplessLeftDockToggle(tabId: string): LeftDockToggle | null {
  const toggle = useContext(LeftDockToggleContext);
  const stripless = useUi((s) => {
    for (const workspace of s.workspaces) {
      const pane = workspace.panes.find((p) => p.tabs.some((t) => t.id === tabId));
      if (pane) return !paneShowsTabStrip(workspace, pane);
    }
    return false;
  });
  return stripless ? toggle : null;
}
