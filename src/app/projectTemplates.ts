import type { AppTab, Pane, QuickWorkspaceTarget, Workspace } from "./store";
import { capWorkspacePanes, workspaceLogicalKey } from "./layoutMerge";
import {
  ALL_PROJECTS,
  filterWorkspacesForProject,
  isProjectFocus,
  type ProjectFocus,
  type ProjectInventory,
} from "./projectFocus";

export type TemplateProjectFocus = Exclude<ProjectFocus, typeof ALL_PROJECTS>;

export interface ProjectTemplate {
  v: 1;
  id: string;
  name: string;
  project: TemplateProjectFocus;
  createdAt: number;
  updatedAt: number;
  workspaces: Workspace[];
  startupCommands: Record<string, string>;
}

export interface ProjectTemplateDraft {
  name: string;
  project: TemplateProjectFocus;
  workspaces: Workspace[];
  startupCommands: Record<string, string>;
}

export interface ProjectTemplateRestore {
  workspaces: Workspace[];
  workspaceIds: Map<string, string>;
  tabIds: Map<string, string>;
  addedWorkspaceIds: string[];
}

function snapshotQuickTarget(target: QuickWorkspaceTarget | undefined): QuickWorkspaceTarget | undefined {
  if (!target) return undefined;
  return {
    id: target.id,
    host: target.host,
    port: target.port,
    username: target.username,
    authKind: target.authKind,
  };
}

function snapshotTab(tab: AppTab, sessionId?: string, connId?: string): AppTab {
  return {
    id: tab.id,
    kind: tab.kind,
    title: tab.title,
    sessionId,
    assetId: tab.assetId,
    tabId: undefined,
    containerId: tab.containerId,
    connId: tab.kind === "db" ? connId : undefined,
    dbKind: tab.dbKind,
    credId: tab.credId,
    path: tab.path,
    shell: tab.shell,
    pendingDirectory: tab.pendingDirectory,
    closable: tab.closable,
    renamedByUser: tab.renamedByUser,
    restorePending: tab.kind === "terminal" || tab.kind === "db" ? true : undefined,
  };
}

function snapshotWorkspace(workspace: Workspace): Workspace {
  return capWorkspacePanes({
    id: workspace.id,
    kind: workspace.kind,
    title: workspace.title,
    sessionId: undefined,
    assetId: workspace.assetId,
    connId: undefined,
    dbKind: workspace.dbKind,
    assetKind: workspace.assetKind,
    quickTarget: snapshotQuickTarget(workspace.quickTarget),
    deletedTarget: undefined,
    restorePending: true,
    panes: workspace.panes.map((pane) => ({
      id: pane.id,
      tabs: pane.tabs.map((tab) => snapshotTab(tab)),
      activeTabId: pane.activeTabId,
    })),
    activePaneId: workspace.activePaneId,
    splitRatio: workspace.splitRatio,
    closable: workspace.closable,
  });
}

export function captureProjectTemplateDraft(
  name: string,
  project: TemplateProjectFocus,
  workspaces: readonly Workspace[],
  inventory: ProjectInventory,
): ProjectTemplateDraft {
  const selected = filterWorkspacesForProject([...workspaces], project, inventory)
    .filter((workspace) => workspace.kind !== "tools")
    .map(snapshotWorkspace);
  return { name: name.trim(), project, workspaces: selected, startupCommands: {} };
}

export function createProjectTemplate(
  draft: ProjectTemplateDraft,
  options: { id: string; now?: number },
): ProjectTemplate {
  const name = draft.name.trim();
  if (!name) throw new Error("模板名称不能为空");
  if (name.length > 80) throw new Error("模板名称不能超过 80 个字符");
  if (draft.workspaces.length === 0) throw new Error("当前项目没有可保存的主机或数据库工作区");
  const now = options.now ?? Date.now();
  const startupCommands = Object.fromEntries(
    Object.entries(draft.startupCommands)
      .filter(([tabId, command]) =>
        typeof command === "string" && command.trim().length > 0 &&
        draft.workspaces.some((workspace) => workspace.panes.some((pane) => pane.tabs.some((tab) => tab.id === tabId))),
      )
      .map(([tabId, command]) => [tabId, command]),
  );
  return {
    v: 1,
    id: options.id,
    name,
    project: draft.project,
    createdAt: now,
    updatedAt: now,
    workspaces: draft.workspaces.map(snapshotWorkspace),
    startupCommands,
  };
}

function validTemplateProject(value: unknown): value is TemplateProjectFocus {
  return isProjectFocus(value) && value !== ALL_PROJECTS;
}

export function normalizeProjectTemplate(value: unknown): ProjectTemplate {
  if (typeof value !== "object" || value === null) throw new Error("项目模板数据无效");
  const candidate = value as Partial<ProjectTemplate>;
  if (
    candidate.v !== 1 || typeof candidate.id !== "string" || !candidate.id ||
    typeof candidate.name !== "string" || !candidate.name.trim() || candidate.name.trim().length > 80 ||
    !validTemplateProject(candidate.project) || typeof candidate.createdAt !== "number" ||
    !Number.isFinite(candidate.createdAt) || typeof candidate.updatedAt !== "number" ||
    !Number.isFinite(candidate.updatedAt) || !Array.isArray(candidate.workspaces) ||
    candidate.workspaces.length === 0 || typeof candidate.startupCommands !== "object" ||
    candidate.startupCommands === null || Array.isArray(candidate.startupCommands)
  ) {
    throw new Error("项目模板数据无效");
  }
  const workspaces = candidate.workspaces
    .filter((workspace) => workspace.kind !== "tools")
    .map(snapshotWorkspace);
  if (workspaces.length !== candidate.workspaces.length) throw new Error("项目模板不能包含全局工具工作区");
  const template = createProjectTemplate(
    {
      name: candidate.name,
      project: candidate.project,
      workspaces,
      startupCommands: candidate.startupCommands as Record<string, string>,
    },
    { id: candidate.id, now: candidate.createdAt },
  );
  template.updatedAt = candidate.updatedAt;
  return template;
}

export function normalizeProjectTemplates(value: unknown): ProjectTemplate[] {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) throw new Error("项目模板存储格式无效");
  const templates = value.map(normalizeProjectTemplate);
  const ids = new Set<string>();
  const names = new Set<string>();
  for (const template of templates) {
    const name = template.name.toLocaleLowerCase();
    if (ids.has(template.id) || names.has(name)) throw new Error("项目模板包含重复标识或名称");
    ids.add(template.id);
    names.add(name);
  }
  return templates.sort((left, right) => left.name.localeCompare(right.name, "zh-CN"));
}

function matchingWorkspace(template: Workspace, current: readonly Workspace[]): Workspace | undefined {
  const direct = current.find((workspace) => workspace.id === template.id);
  if (direct) return direct;
  const key = workspaceLogicalKey(template);
  const matches = current.filter((workspace) => workspaceLogicalKey(workspace) === key);
  return matches.length === 1 ? matches[0] : undefined;
}

function restoreTab(template: AppTab, workspace: Workspace): AppTab {
  return snapshotTab(template, workspace.sessionId, workspace.connId);
}

function mergeTemplateWorkspace(template: Workspace, existing: Workspace): Workspace {
  const panes: Pane[] = existing.panes.map((pane) => ({ ...pane, tabs: [...pane.tabs] }));
  for (const [paneIndex, templatePane] of template.panes.entries()) {
    let pane = panes.find((candidate) => candidate.id === templatePane.id) ?? panes[paneIndex];
    if (!pane && panes.length < 2) {
      pane = { id: templatePane.id, tabs: [], activeTabId: null };
      panes.push(pane);
    }
    if (!pane) continue;
    for (const templateTab of templatePane.tabs) {
      if (pane.tabs.some((tab) => tab.id === templateTab.id)) continue;
      pane.tabs.push(restoreTab(templateTab, existing));
    }
    if (!pane.activeTabId || !pane.tabs.some((tab) => tab.id === pane.activeTabId)) {
      pane.activeTabId = pane.tabs[pane.tabs.length - 1]?.id ?? null;
    }
  }
  return capWorkspacePanes({ ...existing, panes });
}

export function restoreProjectTemplate(
  template: ProjectTemplate,
  current: readonly Workspace[],
): ProjectTemplateRestore {
  const workspaces = [...current];
  const workspaceIds = new Map<string, string>();
  const tabIds = new Map<string, string>();
  const addedWorkspaceIds: string[] = [];
  for (const templateWorkspace of template.workspaces) {
    const existing = matchingWorkspace(templateWorkspace, workspaces);
    const restored = existing
      ? mergeTemplateWorkspace(templateWorkspace, existing)
      : snapshotWorkspace(templateWorkspace);
    if (existing) {
      const index = workspaces.findIndex((workspace) => workspace.id === existing.id);
      workspaces[index] = restored;
    } else {
      workspaces.push(restored);
      addedWorkspaceIds.push(restored.id);
    }
    workspaceIds.set(templateWorkspace.id, restored.id);
    for (const templateTab of templateWorkspace.panes.flatMap((pane) => pane.tabs)) {
      const actual = restored.panes.flatMap((pane) => pane.tabs).find((tab) => tab.id === templateTab.id);
      if (actual) tabIds.set(templateTab.id, actual.id);
    }
  }
  return { workspaces, workspaceIds, tabIds, addedWorkspaceIds };
}

export function templateAssetIds(template: ProjectTemplate): Set<string> {
  return new Set(template.workspaces.map((workspace) => workspace.assetId).filter((id): id is string => Boolean(id)));
}

export function templateCommandEntries(template: ProjectTemplate): {
  workspace: Workspace;
  tab: AppTab;
  command: string;
}[] {
  const entries: { workspace: Workspace; tab: AppTab; command: string }[] = [];
  for (const workspace of template.workspaces) {
    for (const pane of workspace.panes) {
      for (const tab of pane.tabs) {
        const command = template.startupCommands[tab.id];
        if (tab.kind === "terminal" && !tab.containerId && command?.trim()) {
          entries.push({ workspace, tab, command });
        }
      }
    }
  }
  return entries;
}

export function templateId(): string {
  const random = typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  return `project-template-${random}`;
}

export function templateTabLabel(workspace: Workspace, tab: AppTab): string {
  return `${workspace.title} / ${tab.title}`;
}

export function templateStorageText(template: ProjectTemplate): string {
  return JSON.stringify(template);
}
