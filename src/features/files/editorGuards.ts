import type { AppTab, Workspace } from "../../app/store";
import { isWindows } from "../../app/platform";
import { joinPath, norm } from "./pathUtils";

export interface EditorGuardState {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
}

interface FileLocator {
  sessionId: string;
  path: string;
}

interface EditorOperation extends FileLocator {
  token: number;
  caseInsensitive: boolean;
}

export interface UnknownFileEditorRename {
  token: number;
  sessionId: string;
  from: string;
  to: string;
  kind: string;
  caseInsensitive: boolean;
}

type RenameOperation = UnknownFileEditorRename;

const dirtyEditors = new Map<string, FileLocator>();
const editorOperations = new Map<number, EditorOperation>();
const renameOperations = new Map<number, RenameOperation>();
const unknownRenames = new Map<number, RenameOperation>();
let operationSeq = 0;

export function fileEditorCaseInsensitive(sessionKind: string): boolean {
  return sessionKind === "winrm" || (sessionKind === "local" && isWindows());
}

function foldFileEditorPath(path: string, caseInsensitive: boolean): string {
  const normalized = norm(path);
  return caseInsensitive ? normalized.toLowerCase() : normalized;
}

export function fileEditorLocatorKey(
  sessionId: string,
  path: string,
  caseInsensitive = false,
): string {
  return JSON.stringify([sessionId, foldFileEditorPath(path, caseInsensitive)]);
}

export function fileEditorPathMatches(
  path: string,
  from: string,
  kind: string,
  caseInsensitive = false,
): boolean {
  const candidate = foldFileEditorPath(path, caseInsensitive);
  const target = foldFileEditorPath(from, caseInsensitive);
  if (candidate === target) return true;
  if (kind !== "dir") return false;
  return candidate.startsWith(target.endsWith("/") ? target : `${target}/`);
}

export function migratedFileEditorPath(
  path: string,
  from: string,
  to: string,
  kind: string,
  caseInsensitive = false,
): string | null {
  if (!fileEditorPathMatches(path, from, kind, caseInsensitive)) return null;
  const candidate = norm(path);
  const source = norm(from);
  if (foldFileEditorPath(candidate, caseInsensitive) === foldFileEditorPath(source, caseInsensitive)) {
    return norm(to);
  }
  const prefix = source.endsWith("/") ? source : `${source}/`;
  return joinPath(norm(to), candidate.slice(prefix.length));
}

function pathsOverlap(
  left: string,
  leftKind: string,
  right: string,
  rightKind: string,
  caseInsensitive: boolean,
): boolean {
  return (
    fileEditorPathMatches(left, right, rightKind, caseInsensitive) ||
    fileEditorPathMatches(right, left, leftKind, caseInsensitive)
  );
}

function renameTouchesPath(operation: RenameOperation, path: string): boolean {
  return (
    fileEditorPathMatches(path, operation.from, operation.kind, operation.caseInsensitive) ||
    fileEditorPathMatches(path, operation.to, operation.kind, operation.caseInsensitive)
  );
}

function renamesOverlap(left: RenameOperation, right: RenameOperation): boolean {
  const caseInsensitive = left.caseInsensitive || right.caseInsensitive;
  return (
    pathsOverlap(left.from, left.kind, right.from, right.kind, caseInsensitive) ||
    pathsOverlap(left.from, left.kind, right.to, right.kind, caseInsensitive) ||
    pathsOverlap(left.to, left.kind, right.from, right.kind, caseInsensitive) ||
    pathsOverlap(left.to, left.kind, right.to, right.kind, caseInsensitive)
  );
}

function nextOperation(sessionId: string, path: string, caseInsensitive: boolean): number {
  const token = ++operationSeq;
  editorOperations.set(token, { token, sessionId, path: norm(path), caseInsensitive });
  return token;
}

export function fileEditorRenamePending(sessionId: string, path: string): boolean {
  for (const operation of renameOperations.values()) {
    if (operation.sessionId === sessionId && renameTouchesPath(operation, path)) return true;
  }
  return false;
}

export function beginFileEditorLoad(
  sessionId: string,
  path: string,
  caseInsensitive = false,
): number | null {
  for (const operation of editorOperations.values()) {
    if (
      operation.sessionId === sessionId &&
      fileEditorPathMatches(operation.path, path, "file", caseInsensitive || operation.caseInsensitive)
    ) {
      return null;
    }
  }
  for (const operation of renameOperations.values()) {
    if (operation.sessionId === sessionId && renameTouchesPath(operation, path)) return null;
  }
  return nextOperation(sessionId, path, caseInsensitive);
}

export function beginFileEditorSave(
  sessionId: string,
  path: string,
  caseInsensitive = false,
): number | "busy" | "rename" | "unknown" {
  for (const operation of editorOperations.values()) {
    if (
      operation.sessionId === sessionId &&
      fileEditorPathMatches(operation.path, path, "file", caseInsensitive || operation.caseInsensitive)
    ) {
      return "busy";
    }
  }
  for (const operation of renameOperations.values()) {
    if (operation.sessionId === sessionId && renameTouchesPath(operation, path)) return "rename";
  }
  for (const operation of unknownRenames.values()) {
    if (
      operation.sessionId === sessionId &&
      fileEditorPathMatches(path, operation.from, operation.kind, operation.caseInsensitive)
    ) {
      return "unknown";
    }
  }
  return nextOperation(sessionId, path, caseInsensitive);
}

export function endFileEditorOperation(token: number): void {
  editorOperations.delete(token);
}

export function beginFileEditorRename(
  sessionId: string,
  from: string,
  to: string,
  kind: string,
  caseInsensitive = false,
): number | "busy" | "unknown" {
  const candidate: RenameOperation = {
    token: operationSeq + 1,
    sessionId,
    from: norm(from),
    to: norm(to),
    kind,
    caseInsensitive,
  };
  for (const operation of unknownRenames.values()) {
    if (operation.sessionId === sessionId && renamesOverlap(operation, candidate)) return "unknown";
  }
  for (const operation of renameOperations.values()) {
    if (operation.sessionId === sessionId && renamesOverlap(operation, candidate)) return "busy";
  }
  for (const operation of editorOperations.values()) {
    const overlaps = (path: string, kind: string) =>
      pathsOverlap(
        operation.path,
        "file",
        path,
        kind,
        caseInsensitive || operation.caseInsensitive,
      );
    if (operation.sessionId === sessionId && (overlaps(candidate.from, candidate.kind) || overlaps(candidate.to, candidate.kind))) {
      return "busy";
    }
  }
  const token = ++operationSeq;
  candidate.token = token;
  renameOperations.set(token, candidate);
  return token;
}

export function finishFileEditorRename(token: number, unknown: boolean): void {
  const operation = renameOperations.get(token);
  if (!operation) return;
  renameOperations.delete(token);
  if (unknown) unknownRenames.set(token, operation);
}

export function unknownFileEditorRename(
  sessionId: string,
  path: string,
): UnknownFileEditorRename | null {
  for (const operation of unknownRenames.values()) {
    if (
      operation.sessionId === sessionId &&
      fileEditorPathMatches(path, operation.from, operation.kind, operation.caseInsensitive)
    ) {
      return operation;
    }
  }
  return null;
}

export function unknownFileEditorRenameByToken(token: number): UnknownFileEditorRename | null {
  return unknownRenames.get(token) ?? null;
}

export function unknownFileEditorRenameForRename(
  sessionId: string,
  from: string,
  to: string,
  kind: string,
  caseInsensitive = false,
): UnknownFileEditorRename | null {
  const candidate: RenameOperation = {
    token: 0,
    sessionId,
    from: norm(from),
    to: norm(to),
    kind,
    caseInsensitive,
  };
  for (const operation of unknownRenames.values()) {
    if (operation.sessionId === sessionId && renamesOverlap(operation, candidate)) return operation;
  }
  return null;
}

export function unknownFileEditorRenameConflict(
  state: EditorGuardState,
  operation: UnknownFileEditorRename,
): AppTab | null {
  for (const workspace of state.workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        if (
          tab.kind === "files" &&
          tab.sessionId === operation.sessionId &&
          tab.path &&
          fileEditorPathMatches(tab.path, operation.to, operation.kind, operation.caseInsensitive) &&
          !fileEditorPathMatches(tab.path, operation.from, operation.kind, operation.caseInsensitive)
        ) {
          return tab;
        }
      }
    }
  }
  return null;
}

export function clearUnknownFileEditorRename(token: number): void {
  unknownRenames.delete(token);
}

export function acknowledgeFileEditorLocation(sessionId: string, path: string): void {
  const operation = unknownFileEditorRename(sessionId, path);
  if (operation) unknownRenames.delete(operation.token);
}

export function migrateFileEditorDirty(
  sessionId: string,
  from: string,
  to: string,
  kind: string,
  caseInsensitive = false,
): void {
  for (const [key, locator] of [...dirtyEditors]) {
    if (locator.sessionId !== sessionId) continue;
    const path = migratedFileEditorPath(locator.path, from, to, kind, caseInsensitive);
    if (path === null) continue;
    dirtyEditors.delete(key);
    dirtyEditors.set(fileEditorLocatorKey(sessionId, path, caseInsensitive), { sessionId, path });
  }
}

export function setFileEditorDirty(
  sessionId: string,
  path: string,
  dirty: boolean,
  caseInsensitive = false,
): void {
  const key = fileEditorLocatorKey(sessionId, path, caseInsensitive);
  if (dirty) dirtyEditors.set(key, { sessionId, path: norm(path) });
  else dirtyEditors.delete(key);
}

export function isActiveFileEditor(
  state: EditorGuardState,
  sessionId: string,
  path: string,
  caseInsensitive = false,
): boolean {
  const workspace =
    state.workspaces.find((w) => w.id === state.activeWorkspaceId) ??
    state.workspaces[state.workspaces.length - 1];
  const pane =
    workspace?.panes.find((p) => p.id === workspace.activePaneId) ?? workspace?.panes[0];
  const activeTabId = pane?.activeTabId ?? pane?.tabs[pane.tabs.length - 1]?.id;
  const tab = pane?.tabs.find((t) => t.id === activeTabId);
  return (
    tab?.kind === "files" &&
    tab.sessionId === sessionId &&
    tab.path !== undefined &&
    fileEditorPathMatches(tab.path, path, "file", caseInsensitive)
  );
}

export function shouldHandleEditorSave(
  state: EditorGuardState,
  sessionId: string,
  path: string,
  dirty: boolean,
  inFlight: boolean,
  caseInsensitive = false,
): boolean {
  return dirty && !inFlight && isActiveFileEditor(state, sessionId, path, caseInsensitive);
}

export function shouldClearEditorDirty(savedVersion: number, currentVersion: number): boolean {
  return savedVersion === currentVersion;
}

export function isDirtyFileEditor(tab: AppTab, caseInsensitive = false): boolean {
  return (
    tab.kind === "files" &&
    tab.sessionId !== undefined &&
    tab.path !== undefined &&
    dirtyEditors.has(fileEditorLocatorKey(tab.sessionId, tab.path, caseInsensitive))
  );
}

export function dirtyFileEditors(
  tabs: AppTab[],
  sessions: { id: string; kind: string }[] = [],
): AppTab[] {
  return tabs.filter((tab) =>
    isDirtyFileEditor(
      tab,
      fileEditorCaseInsensitive(sessions.find((session) => session.id === tab.sessionId)?.kind ?? ""),
    ),
  );
}
