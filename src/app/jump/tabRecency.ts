import { useEffect } from "react";
import { useUi } from "../store";

// Tab activation order for the jump palette: a monotonic sequence, so it stays deterministic and cheap.
const activations = new Map<string, number>();
let sequence = 0;
let lastActive: string | null = null;

export function noteTabActivated(tabId: string): void {
  if (lastActive === tabId) return;
  lastActive = tabId;
  sequence += 1;
  activations.set(tabId, sequence);
}

// tabRecency is larger for more recently activated tabs and 0 for tabs not seen active.
export function tabRecency(tabId: string): number {
  return activations.get(tabId) ?? 0;
}

// pruneTabRecency forgets tabs that are no longer open.
export function pruneTabRecency(openTabIds: ReadonlySet<string>): void {
  for (const tabId of activations.keys()) {
    if (!openTabIds.has(tabId)) activations.delete(tabId);
  }
  if (lastActive && !openTabIds.has(lastActive)) lastActive = null;
}

export function resetTabRecency(): void {
  activations.clear();
  sequence = 0;
  lastActive = null;
}

type RecencyState = ReturnType<typeof useUi.getState>;

function activeTabOf(state: RecencyState): string | null {
  const workspace = state.workspaces.find((item) => item.id === state.activeWorkspaceId);
  const pane = workspace?.panes.find((item) => item.id === workspace.activePaneId) ?? workspace?.panes[0];
  return pane?.activeTabId ?? null;
}

// useTabRecencyTracker records which tab is active as the user moves around, for the jump palette's order.
export function useTabRecencyTracker(): void {
  useEffect(() => {
    let workspaces: RecencyState["workspaces"] | null = null;
    const record = (state: RecencyState) => {
      if (state.workspaces !== workspaces) {
        workspaces = state.workspaces;
        pruneTabRecency(new Set(workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)))));
      }
      const tabId = activeTabOf(state);
      if (tabId) noteTabActivated(tabId);
    };
    record(useUi.getState());
    return useUi.subscribe(record);
  }, []);
}
