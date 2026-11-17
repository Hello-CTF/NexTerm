import { create } from "zustand";
import { initTheme, subscribeTheme } from "../theme";
import { retainUnreadTabs } from "../unreadStore";
import { reconcileActiveWorkspaceForFocus } from "../projectFocus";
import { createAiSlice } from "./aiSlice";
import { createHelperSlice } from "./helperSlice";
import { saveLayout } from "./persistence";
import { createRuntimeSlice } from "./runtimeSlice";
import { createSessionSlice } from "./sessionSlice";
import { createTabSlice } from "./tabSlice";
import type { UiState } from "./types";
import { createUiSlice } from "./uiSlice";
import { createWorkspaceSlice, pruneZoomedPanes } from "./workspaceSlice";

// The UI store is composed from domain slices; each slice owns a disjoint set of UiState keys.
export const useUi = create<UiState>((set, get) => ({
  ...createUiSlice(set, get),
  ...createWorkspaceSlice(set, get),
  ...createTabSlice(set, get),
  ...createSessionSlice(set, get),
  ...createRuntimeSlice(set, get),
  ...createAiSlice(set, get),
  ...createHelperSlice(set, get),
}));

useUi.subscribe((state, previous) => {
  if (
    state.activeProject === previous.activeProject &&
    state.projectInventory === previous.projectInventory &&
    state.workspaces === previous.workspaces &&
    state.activeWorkspaceId === previous.activeWorkspaceId
  ) {
    return;
  }
  const activeWorkspaceId = reconcileActiveWorkspaceForFocus(state);
  if (activeWorkspaceId !== state.activeWorkspaceId) useUi.setState({ activeWorkspaceId });
});

// A pane zoom lapses when focus leaves its pane by any path; drop it then, so it does not return with the focus.
useUi.subscribe((state, previous) => {
  if (state.workspaces === previous.workspaces && state.zoomedPanes === previous.zoomedPanes) return;
  const zoomedPanes = pruneZoomedPanes(state.workspaces, state.zoomedPanes);
  if (zoomedPanes !== state.zoomedPanes) useUi.setState({ zoomedPanes });
});

// The sidebars follow the workspace: leaving one records what it showed, and entering one shows what it showed last.
// A workspace with no record yet keeps the current sidebars.
useUi.subscribe((state, previous) => {
  if (state.activeWorkspaceId === previous.activeWorkspaceId) return;
  let workspaceDocks = state.workspaceDocks;
  const left = previous.activeWorkspaceId;
  if (left && state.workspaces.some((workspace) => workspace.id === left)) {
    const recorded = workspaceDocks[left];
    if (recorded?.leftOpen !== previous.leftOpen || recorded?.rightOpen !== previous.rightOpen) {
      workspaceDocks = { ...workspaceDocks, [left]: { leftOpen: previous.leftOpen, rightOpen: previous.rightOpen } };
    }
  }
  const entered = state.activeWorkspaceId ? workspaceDocks[state.activeWorkspaceId] : undefined;
  const leftOpen = entered?.leftOpen ?? state.leftOpen;
  const rightOpen = entered?.rightOpen ?? state.rightOpen;
  if (workspaceDocks !== state.workspaceDocks || leftOpen !== state.leftOpen || rightOpen !== state.rightOpen) {
    useUi.setState({ workspaceDocks, leftOpen, rightOpen });
  }
});

useUi.subscribe((state, previous) => {
  if (
    state.rightOpen !== previous.rightOpen ||
    state.leftWidth !== previous.leftWidth ||
    state.rightWidth !== previous.rightWidth
  ) {
    // Cache remote changes too, so startup matches the last visible layout before sync finishes.
    saveLayout({
      leftWidth: state.leftWidth,
      rightWidth: state.rightWidth,
      rightOpen: state.rightOpen,
    });
  }
});

useUi.subscribe((state, previous) => {
  if (state.workspaces === previous.workspaces) return;
  const openTabIds = new Set(
    state.workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)),
    ),
  );
  retainUnreadTabs(openTabIds);
});

initTheme();
subscribeTheme((m, resolved) => {
  useUi.setState({ themeMode: m, resolvedTheme: resolved });
});
