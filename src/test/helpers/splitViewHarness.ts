import { createElement, useEffect } from "react";
import type { Workspace } from "../../app/store";
import type { WorkspaceBodyProps } from "../../app/shell/WorkspaceBody";

// Shared by the split view tests: a lazyPanels stand-in whose terminal counts mounts and unmounts per
// tab (a remount would mean a reconnect in the app), and props for rendering WorkspaceBody alone.
// Use it from a test file with
//   vi.mock("../../app/lazyPanels", async () => (await import("../helpers/splitViewHarness")).lazyPanelsMock());

export const terminalMounts = { count: new Map<string, number>(), unmounts: new Map<string, number>() };

export function resetTerminalMounts(): void {
  terminalMounts.count.clear();
  terminalMounts.unmounts.clear();
}

function TerminalPane({ storeTabId }: { storeTabId: string }) {
  useEffect(() => {
    terminalMounts.count.set(storeTabId, (terminalMounts.count.get(storeTabId) ?? 0) + 1);
    return () => {
      terminalMounts.unmounts.set(storeTabId, (terminalMounts.unmounts.get(storeTabId) ?? 0) + 1);
    };
  }, [storeTabId]);
  return createElement("div", { "data-terminal": storeTabId });
}

export function lazyPanelsMock() {
  const stub = (name: string) => () =>
    createElement("div", { "data-panel": name }, createElement("button", { type: "button" }, `${name}-action`));
  return {
    TerminalPane,
    AiSidebar: stub("ai"),
    AssetTree: stub("assets"),
    CredentialsSidebar: stub("credentials"),
    FileTree: stub("file-tree"),
    BackgroundSessions: stub("background"),
    TranscriptHistoryPanel: stub("history"),
    FileBrowser: stub("browser"),
    MountPanel: stub("mount"),
    FileEditor: stub("editor"),
    LogViewer: stub("log"),
    ForwardPanel: stub("forward"),
    MultiHostLogTail: stub("multi-log"),
    FleetHealthPanel: stub("fleet"),
    DockerPanel: stub("docker"),
    DbPanel: stub("db"),
    ExecBatchPanel: stub("exec"),
    SettingsView: stub("settings"),
    AuditView: stub("audit"),
    CredentialsPanel: stub("credentials-panel"),
  };
}

/** WorkspaceBody props showing `workspaces` with the first one active and both docks closed. */
export function workspaceBodyProps(workspaces: Workspace[], singlePane: boolean): WorkspaceBodyProps {
  return {
    ws: workspaces[0],
    visibleWorkspaces: workspaces,
    activeTab: null,
    activeSessionId: "s",
    overlaySidebars: false,
    splitAllowed: true,
    singlePane,
    docks: {
      overlayDock: null,
      setOverlayDock: () => undefined,
      leftDockOpen: false,
      rightDockOpen: false,
      setLeftOpen: () => undefined,
      setRightOpen: () => undefined,
    },
    onNewTerminal: () => undefined,
    onLocalTerminal: () => undefined,
    onOpenPalette: () => undefined,
  };
}
