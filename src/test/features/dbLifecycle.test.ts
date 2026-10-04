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

function dbTab(id: string, connId: string, kind: "mysql" | "redis"): AppTab {
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

describe("数据库标签关闭", () => {
  it("MySQL 标签关闭先 db_disconnect 再移除 UI", async () => {
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

  it("Redis 标签关闭同样真实断开", async () => {
    const tab = dbTab("db-redis", "conn-redis", "redis");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledWith("conn-redis");
    expect(tabExists(tab.id)).toBe(false);
  });

  it("断开失败时 toast 真实错误并保留标签，重试成功后关闭", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    mocks.disconnect.mockRejectedValueOnce({
      code: "internal",
      message: "内部错误: 关闭数据库连接: driver: bad conn",
    });

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(false);

    expect(tabExists(tab.id)).toBe(true);
    expect(toastText()).toContain("数据库断开失败：内部错误: 关闭数据库连接: driver: bad conn");

    await expect(useUi.getState().closeTab(tab.id)).resolves.toBe(true);

    expect(mocks.disconnect).toHaveBeenCalledTimes(2);
    expect(tabExists(tab.id)).toBe(false);
  });

  it("终端标签关闭不触碰 db_disconnect", async () => {
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

describe("工作区关闭", () => {
  it("MySQL 与 Redis 标签随工作区关闭全部断开", async () => {
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

  it("部分断开失败时 toast 计数并保留工作区，重试后关闭", async () => {
    const mysql = dbTab("db-mysql", "conn-mysql", "mysql");
    const redis = dbTab("db-redis", "conn-redis", "redis");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [mysql, redis], activeTabId: mysql.id }])],
      activeWorkspaceId: "ws",
    });
    mocks.disconnect.mockRejectedValueOnce(new Error("连接重置"));

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(toastText()).toContain("1/2 个数据库断开失败：连接重置");

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.disconnect).toHaveBeenCalledTimes(4);
    expect(workspaceExists("ws")).toBe(false);
  });

  it("纯终端工作区关闭不触碰 db_disconnect", async () => {
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

describe("取消分屏", () => {
  it("被移除面板里的数据库标签随取消分屏断开", async () => {
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

  it("断开失败时保留面板并 toast", async () => {
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
    mocks.disconnect.mockRejectedValueOnce(new Error("连接重置"));

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(tabExists(db.id)).toBe(true);
    expect(toastText()).toContain("1/1 个数据库断开失败：连接重置");
    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
  });
});
