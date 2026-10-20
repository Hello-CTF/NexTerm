import { clientId } from "../../ipc/env";
import {
  terminalApi,
  type AttachedTabInfo,
} from "../../ipc/commands";
import type { BroadcastEnvironmentConfirmation, BroadcastProductionChallenge } from "../production/productionChallenge";
import { useUi, type AppTab, type Workspace } from "../../app/store";
import { templateCommandEntries, type ProjectTemplate } from "../../app/projectTemplates";
import { describeError } from "../../ui/errorText";
import { getBroadcastConfirmationChallenge } from "./broadcast";

export type ProjectCommandStatus = "sent" | "failed" | "canceled" | "skipped";

export interface ProjectCommandResult {
  workspaceId: string;
  tabId: string;
  title: string;
  status: ProjectCommandStatus;
  detail?: string;
}

export interface ProjectCommandDependencies {
  createTab(sessionId: string, title: string): Promise<AttachedTabInfo>;
  claim(tabId: string): Promise<unknown>;
  prepare(tabId: string, data: Uint8Array, confirmation?: BroadcastEnvironmentConfirmation): Promise<void>;
  write(tabId: string, data: Uint8Array, confirmation?: BroadcastEnvironmentConfirmation): Promise<void>;
  // Legacy acknowledge-only confirmation (pre-typed-name challenges). Typed-name
  // challenges need confirmProductionEnvironmentNames instead.
  confirmProduction(challenge: BroadcastProductionChallenge, command: string): Promise<boolean>;
  // R-01 typed confirmation: resolves the per-target environment names the
  // user entered, or null when canceled.
  confirmProductionEnvironmentNames?(challenge: BroadcastProductionChallenge, command: string): Promise<string[] | null>;
}

function defaultDependencies(): ProjectCommandDependencies {
  return {
    createTab: (sessionId, title) => terminalApi.createTab(sessionId, { title }),
    claim: (tabId) => terminalApi.claim(tabId),
    prepare: (tabId, data, confirmation) => terminalApi.prepareBroadcast([tabId], data, confirmation),
    write: (tabId, data, confirmation) => terminalApi.write(tabId, data, {
      targetTabIds: [tabId],
      ...(confirmation ? { productionConfirmation: confirmation } : {}),
    }),
    confirmProduction: async () => false,
  };
}

function locateTab(storeTabId: string): { workspace: Workspace; tab: AppTab } | undefined {
  for (const workspace of useUi.getState().workspaces) {
    for (const pane of workspace.panes) {
      const tab = pane.tabs.find((candidate) => candidate.id === storeTabId);
      if (tab) return { workspace, tab };
    }
  }
  return undefined;
}

async function ensureTerminalTab(storeTabId: string, deps: ProjectCommandDependencies): Promise<string> {
  const initial = locateTab(storeTabId);
  if (!initial || initial.tab.kind !== "terminal" || initial.tab.containerId) {
    throw new Error("终端标签已不可用");
  }
  if (initial.tab.tabId) return initial.tab.tabId;
  if (initial.tab.dead || initial.tab.exited) throw new Error("终端已失效或已退出");
  if (!initial.tab.sessionId) throw new Error("主机连接尚未建立");
  const session = useUi.getState().sessions.find((candidate) => candidate.id === initial.tab.sessionId);
  if (session?.status !== "connected") throw new Error("主机连接尚未建立");
  const info = await deps.createTab(initial.tab.sessionId, initial.tab.title);
  const current = locateTab(storeTabId);
  if (current?.tab.tabId && current.tab.tabId !== info.tabId) {
    await terminalApi.closeTab(info.tabId, "kill", initial.tab.sessionId).catch(() => undefined);
    return current.tab.tabId;
  }
  if (!current) {
    await terminalApi.closeTab(info.tabId, "kill", initial.tab.sessionId).catch(() => undefined);
    throw new Error("终端标签已关闭");
  }
  useUi.getState().updateTab(storeTabId, {
    tabId: info.tabId,
    restorePending: undefined,
    dead: false,
    exited: info.exited,
    retainable: info.retainable,
    shell: info.shell,
  });
  if (info.controller !== clientId()) await deps.claim(info.tabId);
  return info.tabId;
}

// Resolve the typed names for a challenge. Legacy challenges (no
// requiredEnvironmentNames) keep the acknowledge-only dep; typed challenges
// require the names-carrying dep so the confirmation the backend demands is
// the one the user actually gave.
async function confirmProductionNames(
  challenge: BroadcastProductionChallenge,
  command: string,
  deps: ProjectCommandDependencies,
): Promise<string[] | null> {
  if (challenge.requiredEnvironmentNames === undefined) {
    return (await deps.confirmProduction(challenge, command)) ? [] : null;
  }
  if (!deps.confirmProductionEnvironmentNames) {
    throw new Error("当前入口不支持逐目标环境名确认，命令未执行");
  }
  return deps.confirmProductionEnvironmentNames(challenge, command);
}

async function writeWithProductionConfirmation(
  kernelTabId: string,
  command: string,
  deps: ProjectCommandDependencies,
): Promise<"sent" | "canceled"> {
  const payload = new TextEncoder().encode(/[\r\n]$/.test(command) ? command : `${command}\r`);
  let confirmation: BroadcastEnvironmentConfirmation | undefined;
  for (let attempt = 0; attempt < 4; attempt += 1) {
    try {
      await deps.prepare(kernelTabId, payload, confirmation);
      await deps.write(kernelTabId, payload, confirmation);
      return "sent";
    } catch (error) {
      const challenge = getBroadcastConfirmationChallenge(error);
      if (!challenge) throw error;
      const environmentNames = await confirmProductionNames(challenge, command, deps);
      if (environmentNames === null) return "canceled";
      confirmation = {
        confirmed: true,
        targetSetHash: challenge.targetSetHash,
        inputHash: challenge.inputHash,
        ...(environmentNames.length > 0 ? { environmentNames } : {}),
      };
    }
  }
  throw new Error("生产确认挑战持续变化，命令未执行");
}

export async function executeProjectTemplateCommands(
  template: ProjectTemplate,
  tabIds: ReadonlyMap<string, string>,
  dependencies: Partial<ProjectCommandDependencies> = {},
): Promise<ProjectCommandResult[]> {
  const deps = { ...defaultDependencies(), ...dependencies };
  const results: ProjectCommandResult[] = [];
  for (const entry of templateCommandEntries(template)) {
    const storeTabId = tabIds.get(entry.tab.id);
    const base = { workspaceId: entry.workspace.id, tabId: entry.tab.id, title: entry.tab.title };
    if (!storeTabId) {
      results.push({ ...base, status: "skipped", detail: "标签未恢复" });
      continue;
    }
    const location = locateTab(storeTabId);
    const sessionId = location?.tab.sessionId ?? location?.workspace.sessionId;
    const session = useUi.getState().sessions.find((candidate) => candidate.id === sessionId);
    if (session?.status !== "connected") {
      results.push({ ...base, status: "skipped", detail: "主机未连接，启动命令未执行" });
      continue;
    }
    try {
      const kernelTabId = await ensureTerminalTab(storeTabId, deps);
      const status = await writeWithProductionConfirmation(kernelTabId, entry.command, deps);
      results.push({
        ...base,
        status,
        ...(status === "canceled" ? { detail: "已取消生产确认，命令未执行" } : {}),
      });
    } catch (error) {
      results.push({ ...base, status: "failed", detail: describeError(error) });
    }
  }
  return results;
}
