import { useCallback, useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useUi, useActiveWorkspace, useWorkspaceList, dbKindOf, showLeftPanel } from "./store";
import { formatBinding, useKeybindings } from "./keybindings";
import { assetApi, type Asset } from "../ipc/commands";
import { ALL_PROJECTS, focusedWorkspaces } from "./projectFocus";
import { useBackgroundSessionsAccess } from "../features/terminal/backgroundSessionsAccess";
import { ErrorBoundary } from "../ui/ErrorBoundary";
import { useShellLayout } from "./shell";
import { useAppBootEffects, useAppDialogHandlers, useWebAuthRefresh } from "./shell/AppBootEffects";
import type { JumpScope } from "./JumpPalette";
import { useTabRecencyTracker } from "./jump/tabRecency";
import { aiToastPlacement, useAiToastInset, useKeysToolbarHeight } from "./shell/chromeInsets";
import { DesktopShell } from "./shell/DesktopShell";
import { useKeyboardInset } from "./shell/keyboardInset";
import { MobileShell } from "./shell/MobileShell";
import { type ShellParts } from "./shell/ShellFrame";
import { useAppKeybindings } from "./shell/useAppKeybindings";
import { useOverlayDocks } from "./shell/useOverlayDocks";
import { useSyncStatusSummary, useVaultStatus } from "./shell/useStatusBarState";
import { useWorkspaceActions } from "./shell/useWorkspaceActions";
import { useWorkspaceSwitcher } from "./shell/useWorkspaceSwitcher";

// Shell helpers keep their historical App exports for existing importers.
export { dialogLevelForKind } from "./shell/AppBootEffects";
export { aiToastPlacement } from "./shell/chromeInsets";
export {
  isTightKeyboardViewport,
  keyboardViewportBox,
  TIGHT_KEYBOARD_VIEWPORT_HEIGHT,
  virtualKeyboardInset,
  visualViewportInset,
} from "./shell/keyboardInset";

export default function App() {
  return (
    <ErrorBoundary title="应用界面发生错误">
      <AppShell />
    </ErrorBoundary>
  );
}

// AppShell is the app controller: it gathers store, query and layout state plus the shared commands,
// then hands them to the shell for the current mode.
function AppShell() {
  const workspaces = useWorkspaceList();
  const activeWorkspaceId = useUi((s) => s.activeWorkspaceId);
  const activeProject = useUi((s) => s.activeProject);
  const projectInventory = useUi((s) => s.projectInventory);
  const setProjectInventory = useUi((s) => s.setProjectInventory);
  const setActiveWorkspace = useUi((s) => s.setActiveWorkspace);
  const rightWidth = useUi((s) => s.rightWidth);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const reconcileDeletedTargets = useUi((s) => s.reconcileDeletedTargets);

  const visibleWorkspaces = useMemo(
    () => focusedWorkspaces({ workspaces, activeWorkspaceId, activeProject, projectInventory }),
    [workspaces, activeWorkspaceId, activeProject, projectInventory],
  );
  const ws = useActiveWorkspace();
  const pane = ws?.panes.find((p) => p.id === ws.activePaneId) ?? ws?.panes[0] ?? null;
  const tabs = pane?.tabs ?? [];
  const activeTabId = pane?.activeTabId ?? null;

  const [jumpScope, setJumpScope] = useState<JumpScope | null>(null);
  const [paletteEditingAsset, setPaletteEditingAsset] = useState<Asset | null>(null);
  const openCommandPalette = useCallback(() => setJumpScope("all"), []);
  const openQuickConnect = useCallback(() => setJumpScope("hosts"), []);
  const openDatabasePicker = useCallback(() => setJumpScope("database"), []);
  useTabRecencyTracker();

  const queryClient = useQueryClient();
  const { vaultStatus, vaultLocked, lockVault, retryVault } = useVaultStatus();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list() });
  const groupsQuery = useQuery({
    queryKey: ["groups"],
    queryFn: () => assetApi.groupList(),
    enabled: activeProject !== ALL_PROJECTS,
  });
  useEffect(() => {
    if (activeProject === ALL_PROJECTS || !assetsQuery.data || !groupsQuery.data) return;
    setProjectInventory({ assets: assetsQuery.data, groups: groupsQuery.data });
  }, [activeProject, assetsQuery.data, groupsQuery.data, setProjectInventory]);
  // The authoritative assets refresh drives deletedTarget: workspaces whose
  // target disappeared are marked and preserved (never auto-disconnected),
  // and the mark clears when the target reappears. The store's epoch guard
  // keeps a snapshot from a previous account from mislabeling.
  useEffect(() => {
    if (!assetsQuery.data) return;
    reconcileDeletedTargets(assetsQuery.data, accountEpoch);
  }, [assetsQuery.data, accountEpoch, reconcileDeletedTargets]);
  const sync = useSyncStatusSummary(vaultLocked);
  useWebAuthRefresh();

  const layout = useShellLayout();
  const { viewport, overlaySidebars } = layout;
  useKeyboardInset();
  const docks = useOverlayDocks(overlaySidebars);
  const { leftDockOpen, rightDockOpen, setLeftOpen, setRightOpen } = docks;
  useAiToastInset(rightDockOpen);

  const active = tabs.find((t) => t.id === activeTabId) ?? null;
  const terminalKeysVisible =
    viewport.terminalKeys &&
    (ws?.panes.some((p) => {
      const current = p.tabs.find((t) => t.id === p.activeTabId) ?? p.tabs[p.tabs.length - 1];
      return current?.kind === "terminal";
    }) ?? false);
  useKeysToolbarHeight(terminalKeysVisible, `${ws?.id ?? ""}:${ws?.panes.map((p) => p.activeTabId).join(",") ?? ""}`);
  const activeSessionId = ws?.kind === "session" ? ws.sessionId : undefined;
  const backgroundAvailable = useBackgroundSessionsAccess(ws);

  const actions = useWorkspaceActions({ ws, backgroundAvailable, openDatabasePicker });
  const { openLocalTerminal, openNewTerminal, openSettings } = actions;
  const { wsMenu, openWorkspaceSwitcher, closeWorkspaceSwitcher } = useWorkspaceSwitcher(
    visibleWorkspaces,
    actions.closeAllWorkspaces,
  );

  useAppKeybindings({
    leftDockOpen,
    rightDockOpen,
    activeTabId,
    setLeftOpen,
    setRightOpen,
    openLocalTerminal,
    openNewTerminal,
    lockVault,
    openCommandPalette,
    openQuickConnect,
    openSettings,
  });
  useAppDialogHandlers();
  useAppBootEffects(queryClient);

  const bindings = useKeybindings();
  const hasDbAssets = assetsQuery.data?.some((a) => dbKindOf(a.kind) !== null) ?? true;

  const parts: ShellParts = {
    layout,
    terminalKeysVisible,
    aiToast: aiToastPlacement(viewport.width, overlaySidebars, rightDockOpen, rightWidth),
    rightWidth,
    activeTabKind: active?.kind ?? null,
    rail: {
      overlaySidebars,
      setOverlayDock: docks.setOverlayDock,
      setLeftOpen,
      leftDockOpen,
      backgroundAvailable,
      workspace: ws,
      hasDbAssets,
      actions,
    },
    header: {
      ws,
      activeSessionId,
      activeTab: active,
      rightDockOpen,
      commandPaletteLabel: formatBinding(bindings.commandPalette),
      onOpenWorkspaceSwitcher: openWorkspaceSwitcher,
      onToggleRight: () => setRightOpen(!rightDockOpen),
      onOpenPalette: openCommandPalette,
    },
    strip: {
      workspaces,
      activeWorkspaceId,
      activeProject,
      projectInventory,
      groups: groupsQuery.data ?? [],
      onSelect: setActiveWorkspace,
      onReorder: (dragId, overId, before) => {
        const current = useUi.getState();
        const from = current.workspaces.findIndex((workspace) => workspace.id === dragId);
        const over = current.workspaces.findIndex((workspace) => workspace.id === overId);
        if (from < 0 || over < 0) return;
        const to = before ? (from < over ? over - 1 : over) : from < over ? over : over + 1;
        current.moveWorkspace(from, to);
      },
    },
    banners: {
      ws,
      onOpenSettings: openSettings,
      onPickOtherAsset: () => {
        if (showLeftPanel("assets", false)) setLeftOpen(true);
      },
    },
    body: {
      ws,
      visibleWorkspaces,
      activeTab: active,
      activeSessionId,
      overlaySidebars,
      splitAllowed: viewport.splitAllowed,
      singlePane: layout.mode === "mobile" || viewport.compact,
      docks,
      onNewTerminal: () => void openNewTerminal(),
      onLocalTerminal: () => void openLocalTerminal(),
      onOpenPalette: openCommandPalette,
    },
    statusBar: {
      vaultStatus,
      onRetryVaultStatus: retryVault,
      sync,
      onOpenVaultProtection: actions.openVaultProtection,
      onOpenSyncSettings: actions.openSyncSettings,
    },
    dialogs: {
      jumpScope,
      setJumpScope,
      editingAsset: paletteEditingAsset,
      setEditingAsset: setPaletteEditingAsset,
      workspaceMenu: wsMenu,
      onCloseWorkspaceMenu: closeWorkspaceSwitcher,
    },
  };

  // The mobile mode follows the device and never changes at runtime, so switching shells never remounts the tree.
  return layout.mode === "mobile" ? <MobileShell {...parts} /> : <DesktopShell {...parts} />;
}
