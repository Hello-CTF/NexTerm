/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  ALL_PROJECTS,
  UNGROUPED_PROJECT,
  filterAssetsForProject,
  filterGroupsForProject,
  filterWorkspacesForProject,
  focusedWorkspaces,
  projectFocusForGroup,
  topLevelProjects,
} from "../../app/projectFocus";
import { cycleActiveWorkspace, useUi, type SessionRuntimeInfo, type Workspace } from "../../app/store";
import type { Asset, AssetGroup } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  disconnect: vi.fn(),
  closeTab: vi.fn(),
  disconnectDb: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  sessionApi: { disconnect: mocks.disconnect, list: vi.fn(async () => []) },
  terminalApi: { closeTab: mocks.closeTab },
  dbApi: { disconnect: mocks.disconnectDb },
  vaultApi: {},
}));

function group(id: string, parentId: string | null = null): AssetGroup {
  return { id, parentId, name: `name-${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
}

function asset(id: string, groupId: string | null): Asset {
  return {
    id,
    groupId,
    kind: "ssh",
    name: id,
    host: "127.0.0.1",
    port: 22,
    username: "root",
    authKind: "agent",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
  };
}

function workspace(id: string, assetId?: string): Workspace {
  const tab = {
    id: `${id}-tab`,
    kind: "terminal" as const,
    title: `${id}-tab`,
    sessionId: `session-${id}`,
    tabId: `kernel-${id}`,
    closable: true,
  };
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    assetKind: "ssh",
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    splitRatio: 0.5,
    closable: true,
  };
}

function session(id: string, assetId: string): SessionRuntimeInfo {
  return { id: `session-${id}`, assetId, name: id, kind: "ssh", status: "connected", tabs: [], createdAt: 1 };
}

const groups = [group("p1"), group("child", "p1"), group("p2")];
const assets = [
  asset("root-asset", "p1"),
  asset("child-asset", "child"),
  asset("other-asset", "p2"),
  asset("free-asset", null),
  asset("orphan-asset", "missing"),
];
const inventory = { assets, groups };
const projectOne = projectFocusForGroup("p1");

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  useUi.setState({
    activeProject: ALL_PROJECTS,
    projectInventory: null,
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
    broadcast: null,
  });
});

describe("project focus resolution", () => {
  it("uses only top-level groups as projects and includes nested assets", () => {
    expect(topLevelProjects(groups).map((item) => item.id)).toEqual(["p1", "p2"]);
    expect(filterAssetsForProject(assets, projectOne, groups).map((item) => item.id)).toEqual([
      "root-asset",
      "child-asset",
    ]);
    expect(filterGroupsForProject(groups, projectOne).map((item) => item.id)).toEqual(["p1", "child"]);
    expect(filterAssetsForProject(assets, ALL_PROJECTS, groups)).toEqual(assets);
  });

  it("collects null, missing, and cyclic group ownership under ungrouped", () => {
    const cyclic = [...groups, group("cycle-a", "cycle-b"), group("cycle-b", "cycle-a")];
    const withCycles = [...assets, asset("cycle-asset", "cycle-a")];
    expect(filterAssetsForProject(withCycles, UNGROUPED_PROJECT, cyclic).map((item) => item.id)).toEqual([
      "free-asset",
      "orphan-asset",
      "cycle-asset",
    ]);
    expect(filterGroupsForProject(cyclic, UNGROUPED_PROJECT)).toEqual([]);
  });

  it("applies the same project and ungrouped rules to workspaces", () => {
    const workspaces = [
      workspace("ws-root", "root-asset"),
      workspace("ws-child", "child-asset"),
      workspace("ws-other", "other-asset"),
      workspace("ws-free", "free-asset"),
      workspace("ws-orphan", "orphan-asset"),
      workspace("ws-quick"),
    ];
    expect(filterWorkspacesForProject(workspaces, projectOne, inventory).map((item) => item.id)).toEqual([
      "ws-root",
      "ws-child",
    ]);
    expect(filterWorkspacesForProject(workspaces, UNGROUPED_PROJECT, inventory).map((item) => item.id)).toEqual([
      "ws-free",
      "ws-orphan",
      "ws-quick",
    ]);
    expect(filterWorkspacesForProject(workspaces, ALL_PROJECTS, inventory)).toEqual(workspaces);
  });
});

describe("project focus store behavior", () => {
  it("keeps all workspaces, sessions, IPC connections, and broadcast generation when switching", () => {
    const workspaces = [
      workspace("ws-root", "root-asset"),
      workspace("ws-child", "child-asset"),
      workspace("ws-other", "other-asset"),
      workspace("ws-free", "free-asset"),
    ];
    const sessions = [session("ws-root", "root-asset"), session("ws-other", "other-asset")];
    useUi.setState({
      workspaces,
      sessions,
      projectInventory: inventory,
      activeWorkspaceId: "ws-other",
    });
    useUi.getState().setBroadcast({
      origin: { workspaceId: "ws-other", storeTabId: "ws-other-tab" },
      targetIds: ["ws-other-tab"],
    });
    const broadcast = useUi.getState().broadcast;

    useUi.getState().setActiveProject(projectOne);

    const state = useUi.getState();
    expect(state.activeWorkspaceId).toBe("ws-child");
    expect(focusedWorkspaces(state).map((item) => item.id)).toEqual(["ws-root", "ws-child"]);
    expect(state.workspaces).toBe(workspaces);
    expect(state.sessions).toBe(sessions);
    expect(state.broadcast).toBe(broadcast);
    expect(mocks.disconnect).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(mocks.disconnectDb).not.toHaveBeenCalled();
  });

  it("cycles only visible workspaces and cannot activate a hidden workspace", () => {
    const workspaces = [
      workspace("ws-root", "root-asset"),
      workspace("ws-child", "child-asset"),
      workspace("ws-other", "other-asset"),
    ];
    useUi.setState({
      workspaces,
      projectInventory: inventory,
      activeProject: projectOne,
      activeWorkspaceId: "ws-child",
    });

    expect(cycleActiveWorkspace(1)).toBe("ws-root");
    expect(cycleActiveWorkspace(1)).toBe("ws-child");
    expect(cycleActiveWorkspace(-1)).toBe("ws-root");
    useUi.getState().setActiveWorkspace("ws-other");
    expect(useUi.getState().activeWorkspaceId).toBe("ws-child");
    expect(workspaces.map((item) => item.id)).toEqual(["ws-root", "ws-child", "ws-other"]);
  });

  it("fails closed while inventory is loading and restores the focused active workspace when ready", () => {
    const workspaces = [workspace("ws-root", "root-asset"), workspace("ws-other", "other-asset")];
    useUi.setState({ workspaces, activeWorkspaceId: "ws-root" });
    useUi.getState().setActiveProject(projectOne);
    expect(focusedWorkspaces(useUi.getState())).toEqual([]);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-root");

    useUi.getState().setProjectInventory(inventory);
    expect(focusedWorkspaces(useUi.getState()).map((item) => item.id)).toEqual(["ws-root"]);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-root");
  });
});
