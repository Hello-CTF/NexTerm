import { useEffect } from "react";
import {
  useUi,
  cycleActiveWorkspace,
  cycleActiveTab,
  focusActivePane,
  requestCloseTab,
  restoreClosedTab,
} from "../store";
import { keybindingRepeats, matchAppKeybinding } from "../keybindings";

export interface AppKeybindingHandlers {
  leftDockOpen: boolean;
  rightDockOpen: boolean;
  activeTabId: string | null;
  setLeftOpen: (open: boolean) => void;
  setRightOpen: (open: boolean) => void;
  toggleSessionPanel: () => void;
  openLocalTerminal: () => Promise<void>;
  openNewTerminal: () => Promise<void>;
  lockVault: () => Promise<void>;
  openCommandPalette: () => void;
  openQuickConnect: () => void;
}

// useAppKeybindings handles the app-level rebindable shortcuts on window keydown.
export function useAppKeybindings({
  leftDockOpen,
  rightDockOpen,
  activeTabId,
  setLeftOpen,
  setRightOpen,
  toggleSessionPanel,
  openLocalTerminal,
  openNewTerminal,
  lockVault,
  openCommandPalette,
  openQuickConnect,
}: AppKeybindingHandlers): void {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const hit = matchAppKeybinding(e);
      if (!hit) return;
      // Only navigation repeats while held; the same rule applies when a terminal has focus (terminalInput).
      if (e.repeat && !keybindingRepeats(hit.action)) {
        e.preventDefault();
        return;
      }
      switch (hit.action) {
        case "restoreClosedTab":
          e.preventDefault();
          void restoreClosedTab();
          return;
        case "commandPalette":
          e.preventDefault();
          openCommandPalette();
          return;
        case "quickConnect":
          e.preventDefault();
          openQuickConnect();
          return;
        case "toggleSidebar":
          e.preventDefault();
          setLeftOpen(!leftDockOpen);
          return;
        case "toggleAiSidebar":
          e.preventDefault();
          setRightOpen(!rightDockOpen);
          return;
        case "toggleSessionPanel":
          e.preventDefault();
          toggleSessionPanel();
          return;
        case "newTerminal":
          e.preventDefault();
          void openNewTerminal();
          return;
        case "nextWorkspace":
          if (cycleActiveWorkspace(1)) e.preventDefault();
          return;
        case "previousWorkspace":
          if (cycleActiveWorkspace(-1)) e.preventDefault();
          return;
        case "nextTab":
          if (cycleActiveTab(1)) e.preventDefault();
          return;
        case "previousTab":
          if (cycleActiveTab(-1)) e.preventDefault();
          return;
        case "focusPaneLeft":
          if (focusActivePane("left")) e.preventDefault();
          return;
        case "focusPaneRight":
          if (focusActivePane("right")) e.preventDefault();
          return;
        case "focusPaneUp":
          if (focusActivePane("up")) e.preventDefault();
          return;
        case "focusPaneDown":
          if (focusActivePane("down")) e.preventDefault();
          return;
        case "splitRight":
        case "splitDown": {
          e.preventDefault();
          const cur = useUi.getState();
          const id = cur.activeWorkspaceId;
          if (!id || !cur.workspaces.some((w) => w.id === id)) return;
          cur.splitPane(hit.action === "splitRight" ? "right" : "down", undefined, id);
          return;
        }
        case "closePane": {
          const cur = useUi.getState();
          const target = cur.workspaces.find((w) => w.id === cur.activeWorkspaceId);
          if (!target || target.panes.length < 2) return;
          e.preventDefault();
          cur.closePane(target.activePaneId, target.id);
          return;
        }
        case "closeTab":
          if (!activeTabId) return;
          e.preventDefault();
          void requestCloseTab(activeTabId);
          return;
        case "lockVault": {
          e.preventDefault();
          void lockVault();
          return;
        }
        case "switchTab": {
          if (hit.digit === null) return;
          const st = useUi.getState();
          const current = st.workspaces.find((w) => w.id === st.activeWorkspaceId);
          const currentPane =
            current?.panes.find((p) => p.id === current.activePaneId) ?? current?.panes[0];
          const target = currentPane?.tabs[hit.digit - 1];
          if (!target) return;
          e.preventDefault();
          st.setActiveTab(target.id);
          return;
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    leftDockOpen,
    rightDockOpen,
    activeTabId,
    setLeftOpen,
    setRightOpen,
    toggleSessionPanel,
    openLocalTerminal,
    openNewTerminal,
    lockVault,
    openCommandPalette,
    openQuickConnect,
  ]);
}
