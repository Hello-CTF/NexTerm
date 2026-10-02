import type { AppTab, Workspace } from "../../app/store";

export interface EditorGuardState {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
}

const dirtyEditors = new Set<string>();

function editorKey(sessionId: string, path: string): string {
  return JSON.stringify([sessionId, path]);
}

export function setFileEditorDirty(sessionId: string, path: string, dirty: boolean): void {
  const key = editorKey(sessionId, path);
  if (dirty) dirtyEditors.add(key);
  else dirtyEditors.delete(key);
}

export function isActiveFileEditor(
  state: EditorGuardState,
  sessionId: string,
  path: string,
): boolean {
  const workspace =
    state.workspaces.find((w) => w.id === state.activeWorkspaceId) ??
    state.workspaces[state.workspaces.length - 1];
  const pane =
    workspace?.panes.find((p) => p.id === workspace.activePaneId) ?? workspace?.panes[0];
  const activeTabId = pane?.activeTabId ?? pane?.tabs[pane.tabs.length - 1]?.id;
  const tab = pane?.tabs.find((t) => t.id === activeTabId);
  return tab?.kind === "files" && tab.sessionId === sessionId && tab.path === path;
}

export function shouldHandleEditorSave(
  state: EditorGuardState,
  sessionId: string,
  path: string,
  dirty: boolean,
  inFlight: boolean,
): boolean {
  return dirty && !inFlight && isActiveFileEditor(state, sessionId, path);
}

export function shouldClearEditorDirty(savedVersion: number, currentVersion: number): boolean {
  return savedVersion === currentVersion;
}

export function isDirtyFileEditor(tab: AppTab): boolean {
  return (
    tab.kind === "files" &&
    tab.sessionId !== undefined &&
    tab.path !== undefined &&
    dirtyEditors.has(editorKey(tab.sessionId, tab.path))
  );
}

export function dirtyFileEditors(tabs: AppTab[]): AppTab[] {
  return tabs.filter(isDirtyFileEditor);
}
