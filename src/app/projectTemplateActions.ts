import { forwardRuleApi, type ForwardRule } from "../ipc/forwardRuleApi";
import { describeError } from "../ui/errorText";
import {
  executeProjectTemplateCommands,
  type ProjectCommandDependencies,
  type ProjectCommandResult,
} from "../features/terminal/projectTemplateCommands";
import {
  reconnectRestoredWorkspaces,
  type ReconnectWorkspace,
} from "../features/terminal/workspaceRestore";
import {
  restoreProjectTemplate,
  templateAssetIds,
  type ProjectTemplate,
  type TemplateProjectFocus,
} from "./projectTemplates";
import type { ProjectInventory } from "./projectFocus";
import { filterWorkspacesForProject } from "./projectFocus";
import { useUi, type WorkspaceRestoreResult } from "./store";

export interface ProjectForwardResult {
  ruleId: string;
  name: string;
  status: "started" | "skipped" | "failed";
  detail?: string;
}

export interface OpenProjectResult {
  workspaces: Record<string, WorkspaceRestoreResult>;
  forwards: ProjectForwardResult[];
  commands: ProjectCommandResult[];
  forwardError?: string;
}

export interface OpenProjectDependencies {
  reconnectWorkspace?: ReconnectWorkspace;
  listForwardRules?: () => Promise<ForwardRule[]>;
  startForwardRule?: (id: string, sessionId: string) => Promise<unknown>;
  executeCommands?: (
    template: ProjectTemplate,
    tabIds: ReadonlyMap<string, string>,
  ) => Promise<ProjectCommandResult[]>;
  commandDependencies?: Partial<ProjectCommandDependencies>;
}

async function startTemplateForwards(
  template: ProjectTemplate,
  dependencies: OpenProjectDependencies,
): Promise<Pick<OpenProjectResult, "forwards" | "forwardError">> {
  const listRules = dependencies.listForwardRules ?? forwardRuleApi.list;
  const startRule = dependencies.startForwardRule ?? forwardRuleApi.start;
  const assetIds = templateAssetIds(template);
  let rules: ForwardRule[];
  try {
    rules = await listRules();
  } catch (error) {
    return { forwards: [], forwardError: describeError(error) };
  }
  const forwards: ProjectForwardResult[] = [];
  for (const rule of rules) {
    if (!assetIds.has(rule.assetId)) continue;
    const base = { ruleId: rule.id, name: rule.name || `127.0.0.1:${rule.listenPort}` };
    if (rule.status === "active" || rule.status === "reconnecting") {
      forwards.push({ ...base, status: "skipped", detail: "规则已在运行" });
      continue;
    }
    const state = useUi.getState();
    const workspaceSessionIds = state.workspaces
      .filter((workspace) => workspace.assetId === rule.assetId)
      .map((workspace) => workspace.sessionId)
      .filter((id): id is string => Boolean(id));
    const session = workspaceSessionIds
      .map((id) => state.sessions.find((candidate) => candidate.id === id))
      .find((candidate) => candidate?.status === "connected" && candidate.kind === "ssh" && candidate.assetId === rule.assetId)
      ?? state.sessions.find((candidate) =>
        candidate.status === "connected" && candidate.kind === "ssh" && candidate.assetId === rule.assetId
      );
    if (!session) {
      forwards.push({ ...base, status: "failed", detail: "规则所属 SSH 主机未连接" });
      continue;
    }
    try {
      await startRule(rule.id, session.id);
      forwards.push({ ...base, status: "started" });
    } catch (error) {
      forwards.push({ ...base, status: "failed", detail: describeError(error) });
    }
  }
  return { forwards };
}

export async function openProjectTemplate(
  template: ProjectTemplate,
  dependencies: OpenProjectDependencies = {},
): Promise<OpenProjectResult> {
  const before = useUi.getState();
  const restored = restoreProjectTemplate(template, before.workspaces);
  const restoredIds = [...new Set(restored.workspaceIds.values())];
  const restoredById = new Map(restored.workspaces.map((workspace) => [workspace.id, workspace]));
  const initialResults = { ...before.workspaceRestoreResults };
  for (const workspaceId of restoredIds) {
    delete initialResults[workspaceId];
    const workspace = restoredById.get(workspaceId);
    const session = before.sessions.find((candidate) => candidate.id === workspace?.sessionId);
    if (session?.status === "connected") initialResults[workspaceId] = { status: "success" };
  }
  useUi.setState({
    workspaces: restored.workspaces,
    activeWorkspaceId: before.activeWorkspaceId,
    workspaceRestoreDismissed: false,
    workspaceRestoreResults: initialResults,
  });
  await reconnectRestoredWorkspaces(restoredIds, dependencies.reconnectWorkspace);
  const latestResults = useUi.getState().workspaceRestoreResults;
  const workspaces: Record<string, WorkspaceRestoreResult> = {};
  for (const workspaceId of restoredIds) {
    const result = latestResults[workspaceId];
    if (result) workspaces[workspaceId] = result;
  }
  const forwardResult = await startTemplateForwards(template, dependencies);
  const executeCommands = dependencies.executeCommands ?? ((candidate, tabIds) =>
    executeProjectTemplateCommands(candidate, tabIds, dependencies.commandDependencies)
  );
  const commands = await executeCommands(template, restored.tabIds);
  return { workspaces, ...forwardResult, commands };
}

export interface CloseProjectResult {
  workspaceId: string;
  title: string;
  closed: boolean;
}

export async function closeProject(
  project: TemplateProjectFocus,
  inventory: ProjectInventory,
  closeWorkspace: (id: string) => Promise<boolean> = (id) => useUi.getState().closeWorkspace(id, "detach"),
): Promise<CloseProjectResult[]> {
  const targets = filterWorkspacesForProject(useUi.getState().workspaces, project, inventory);
  const results: CloseProjectResult[] = [];
  for (const workspace of targets) {
    results.push({
      workspaceId: workspace.id,
      title: workspace.title,
      closed: await closeWorkspace(workspace.id),
    });
  }
  return results;
}
