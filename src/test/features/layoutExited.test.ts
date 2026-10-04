/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  closeTab: vi.fn(),
  layoutGet: vi.fn(),
  layoutPut: vi.fn(),
  listLive: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  layoutApi: { get: mocks.layoutGet, put: mocks.layoutPut },
  terminalApi: { closeTab: mocks.closeTab, listLive: mocks.listLive },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import {
  mergeExitedTabs,
  onRemoteChange,
  resetLayoutSyncForTest,
  serializeLayout,
  type PersistedLayout,
} from "../../app/layout";
import { requestCloseTab, useUi, type AppTab, type Workspace } from "../../app/store";

function terminal(id = "terminal"): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}
function workspace(panes: { id: string; tabs: AppTab[] }[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    assetKind: "ssh",
    panes: panes.map((p) => ({ ...p, activeTabId: p.tabs[0]?.id ?? null })),
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}
function seed(panes: { id: string; tabs: AppTab[] }[]) {
  useUi.setState({
    workspaces: [workspace(panes)],
    activeWorkspaceId: "ws",
    sessions: [],
    toasts: [],
  });
}
function toastText(): string {
  return useUi.getState().toasts.map((t) => t.text).join("\n");
}
async function flushAsync() {
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

describe("remote layout preserves exited terminal state", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listLive.mockResolvedValue([]);
    mocks.layoutPut.mockResolvedValue({ saved: true, revision: 1, conflict: false });
  });
  afterEach(() => resetLayoutSyncForTest());

  it("serializeLayout keeps exited in the persisted DTO", () => {
    seed([{ id: "pane", tabs: [{ ...terminal(), exited: true }] }]);
    const dto = serializeLayout(useUi.getState());
    expect(dto).not.toBeNull();
    const persisted = dto as PersistedLayout;
    expect(persisted.workspaces[0].panes[0].tabs[0].exited).toBe(true);
  });

  it("mergeExitedTabs keeps local exited over a stale remote copy and leaves clean layouts untouched", () => {
    const exitedTab = { ...terminal(), exited: true };
    const current = [workspace([{ id: "pane", tabs: [exitedTab] }])];
    const staleRemote: PersistedLayout = {
      v: 1,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [workspace([{ id: "pane", tabs: [terminal()] }])],
      activeWorkspaceId: "ws",
    };
    const merged = mergeExitedTabs(staleRemote, current);
    expect(merged[0].panes[0].tabs[0].exited).toBe(true);

    const cleanRemote: PersistedLayout = { ...staleRemote, workspaces: [workspace([{ id: "pane", tabs: [terminal()] }])] };
    expect(mergeExitedTabs(cleanRemote, [workspace([{ id: "pane", tabs: [terminal()] }])])).toBe(
      cleanRemote.workspaces,
    );
  });

  it("after serializeLayout → onRemoteChange, closing the exited tab kills instead of detaching", async () => {
    const exitedTab = { ...terminal(), exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    const dto = serializeLayout(useUi.getState());
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    const after = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(after.exited).toBe(true);

    await requestCloseTab(exitedTab.id);

    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(toastText()).not.toContain("已转入后台");
  });

  it("workspace close after a remote change still cleans up the exited backend tab", async () => {
    const exitedTab = { ...terminal("x1"), exited: true };
    seed([
      { id: "p1", tabs: [terminal("t1")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const dto = serializeLayout(useUi.getState());
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("unsplit after a remote change still cleans up the exited backend tab", async () => {
    const exitedTab = { ...terminal("x1"), exited: true };
    seed([
      { id: "p1", tabs: [terminal("keep")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const dto = serializeLayout(useUi.getState());
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", "kill");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });

  it("does not stamp exited onto a reconnected tab with a new backend tabId", async () => {
    const exitedTab = { ...terminal("shared"), tabId: "kernel-old", exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    const remoteDto: PersistedLayout = {
      v: 1,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [
        workspace([{ id: "pane", tabs: [{ ...terminal("shared"), tabId: "kernel-new" }] }]),
      ],
      activeWorkspaceId: "ws",
    };
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    const after = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(after.tabId).toBe("kernel-new");
    expect(after.exited).not.toBe(true);

    await requestCloseTab("shared");

    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-new", "detach");
    expect(toastText()).toContain("已转入后台");
  });

  it("workspace close after a reconnect detaches the new process instead of cleanup kill", async () => {
    const exitedTab = { ...terminal("shared"), tabId: "kernel-old", exited: true };
    seed([
      { id: "p1", tabs: [terminal("t1")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const remoteDto: PersistedLayout = {
      v: 1,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [
        workspace([
          { id: "p1", tabs: [terminal("t1")] },
          { id: "p2", tabs: [{ ...terminal("shared"), tabId: "kernel-new" }] },
        ]),
      ],
      activeWorkspaceId: "ws",
    };
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-new", "detach");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-new", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("unsplit after a reconnect detaches the new process instead of cleanup kill", async () => {
    const exitedTab = { ...terminal("shared"), tabId: "kernel-old", exited: true };
    seed([
      { id: "p1", tabs: [terminal("keep")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const remoteDto: PersistedLayout = {
      v: 1,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [
        workspace([
          { id: "p1", tabs: [terminal("keep")] },
          { id: "p2", tabs: [{ ...terminal("shared"), tabId: "kernel-new" }] },
        ]),
      ],
      activeWorkspaceId: "ws",
    };
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange({ revision: 1 });
    await flushAsync();

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-new", "detach");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-new", "kill");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });
});
