import {
  aiTabLogicalKey,
  paneLogicalKey,
  tabLogicalKey,
  workspaceLogicalKey,
  type PersistedLayout,
} from "./layoutMerge";
import type { AiBoard, AiDockTab, AppTab, LeftMode, Pane, Workspace } from "./store";
import { ALL_PROJECTS, isProjectFocus, type ProjectFocus } from "./projectFocus";
import { workspacePaneOrder } from "./paneLayout";

const DEVICE_LAYOUT_PREFIX = "nexterm:layout-device-v1:";

interface FocusReference {
  id: string;
  key: string;
}

interface DevicePaneFocus {
  pane: FocusReference;
  activeTab: FocusReference | null;
}

interface DeviceWorkspaceFocus {
  workspace: FocusReference;
  activePane: FocusReference | null;
  panes: DevicePaneFocus[];
}

interface DeviceAiFocus {
  boardKey: string;
  workspace: FocusReference | null;
  activeTab: FocusReference | null;
}

export interface DeviceLayoutState {
  v: 1;
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  activeProject?: ProjectFocus;
  activeWorkspace: FocusReference | null;
  workspaces: DeviceWorkspaceFocus[];
  aiBoards: DeviceAiFocus[];
}

export interface DeviceLayoutSource {
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  activeProject?: ProjectFocus;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  aiBoards: Record<string, AiBoard>;
}

// normalizeLeftMode reads a stored left dock mode; anything unknown falls back to the asset tree.
export function normalizeLeftMode(raw: unknown): LeftMode {
  return raw === "credentials" || raw === "files" ? raw : "assets";
}

function reference(id: string, key: string): FocusReference {
  return { id, key };
}

function workspaceReference(workspace: Workspace): FocusReference {
  return reference(workspace.id, workspaceLogicalKey(workspace));
}

function tabReference(tab: AppTab): FocusReference {
  return reference(tab.id, tabLogicalKey(tab));
}

function aiTabReference(tab: AiDockTab): FocusReference {
  return reference(tab.id, aiTabLogicalKey(tab));
}

export function captureDeviceLayout(source: DeviceLayoutSource): DeviceLayoutState {
  const active = source.workspaces.find((workspace) => workspace.id === source.activeWorkspaceId);
  return {
    v: 1,
    leftOpen: source.leftOpen,
    leftMode: source.leftMode,
    rightOpen: source.rightOpen,
    leftWidth: source.leftWidth,
    rightWidth: source.rightWidth,
    activeProject: source.activeProject ?? ALL_PROJECTS,
    activeWorkspace: active ? workspaceReference(active) : null,
    workspaces: source.workspaces.map((workspace) => {
      const activePane = workspace.panes.find((pane) => pane.id === workspace.activePaneId);
      return {
        workspace: workspaceReference(workspace),
        activePane: activePane
          ? reference(activePane.id, paneLogicalKey(activePane))
          : null,
        panes: workspace.panes.map((pane) => {
          const activeTab = pane.tabs.find((tab) => tab.id === pane.activeTabId);
          return {
            pane: reference(pane.id, paneLogicalKey(pane)),
            activeTab: activeTab ? tabReference(activeTab) : null,
          };
        }),
      };
    }),
    aiBoards: Object.entries(source.aiBoards).map(([boardKey, board]) => {
      const workspace = source.workspaces.find((item) => item.id === boardKey);
      const activeTab = board.tabs.find((tab) => tab.id === board.activeTabId);
      return {
        boardKey,
        workspace: workspace ? workspaceReference(workspace) : null,
        activeTab: activeTab ? aiTabReference(activeTab) : null,
      };
    }),
  };
}

function isReference(value: unknown): value is FocusReference {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<FocusReference>;
  return typeof candidate.id === "string" && typeof candidate.key === "string";
}

function isNullableReference(value: unknown): value is FocusReference | null {
  return value === null || isReference(value);
}

function validDeviceLayout(value: unknown): value is DeviceLayoutState {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<DeviceLayoutState>;
  return (
    candidate.v === 1 &&
    typeof candidate.leftOpen === "boolean" &&
    (candidate.leftMode === "assets" || candidate.leftMode === "files" || candidate.leftMode === "credentials") &&
    typeof candidate.rightOpen === "boolean" &&
    typeof candidate.leftWidth === "number" && Number.isFinite(candidate.leftWidth) &&
    typeof candidate.rightWidth === "number" && Number.isFinite(candidate.rightWidth) &&
    (candidate.activeProject === undefined || isProjectFocus(candidate.activeProject)) &&
    isNullableReference(candidate.activeWorkspace) &&
    Array.isArray(candidate.workspaces) &&
    candidate.workspaces.every((workspace) =>
      isReference(workspace.workspace) &&
      isNullableReference(workspace.activePane) &&
      Array.isArray(workspace.panes) &&
      workspace.panes.every((pane) => isReference(pane.pane) && isNullableReference(pane.activeTab))
    ) &&
    Array.isArray(candidate.aiBoards) &&
    candidate.aiBoards.every((board) =>
      typeof board.boardKey === "string" &&
      isNullableReference(board.workspace) &&
      isNullableReference(board.activeTab)
    )
  );
}

export function deviceLayoutStorageKey(accountId: string): string {
  return `${DEVICE_LAYOUT_PREFIX}${encodeURIComponent(accountId)}`;
}

export function loadDeviceLayout(key: string): DeviceLayoutState | null {
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    const parsed: unknown = JSON.parse(raw);
    return validDeviceLayout(parsed) ? { ...parsed, leftMode: normalizeLeftMode(parsed.leftMode) } : null;
  } catch {
    return null;
  }
}

export function saveDeviceLayout(key: string, state: DeviceLayoutState): void {
  try {
    window.localStorage.setItem(key, JSON.stringify(state));
  } catch {
  }
}

function matchReference<T>(
  ref: FocusReference,
  candidates: T[],
  id: (candidate: T) => string,
  key: (candidate: T, index: number) => string,
): T | undefined {
  const direct = candidates.find((candidate) => id(candidate) === ref.id);
  if (direct) return direct;
  const matches = candidates.filter((candidate, index) => key(candidate, index) === ref.key);
  return matches.length === 1 ? matches[0] : undefined;
}

function matchWorkspace(ref: FocusReference, workspaces: Workspace[]): Workspace | undefined {
  return matchReference(ref, workspaces, (workspace) => workspace.id, workspaceLogicalKey);
}

function matchPane(ref: FocusReference, panes: Pane[]): Pane | undefined {
  return matchReference(ref, panes, (pane) => pane.id, paneLogicalKey);
}

function matchTab(ref: FocusReference, tabs: AppTab[]): AppTab | undefined {
  return matchReference(ref, tabs, (tab) => tab.id, tabLogicalKey);
}

function matchAiTab(ref: FocusReference, tabs: AiDockTab[]): AiDockTab | undefined {
  return matchReference(ref, tabs, (tab) => tab.id, aiTabLogicalKey);
}

function lastTabId(tabs: { id: string }[]): string | null {
  return tabs[tabs.length - 1]?.id ?? null;
}

export function restoreDeviceLayout(
  layout: PersistedLayout,
  device: DeviceLayoutState | null,
): PersistedLayout {
  if (!device) return layout;
  const workspaces = layout.workspaces.map((workspace) => {
    const stored = device.workspaces.filter((candidate) => {
      if (candidate.workspace.id === workspace.id) return true;
      return candidate.workspace.key === workspaceLogicalKey(workspace);
    });
    const storedWorkspace = stored.length === 1 ? stored[0] : stored.find((candidate) => candidate.workspace.id === workspace.id);
    if (!storedWorkspace) return workspace;
    // Panes match by id; a lone pane on both sides is the same pane even when another device
    // opened the workspace and minted a different id.
    const lonePane = storedWorkspace.panes.length === 1 && workspace.panes.length === 1;
    const panes = workspace.panes.map((pane) => {
      const storedPane = storedWorkspace.panes.find(
        (candidate) => candidate.pane.id === pane.id || candidate.pane.key === paneLogicalKey(pane),
      ) ?? (lonePane ? storedWorkspace.panes[0] : undefined);
      if (!storedPane) return pane;
      const active = storedPane.activeTab ? matchTab(storedPane.activeTab, pane.tabs) : undefined;
      return { ...pane, activeTabId: active?.id ?? lastTabId(pane.tabs) };
    });
    const activePane = storedWorkspace.activePane ? matchPane(storedWorkspace.activePane, panes) : undefined;
    return {
      ...workspace,
      panes,
      activePaneId: activePane?.id ?? workspacePaneOrder({ ...workspace, panes })[0] ?? workspace.activePaneId,
    };
  });
  const activeWorkspace = device.activeWorkspace
    ? matchWorkspace(device.activeWorkspace, workspaces)
    : undefined;
  const aiBoards = Object.fromEntries(
    Object.entries(layout.aiBoards).map(([boardKey, board]) => {
      const workspace = workspaces.find((item) => item.id === boardKey);
      const stored = device.aiBoards.find((candidate) => {
        if (candidate.boardKey === boardKey) return true;
        if (boardKey === "global" || !workspace || !candidate.workspace) return false;
        return candidate.workspace.key === workspaceLogicalKey(workspace);
      });
      if (!stored) return [boardKey, board];
      const active = stored.activeTab ? matchAiTab(stored.activeTab, board.tabs) : undefined;
      return [boardKey, { ...board, activeTabId: active?.id ?? lastTabId(board.tabs) }];
    }),
  );
  return {
    ...layout,
    leftOpen: device.leftOpen,
    leftMode: device.leftMode,
    rightOpen: device.rightOpen,
    leftWidth: device.leftWidth,
    rightWidth: device.rightWidth,
    workspaces,
    activeWorkspaceId: activeWorkspace?.id ?? workspaces[0]?.id ?? null,
    aiBoards,
  };
}

export function hasDeviceLayoutChange(previous: DeviceLayoutSource, next: DeviceLayoutSource): boolean {
  if (
    previous.leftOpen !== next.leftOpen ||
    previous.leftMode !== next.leftMode ||
    previous.rightOpen !== next.rightOpen ||
    previous.leftWidth !== next.leftWidth ||
    previous.rightWidth !== next.rightWidth ||
    (previous.activeProject ?? ALL_PROJECTS) !== (next.activeProject ?? ALL_PROJECTS) ||
    previous.activeWorkspaceId !== next.activeWorkspaceId
  ) {
    return true;
  }
  if (previous.workspaces !== next.workspaces) {
    if (previous.workspaces.length !== next.workspaces.length) return true;
    for (const [index, workspace] of previous.workspaces.entries()) {
      const other = next.workspaces[index];
      if (workspace === other) continue;
      if (workspace.id !== other.id || workspace.activePaneId !== other.activePaneId) return true;
      if (workspace.panes.length !== other.panes.length) return true;
      for (const [paneIndex, pane] of workspace.panes.entries()) {
        const otherPane = other.panes[paneIndex];
        if (pane === otherPane) continue;
        if (pane.id !== otherPane.id || pane.activeTabId !== otherPane.activeTabId) return true;
      }
    }
  }
  if (previous.aiBoards !== next.aiBoards) {
    const keys = Object.keys(previous.aiBoards);
    if (keys.length !== Object.keys(next.aiBoards).length) return true;
    for (const key of keys) {
      const board = previous.aiBoards[key];
      const other = next.aiBoards[key];
      if (!other || board.activeTabId !== other.activeTabId) return true;
    }
  }
  return false;
}
