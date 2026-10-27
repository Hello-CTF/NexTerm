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
    const record = (state: RecencyState) => {
      const tabId = activeTabOf(state);
      if (tabId) noteTabActivated(tabId);
    };
    record(useUi.getState());
    return useUi.subscribe(record);
  }, []);
}
