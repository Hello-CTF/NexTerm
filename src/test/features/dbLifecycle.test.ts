/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  disconnect: vi.fn(),
  closeTerminalTab: vi.fn(),
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: mocks.disconnect },
  sessionApi: {},
  terminalApi: { closeTab: mocks.closeTerminalTab },
  vaultApi: {},
}));

import { useUi, type AppTab, type Pane, type Workspace } from "../../app/store";

function dbTab(id: string, connId: string, kind: "mysql" | "postgres" | "redis"): AppTab {
  return { id, kind: "db", title: id, connId, dbKind: kind, closable: true };
}

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function toastText(): string {
  return useUi.getState().toasts.map((t) => t.text).join("\n");
}

function tabExists(id: string): boolean {
  return useUi
    .getState()
    .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.id === id)));
}

function workspaceExists(id: string): boolean {
  return useUi.getState().workspaces.some((w) => w.id === id);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.closeTerminalTab.mockResolvedValue(undefined);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], toasts: [] });
});

describe("Database tab closing", () => {
  it("closing a MySQL tab calls db_disconnect before removing the UI", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
    expect(mocks.disconnect).toHaveBeenCalledWith("conn-mysql");
    expect(tabExists(tab.id)).toBe(false);
  });

  it("closing a Redis tab also disconnects for real", async () => {
    const tab = dbTab("db-redis", "conn-redis", "redis");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledWith("conn-redis");
    expect(tabExists(tab.id)).toBe(false);
  });

  it("closing a PostgreSQL tab also disconnects for real", async () => {
    const tab = dbTab("db-pg", "conn-pg", "postgres");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledWith("conn-pg");
    expect(tabExists(tab.id)).toBe(false);
  });

  it("shows the real error in a toast and keeps the tab when disconnecting fails, then closes it after a successful retry", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    mocks.disconnect.mockRejectedValueOnce({
      code: "internal",
      message: "Internal error: failed to close database connection: driver: bad conn",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(false);

    expect(tabExists(tab.id)).toBe(true);
    expect(toastText()).toContain("数据库断开失败：内部错误：Internal error: failed to close database connection: driver: bad conn");

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledTimes(2);
    expect(tabExists(tab.id)).toBe(false);
  });

  it("closing a terminal tab does not call db_disconnect", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [{ id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    });

    await expect(useUi.getState().closeTab(tab.id, "detach")).resolves.toBe(true);

    expect(mocks.closeTerminalTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(mocks.disconnect).not.toHaveBeenCalled();
  });
});

describe("Workspace closing", () => {
  it("closing a workspace disconnects all MySQL and Redis tabs", async () => {
    const mysql = dbTab("db-mysql", "conn-mysql", "mysql");
    const redis = dbTab("db-redis", "conn-redis", "redis");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [mysql, redis], activeTabId: mysql.id }])],
      activeWorkspaceId: "ws",
    });

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.disconnect).toHaveBeenCalledTimes(2);
    expect(mocks.disconnect).toHaveBeenCalledWith("conn-mysql");
    expect(mocks.disconnect).toHaveBeenCalledWith("conn-redis");
    expect(workspaceExists("ws")).toBe(false);
  });

  it("shows the failure count in a toast and keeps the workspace when some disconnects fail, then closes it after a retry", async () => {
    const mysql = dbTab("db-mysql", "conn-mysql", "mysql");
    const redis = dbTab("db-redis", "conn-redis", "redis");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [mysql, redis], activeTabId: mysql.id }])],
      activeWorkspaceId: "ws",
    });
    mocks.disconnect.mockRejectedValueOnce(new Error("Connection reset"));

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(toastText()).toContain("1/2 个数据库断开失败：Connection reset");

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.disconnect).toHaveBeenCalledTimes(4);
    expect(workspaceExists("ws")).toBe(false);
  });

  it("closing a terminal-only workspace does not call db_disconnect", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
      sessions: [{ id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    });

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.closeTerminalTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(mocks.disconnect).not.toHaveBeenCalled();
    expect(workspaceExists("ws")).toBe(false);
  });
});

describe("Unsplitting", () => {
  it("disconnects database tabs in the removed pane when unsplitting", async () => {
    const db = dbTab("db-mysql", "conn-mysql", "mysql");
    const term = terminal();
    useUi.setState({
      workspaces: [
        ws([
          { id: "p1", tabs: [term], activeTabId: term.id },
          { id: "p2", tabs: [db], activeTabId: db.id },
        ]),
      ],
      activeWorkspaceId: "ws",
      sessions: [{ id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    });

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.disconnect).toHaveBeenCalledWith("conn-mysql");
    expect(tabExists(db.id)).toBe(false);
  });

  it("keeps the pane and shows a toast when disconnecting fails", async () => {
    const db = dbTab("db-mysql", "conn-mysql", "mysql");
    const term = terminal();
    useUi.setState({
      workspaces: [
        ws([
          { id: "p1", tabs: [term], activeTabId: term.id },
          { id: "p2", tabs: [db], activeTabId: db.id },
        ]),
      ],
      activeWorkspaceId: "ws",
      sessions: [{ id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    });
    mocks.disconnect.mockRejectedValueOnce(new Error("Connection reset"));

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(tabExists(db.id)).toBe(true);
    expect(toastText()).toContain("1/1 个数据库断开失败：Connection reset");
    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
  });
});
