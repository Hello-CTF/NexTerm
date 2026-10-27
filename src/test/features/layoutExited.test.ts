/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushMacrotask } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  closeTab: vi.fn(),
  layoutGet: vi.fn(),
  layoutPut: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  listSessions: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    layoutApi: { get: mocks.layoutGet, put: mocks.layoutPut },
    terminalApi: { closeTab: mocks.closeTab, listLive: mocks.listLive, listDurable: mocks.listDurable },
    sessionApi: { list: mocks.listSessions },
  });
});
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import {
  flushLayout,
  mergeExitedTabs,
  collectRemovedTerminals,
  notifyBackgroundedTerminals,
  onRemoteChange,
  resetLayoutSyncForTest,
  serializeLayout,
  type PersistedLayout,
} from "../../app/layout";
import { setTerminalCloseAction } from "../../app/preferences";
import { requestCloseTab, useUi, type AppTab, type Workspace } from "../../app/store";
import { isTabUnread, resetUnreadTabs, setTabUnread } from "../../app/unreadStore";

function terminal(id = "terminal"): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true, retainable: true };
}
function liveTab(tabId: string, exited = false) {
  return {
    tabId,
    sessionId: "s",
    sessionName: "session",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    sizeOwner: null,
    subscribers: 1,
    viewers: 1,
    exited,
    lastOutputMsAgo: 0,
  };
}
function workspace(panes: { id: string; tabs: AppTab[] }[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    assetKind: "ssh",
    panes: panes.map((p) => ({ ...p, activeTabId: p.tabs[0]?.id ?? null })),
    activePaneId: panes[0].id,
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
  await flushMacrotask();
  await flushMacrotask();
}
function applyAttachedCapabilities() {
  const st = useUi.getState();
  for (const tab of st.workspaces.flatMap((w) => w.panes).flatMap((p) => p.tabs)) {
    if (tab.kind === "terminal") st.updateTab(tab.id, { retainable: true });
  }
}

describe("remote layout preserves exited terminal state", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetUnreadTabs();
    setTerminalCloseAction("detach");
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listSessions.mockResolvedValue([]);
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

  it("serializeLayout drops runtime shell, background capability, and pending directory state", () => {
    seed([
      {
        id: "pane",
        tabs: [
          {
            ...terminal(),
            shell: "powershell",
            pendingDirectory: "D:/Project [x]/child's dir",
          },
        ],
      },
    ]);

    const dto = serializeLayout(useUi.getState());
    const persistedTab = dto?.workspaces[0]?.panes[0]?.tabs[0];
    expect(persistedTab).not.toHaveProperty("shell");
    expect(persistedTab).not.toHaveProperty("retainable");
    expect(persistedTab).not.toHaveProperty("pendingDirectory");
  });

  it.each([true, false, undefined])("reports remotely closed terminals according to observed background capability: %s", async (retainable) => {
    seed([{ id: "pane", tabs: [{ ...terminal("removed"), retainable }] }]);
    const next = serializeLayout(useUi.getState());
    if (!next) throw new Error("Missing persisted layout");
    next.workspaces[0].panes[0].tabs = [];
    const removed = collectRemovedTerminals(next);
    mocks.listLive.mockResolvedValue([liveTab("kernel-removed")]);

    await notifyBackgroundedTerminals(removed);

    expect(toastText()).toContain("标签已被其他设备关闭");
    expect(toastText().includes("可在「后台会话」里接管")).toBe(retainable === true);
  });

  it("mergeExitedTabs keeps local exited over a stale remote copy and leaves clean layouts untouched", () => {
    const exitedTab = { ...terminal(), exited: true };
    const current = [workspace([{ id: "pane", tabs: [exitedTab] }])];
    const staleRemote: PersistedLayout = {
      v: 4,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [workspace([{ id: "pane", tabs: [terminal()] }])],
      activeWorkspaceId: "ws",
      aiBoards: {},
    };
    const merged = mergeExitedTabs(staleRemote, current);
    expect(merged[0].panes[0].tabs[0].exited).toBe(true);

    const cleanRemote: PersistedLayout = { ...staleRemote, workspaces: [workspace([{ id: "pane", tabs: [terminal()] }])] };
    expect(mergeExitedTabs(cleanRemote, [workspace([{ id: "pane", tabs: [terminal()] }])])).toBe(
      cleanRemote.workspaces,
    );
  });

  it("after serializeLayout → onRemoteChange, a stale exited flag detaches when the current tab is live", async () => {
    const exitedTab = { ...terminal(), exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    const dto = serializeLayout(useUi.getState());
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();

    const after = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(after.exited).toBe(true);
    mocks.listLive.mockResolvedValue([liveTab("kernel-terminal")]);

    await requestCloseTab(exitedTab.id);

    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach", "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-terminal", "kill", "s");
    expect(toastText()).toContain("已转入后台");
  });

  it("close paths resync the session list from the backend and apply the result", async () => {
    const refreshed = [
      {
        id: "s-refreshed",
        assetId: "a1",
        name: "web-01",
        kind: "ssh",
        status: "connected",
        tabs: [],
        createdAt: 0,
        staleConfig: false,
      },
    ];
    mocks.listSessions.mockResolvedValue(refreshed);
    const exitedTab = { ...terminal(), exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    applyAttachedCapabilities();
    mocks.listLive.mockResolvedValue([liveTab("kernel-terminal")]);

    await requestCloseTab(exitedTab.id);
    await flushAsync();

    expect(mocks.listSessions).toHaveBeenCalled();
    expect(useUi.getState().sessions).toEqual(refreshed);
    expect(useUi.getState().sessionResyncCompleted).toBe(true);
    expect(useUi.getState().sessionResyncFailed).toBe(false);
  });

  it("after serializeLayout → onRemoteChange, a current durable-dead tab uses default cleanup", async () => {
    const exitedTab = { ...terminal(), exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    const dto = serializeLayout(useUi.getState());
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();

    expect(useUi.getState().workspaces[0].panes[0].tabs[0].exited).toBe(true);
    mocks.listDurable.mockResolvedValue([{ id: "kernel-terminal", dead: true }]);

    await requestCloseTab(exitedTab.id);

    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", undefined, "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-terminal", "kill", "s");
  });

  it.each([false, true])("workspace close after a remote change handles current exited=%s safely", async (currentExited) => {
    const exitedTab = { ...terminal("x1"), exited: true };
    seed([
      { id: "p1", tabs: [terminal("t1")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const dto = serializeLayout(useUi.getState());
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();
    mocks.listLive.mockResolvedValue([
      liveTab("kernel-t1"),
      liveTab("kernel-x1", currentExited),
    ]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", currentExited ? undefined : "detach", "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-x1", "kill", "s");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it.each([false, true])("closing a pane after a remote change keeps the exited=%s terminal without close IPC", async (currentExited) => {
    const exitedTab = { ...terminal("x1"), exited: true };
    seed([
      { id: "p1", tabs: [terminal("keep")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const dto = serializeLayout(useUi.getState());
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: dto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();
    mocks.listLive.mockResolvedValue([liveTab("kernel-x1", currentExited)]);

    useUi.getState().closePane("p2", "ws");

    // Closing a pane keeps its tabs: the exited terminal moves to the remaining pane untouched.
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
    expect(useUi.getState().workspaces[0].panes[0].tabs.find((tab) => tab.id === "x1")?.tabId).toBe("kernel-x1");
  });

  it("does not stamp exited onto a reconnected tab with a new backend tabId", async () => {
    const exitedTab = { ...terminal("shared"), tabId: "kernel-old", exited: true };
    seed([{ id: "pane", tabs: [exitedTab] }]);
    const remoteDto: PersistedLayout = {
      v: 4,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [
        workspace([{ id: "pane", tabs: [{ ...terminal("shared"), tabId: "kernel-new" }] }]),
      ],
      activeWorkspaceId: "ws",
      aiBoards: {},
    };
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();

    const after = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(after.tabId).toBe("kernel-new");
    expect(after.exited).not.toBe(true);

    mocks.listLive.mockResolvedValue([{ tabId: "kernel-new", exited: false }]);
    await requestCloseTab("shared");

    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-new", "detach", "s");
    expect(toastText()).toContain("已转入后台");
  });

  it("workspace close after a reconnect detaches the new process instead of cleanup kill", async () => {
    const exitedTab = { ...terminal("shared"), tabId: "kernel-old", exited: true };
    seed([
      { id: "p1", tabs: [terminal("t1")] },
      { id: "p2", tabs: [exitedTab] },
    ]);
    const remoteDto: PersistedLayout = {
      v: 4,
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
      aiBoards: {},
    };
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange(null);
    await flushAsync();
    applyAttachedCapabilities();

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-new", "detach", "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-new", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("keeps a new terminal attaching when its saved layout is echoed before helper installation finishes", async () => {
    const pending: AppTab = {
      ...terminal("installing"),
      tabId: undefined,
      retainable: undefined,
      pendingDirectory: "/tmp/project",
    };
    seed([{ id: "pane", tabs: [pending] }]);
    const persisted = serializeLayout(useUi.getState());
    expect(persisted?.workspaces[0].panes[0].tabs[0].restorePending).toBe(true);
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: persisted });

    onRemoteChange(null);
    await flushAsync();

    const current = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(current.restorePending).not.toBe(true);
    expect(current.tabId).toBeUndefined();
    expect(current.pendingDirectory).toBe("/tmp/project");
    expect(mocks.closeTab).not.toHaveBeenCalled();

    useUi.getState().updateTab("installing", { tabId: "kernel-installed", retainable: true });
    const attached = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(attached.tabId).toBe("kernel-installed");
    expect(attached.restorePending).not.toBe(true);
  });

  it("keeps cold-restored terminals pending instead of manufacturing an in-flight attachment", async () => {
    seed([{ id: "pane", tabs: [{ ...terminal("restored"), tabId: undefined, restorePending: true }] }]);
    const persisted = serializeLayout(useUi.getState());
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: persisted });

    onRemoteChange(null);
    await flushAsync();

    const current = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(current.restorePending).toBe(true);
    expect(current.tabId).toBeUndefined();
  });

  it.each([
    { sessionId: "other-session", containerId: undefined },
    { sessionId: "s", containerId: "other-container" },
  ])("does not transfer an in-flight attachment to a changed target: %j", async (target) => {
    seed([{ id: "pane", tabs: [{ ...terminal("installing"), tabId: undefined, pendingDirectory: "/tmp/project" }] }]);
    const persisted = serializeLayout(useUi.getState());
    if (!persisted) throw new Error("Missing persisted layout");
    Object.assign(persisted.workspaces[0].panes[0].tabs[0], target);
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: persisted });

    onRemoteChange(null);
    await flushAsync();

    const current = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(current.restorePending).toBe(true);
    expect(current.pendingDirectory).toBeUndefined();
  });

  it("preserves runtime only for the same backend tab across remote layout moves", async () => {
    const attached: AppTab = {
      ...terminal("attached"),
      shell: "posix",
      pendingDirectory: "/tmp/project",
      closeError: "close failed",
    };
    const removed: AppTab = {
      ...terminal("removed"),
      shell: "powershell",
      pendingDirectory: "D:/removed",
    };
    const reconnected: AppTab = {
      ...terminal("shared"),
      tabId: "kernel-old",
      shell: "posix",
      pendingDirectory: "/tmp/old",
    };
    seed([
      { id: "p1", tabs: [removed, attached] },
      { id: "p2", tabs: [reconnected] },
    ]);
    setTabUnread("attached", true);
    setTabUnread("removed", true);
    setTabUnread("shared", true);
    const moved = {
      ...workspace([
        {
          id: "moved",
          tabs: [
            terminal("restored"),
            terminal("attached"),
            { ...terminal("shared"), tabId: "kernel-new" },
          ],
        },
      ]),
      id: "moved-ws",
    };
    const remoteDto: PersistedLayout = {
      v: 4,
      leftOpen: true,
      leftMode: "assets",
      rightOpen: true,
      leftWidth: 248,
      rightWidth: 352,
      workspaces: [moved],
      activeWorkspaceId: "moved-ws",
      aiBoards: {},
    };
    await flushLayout();
    mocks.layoutGet.mockResolvedValue({ revision: 1, updatedAt: 0, data: remoteDto });

    onRemoteChange(null);
    await flushAsync();

    const state = useUi.getState();
    expect(state.workspaces.map((w) => w.id)).toEqual(["moved-ws"]);
    expect(state.activeWorkspaceId).toBe("moved-ws");
    const tabs = state.workspaces[0].panes[0].tabs;
    expect(tabs.map((tab) => tab.id)).toEqual(["restored", "attached", "shared"]);
    expect(tabs[1]).toMatchObject({
      tabId: "kernel-attached",
      shell: "posix",
      retainable: true,
      pendingDirectory: "/tmp/project",
      closeError: "close failed",
    });
    expect(isTabUnread("attached")).toBe(true);
    expect(isTabUnread("removed")).toBe(false);
    expect(isTabUnread("shared")).toBe(false);
    expect(isTabUnread("restored")).toBe(false);
    for (const restored of [tabs[0], tabs[2]]) {
      expect(restored).not.toHaveProperty("shell");
      expect(restored).not.toHaveProperty("retainable");
      expect(restored).not.toHaveProperty("pendingDirectory");
      expect(restored).not.toHaveProperty("closeError");
    }
    expect(tabs[2].tabId).toBe("kernel-new");
  });
});
