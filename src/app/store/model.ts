import {
  fileEditorCaseInsensitive,
  fileEditorPathMatches,
  migratedFileEditorPath,
  migrateFileEditorDirty,
} from "../../features/files/editorGuards";
import { baseName } from "../../features/files/pathUtils";
import { isFleetHealthTabId } from "../../features/fleet/fleetHealthTab";
import type {
  AiBoard,
  AiDockTab,
  AppTab,
  BroadcastState,
  DbKind,
  Pane,
  PaneKind,
  UiState,
  Workspace,
  WorkspaceRuntime,
  WorkspaceSpec,
} from "./types";

const TOOL_TAB_KINDS = new Set<PaneKind>([
  "credentials",
  "settings",
  "audit",
  "devices",
  "history",
  "execBatch",
]);

export function isToolTabKind(kind: PaneKind): boolean {
  return TOOL_TAB_KINDS.has(kind);
}

export function isGlobalToolTab(tab: { kind: PaneKind; sessionId?: string }): boolean {
  return tab.kind === "forward" && !tab.sessionId;
}

export function isWorkspaceToolTab(tab: { kind: PaneKind; sessionId?: string }): boolean {
  return isToolTabKind(tab.kind) || isGlobalToolTab(tab);
}

export const DB_KIND_LABEL: Record<DbKind, string> = {
  mysql: "SQL",
  postgres: "PostgreSQL",
  redis: "Redis",
};

export function dbKindOf(kind: string): DbKind | null {
  return kind === "mysql" || kind === "postgres" || kind === "redis" ? kind : null;
}

// Without a remote-host workspace (no workspace or the "tools" workspace), the AI board falls into the global group.
export const GLOBAL_AI_BOARD_KEY = "global";

let tabSeq = 1;

let broadcastGeneration = 0;

export function pruneBroadcastToWorkspaces(
  broadcast: BroadcastState,
  workspaces: readonly Workspace[],
): BroadcastState | null {
  const openTabIds = new Set(
    workspaces.flatMap((workspace) =>
      workspace.panes.flatMap((pane) => pane.tabs.map((tab) => tab.id)),
    ),
  );
  const originExists = workspaces.some(
    (workspace) =>
      workspace.id === broadcast.origin.workspaceId &&
      workspace.panes.some((pane) => pane.tabs.some((tab) => tab.id === broadcast.origin.storeTabId)),
  );
  const remaining = broadcast.targetIds.filter((targetId) => openTabIds.has(targetId));
  if (!originExists || remaining.length === 0) return null;
  if (remaining.length === broadcast.targetIds.length) return broadcast;
  return {
    ...broadcast,
    targetIds: remaining,
    generation: ++broadcastGeneration,
    results: {},
  };
}

export function nextTabId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${tabSeq++}`;
}

export function makeAiDockTab(): AiDockTab {
  return { id: nextTabId("ai"), title: "新会话" };
}

export function makeAiBoard(): AiBoard {
  const tab = makeAiDockTab();
  return { tabs: [tab], activeTabId: tab.id };
}

export function omitAiBoard(boards: Record<string, AiBoard>, key: string): Record<string, AiBoard> {
  const next = { ...boards };
  delete next[key];
  return next;
}

export function omitWorkspaceRuntime(
  runtime: Record<string, WorkspaceRuntime>,
  key: string,
): Record<string, WorkspaceRuntime> {
  const next = { ...runtime };
  delete next[key];
  return next;
}

const PLACEHOLDER_WS_TITLES = new Set(["工作区", "会话", "标签"]);

export function workspaceFieldPatch(
  w: Workspace,
  spec: WorkspaceSpec,
): Partial<Pick<Workspace, "title" | "assetId" | "assetKind" | "quickTarget">> | null {
  const patch: Partial<Pick<Workspace, "title" | "assetId" | "assetKind" | "quickTarget">> = {};
  let changed = false;
  if (!w.assetId && spec.assetId) {
    patch.assetId = spec.assetId;
    changed = true;
  }
  if (!w.assetKind && spec.assetKind) {
    patch.assetKind = spec.assetKind;
    changed = true;
  }
  if (!w.quickTarget && spec.quickTarget) {
    patch.quickTarget = spec.quickTarget;
    changed = true;
  }
  if (spec.title && spec.title !== w.title && (!w.title || PLACEHOLDER_WS_TITLES.has(w.title))) {
    patch.title = spec.title;
    changed = true;
  }
  return changed ? patch : null;
}

export function migrateWorkspaceFileLocators(
  st: UiState,
  sessionId: string,
  from: string,
  to: string,
  kind: string,
): UiState {
  const caseInsensitive = fileEditorCaseInsensitive(
    st.sessions.find((session) => session.id === sessionId)?.kind ?? "",
  );
  let changed = false;
  const workspaces = st.workspaces.map((workspace) => {
    let workspaceChanged = false;
    const panes = workspace.panes.map((pane) => {
      let paneChanged = false;
      const tabs = pane.tabs.map((tab) => {
        if ((tab.kind !== "files" && tab.kind !== "log") || tab.sessionId !== sessionId || !tab.path) {
          return tab;
        }
        const path = migratedFileEditorPath(tab.path, from, to, kind, caseInsensitive);
        if (path === null || path === tab.path) return tab;
        changed = true;
        workspaceChanged = true;
        paneChanged = true;
        return { ...tab, path, title: baseName(path) };
      });
      return paneChanged ? { ...pane, tabs } : pane;
    });
    return workspaceChanged ? { ...workspace, panes } : workspace;
  });
  migrateFileEditorDirty(sessionId, from, to, kind, caseInsensitive);
  return changed ? { ...st, workspaces } : st;
}

export function explicitWorkspaceMatchesTab(tab: AppTab, workspace: Workspace): boolean {
  if (isWorkspaceToolTab(tab)) return workspace.kind === "tools";
  if (tab.kind === "background" && workspace.kind !== "session") return false;
  if (tab.sessionId) return workspace.sessionId === tab.sessionId;
  if (tab.connId) return workspace.connId === tab.connId;
  return true;
}

export function rebindWorkspaceSession(
  workspaces: Workspace[],
  workspaceId: string,
  fromSessionId: string | undefined,
  sessionId: string,
): Workspace[] {
  if (fromSessionId === sessionId) return workspaces;
  return workspaces.map((w) => {
    if (w.id !== workspaceId || w.sessionId !== fromSessionId) return w;
    return {
      ...w,
      sessionId,
      panes:
        w.kind === "session" && fromSessionId !== undefined
          ? w.panes.map((pane) => ({
              ...pane,
              tabs: pane.tabs.map((tab) =>
                tab.sessionId === fromSessionId ? { ...tab, sessionId } : tab,
              ),
            }))
          : w.panes,
    };
  });
}

export function makePane(): Pane {
  return { id: nextTabId("pane"), tabs: [], activeTabId: null };
}

export function normalizeWorkspacePanes(w: Workspace): Workspace {
  const withActiveTabs = w.panes.map((pane) => ({
    ...pane,
    activeTabId:
      pane.activeTabId && pane.tabs.some((tab) => tab.id === pane.activeTabId)
        ? pane.activeTabId
        : (pane.tabs[pane.tabs.length - 1]?.id ?? null),
  }));
  const nonEmpty = withActiveTabs.filter((pane) => pane.tabs.length > 0);
  let panes: Pane[] = nonEmpty.length > 0 ? nonEmpty : withActiveTabs.slice(0, 1);
  if (panes.length === 0) panes = [makePane()];
  return {
    ...w,
    panes,
    activePaneId: panes.some((pane) => pane.id === w.activePaneId)
      ? w.activePaneId
      : panes[0].id,
  };
}

function hasValidAiBoard(aiBoards: Record<string, unknown> | undefined, workspaceId: string): boolean {
  const board = aiBoards?.[workspaceId];
  if (typeof board !== "object" || board === null || Array.isArray(board)) return false;
  const tabs = (board as { tabs?: unknown }).tabs;
  if (!Array.isArray(tabs)) return false;
  return tabs.some((tab) => {
    if (typeof tab !== "object" || tab === null || Array.isArray(tab)) return false;
    const id = (tab as { id?: unknown }).id;
    return typeof id === "string" && id.length > 0;
  });
}

export function migrateToolTabs(
  workspaces: Workspace[],
  aiBoards?: Record<string, unknown>,
): Workspace[] {
  const toolsIndex = workspaces.findIndex((w) => w.kind === "tools");
  const sourceTools = toolsIndex >= 0 ? workspaces[toolsIndex] : undefined;
  const hasToolTabs = workspaces.some((w) =>
    w.panes.some((pane) => pane.tabs.some((tab) => isWorkspaceToolTab(tab))),
  );
  if (!sourceTools && !hasToolTabs) return workspaces;

  const preservedTools = new Set(
    workspaces.filter((w) => w.kind === "tools" && hasValidAiBoard(aiBoards, w.id)),
  );
  const multipleTools = workspaces.some((w, index) => w.kind === "tools" && index !== toolsIndex);
  const hasExternalToolTabs = workspaces.some(
    (w) =>
      w !== sourceTools &&
      w.panes.some((pane) => pane.tabs.some((tab) => isWorkspaceToolTab(tab))),
  );
  const kinds = new Set<PaneKind>();
  let hasDuplicateToolTabs = false;
  for (const pane of sourceTools?.panes ?? []) {
    for (const tab of pane.tabs) {
      if (!isWorkspaceToolTab(tab)) continue;
      if (kinds.has(tab.kind)) hasDuplicateToolTabs = true;
      kinds.add(tab.kind);
    }
  }
  if (sourceTools && !multipleTools && !hasExternalToolTabs && !hasDuplicateToolTabs) {
    if (sourceTools.title === "工具") return workspaces;
    return workspaces.map((w) => (w === sourceTools ? { ...w, title: "工具" } : w));
  }

  let tools: Workspace;
  if (sourceTools) {
    tools = { ...sourceTools, title: "工具" };
  } else {
    const pane = makePane();
    tools = {
      id: nextTabId("ws-tools"),
      kind: "tools",
      title: "工具",
      panes: [pane],
      activePaneId: pane.id,
      splitRatio: 0.5,
      closable: true,
    };
  }

  const selected = new Map<PaneKind, AppTab>();
  const collect = (w: Workspace) => {
    for (const pane of w.panes) {
      for (const tab of pane.tabs) {
        if (!isWorkspaceToolTab(tab)) continue;
        const candidate =
          tab.kind === "history" && w.kind === "session"
            ? {
                ...tab,
                sessionId: tab.sessionId ?? w.sessionId,
                assetId: tab.assetId ?? w.assetId,
              }
            : tab;
        const existing = selected.get(tab.kind);
        if (!existing) {
          selected.set(tab.kind, candidate);
          continue;
        }
        if (tab.kind !== "history") continue;
        const sessionId = existing.sessionId ?? candidate.sessionId;
        const assetId = existing.assetId ?? candidate.assetId;
        if (sessionId !== existing.sessionId || assetId !== existing.assetId) {
          selected.set(tab.kind, { ...existing, sessionId, assetId });
        }
      }
    }
  };
  if (sourceTools) collect(sourceTools);
  for (const w of workspaces) {
    if (w !== sourceTools) collect(w);
  }

  const kept = new Set<AppTab>();
  const basePanes = (tools.panes.length > 0 ? tools.panes : [makePane()]).map((pane) => ({
    ...pane,
    tabs: pane.tabs.filter((tab) => {
      if (!isWorkspaceToolTab(tab)) return true;
      if (selected.get(tab.kind) !== tab) return false;
      kept.add(tab);
      return true;
    }),
  }));
  const additions = [...selected.values()].filter((tab) => !kept.has(tab));
  for (const w of workspaces) {
    if (w.kind !== "tools" || w === sourceTools) continue;
    for (const pane of w.panes) {
      additions.push(...pane.tabs.filter((tab) => !isWorkspaceToolTab(tab)));
    }
  }
  const targetPaneId = basePanes.some((pane) => pane.id === tools.activePaneId)
    ? tools.activePaneId
    : basePanes[0].id;
  const normalizedTools = normalizeWorkspacePanes({
    ...tools,
    panes: basePanes.map((pane) =>
      pane.id === targetPaneId ? { ...pane, tabs: [...pane.tabs, ...additions] } : pane,
    ),
  });

  const next: Workspace[] = [];
  let inserted = false;
  for (let i = 0; i < workspaces.length; i++) {
    const w = workspaces[i];
    if (i === toolsIndex) {
      next.push(normalizedTools);
      inserted = true;
      continue;
    }
    if (w.kind === "tools") {
      if (preservedTools.has(w)) {
        const panes = w.panes.map((pane) => ({ ...pane, tabs: [], activeTabId: null }));
        next.push({
          ...w,
          panes,
          activePaneId: panes.some((pane) => pane.id === w.activePaneId)
            ? w.activePaneId
            : (panes[0]?.id ?? w.activePaneId),
        });
      }
      continue;
    }
    const panes = w.panes.map((pane) => ({
      ...pane,
      tabs: pane.tabs.filter((tab) => !isWorkspaceToolTab(tab)),
    }));
    const changed = w.panes.some((pane, index) => pane.tabs.length !== panes[index].tabs.length);
    next.push(changed ? normalizeWorkspacePanes({ ...w, panes }) : w);
  }
  if (!inserted) next.push(normalizedTools);
  return next;
}

export function findTabLocation(
  workspaces: Workspace[],
  preferredWorkspaceId: string | null,
  id: string,
): { workspace: Workspace; pane: Pane; tab: AppTab } | null {
  let first: { workspace: Workspace; pane: Pane; tab: AppTab } | null = null;
  for (const workspace of workspaces) {
    for (const pane of workspace.panes) {
      const tab = pane.tabs.find((t) => t.id === id);
      if (!tab) continue;
      if (workspace.id === preferredWorkspaceId) return { workspace, pane, tab };
      if (!first) first = { workspace, pane, tab };
    }
  }
  return first;
}

export function sameTab(t: AppTab, tab: AppTab, caseInsensitive = false): boolean {
  if (t.id === tab.id) return true;
  if (t.tabId && tab.tabId && t.tabId === tab.tabId) return true;
  if (tab.kind === "background" && t.kind === "background") return true;
  if (isWorkspaceToolTab(tab) && t.kind === tab.kind) return true;
  if ((tab.kind === "files" || tab.kind === "log") && tab.path && t.path) {
    return (
      t.kind === tab.kind &&
      t.sessionId === tab.sessionId &&
      fileEditorPathMatches(t.path, tab.path, "file", caseInsensitive)
    );
  }
  return (
    tab.kind !== "terminal" &&
    t.kind === tab.kind &&
    t.sessionId === tab.sessionId &&
    t.connId === tab.connId &&
    !t.tabId &&
    !tab.tabId &&
    t.path === tab.path &&
    !isFleetHealthTabId(t.id) &&
    !isFleetHealthTabId(tab.id)
  );
}

export function pickPaneForTab(panes: Pane[], activePaneId: string, tab: AppTab): Pane {
  const active = panes.find((p) => p.id === activePaneId) ?? panes[0];
  if (tab.kind !== "files" || !tab.path) return active;
  return panes.find((p) => p.tabs.some((t) => t.kind === "files" && t.path)) ?? active;
}

export function nextBroadcastGeneration(): number {
  return ++broadcastGeneration;
}
