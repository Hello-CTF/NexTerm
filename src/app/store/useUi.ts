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
import { createWorkspaceSlice } from "./workspaceSlice";

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
