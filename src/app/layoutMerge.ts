import type { AiBoard, AiDockTab, AppTab, LeftMode, Pane, Workspace } from "./store";

export interface PersistedLayout {
  v: number;
  leftOpen: boolean;
  leftMode: LeftMode;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  aiBoards: Record<string, AiBoard>;
}

export interface LayoutMergeOptions {
  preserveLocalTab?: (tab: AppTab, workspace: Workspace) => boolean;
  preserveLocalWorkspace?: (workspace: Workspace) => boolean;
}

interface EntityGroup<T> {
  key: string;
  base?: T;
  local?: T;
  remote?: T;
  baseIndex?: number;
  localIndex?: number;
  remoteIndex?: number;
}

interface EntityItem<T> {
  side: "base" | "local" | "remote";
  index: number;
  value: T;
  id: string;
  logicalKey: string;
  parent: number;
}

const TOOL_TAB_KINDS = new Set(["credentials", "settings", "audit", "devices", "history", "execBatch"]);
const GLOBAL_BOARD = "global";

function sameValue(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  return JSON.stringify(left) === JSON.stringify(right);
}

function valueOrder(value: unknown): string {
  if (value === undefined) return "0";
  return `1:${JSON.stringify(value)}`;
}

function mergeAddedValue<T>(local: T | undefined, remote: T | undefined): T | undefined {
  if (sameValue(local, remote)) return local;
  if (local === undefined) return remote;
  if (remote === undefined) return local;
  return valueOrder(local) <= valueOrder(remote) ? local : remote;
}

function mergeValue<T>(base: T | undefined, local: T | undefined, remote: T | undefined): T | undefined {
  if (sameValue(local, remote)) return local;
  if (sameValue(local, base)) return remote;
  if (sameValue(remote, base)) return local;
  return valueOrder(local) <= valueOrder(remote) ? local : remote;
}

export function workspaceLogicalKey(workspace: Workspace): string {
  if (workspace.kind === "tools") return "tools";
  if (workspace.assetId) return `${workspace.kind}:asset:${workspace.assetId}`;
  if (workspace.quickTarget) {
    const target = workspace.quickTarget;
    return `session:quick:${JSON.stringify([
      target.host.trim().toLowerCase(),
      target.port,
      target.username,
    ])}`;
  }
  if (workspace.sessionId) return `session:session:${workspace.sessionId}`;
  if (workspace.connId) return `db:conn:${workspace.connId}`;
  return `${workspace.kind}:id:${workspace.id}`;
}

export function paneLogicalKey(_pane: Pane, index: number): string {
  return `pane:${index}`;
}

export function tabLogicalKey(tab: AppTab): string {
  if (tab.kind === "terminal") {
    if (tab.tabId) return `terminal:tab:${tab.tabId}`;
    if (tab.containerId) return `terminal:container:${tab.containerId}:${tab.id}`;
    return `terminal:id:${tab.id}`;
  }
  if (tab.kind === "background") return "background";
  if ((tab.kind === "forward" && !tab.sessionId) || TOOL_TAB_KINDS.has(tab.kind)) return `tool:${tab.kind}`;
  if ((tab.kind === "files" || tab.kind === "log") && tab.path) {
    return `${tab.kind}:path:${tab.path}`;
  }
  if (tab.kind === "db") return `db:${tab.dbKind ?? ""}`;
  return `${tab.kind}:${JSON.stringify([tab.path ?? null, tab.containerId ?? null, tab.credId ?? null])}`;
}

export function aiTabLogicalKey(tab: AiDockTab): string {
  return tab.conversationId ? `conversation:${tab.conversationId}` : `ai:id:${tab.id}`;
}

function matchEntities<T>(
  base: T[],
  local: T[],
  remote: T[],
  id: (value: T) => string,
  logicalKey: (value: T, index: number) => string,
): EntityGroup<T>[] {
  const items: EntityItem<T>[] = [];
  const append = (side: EntityItem<T>["side"], values: T[]) => {
    values.forEach((value, index) => {
      items.push({
        side,
        index,
        value,
        id: id(value),
        logicalKey: logicalKey(value, index),
        parent: items.length,
      });
    });
  };
  append("base", base);
  append("local", local);
  append("remote", remote);

  const root = (item: EntityItem<T>): number => {
    let cursor = item.parent;
    while (items[cursor].parent !== cursor) cursor = items[cursor].parent;
    return cursor;
  };
  const union = (left: EntityItem<T>, right: EntityItem<T>) => {
    const leftRoot = root(left);
    const rightRoot = root(right);
    if (leftRoot !== rightRoot) items[rightRoot].parent = leftRoot;
  };

  const byId = new Map<string, EntityItem<T>[]>();
  for (const item of items) {
    const matches = byId.get(item.id) ?? [];
    matches.push(item);
    byId.set(item.id, matches);
  }
  for (const matches of byId.values()) {
    const sides = new Set(matches.map((item) => item.side));
    if (sides.size !== matches.length) continue;
    for (let index = 1; index < matches.length; index += 1) union(matches[0], matches[index]);
  }

  const byLogicalKey = new Map<string, EntityItem<T>[]>();
  for (const item of items) {
    const matches = byLogicalKey.get(item.logicalKey) ?? [];
    matches.push(item);
    byLogicalKey.set(item.logicalKey, matches);
  }
  for (const matches of byLogicalKey.values()) {
    const counts = new Map<EntityItem<T>["side"], number>();
    for (const item of matches) counts.set(item.side, (counts.get(item.side) ?? 0) + 1);
    if ([...counts.values()].some((count) => count > 1)) continue;
    for (let index = 1; index < matches.length; index += 1) union(matches[0], matches[index]);
  }

  const groups = new Map<number, EntityGroup<T> & { items: EntityItem<T>[] }>();
  for (const item of items) {
    const itemRoot = root(item);
    const group = groups.get(itemRoot) ?? {
      key: item.logicalKey,
      items: [],
    };
    group[item.side] = item.value;
    group[`${item.side}Index`] = item.index;
    group.items.push(item);
    groups.set(itemRoot, group);
  }
  const grouped = [...groups.values()];
  const keyCounts = new Map<string, number>();
  for (const group of grouped) keyCounts.set(group.key, (keyCounts.get(group.key) ?? 0) + 1);
  return grouped.map((group) => ({
    base: group.base,
    local: group.local,
    remote: group.remote,
    baseIndex: group.baseIndex,
    localIndex: group.localIndex,
    remoteIndex: group.remoteIndex,
    key:
      (keyCounts.get(group.key) ?? 0) > 1
        ? `${group.key}\0id:${group.items.map((item) => item.id).sort()[0] ?? ""}`
        : group.key,
  }));
}

function sameStrings(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function orderGroups<T>(groups: EntityGroup<T>[], merged: Map<EntityGroup<T>, T>): T[] {
  const kept = new Set(groups.filter((group) => merged.has(group)));
  const byKey = new Map(groups.map((group) => [group.key, group] as const));
  const sideOrder = (side: "base" | "local" | "remote") =>
    groups
      .filter((group) => group[side] !== undefined)
      .sort((left, right) => (left[`${side}Index`] ?? 0) - (right[`${side}Index`] ?? 0))
      .map((group) => group.key);
  const baseOrder = sideOrder("base");
  const localOrder = sideOrder("local");
  const remoteOrder = sideOrder("remote");
  const baseSet = new Set(baseOrder);
  const localSet = new Set(localOrder);
  const remoteSet = new Set(remoteOrder);
  const keptBase = baseOrder.filter((key) => kept.has(byKey.get(key) as EntityGroup<T>));

  const sideRelativeOrder = (order: string[], present: Set<string>) => {
    const common = baseOrder.filter((key) => present.has(key) && kept.has(byKey.get(key) as EntityGroup<T>));
    const actual = order.filter((key) => baseSet.has(key) && kept.has(byKey.get(key) as EntityGroup<T>));
    return { changed: !sameStrings(common, actual), actual };
  };
  const localRelative = sideRelativeOrder(localOrder, localSet);
  const remoteRelative = sideRelativeOrder(remoteOrder, remoteSet);
  let result = [...keptBase];
  if (localRelative.changed && remoteRelative.changed && !sameStrings(localRelative.actual, remoteRelative.actual)) {
    result.sort((left, right) => left.localeCompare(right));
  } else if (localRelative.changed || remoteRelative.changed) {
    const selected = localRelative.changed ? localRelative.actual : remoteRelative.actual;
    result = [...selected, ...keptBase.filter((key) => !selected.includes(key))];
  }

  const localAdditions = localOrder.filter((key) => !baseSet.has(key) && kept.has(byKey.get(key) as EntityGroup<T>));
  const remoteAdditions = remoteOrder.filter((key) => !baseSet.has(key) && kept.has(byKey.get(key) as EntityGroup<T>));
  if (localAdditions.length > 0 && remoteAdditions.length > 0) {
    const additions = [...new Set([...localAdditions, ...remoteAdditions])].sort((left, right) =>
      left.localeCompare(right),
    );
    result.push(...additions.filter((key) => !result.includes(key)));
  } else if (localAdditions.length > 0 || remoteAdditions.length > 0) {
    const source = localAdditions.length > 0 ? localOrder : remoteOrder;
    const additions = new Set(localAdditions.length > 0 ? localAdditions : remoteAdditions);
    const withAdditions: string[] = [];
    let run: string[] = [];
    const flushRun = () => {
      withAdditions.push(...run);
      run = [];
    };
    for (const key of source) {
      if (additions.has(key) && kept.has(byKey.get(key) as EntityGroup<T>)) {
        run.push(key);
        continue;
      }
      if (!result.includes(key)) continue;
      flushRun();
      if (!withAdditions.includes(key)) withAdditions.push(key);
    }
    flushRun();
    result = [...withAdditions, ...result.filter((key) => !withAdditions.includes(key))];
  }
  return result
    .filter((key, index) => result.indexOf(key) === index)
    .map((key) => merged.get(byKey.get(key) as EntityGroup<T>))
    .filter((value): value is T => value !== undefined);
}

function mergeLists<T>(
  base: T[],
  local: T[],
  remote: T[],
  id: (value: T) => string,
  logicalKey: (value: T, index: number) => string,
  mergeGroup: (group: EntityGroup<T>) => T | undefined,
): { values: T[]; groups: EntityGroup<T>[]; merged: Map<EntityGroup<T>, T> } {
  const groups = matchEntities(base, local, remote, id, logicalKey);
  const merged = new Map<EntityGroup<T>, T>();
  for (const group of groups) {
    const value = mergeGroup(group);
    if (value !== undefined) merged.set(group, value);
  }
  return { values: orderGroups(groups, merged), groups, merged };
}

const WORKSPACE_FIELDS = [
  "kind",
  "title",
  "sessionId",
  "assetId",
  "connId",
  "dbKind",
  "assetKind",
  "quickTarget",
  "deletedTarget",
  "restorePending",
  "splitRatio",
  "closable",
] as const;

const TAB_FIELDS = [
  "kind",
  "title",
  "sessionId",
  "assetId",
  "tabId",
  "containerId",
  "connId",
  "dbKind",
  "credId",
  "path",
  "closable",
  "exited",
  "renamedByUser",
  "restorePending",
] as const;

function mergeWorkspaceMeta(group: EntityGroup<Workspace>, id: string): Workspace {
  const local = group.local;
  const remote = group.remote;
  const source = local ?? remote ?? group.base;
  if (!source) throw new Error("layout merge requires a workspace");
  const output = { ...source, id } as Workspace;
  for (const field of WORKSPACE_FIELDS) {
    const value = group.base
      ? mergeValue(group.base[field], local?.[field], remote?.[field])
      : mergeAddedValue(local?.[field], remote?.[field]);
    output[field] = value as never;
  }
  return output;
}

function mergeTabMeta(group: EntityGroup<AppTab>, id: string): AppTab {
  const local = group.local;
  const remote = group.remote;
  const source = local ?? remote ?? group.base;
  if (!source) throw new Error("layout merge requires a tab");
  const output = { ...source, id } as AppTab;
  for (const field of TAB_FIELDS) {
    const value = group.base
      ? mergeValue(group.base[field], local?.[field], remote?.[field])
      : mergeAddedValue(local?.[field], remote?.[field]);
    output[field] = value as never;
  }
  return output;
}

function normalizePane(pane: Pane, fallbackActive = false): Pane {
  const activeTabId =
    pane.activeTabId && pane.tabs.some((tab) => tab.id === pane.activeTabId)
      ? pane.activeTabId
      : fallbackActive && pane.tabs.length > 0
        ? pane.tabs[pane.tabs.length - 1].id
        : (pane.tabs[pane.tabs.length - 1]?.id ?? null);
  return { ...pane, activeTabId };
}

export function capWorkspacePanes(workspace: Workspace): Workspace {
  let panes = workspace.panes.map((pane) => normalizePane(pane));
  if (panes.length === 0) return { ...workspace, panes, activePaneId: workspace.activePaneId };
  if (panes.length > 2) {
    const overflowActive = panes.slice(2).find((pane) => pane.id === workspace.activePaneId)?.activeTabId;
    const seen = new Set<string>();
    const tabs: AppTab[] = [];
    for (const pane of panes.slice(1)) {
      for (const tab of pane.tabs) {
        const key = tabLogicalKey(tab);
        if (seen.has(key)) continue;
        seen.add(key);
        tabs.push(tab);
      }
    }
    const second = normalizePane({
      ...panes[1],
      tabs,
      activeTabId:
        overflowActive && tabs.some((tab) => tab.id === overflowActive)
          ? overflowActive
          : panes[1].activeTabId,
    });
    panes = [panes[0], second];
  }
  const activePaneId = panes.some((pane) => pane.id === workspace.activePaneId)
    ? workspace.activePaneId
    : panes[0].id;
  return { ...workspace, panes, activePaneId };
}

function mergeTabs(
  base: AppTab[],
  local: AppTab[],
  remote: AppTab[],
  localWorkspace: Workspace | undefined,
  options: LayoutMergeOptions,
): AppTab[] {
  return mergeLists(base, local, remote, (tab) => tab.id, tabLogicalKey, (group) => {
    if (group.base && !group.local) return undefined;
    if (group.base && !group.remote) {
      if (group.local && localWorkspace && options.preserveLocalTab?.(group.local, localWorkspace)) {
        return group.local;
      }
      return undefined;
    }
    if (!group.local) return group.remote;
    if (!group.remote) return group.local;
    return mergeTabMeta(group, group.local.id);
  }).values;
}

function mergePanes(
  base: Pane[],
  local: Pane[],
  remote: Pane[],
  localWorkspace: Workspace | undefined,
  options: LayoutMergeOptions,
): Pane[] {
  return mergeLists(base, local, remote, (pane) => pane.id, paneLogicalKey, (group) => {
    if (group.base && !group.local) return undefined;
    if (group.base && !group.remote) {
      if (!group.local || !localWorkspace) return undefined;
      const protectedTabs = group.local.tabs.filter(
        (tab) => options.preserveLocalTab?.(tab, localWorkspace) === true,
      );
      return protectedTabs.length > 0
        ? normalizePane({ ...group.local, tabs: protectedTabs })
        : undefined;
    }
    if (!group.local) return group.remote;
    if (!group.remote) return group.local;
    const paneId = group.local.id;
    const tabs = mergeTabs(
      group.base?.tabs ?? [],
      group.local.tabs,
      group.remote.tabs,
      localWorkspace,
      options,
    );
    const localActive = group.local.activeTabId;
    const remoteActive = group.remote.activeTabId;
    let activeTabId: string | null;
    if (localActive && tabs.some((tab) => tab.id === localActive)) activeTabId = localActive;
    else if (localWorkspace && group.local.tabs.length > 0) activeTabId = tabs[tabs.length - 1]?.id ?? null;
    else if (remoteActive && tabs.some((tab) => tab.id === remoteActive)) activeTabId = remoteActive;
    else activeTabId = tabs[tabs.length - 1]?.id ?? null;
    return {
      id: paneId,
      tabs,
      activeTabId,
    };
  }).values.map((pane) => normalizePane(pane));
}

function protectedWorkspace(workspace: Workspace, options: LayoutMergeOptions): Workspace | undefined {
  if (options.preserveLocalWorkspace?.(workspace)) return capWorkspacePanes(workspace);
  const panes = workspace.panes
    .map((pane) => ({
      ...pane,
      tabs: pane.tabs.filter((tab) => options.preserveLocalTab?.(tab, workspace) === true),
    }))
    .filter((pane) => pane.tabs.length > 0);
  return panes.length > 0 ? capWorkspacePanes({ ...workspace, panes }) : undefined;
}

function mergeWorkspaces(
  base: Workspace[],
  local: Workspace[],
  remote: Workspace[],
  options: LayoutMergeOptions,
) {
  return mergeLists(base, local, remote, (workspace) => workspace.id, workspaceLogicalKey, (group) => {
    if (group.base && !group.local) return undefined;
    if (group.base && !group.remote) {
      return group.local ? protectedWorkspace(group.local, options) : undefined;
    }
    if (!group.local) return group.remote ? capWorkspacePanes(group.remote) : undefined;
    if (!group.remote) return capWorkspacePanes(group.local);
    const workspace = mergeWorkspaceMeta(group, group.local.id);
    const panes = mergePanes(
      group.base?.panes ?? [],
      group.local.panes,
      group.remote.panes,
      group.local,
      options,
    );
    const capped = capWorkspacePanes({ ...workspace, panes });
    if (capped.panes.some((pane) => pane.id === group.local?.activePaneId)) {
      return { ...capped, activePaneId: group.local?.activePaneId ?? capped.activePaneId };
    }
    if (group.local.panes.length > 0) return { ...capped, activePaneId: capped.panes[0]?.id ?? capped.activePaneId };
    if (capped.panes.some((pane) => pane.id === group.remote?.activePaneId)) {
      return { ...capped, activePaneId: group.remote?.activePaneId ?? capped.activePaneId };
    }
    return capped;
  });
}

function mergeAiTab(group: EntityGroup<AiDockTab>): AiDockTab | undefined {
  if (group.base && (!group.local || !group.remote)) return undefined;
  const local = group.local;
  const remote = group.remote;
  if (!local) return remote;
  if (!remote) return local;
  const output: AiDockTab = { ...local, id: local.id };
  for (const field of ["title", "conversationId"] as const) {
    const value = group.base
      ? mergeValue(group.base[field], local[field], remote[field])
      : mergeAddedValue(local[field], remote[field]);
    output[field] = value as never;
  }
  return output;
}

function mergeBoards(
  base: PersistedLayout | null,
  local: PersistedLayout,
  remote: PersistedLayout,
  workspaceResult: ReturnType<typeof mergeWorkspaces>,
): Record<string, AiBoard> {
  const aliases = {
    base: new Map<string, Workspace>(),
    local: new Map<string, Workspace>(),
    remote: new Map<string, Workspace>(),
  };
  for (const group of workspaceResult.groups) {
    const merged = workspaceResult.merged.get(group);
    if (!merged) continue;
    if (group.base) aliases.base.set(group.base.id, merged);
    if (group.local) aliases.local.set(group.local.id, merged);
    if (group.remote) aliases.remote.set(group.remote.id, merged);
  }

  interface BoardGroup {
    key: string;
    base?: AiBoard;
    local?: AiBoard;
    remote?: AiBoard;
  }
  const boards = new Map<string, BoardGroup>();
  const collect = (side: "base" | "local" | "remote", layout: PersistedLayout | null) => {
    if (!layout) return;
    for (const [boardKey, board] of Object.entries(layout.aiBoards)) {
      const key = boardKey === GLOBAL_BOARD ? GLOBAL_BOARD : aliases[side].get(boardKey)?.id;
      if (!key) continue;
      const group = boards.get(key) ?? { key };
      if (!group[side]) group[side] = board;
      boards.set(key, group);
    }
  };
  collect("base", base);
  collect("local", local);
  collect("remote", remote);

  const output: Record<string, AiBoard> = {};
  for (const group of boards.values()) {
    if (group.base && (!group.local || !group.remote)) continue;
    const board = group.local ?? group.remote;
    if (!board) continue;
    const tabs = mergeLists(
      group.base?.tabs ?? [],
      group.local?.tabs ?? [],
      group.remote?.tabs ?? [],
      (tab) => tab.id,
      aiTabLogicalKey,
      mergeAiTab,
    ).values;
    if (tabs.length === 0) continue;
    let activeTabId: string | null = null;
    if (group.local?.activeTabId && tabs.some((tab) => tab.id === group.local?.activeTabId)) {
      activeTabId = group.local.activeTabId;
    } else if (group.local && group.local.tabs.length > 0) {
      activeTabId = tabs[tabs.length - 1].id;
    } else if (group.remote?.activeTabId && tabs.some((tab) => tab.id === group.remote?.activeTabId)) {
      activeTabId = group.remote.activeTabId;
    } else {
      activeTabId = tabs[tabs.length - 1].id;
    }
    output[group.key] = { tabs, activeTabId };
  }
  return output;
}

export function mergeLayout(
  base: PersistedLayout | null,
  local: PersistedLayout,
  remote: PersistedLayout,
  options: LayoutMergeOptions = {},
): PersistedLayout {
  const workspaceResult = mergeWorkspaces(base?.workspaces ?? [], local.workspaces, remote.workspaces, options);
  const workspaces = workspaceResult.values;
  const localActive = workspaceResult.groups.find(
    (group) => group.local?.id === local.activeWorkspaceId && workspaceResult.merged.has(group),
  );
  const remoteActive = workspaceResult.groups.find(
    (group) => group.remote?.id === remote.activeWorkspaceId && workspaceResult.merged.has(group),
  );
  let activeWorkspaceId: string | null = null;
  if (localActive) activeWorkspaceId = workspaceResult.merged.get(localActive)?.id ?? null;
  else if (local.workspaces.length > 0) activeWorkspaceId = workspaces[0]?.id ?? null;
  else if (remoteActive) activeWorkspaceId = workspaceResult.merged.get(remoteActive)?.id ?? null;
  else activeWorkspaceId = workspaces[0]?.id ?? null;
  return {
    v: 3,
    leftOpen: local.leftOpen,
    leftMode: local.leftMode,
    rightOpen: local.rightOpen,
    leftWidth: local.leftWidth,
    rightWidth: local.rightWidth,
    workspaces,
    activeWorkspaceId,
    aiBoards: mergeBoards(base, local, remote, workspaceResult),
  };
}

function duplicateKeys<T>(values: T[], key: (value: T, index: number) => string): string[] {
  const counts = new Map<string, number>();
  for (const [index, value] of values.entries()) {
    const current = key(value, index);
    counts.set(current, (counts.get(current) ?? 0) + 1);
  }
  const occurrences = new Map<string, number>();
  return values.map((value, index) => {
    const current = key(value, index);
    const occurrence = (occurrences.get(current) ?? 0) + 1;
    occurrences.set(current, occurrence);
    return (counts.get(current) ?? 0) > 1 ? `${current}#${occurrence}` : current;
  });
}

function canonicalWorkspace(workspace: Workspace, key: string) {
  const meta = Object.fromEntries(
    WORKSPACE_FIELDS.map((field) => [field, workspace[field]]).filter(([, value]) => value !== undefined),
  );
  return {
    key,
    ...meta,
    panes: workspace.panes.map((pane) => ({
      tabs: pane.tabs.map((tab, index) => ({
        key: duplicateKeys(pane.tabs, tabLogicalKey)[index],
        ...Object.fromEntries(
          TAB_FIELDS.map((field) => [field, tab[field]]).filter(([, value]) => value !== undefined),
        ),
      })),
    })),
  };
}

export function sharedLayoutKey(layout: PersistedLayout | null): string {
  if (!layout) return JSON.stringify({ workspaces: [], aiBoards: [] });
  const workspaceKeys = duplicateKeys(layout.workspaces, workspaceLogicalKey);
  const boardKeys = new Map<string, string>();
  layout.workspaces.forEach((workspace, index) => boardKeys.set(workspace.id, workspaceKeys[index]));
  const boards = Object.entries(layout.aiBoards)
    .map(([key, board]) => ({
      key: key === GLOBAL_BOARD ? key : (boardKeys.get(key) ?? `missing:${key}`),
      tabs: board.tabs.map((tab, index) => ({
        key: duplicateKeys(board.tabs, aiTabLogicalKey)[index],
        title: tab.title,
        conversationId: tab.conversationId,
      })),
    }))
    .sort((left, right) => left.key.localeCompare(right.key));
  return JSON.stringify({
    workspaces: layout.workspaces.map((workspace, index) => canonicalWorkspace(workspace, workspaceKeys[index])),
    aiBoards: boards,
  });
}

function matchingWorkspace(reference: Workspace, candidates: Workspace[]): Workspace | undefined {
  const direct = candidates.find((candidate) => candidate.id === reference.id);
  if (direct) return direct;
  const key = workspaceLogicalKey(reference);
  const matches = candidates.filter((candidate) => workspaceLogicalKey(candidate) === key);
  return matches.length === 1 ? matches[0] : undefined;
}

function matchingPane(reference: Pane, referenceIndex: number, candidates: Pane[]): Pane | undefined {
  return candidates.find((pane) => pane.id === reference.id) ?? candidates[referenceIndex];
}

function matchingTab(reference: AppTab, candidates: AppTab[]): AppTab | undefined {
  const direct = candidates.find((candidate) => candidate.id === reference.id);
  if (direct) return direct;
  const key = tabLogicalKey(reference);
  const matches = candidates.filter((candidate) => tabLogicalKey(candidate) === key);
  return matches.length === 1 ? matches[0] : undefined;
}

function matchingAiTab(reference: AiDockTab, candidates: AiDockTab[]): AiDockTab | undefined {
  const direct = candidates.find((candidate) => candidate.id === reference.id);
  if (direct) return direct;
  const key = aiTabLogicalKey(reference);
  const matches = candidates.filter((candidate) => aiTabLogicalKey(candidate) === key);
  return matches.length === 1 ? matches[0] : undefined;
}

export function projectSharedLayout(
  candidate: PersistedLayout,
  compatibilityBase: PersistedLayout | null,
): PersistedLayout {
  const base = compatibilityBase;
  if (!base) return candidate;
  const workspaces = candidate.workspaces.map((workspace) => {
    const baseWorkspace = matchingWorkspace(workspace, base.workspaces);
    if (!baseWorkspace) return workspace;
    const panes = workspace.panes.map((pane, paneIndex) => {
      const basePane =
        baseWorkspace.panes.find((item) => item.id === pane.id) ?? baseWorkspace.panes[paneIndex];
      if (!basePane) return pane;
      const reference = basePane.tabs.find((tab) => tab.id === basePane.activeTabId);
      const active = reference ? matchingTab(reference, pane.tabs) : undefined;
      return active ? { ...pane, activeTabId: active.id } : pane;
    });
    const baseActiveIndex = baseWorkspace.panes.findIndex((pane) => pane.id === baseWorkspace.activePaneId);
    const baseActive = baseActiveIndex >= 0 ? baseWorkspace.panes[baseActiveIndex] : undefined;
    const activePane = baseActive ? matchingPane(baseActive, baseActiveIndex, panes) : undefined;
    return { ...workspace, panes, activePaneId: activePane?.id ?? workspace.activePaneId };
  });
  const baseActiveWorkspace = base.workspaces.find((workspace) => workspace.id === base.activeWorkspaceId);
  const activeWorkspace = baseActiveWorkspace
    ? matchingWorkspace(baseActiveWorkspace, workspaces)
    : undefined;
  const aiBoards = Object.fromEntries(
    Object.entries(candidate.aiBoards).map(([key, board]) => {
      if (key === GLOBAL_BOARD) {
        const baseBoard = base.aiBoards[key];
        const reference = baseBoard?.tabs.find((tab) => tab.id === baseBoard.activeTabId);
        const active = reference ? matchingAiTab(reference, board.tabs) : undefined;
        return [key, active ? { ...board, activeTabId: active.id } : board];
      }
      const workspace = workspaces.find((item) => item.id === key);
      const baseWorkspace = workspace ? matchingWorkspace(workspace, base.workspaces) : undefined;
      const baseBoard = baseWorkspace ? base.aiBoards[baseWorkspace.id] : undefined;
      const reference = baseBoard?.tabs.find((tab) => tab.id === baseBoard.activeTabId);
      const active = reference ? matchingAiTab(reference, board.tabs) : undefined;
      return [key, active ? { ...board, activeTabId: active.id } : board];
    }),
  );
  return {
    ...candidate,
    leftOpen: base.leftOpen,
    leftMode: base.leftMode,
    rightOpen: base.rightOpen,
    leftWidth: base.leftWidth,
    rightWidth: base.rightWidth,
    workspaces,
    activeWorkspaceId: activeWorkspace?.id ?? candidate.activeWorkspaceId,
    aiBoards,
  };
}

function persistentTabEqual(left: AppTab, right: AppTab): boolean {
  return left.id === right.id && TAB_FIELDS.every((field) => sameValue(left[field], right[field]));
}

function persistentPaneEqual(left: Pane, right: Pane): boolean {
  if (left === right) return true;
  return (
    left.id === right.id &&
    left.tabs.length === right.tabs.length &&
    left.tabs.every((tab, index) => persistentTabEqual(tab, right.tabs[index]))
  );
}

function persistentWorkspaceEqual(left: Workspace, right: Workspace): boolean {
  if (left === right) return true;
  return (
    left.id === right.id &&
    WORKSPACE_FIELDS.every((field) => sameValue(left[field], right[field])) &&
    left.panes.length === right.panes.length &&
    left.panes.every((pane, index) => persistentPaneEqual(pane, right.panes[index]))
  );
}

function persistentBoardEqual(left: AiBoard, right: AiBoard): boolean {
  if (left === right) return true;
  return (
    left.tabs.length === right.tabs.length &&
    left.tabs.every((tab, index) => {
      const other = right.tabs[index];
      return (
        tab === other ||
        (tab.id === other.id && tab.title === other.title && tab.conversationId === other.conversationId)
      );
    })
  );
}

export interface PersistentLayoutReferences {
  workspaces: Workspace[];
  aiBoards: Record<string, AiBoard>;
}

export function hasPersistentLayoutChange(
  previous: PersistentLayoutReferences,
  next: PersistentLayoutReferences,
): boolean {
  if (previous.workspaces === next.workspaces && previous.aiBoards === next.aiBoards) return false;
  if (
    previous.workspaces.length !== next.workspaces.length ||
    !previous.workspaces.every((workspace, index) => persistentWorkspaceEqual(workspace, next.workspaces[index]))
  ) {
    return true;
  }
  const previousKeys = Object.keys(previous.aiBoards);
  const nextKeys = Object.keys(next.aiBoards);
  return (
    previousKeys.length !== nextKeys.length ||
    previousKeys.some((key) => !next.aiBoards[key] || !persistentBoardEqual(previous.aiBoards[key], next.aiBoards[key]))
  );
}
