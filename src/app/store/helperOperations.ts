import type { HelperProgressEvent } from "../../ipc/events";
import type { HelperOperation, HelperOperationKind } from "../../features/terminal/helperOperationState";
import type { UiState } from "./types";

export interface HelperTabContext {
  tabId: string;
  sessionId: string;
  kind: HelperOperationKind;
  originalTabId?: string;
  quickConnect: boolean;
}

export function helperTabContext(state: UiState, tabId: string): HelperTabContext | null {
  for (const workspace of state.workspaces) {
    for (const pane of workspace.panes) {
      const tab = pane.tabs.find((candidate) => candidate.id === tabId);
      if (!tab || tab.kind !== "terminal" || !tab.sessionId) continue;
      return {
        tabId: tab.id,
        sessionId: tab.sessionId,
        kind: tab.tabId ? "reattach" : "deploy",
        originalTabId: tab.tabId,
        quickConnect: Boolean(workspace.quickTarget && !workspace.assetId),
      };
    }
  }
  return null;
}

function latestHelperOperationIntent(state: UiState, sessionId: string): HelperTabContext | null {
  let latest: HelperTabContext | null = null;
  let sequence = 0;
  for (const [tabId, intent] of Object.entries(state.helperOperationIntents)) {
    if (intent.sequence < sequence) continue;
    const context = helperTabContext(state, tabId);
    if (context?.sessionId !== sessionId) continue;
    latest = { ...context, kind: intent.kind, originalTabId: intent.originalTabId };
    sequence = intent.sequence;
  }
  return latest;
}

export function helperProgressContext(state: UiState, progress: HelperProgressEvent): HelperTabContext | null {
  for (const [tabId, operation] of Object.entries(state.helperOperations.operations)) {
    if (operation.operationId === progress.operationId) {
      const context = helperTabContext(state, tabId);
      if (context?.sessionId === progress.sessionId) return context;
    }
  }
  const pending = state.helperRetries[progress.sessionId];
  if (pending) {
    const context = helperTabContext(state, pending.tabId);
    if (context?.sessionId === progress.sessionId) return context;
  }
  const intended = latestHelperOperationIntent(state, progress.sessionId);
  if (intended) return intended;
  const mapped = state.helperOperationTabs[progress.sessionId];
  if (mapped) {
    const context = helperTabContext(state, mapped);
    if (context?.sessionId === progress.sessionId) return context;
  }
  const activeWorkspace = state.workspaces.find((workspace) => workspace.id === state.activeWorkspaceId);
  const activePane = activeWorkspace?.panes.find((pane) => pane.id === activeWorkspace.activePaneId);
  const activeTab = activePane?.activeTabId;
  if (activeTab) {
    const context = helperTabContext(state, activeTab);
    if (context?.sessionId === progress.sessionId) return context;
  }
  for (const workspace of state.workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        const context = helperTabContext(state, tab.id);
        if (context?.sessionId === progress.sessionId) return context;
      }
    }
  }
  return null;
}

export function helperProgressKey(progress: Pick<HelperProgressEvent, "sessionId" | "assetId">): string {
  return JSON.stringify([progress.sessionId, progress.assetId]);
}

export function helperOperationMessage(progress: HelperProgressEvent): string | undefined {
  return progress.cause?.trim() || progress.error?.trim() || undefined;
}

export function helperOperationLive(operation: HelperOperation | undefined): boolean {
  return operation?.status === "running" || operation?.status === "awaiting_choice";
}

let localHelperOperationSeq = 0;

export function nextLocalHelperOperationID(): string {
  localHelperOperationSeq += 1;
  return `local-restore-${localHelperOperationSeq}`;
}
