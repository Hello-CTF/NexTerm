/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AttachedTabInfo, BroadcastConfirmationChallenge, SessionInfo } from "../../ipc/commands";
import type { AppTab, Workspace } from "../../app/store";

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {}, dbApi: {}, sessionApi: {}, vaultApi: {}, syncApi: {},
    terminalApi: { closeTab: vi.fn(async () => undefined) },
  };
});

import { useUi } from "../../app/store";
import { captureProjectTemplateDraft, createProjectTemplate, type ProjectTemplate } from "../../app/projectTemplates";
import { projectFocusForGroup, type ProjectInventory } from "../../app/projectFocus";
import {
  executeProjectTemplateCommands,
  type ProjectCommandDependencies,
} from "../../features/terminal/projectTemplateCommands";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "session-1", closable: true };
}

function hostWorkspace(): Workspace {
  const first = terminal("tab-1");
  const second = terminal("tab-2");
  return {
    id: "ws-1", kind: "session", title: "host", sessionId: "session-1", assetId: "asset-1", assetKind: "ssh",
    panes: [{ id: "pane-1", tabs: [first, second], activeTabId: first.id }],
    activePaneId: "pane-1", splitRatio: 0.5, closable: true,
  };
}

const inventory: ProjectInventory = {
  assets: [{
    id: "asset-1", groupId: "project", kind: "ssh", name: "host", host: null, port: null,
    username: null, authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "",
    sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  }],
  groups: [{ id: "project", parentId: null, name: "project", sort: 0, createdAt: 1, updatedAt: 1 }],
};

function makeTemplate(): ProjectTemplate {
  const draft = captureProjectTemplateDraft("Commands", projectFocusForGroup("project"), [hostWorkspace()], inventory);
  draft.startupCommands = { "tab-1": "cd /srv/app", "tab-2": "tail -f app.log" };
  return createProjectTemplate(draft, { id: "template-commands", now: 1 });
}

function connectedSession(): SessionInfo {
  return { id: "session-1", assetId: "asset-1", name: "host", kind: "ssh", status: "connected", tabs: [], createdAt: 1 };
}

function attached(tabId: string): AttachedTabInfo {
  return {
    tabId, sessionId: "session-1", cols: 80, rows: 24, controller: null,
    subscribers: 0, viewers: 0, exited: false, retainable: true, shell: "posix",
  };
}

function seed(): ProjectTemplate {
  const template = makeTemplate();
  useUi.setState({
    workspaces: [hostWorkspace()],
    sessions: [connectedSession()],
    activeWorkspaceId: "ws-1",
    workspaceRestoreResults: {},
  });
  return template;
}

function dependencies(events: string[]): Partial<ProjectCommandDependencies> {
  return {
    createTab: vi.fn(async (_sessionId, title) => {
      events.push(`create:${title}`);
      return attached(`kernel-${title}`);
    }),
    claim: vi.fn(async (tabId) => {
      events.push(`claim:${tabId}`);
      return null;
    }),
    prepare: vi.fn(async (tabId) => {
      events.push(`prepare:${tabId}`);
    }),
    write: vi.fn(async (tabId) => {
      events.push(`write:${tabId}`);
    }),
  };
}

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
});

describe("project template startup commands", () => {
  it("creates connected terminal tabs and executes commands sequentially in template order", async () => {
    const template = seed();
    const events: string[] = [];
    const deps = dependencies(events);
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"], ["tab-2", "tab-2"]]), deps);

    expect(results.map((item) => item.status)).toEqual(["sent", "sent"]);
    expect(events).toEqual([
      "create:tab-1", "claim:kernel-tab-1", "prepare:kernel-tab-1", "write:kernel-tab-1",
      "create:tab-2", "claim:kernel-tab-2", "prepare:kernel-tab-2", "write:kernel-tab-2",
    ]);
    expect(useUi.getState().workspaces[0].panes[0].tabs.map((tab) => tab.tabId)).toEqual(["kernel-tab-1", "kernel-tab-2"]);
    const firstWrite = vi.mocked(deps.write!).mock.calls[0];
    expect(new TextDecoder().decode(firstWrite[1])).toBe("cd /srv/app\r");
  });

  it("binds production confirmation to the challenged input, target, and typed environment names", async () => {
    const template = seed();
    template.startupCommands = { "tab-1": "docker compose logs -f" };
    const challenge: BroadcastConfirmationChallenge & { requiredEnvironmentNames: string[] } = {
      kind: "broadcast_production_confirmation",
      targetSetHash: "a".repeat(64),
      inputHash: "b".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [{
        tabId: "kernel-tab-1", sessionId: "session-1", sessionName: "prod", environment: "production",
      }],
    };
    const confirmProductionEnvironmentNames = vi.fn(async () => ["production"]);
    const prepare = vi.fn()
      .mockRejectedValueOnce({ code: "needs_confirm", detail: challenge })
      .mockResolvedValue(undefined);
    const write = vi.fn(async (_tabId: string, _data: Uint8Array, _confirmation?: unknown) => undefined);
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"]]), {
      createTab: async () => attached("kernel-tab-1"),
      claim: async () => null,
      prepare,
      write,
      confirmProductionEnvironmentNames,
    });

    expect(results[0].status).toBe("sent");
    expect(confirmProductionEnvironmentNames).toHaveBeenCalledWith(challenge, "docker compose logs -f");
    expect(prepare).toHaveBeenCalledTimes(2);
    expect(prepare.mock.calls[1][2]).toEqual({
      confirmed: true, targetSetHash: challenge.targetSetHash, inputHash: challenge.inputHash,
      environmentNames: ["production"],
    });
    expect(write.mock.calls[0][2]).toEqual(prepare.mock.calls[1][2]);
  });

  it("does not write a production command when confirmation is canceled", async () => {
    const template = seed();
    template.startupCommands = { "tab-1": "tail -f prod.log" };
    const challenge: BroadcastConfirmationChallenge & { requiredEnvironmentNames: string[] } = {
      kind: "broadcast_production_confirmation",
      targetSetHash: "c".repeat(64),
      inputHash: "d".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [{ tabId: "kernel-tab-1", sessionId: "session-1", sessionName: "prod", environment: "production" }],
    };
    const write = vi.fn(async () => undefined);
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"]]), {
      createTab: async () => attached("kernel-tab-1"),
      claim: async () => null,
      prepare: vi.fn(async () => { throw { code: "needs_confirm", detail: challenge }; }),
      write,
      confirmProductionEnvironmentNames: vi.fn(async () => null),
    });

    expect(results[0]).toMatchObject({ status: "canceled" });
    expect(write).not.toHaveBeenCalled();
  });

  it("fails closed without writing when a challenge omits required environment names", async () => {
    const template = seed();
    template.startupCommands = { "tab-1": "tail -f prod.log" };
    const challenge: BroadcastConfirmationChallenge = {
      kind: "broadcast_production_confirmation",
      targetSetHash: "c".repeat(64),
      inputHash: "d".repeat(64),
      productionTargets: [{ tabId: "kernel-tab-1", sessionId: "session-1", sessionName: "prod", environment: "production" }],
    };
    const write = vi.fn(async () => undefined);
    const confirmProductionEnvironmentNames = vi.fn(async () => ["production"]);
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"]]), {
      createTab: async () => attached("kernel-tab-1"),
      claim: async () => null,
      prepare: vi.fn(async () => { throw { code: "needs_confirm", detail: challenge }; }),
      write,
      confirmProductionEnvironmentNames,
    });

    expect(results[0]).toMatchObject({ status: "failed" });
    expect(confirmProductionEnvironmentNames).not.toHaveBeenCalled();
    expect(write).not.toHaveBeenCalled();
  });

  it("fails closed without retrying when confirmation returns no environment names", async () => {
    const template = seed();
    template.startupCommands = { "tab-1": "tail -f prod.log" };
    const challenge: BroadcastConfirmationChallenge & { requiredEnvironmentNames: string[] } = {
      kind: "broadcast_production_confirmation",
      targetSetHash: "c".repeat(64),
      inputHash: "d".repeat(64),
      requiredEnvironmentNames: ["production"],
      productionTargets: [{ tabId: "kernel-tab-1", sessionId: "session-1", sessionName: "prod", environment: "production" }],
    };
    const prepare = vi.fn(async () => { throw { code: "needs_confirm", detail: challenge }; });
    const write = vi.fn(async () => undefined);
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"]]), {
      createTab: async () => attached("kernel-tab-1"),
      claim: async () => null,
      prepare,
      write,
      confirmProductionEnvironmentNames: vi.fn(async () => []),
    });

    expect(results[0]).toMatchObject({ status: "failed" });
    expect(prepare).toHaveBeenCalledTimes(1);
    expect(write).not.toHaveBeenCalled();
  });

  it("attributes a failed host connection to its commands without creating tabs", async () => {
    const template = seed();
    template.startupCommands = { "tab-1": "tail -f app.log" };
    useUi.setState((state) => ({
      workspaceRestoreResults: { "ws-1": { status: "failed", detail: "offline" } },
      sessions: state.sessions.map((session) => ({ ...session, status: "disconnected" })),
    }));
    const createTab = vi.fn(async () => attached("kernel-tab-1"));
    const results = await executeProjectTemplateCommands(template, new Map([["tab-1", "tab-1"]]), { createTab });

    expect(results[0]).toMatchObject({ status: "skipped", detail: "主机未连接，启动命令未执行" });
    expect(createTab).not.toHaveBeenCalled();
  });
});
