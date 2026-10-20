/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionInfo } from "../../ipc/commands";
import type { ForwardRule } from "../../ipc/forwardRuleApi";
import type { AppTab, ConnectOutcome, Workspace } from "../../app/store";
import { deferred, flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  closeTab: vi.fn(),
  listLive: vi.fn(),
  sessionList: vi.fn(),
  dbDisconnect: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {}, vaultApi: {}, syncApi: {},
  sessionApi: { list: mocks.sessionList },
  dbApi: { disconnect: mocks.dbDisconnect },
  terminalApi: {
    closeTab: mocks.closeTab,
    listLive: mocks.listLive,
    recordingStatus: vi.fn(),
  },
}));

import { useUi } from "../../app/store";
import { setTerminalCloseAction } from "../../app/preferences";
import { closeProject, openProjectTemplate } from "../../app/projectTemplateActions";
import { captureProjectTemplateDraft, createProjectTemplate, type ProjectTemplate } from "../../app/projectTemplates";
import { projectFocusForGroup, type ProjectInventory } from "../../app/projectFocus";

function group(id: string) {
  return { id, parentId: null, name: id, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, kind: "ssh" | "local" = "ssh") {
  return {
    id, groupId: "project", kind, name: id, host: null, port: null, username: null,
    authKind: null, keyPath: null, credId: null, options: {}, tags: "", note: "",
    sort: 0, createdAt: 1, updatedAt: 1, deletedAt: null, builtin: false,
  } as const;
}

const inventory: ProjectInventory = {
  assets: [{ ...asset("asset-other"), groupId: "other" }, ...Array.from({ length: 6 }, (_, index) => asset(`asset-ws-${index + 1}`)), asset("asset-local", "local")],
  groups: [group("project"), group("other")],
};

function hostWorkspace(id: string, patch: Partial<Workspace> = {}): Workspace {
  const tab: AppTab = {
    id: `${id}-tab`, kind: "terminal", title: `${id}-tab`, sessionId: `session-${id}`,
    tabId: `kernel-${id}`, closable: true, retainable: true,
  };
  return {
    id, kind: "session", title: id, sessionId: `session-${id}`, assetId: `asset-${id}`,
    assetKind: "ssh", restorePending: true,
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`, splitRatio: 0.5, closable: true, ...patch,
  };
}

function connectedSession(id: string, kind = "ssh"): SessionInfo {
  return { id: `session-${id}`, assetId: `asset-${id}`, name: id, kind, status: "connected", tabs: [], createdAt: 1 };
}

function makeTemplate(ids: string[]): ProjectTemplate {
  const draft = captureProjectTemplateDraft("Fleet", projectFocusForGroup("project"), ids.map((id) => hostWorkspace(id)), inventory);
  return createProjectTemplate(draft, { id: "template-fleet", now: 1 });
}

function rule(id: string, assetId: string, status = "stopped"): ForwardRule {
  return {
    id, assetId, name: id, kind: "local", listenHost: "127.0.0.1", listenPort: 10000,
    targetHost: "localhost", targetPort: 20000, status, createdAt: 1, updatedAt: 1,
  };
}

function succeed(workspaceId: string): void {
  useUi.setState((state) => ({
    sessions: [...state.sessions.filter((session) => session.id !== `session-${workspaceId}`), connectedSession(workspaceId)],
    workspaces: state.workspaces.map((workspace) => workspace.id === workspaceId ? {
      ...workspace,
      sessionId: `session-${workspaceId}`,
      restorePending: undefined,
      panes: workspace.panes.map((pane) => ({
        ...pane,
        tabs: pane.tabs.map((tab) => ({ ...tab, sessionId: `session-${workspaceId}` })),
      })),
    } : workspace),
  }));
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.listLive.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.dbDisconnect.mockResolvedValue(undefined);
  useUi.setState({
    workspaces: [], sessions: [], activeWorkspaceId: null, workspaceRestoreResults: {},
    workspaceRestoreDismissed: true, workspaceRuntime: {}, aiBoards: {}, toasts: [],
  });
});

describe("open project orchestration", () => {
  it("uses the P-38 queue with concurrency four, starts only related forwards, then runs commands without changing focus", async () => {
    const ids = ["ws-1", "ws-2", "ws-3", "ws-4", "ws-5", "ws-6"];
    const template = makeTemplate(ids);
    const current = hostWorkspace("current", { assetId: "asset-other", restorePending: undefined });
    useUi.setState({
      workspaces: [current], activeWorkspaceId: "current", activeProject: projectFocusForGroup("project"),
    });
    const gates = new Map(ids.map((id) => [id, deferred<ConnectOutcome>()]));
    let active = 0;
    let maximum = 0;
    const reconnectWorkspace = vi.fn((workspaceId: string) => {
      active += 1;
      maximum = Math.max(maximum, active);
      return gates.get(workspaceId)!.promise.finally(() => { active -= 1; });
    });
    const events: string[] = [];
    const startForwardRule = vi.fn(async (id: string) => { events.push(`forward:${id}`); });
    const executeCommands = vi.fn(async () => {
      events.push("commands");
      return [];
    });
    const running = openProjectTemplate(template, {
      reconnectWorkspace,
      listForwardRules: async () => [rule("related", "asset-ws-1"), rule("other", "asset-other"), rule("active", "asset-ws-2", "active")],
      startForwardRule,
      executeCommands,
    });

    expect(reconnectWorkspace).toHaveBeenCalledTimes(4);
    expect(maximum).toBe(4);
    succeed("ws-2");
    gates.get("ws-2")!.resolve({ ok: true });
    await flush();
    expect(reconnectWorkspace).toHaveBeenCalledTimes(5);
    succeed("ws-1");
    gates.get("ws-1")!.resolve({ ok: true });
    await flush();
    expect(reconnectWorkspace).toHaveBeenCalledTimes(6);
    for (const id of ids.slice(2)) {
      succeed(id);
      gates.get(id)!.resolve({ ok: true });
    }
    const result = await running;

    expect(maximum).toBe(4);
    expect(startForwardRule).toHaveBeenCalledWith("related", "session-ws-1");
    expect(startForwardRule).toHaveBeenCalledTimes(1);
    expect(events).toEqual(["forward:related", "commands"]);
    expect(result.forwards.find((item) => item.ruleId === "active")?.status).toBe("skipped");
    expect(result.workspaces["ws-1"]?.status).toBe("success");
    expect(useUi.getState().activeWorkspaceId).toBe("current");
    expect(useUi.getState().activeProject).toBe(projectFocusForGroup("project"));
    expect(useUi.getState().workspaces[0]).toBe(current);
  });

  it("restores only the missing subset and keeps the already-connected member untouched", async () => {
    const template = makeTemplate(["ws-1", "ws-2"]);
    const existing = hostWorkspace("ws-1", { restorePending: undefined });
    useUi.setState({
      workspaces: [existing],
      sessions: [connectedSession("ws-1")],
    });
    const reconnectWorkspace = vi.fn(async (id: string): Promise<ConnectOutcome> => {
      succeed(id);
      return { ok: true };
    });

    const result = await openProjectTemplate(template, {
      reconnectWorkspace,
      listForwardRules: async () => [],
      executeCommands: async () => [],
    });

    // Only the missing subset reconnects; the connected member reports success without a reconnect.
    expect(reconnectWorkspace).toHaveBeenCalledTimes(1);
    expect(reconnectWorkspace).toHaveBeenCalledWith("ws-2", { preserveUserFocus: true });
    expect(result.workspaces["ws-1"]).toEqual({ status: "success" });
    expect(result.workspaces["ws-2"]?.status).toBe("success");
    expect(Object.keys(result.workspaces).sort()).toEqual(["ws-1", "ws-2"]);

    const workspaces = useUi.getState().workspaces;
    const kept = workspaces.find((workspace) => workspace.id === "ws-1");
    expect(kept?.sessionId).toBe("session-ws-1");
    expect(kept?.restorePending).toBeUndefined();
    const added = workspaces.find((workspace) => workspace.id === "ws-2");
    expect(added?.sessionId).toBe("session-ws-2");
    expect(added?.restorePending).toBeUndefined();
  });

  it("keeps a failed connection attached to its workspace and does not start its forward", async () => {
    const template = makeTemplate(["ws-1", "ws-2"]);
    const startForwardRule = vi.fn(async () => undefined);
    const result = await openProjectTemplate(template, {
      reconnectWorkspace: async (id) => {
        if (id === "ws-2") return { ok: false, error: "offline" };
        succeed(id);
        return { ok: true };
      },
      listForwardRules: async () => [rule("one", "asset-ws-1"), rule("two", "asset-ws-2")],
      startForwardRule,
      executeCommands: async () => [],
    });

    expect(result.workspaces["ws-1"]?.status).toBe("success");
    expect(result.workspaces["ws-2"]).toMatchObject({ status: "failed", detail: "offline" });
    expect(result.forwards.find((item) => item.ruleId === "two")).toMatchObject({ status: "failed" });
    expect(startForwardRule).toHaveBeenCalledTimes(1);
  });

  it("does not include unrelated stale failures in this open result", async () => {
    const template = makeTemplate(["ws-1"]);
    const unrelated = hostWorkspace("unrelated", { assetId: "asset-other", restorePending: undefined });
    useUi.setState({
      workspaces: [unrelated],
      workspaceRestoreResults: {
        unrelated: { status: "failed", detail: "other project offline" },
        "stale-workspace": { status: "failed", detail: "stale failure" },
      },
    });

    const result = await openProjectTemplate(template, {
      reconnectWorkspace: async (id) => {
        succeed(id);
        return { ok: true };
      },
      listForwardRules: async () => [],
      executeCommands: async () => [],
    });

    expect(Object.keys(result.workspaces)).toEqual(["ws-1"]);
    expect(result.workspaces["ws-1"]?.status).toBe("success");
    expect(result.workspaces.unrelated).toBeUndefined();
    expect(useUi.getState().workspaceRestoreResults.unrelated).toMatchObject({ status: "failed" });
  });

  it("executes startup commands for a connected target despite its stale failure", async () => {
    const template = makeTemplate(["ws-1"]);
    template.startupCommands["ws-1-tab"] = "tail -f app.log";
    const existing = hostWorkspace("ws-1", { restorePending: undefined });
    useUi.setState({
      workspaces: [existing],
      sessions: [connectedSession("ws-1")],
      workspaceRestoreResults: { "ws-1": { status: "failed", detail: "previous failure" } },
    });
    const reconnectWorkspace = vi.fn(async (): Promise<ConnectOutcome> => ({ ok: true }));
    const prepare = vi.fn(async () => undefined);
    const write = vi.fn(async () => undefined);

    const result = await openProjectTemplate(template, {
      reconnectWorkspace,
      listForwardRules: async () => [],
      commandDependencies: { prepare, write },
    });

    expect(reconnectWorkspace).not.toHaveBeenCalled();
    expect(result.workspaces).toEqual({ "ws-1": { status: "success" } });
    expect(result.commands).toHaveLength(1);
    expect(result.commands[0].status).toBe("sent");
    expect(write).toHaveBeenCalledTimes(1);
  });
});

describe("close project", () => {
  it("detaches durable terminals even when the saved close preference is kill", async () => {
    const project = hostWorkspace("ws-1", { restorePending: undefined });
    const other = hostWorkspace("other", { assetId: "asset-other", restorePending: undefined });
    const session = connectedSession("ws-1");
    useUi.setState({ workspaces: [project, other], sessions: [session], activeWorkspaceId: project.id });
    mocks.sessionList.mockResolvedValue([session]);
    mocks.listLive.mockResolvedValue([{
      tabId: "kernel-ws-1", sessionId: "session-ws-1", sessionName: "ws-1", sessionKind: "ssh",
      cols: 80, rows: 24, controller: null, subscribers: 1, viewers: 1, exited: false, lastOutputMsAgo: 0,
    }]);
    setTerminalCloseAction("kill");

    const results = await closeProject(projectFocusForGroup("project"), inventory);

    expect(results).toEqual([{ workspaceId: "ws-1", title: "ws-1", closed: true }]);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-ws-1", "detach", "session-ws-1");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-ws-1", "kill", "session-ws-1");
    expect(useUi.getState().workspaces.map((workspace) => workspace.id)).toEqual(["other"]);
  });

  it("keeps unsupported terminals and never falls back to kill", async () => {
    const local = hostWorkspace("ws-local", {
      assetId: "asset-local", assetKind: "local", restorePending: undefined,
      panes: [{
        id: "local-pane",
        tabs: [{ id: "local-tab", kind: "terminal", title: "local", sessionId: "session-local", tabId: "kernel-local", closable: true }],
        activeTabId: "local-tab",
      }],
      activePaneId: "local-pane",
    });
    const session = { ...connectedSession("local", "local"), assetId: "asset-local" };
    useUi.setState({ workspaces: [local], sessions: [session], activeWorkspaceId: local.id });
    mocks.listLive.mockResolvedValue([{
      tabId: "kernel-local", sessionId: "session-local", sessionName: "local", sessionKind: "local",
      cols: 80, rows: 24, controller: null, subscribers: 1, viewers: 1, exited: false, lastOutputMsAgo: 0,
    }]);

    const results = await closeProject(projectFocusForGroup("project"), inventory);

    expect(results[0].closed).toBe(false);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
  });
});
