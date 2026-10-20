import type { Asset, AssetGroup } from "../ipc/commands";
import type { Workspace } from "./store";

export const ALL_PROJECTS = "all" as const;
export const UNGROUPED_PROJECT = "ungrouped" as const;

const PROJECT_PREFIX = "project:";

export type ProjectFocus = typeof ALL_PROJECTS | typeof UNGROUPED_PROJECT | `${typeof PROJECT_PREFIX}${string}`;

export interface ProjectInventory {
  assets: readonly Asset[];
  groups: readonly AssetGroup[];
}

export interface ProjectFocusScope {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  activeProject: ProjectFocus;
  projectInventory: ProjectInventory | null;
}

export function projectFocusForGroup(groupId: string): `project:${string}` {
  return `${PROJECT_PREFIX}${groupId}`;
}

export function projectGroupId(focus: ProjectFocus): string | null {
  if (!focus.startsWith(PROJECT_PREFIX)) return null;
  return focus.slice(PROJECT_PREFIX.length) || null;
}

export function isProjectFocus(value: unknown): value is ProjectFocus {
  return value === ALL_PROJECTS || value === UNGROUPED_PROJECT || (
    typeof value === "string" && value.startsWith(PROJECT_PREFIX) && value.length > PROJECT_PREFIX.length
  );
}

export function topLevelProjects(groups: readonly AssetGroup[]): AssetGroup[] {
  return groups.filter((group) => group.parentId === null);
}

function createProjectResolver(groups: readonly AssetGroup[]) {
  const byId = new Map(groups.map((group) => [group.id, group]));
  const cache = new Map<string | null, string | null>([[null, null]]);

  return (groupId: string | null): string | null => {
    if (cache.has(groupId)) return cache.get(groupId) ?? null;
    let current = groupId ? byId.get(groupId) : undefined;
    if (!current) {
      cache.set(groupId, null);
      return null;
    }
    const seen = new Set<string>();
    while (current.parentId !== null) {
      if (seen.has(current.id)) {
        cache.set(groupId, null);
        return null;
      }
      seen.add(current.id);
      const parent = byId.get(current.parentId);
      if (!parent) {
        cache.set(groupId, null);
        return null;
      }
      current = parent;
    }
    cache.set(groupId, current.id);
    return current.id;
  };
}

export function filterAssetsForProject(
  assets: readonly Asset[],
  focus: ProjectFocus,
  groups: readonly AssetGroup[],
): Asset[] {
  if (focus === ALL_PROJECTS) return [...assets];
  const selected = focus === UNGROUPED_PROJECT ? null : projectGroupId(focus);
  const resolve = createProjectResolver(groups);
  return assets.filter((asset) => resolve(asset.groupId) === selected);
}

export function filterGroupsForProject(
  groups: readonly AssetGroup[],
  focus: ProjectFocus,
): AssetGroup[] {
  if (focus === ALL_PROJECTS) return [...groups];
  if (focus === UNGROUPED_PROJECT) return [];
  const selected = projectGroupId(focus);
  const resolve = createProjectResolver(groups);
  return groups.filter((group) => resolve(group.id) === selected);
}

export function filterWorkspacesForProject(
  workspaces: readonly Workspace[],
  focus: ProjectFocus,
  inventory: ProjectInventory,
): Workspace[] {
  if (focus === ALL_PROJECTS) return [...workspaces];
  const selected = focus === UNGROUPED_PROJECT ? null : projectGroupId(focus);
  const resolve = createProjectResolver(inventory.groups);
  const assets = new Map(inventory.assets.map((asset) => [asset.id, asset]));
  return workspaces.filter((workspace) => {
    const asset = workspace.assetId ? assets.get(workspace.assetId) : undefined;
    return resolve(asset?.groupId ?? null) === selected;
  });
}

export function focusedWorkspaces(state: ProjectFocusScope): Workspace[] {
  if (state.activeProject === ALL_PROJECTS) return state.workspaces;
  if (!state.projectInventory) return [];
  return filterWorkspacesForProject(state.workspaces, state.activeProject, state.projectInventory);
}

export function focusedActiveWorkspace(state: ProjectFocusScope): Workspace | null {
  const visible = focusedWorkspaces(state);
  return visible.find((workspace) => workspace.id === state.activeWorkspaceId) ??
    visible[visible.length - 1] ??
    null;
}

export function reconcileActiveWorkspaceForFocus(state: ProjectFocusScope): string | null {
  if (state.activeProject === ALL_PROJECTS || !state.projectInventory) return state.activeWorkspaceId;
  return focusedActiveWorkspace(state)?.id ?? null;
}

export function projectFocusLabel(focus: ProjectFocus, groups: readonly AssetGroup[]): string {
  if (focus === ALL_PROJECTS) return "全部";
  if (focus === UNGROUPED_PROJECT) return "未分组";
  const id = projectGroupId(focus);
  return groups.find((group) => group.id === id && group.parentId === null)?.name ?? "未知项目";
}

export const PROJECT_GROUP_COLLAPSE_LIMIT = 256;

const PROJECT_GROUP_COLLAPSE_VERSION = 1;

export function sanitizeProjectGroupCollapse(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const latest = new Map<string, true>();
  for (const value of raw) {
    if (typeof value === "string" && value.length > 0) {
      latest.delete(value);
      latest.set(value, true);
    }
  }
  return [...latest.keys()].slice(-PROJECT_GROUP_COLLAPSE_LIMIT);
}

export function serializeProjectGroupCollapse(collapsed: readonly string[]): string {
  return JSON.stringify({ v: PROJECT_GROUP_COLLAPSE_VERSION, collapsed: sanitizeProjectGroupCollapse(collapsed) });
}

export function parseProjectGroupCollapse(text: string): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return [];
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return [];
  const payload = parsed as Record<string, unknown>;
  if (payload.v !== PROJECT_GROUP_COLLAPSE_VERSION) return [];
  return sanitizeProjectGroupCollapse(payload.collapsed);
}

export function isProjectGroupCollapsed(collapsed: readonly string[], groupId: string): boolean {
  return collapsed.includes(groupId);
}

export function toggleProjectGroupCollapse(collapsed: readonly string[], groupId: string): string[] {
  if (!groupId) return [...collapsed];
  const next = collapsed.includes(groupId)
    ? collapsed.filter((id) => id !== groupId)
    : [...collapsed, groupId];
  return next.slice(-PROJECT_GROUP_COLLAPSE_LIMIT);
}
