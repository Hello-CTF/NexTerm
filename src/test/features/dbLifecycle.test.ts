/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  disconnect: vi.fn(),
  closeTerminalTab: vi.fn(),
  sessionList: vi.fn().mockResolvedValue([]),
}));
vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: mocks.disconnect },
  sessionApi: { list: mocks.sessionList },
  terminalApi: { closeTab: mocks.closeTerminalTab },
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { GLOBAL_AI_BOARD_KEY, sanitizeWorkspaceProblemText, useUi, type AppTab, type Pane, type SqlDraft, type Workspace } from "../../app/store";
import { workspaceProblemCopyText } from "../../features/problems/WorkspaceProblems";
import { aiTabBusyIds, aiTabRunControls, registerAiRunGeneration } from "../../features/ai/runOwnership";
import { setFileEditorDirty } from "../../features/files/editorGuards";
import { setTerminalCloseAction } from "../../app/preferences";

function dbTab(id: string, connId: string, kind: "mysql" | "postgres" | "redis"): AppTab {
  return { id, kind: "db", title: id, connId, dbKind: kind, closable: true };
}

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true, retainable: true };
}

function ws(panes: Pane[], id = "ws"): Workspace {
  return {
    id,
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function sqlDraft(text = "SELECT 1"): SqlDraft {
  return {
    text,
    selection: { from: 0, to: 8 },
    schema: "public",
    connId: "conn-mysql",
    dbKind: "mysql",
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
  mocks.ask.mockResolvedValue(true);
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.closeTerminalTab.mockResolvedValue(undefined);
  setTerminalCloseAction("detach");
  aiTabBusyIds.clear();
  aiTabRunControls.clear();
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [], aiBoards: {}, workspaceRuntime: {}, toasts: [] });
});

afterEach(() => {
  setFileEditorDirty("s", "/dirty.txt", false);
  aiTabBusyIds.clear();
  aiTabRunControls.clear();
  vi.useRealTimers();
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

    expect(mocks.closeTerminalTab).toHaveBeenCalledWith("kernel-terminal", "detach", "s");
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

    expect(mocks.disconnect).toHaveBeenCalledTimes(3);
    expect(mocks.disconnect.mock.calls[2][0]).toBe("conn-mysql");
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

    expect(mocks.closeTerminalTab).toHaveBeenCalledWith("kernel-terminal", "detach", "s");
    expect(mocks.disconnect).not.toHaveBeenCalled();
    expect(workspaceExists("ws")).toBe(false);
  });

  it("closing a DB tab keeps the workspace draft", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    const generation = useUi.getState().getWorkspaceRuntime("ws")?.generation;

    await useUi.getState().closeTab(tab.id);

    expect(useUi.getState().getWorkspaceRuntime("ws")).toEqual({ generation, sqlDraft: sqlDraft() });
    expect(workspaceExists("ws")).toBe(true);
  });

  it("cleans runtime and AI board only after a workspace closes successfully", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    useUi.getState().ensureAiBoard("ws");

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(false);
    expect(useUi.getState().getWorkspaceRuntime("ws")).toBeUndefined();
    expect(useUi.getState().aiBoards["ws"]).toBeUndefined();
  });

  it("keeps runtime and AI board when disconnecting fails", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    useUi.getState().ensureAiBoard("ws");
    mocks.disconnect.mockRejectedValueOnce(new Error("Connection reset"));

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("keeps runtime and AI board when the close confirmation is canceled", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    useUi.getState().ensureAiBoard("ws");
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.disconnect).not.toHaveBeenCalled();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("SQL 草稿");
    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("uses the latest state so a workspace added during close is preserved", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));

    const closing = useUi.getState().closeWorkspace("ws");
    const added = ws([{ id: "p-new", tabs: [], activeTabId: null }], "ws-new");
    useUi.setState({
      workspaces: [...useUi.getState().workspaces, added],
      activeWorkspaceId: added.id,
    });
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(false);
    expect(workspaceExists(added.id)).toBe(true);
    expect(useUi.getState().activeWorkspaceId).toBe(added.id);
  });

  it("keeps the workspace when a dirty editor arrives during close", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));
    const dirty: AppTab = {
      id: "dirty",
      kind: "files",
      title: "dirty.txt",
      sessionId: "s",
      path: "/dirty.txt",
      closable: true,
    };

    const closing = useUi.getState().closeWorkspace("ws");
    useUi.setState({
      workspaces: useUi.getState().workspaces.map((workspace) =>
        workspace.id === "ws"
          ? {
              ...workspace,
              panes: workspace.panes.map((pane) => ({ ...pane, tabs: [...pane.tabs, dirty] })),
            }
          : workspace,
      ),
    });
    setFileEditorDirty("s", "/dirty.txt", true);
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(true);
    expect(toastText()).toContain("新增");
    expect(toastText()).toContain("未保存");
  });

  it("keeps a SQL draft that arrives during close", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));

    const closing = useUi.getState().closeWorkspace("ws");
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(toastText()).toContain("新增");
    expect(toastText()).toContain("未保存");
  });

  it("removes successful DB tabs while keeping failures and runtime state", async () => {
    const succeeded = dbTab("db-succeeded", "conn-succeeded", "mysql");
    const failed = dbTab("db-failed", "conn-failed", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [succeeded, failed], activeTabId: succeeded.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    useUi.getState().ensureAiBoard("ws");
    mocks.disconnect.mockImplementation(async (connId: string) => {
      if (connId === "conn-failed") throw new Error("Connection reset");
    });

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(tabExists(succeeded.id)).toBe(false);
    expect(tabExists(failed.id)).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("keeps a clean terminal added during close", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));
    const added = terminal("added");

    const closing = useUi.getState().closeWorkspace("ws");
    useUi.setState({
      workspaces: useUi.getState().workspaces.map((workspace) =>
        workspace.id === "ws"
          ? {
              ...workspace,
              panes: workspace.panes.map((pane) => ({ ...pane, tabs: [...pane.tabs, added] })),
            }
          : workspace,
      ),
    });
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(true);
    expect(tabExists(added.id)).toBe(true);
  });

  it("keeps a SQL draft edited again during close", async () => {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    const generation = useUi.getState().getWorkspaceRuntime("ws")?.generation;
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));

    const closing = useUi.getState().closeWorkspace("ws");
    useUi.getState().updateSqlDraft("ws", sqlDraft("SELECT 2"), generation);
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft?.text).toBe("SELECT 2");
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

  it("keeps the split when reclaiming a terminal fails", async () => {
    const keep = terminal("keep");
    const failed = terminal("failed");
    useUi.setState({
      workspaces: [
        ws([
          { id: "p1", tabs: [keep], activeTabId: keep.id },
          { id: "p2", tabs: [failed], activeTabId: failed.id },
        ]),
      ],
      activeWorkspaceId: "ws",
      sessions: [{ id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
    });
    mocks.closeTerminalTab.mockRejectedValueOnce(new Error("close failed"));

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(useUi.getState().workspaces[0].panes).toHaveLength(2);
    expect(tabExists(failed.id)).toBe(true);
    expect(toastText()).toContain("终端关闭失败");
  });
});

describe("Workspace AI close guards", () => {
  function runningTabs(count: number): { first: string; second?: string; third?: string } {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [], activeTabId: null }])],
      activeWorkspaceId: "ws",
      workspaceRuntime: {},
    });
    const first = useUi.getState().addAiTab("ws");
    const second = count > 1 ? useUi.getState().addAiTab("ws") : undefined;
    const third = count > 2 ? useUi.getState().addAiTab("ws") : undefined;
    useUi.getState().updateAiTab("ws", first, { title: "First" });
    if (second) useUi.getState().updateAiTab("ws", second, { title: "Second" });
    if (third) useUi.getState().updateAiTab("ws", third, { title: "Third" });
    aiTabBusyIds.add(first);
    if (second) aiTabBusyIds.add(second);
    if (third) aiTabBusyIds.add(third);
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    useUi.getState().ensureAiBoard("ws");
    return { first, second, third };
  }

  it("cancels every running AI tab before deleting the workspace", async () => {
    const { first, second } = runningTabs(2);
    const calls: string[] = [];
    aiTabRunControls.set(first, async () => {
      calls.push(first);
      return true;
    });
    aiTabRunControls.set(second!, async () => {
      calls.push(second!);
      return true;
    });

    await useUi.getState().closeWorkspace("ws");

    expect(calls).toEqual([first, second]);
    expect(mocks.ask.mock.calls[0][0]).toContain("First");
    expect(mocks.ask.mock.calls[0][0]).toContain("Second");
    expect(workspaceExists("ws")).toBe(false);
    expect(useUi.getState().getWorkspaceRuntime("ws")).toBeUndefined();
    expect(useUi.getState().aiBoards["ws"]).toBeUndefined();
  });

  it("keeps the workspace when the cancellation control is missing", async () => {
    runningTabs(1);

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
    expect(toastText()).toContain("运行状态不可用");
  });

  it("keeps the workspace when cancellation is rejected", async () => {
    const { first } = runningTabs(1);
    const cancel = vi.fn().mockRejectedValue(new Error("offline"));
    aiTabRunControls.set(first, cancel);

    await useUi.getState().closeWorkspace("ws");

    expect(cancel).toHaveBeenCalledTimes(1);
    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("keeps the workspace when cancellation times out", async () => {
    vi.useFakeTimers();
    const { first } = runningTabs(1);
    const cancel = vi.fn().mockImplementation(() => new Promise<boolean>(() => undefined));
    aiTabRunControls.set(first, cancel);
    const closing = useUi.getState().closeWorkspace("ws");

    await vi.waitFor(() => expect(cancel).toHaveBeenCalled());
    await vi.advanceTimersByTimeAsync(5000);
    await closing;

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("waits for a pending cancellation instead of treating it as stopped", async () => {
    const { first } = runningTabs(1);
    let settle!: (value: boolean) => void;
    const cancel = vi.fn().mockImplementation(() => new Promise<boolean>((resolve) => {
      settle = resolve;
    }));
    aiTabRunControls.set(first, cancel);
    const closing = useUi.getState().closeWorkspace("ws");

    await vi.waitFor(() => expect(cancel).toHaveBeenCalled());
    expect(workspaceExists("ws")).toBe(true);
    settle(true);
    await closing;

    expect(workspaceExists("ws")).toBe(false);
  });

  it("stops after the first failed cancellation and skips later controls", async () => {
    const { first, second, third } = runningTabs(3);
    const firstResult = vi.fn().mockResolvedValue(true);
    const secondResult = vi.fn().mockResolvedValue(false);
    const thirdResult = vi.fn().mockResolvedValue(true);
    aiTabRunControls.set(first, firstResult);
    aiTabRunControls.set(second!, secondResult);
    aiTabRunControls.set(third!, thirdResult);

    await useUi.getState().closeWorkspace("ws");

    expect(firstResult).toHaveBeenCalledTimes(1);
    expect(secondResult).toHaveBeenCalledTimes(1);
    expect(thirdResult).not.toHaveBeenCalled();
    expect(workspaceExists("ws")).toBe(true);
  });

  it("does not cancel when only switching workspaces", () => {
    const { first } = runningTabs(1);
    const cancel = vi.fn();
    aiTabRunControls.set(first, cancel);
    const other = ws([{ id: "other", tabs: [], activeTabId: null }], "other");

    useUi.setState({
      workspaces: [...useUi.getState().workspaces, other],
      activeWorkspaceId: other.id,
    });

    expect(cancel).not.toHaveBeenCalled();
    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")?.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("does not issue terminal or DB IPC when AI cancellation fails", async () => {
    const { first } = runningTabs(1);
    aiTabRunControls.set(first, () => Promise.reject(new Error("offline")));
    const db = dbTab("db-1", "conn-1", "mysql");
    const term = terminal("term-1");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [db, term], activeTabId: db.id }])],
      activeWorkspaceId: "ws",
    });

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.disconnect).not.toHaveBeenCalled();
    expect(mocks.closeTerminalTab).not.toHaveBeenCalled();
    expect(workspaceExists("ws")).toBe(true);
  });

  it("keeps the workspace when another AI tab becomes busy before final deletion", async () => {
    const { first } = runningTabs(1);
    let added = "";
    aiTabRunControls.set(first, async () => {
      added = useUi.getState().addAiTab("ws");
      aiTabBusyIds.add(added);
      return true;
    });

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().aiBoards["ws"].tabs.some((tab) => tab.id === added)).toBe(true);
  });

  it("keeps the workspace when an AI tab is added during close", async () => {
    const { first } = runningTabs(1);
    let added = "";
    aiTabRunControls.set(first, async () => {
      added = useUi.getState().addAiTab("ws");
      return true;
    });

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().aiBoards["ws"].tabs.some((tab) => tab.id === added)).toBe(true);
  });

  it("keeps the workspace when an AI run restarts during close", async () => {
    const { first } = runningTabs(1);
    registerAiRunGeneration(first, 1);
    aiTabRunControls.set(first, async () => {
      registerAiRunGeneration(first, 2);
      return true;
    });

    await useUi.getState().closeWorkspace("ws");

    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("uses the tools workspace kind for the AI board key", async () => {
    const { first } = runningTabs(1);
    aiTabRunControls.set(first, async () => true);
    const toolsWorkspace = {
      ...ws([{ id: "p", tabs: [], activeTabId: null }], "ws-tools-1"),
      kind: "tools" as const,
    };
    useUi.setState({
      workspaces: [toolsWorkspace],
      activeWorkspaceId: toolsWorkspace.id,
      aiBoards: { [GLOBAL_AI_BOARD_KEY]: useUi.getState().aiBoards.ws },
    });

    await useUi.getState().closeWorkspace("ws-tools-1");

    expect(workspaceExists("ws-tools-1")).toBe(false);
    expect(useUi.getState().aiBoards["ws-tools-1"]).toBeUndefined();
  });

  it("keeps the workspace when a clean editor becomes dirty during close", async () => {
    const db = dbTab("db-1", "conn-1", "mysql");
    const file: AppTab = {
      id: "clean-file",
      kind: "files",
      title: "dirty.txt",
      sessionId: "s",
      path: "/dirty.txt",
      closable: true,
    };
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [db, file], activeTabId: file.id }])],
      activeWorkspaceId: "ws",
    });
    let release!: () => void;
    mocks.disconnect.mockReturnValue(new Promise<void>((resolve) => {
      release = resolve;
    }));
    const closing = useUi.getState().closeWorkspace("ws");
    setFileEditorDirty("s", "/dirty.txt", true);
    release();
    await closing;

    expect(workspaceExists("ws")).toBe(true);
  });
});

describe("Workspace problem runtime", () => {
  function prepareWorkspace(): number {
    const tab = dbTab("db-mysql", "conn-mysql", "mysql");
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
    useUi.getState().updateSqlDraft("ws", sqlDraft());
    return useUi.getState().getWorkspaceRuntime("ws")!.generation;
  }

  function input(generation: number, sequence: number) {
    return {
      generation,
      createdAt: sequence * 1000,
      source: "sql-query" as const,
      operation: "执行 SQL",
      target: {
        title: "workspace",
        identity: "conn-mysql",
        kind: "mysql",
        connId: "conn-mysql",
        schema: "public",
      },
      summary: `failure ${sequence}`,
      detail: `failure ${sequence}`,
      canRetryRead: false,
      sql: `SELECT ${sequence};`,
    };
  }

  it("keeps the newest 50, bounds details, and rejects a stale generation", () => {
    const generation = prepareWorkspace();
    for (let sequence = 1; sequence <= 51; sequence += 1) {
      useUi.getState().recordWorkspaceProblem("ws", input(generation, sequence));
    }

    const problems = useUi.getState().getWorkspaceRuntime("ws")!.problems!;
    expect(problems).toHaveLength(50);
    expect(problems[0].summary).toBe("failure 51");
    expect(problems[49].summary).toBe("failure 2");
    expect(problems[0]).toMatchObject({
      id: expect.any(Number),
      generation,
      createdAt: 51_000,
      operation: "执行 SQL",
      canRetryRead: false,
    });

    useUi.getState().recordWorkspaceProblem("ws", {
      ...input(generation, 52),
      detail: "😀".repeat(5000),
    });
    const bounded = useUi.getState().getWorkspaceRuntime("ws")!.problems![0];
    expect(new TextEncoder().encode(bounded.detail).length).toBeLessThanOrEqual(16 * 1024);
    expect(bounded.detailTruncated).toBe(true);
    expect(bounded.detail.endsWith("😀")).toBe(true);
    expect(bounded.detail).not.toContain("\uFFFD");

    useUi.setState({ workspaceRuntime: {} });
    const currentGeneration = prepareWorkspace();
    expect(currentGeneration).not.toBe(generation);
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 53));
    expect(useUi.getState().getWorkspaceRuntime("ws")!.problems ?? []).toEqual([]);
  });

  it("dismisses only the card and clears problems on session reset without touching SQL history", () => {
    const generation = prepareWorkspace();
    useUi.getState().recordSqlExecution("ws", {
      generation,
      target: { connId: "conn-mysql", dbKind: "mysql", title: "workspace" },
      sql: "SELECT 1;",
      schema: "public",
      startedAt: 1,
      endedAt: 2,
      durationMs: 1,
      rowsAffected: 1,
      error: null,
      outcome: "success",
    });
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 1));
    useUi.getState().ensureAiBoard("ws");
    const problemId = useUi.getState().getWorkspaceRuntime("ws")!.problems![0].id;

    useUi.getState().dismissWorkspaceProblem("ws", problemId, generation);

    expect(useUi.getState().getWorkspaceRuntime("ws")!.problems ?? []).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime("ws")!.sqlExecutionHistory).toHaveLength(1);
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 2));
    const runtimeEpoch = useUi.getState().workspaceRuntimeEpoch;

    useUi.getState().resetSessions();

    expect(useUi.getState().getWorkspaceRuntime("ws")!.problems ?? []).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime("ws")!.sqlDraft).toEqual(sqlDraft());
    expect(useUi.getState().getWorkspaceRuntime("ws")!.sqlExecutionHistory).toHaveLength(1);
    expect(useUi.getState().workspaceRuntimeEpoch).toBe(runtimeEpoch);
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("clears SQL and diagnostic runtime on account reset and rejects stale writes", () => {
    const generation = prepareWorkspace();
    const runtimeEpoch = useUi.getState().workspaceRuntimeEpoch;
    useUi.getState().recordSqlExecution("ws", {
      generation,
      target: { connId: "conn-mysql", dbKind: "mysql", title: "workspace" },
      sql: "SELECT 1;",
      schema: "public",
      startedAt: 1,
      endedAt: 2,
      durationMs: 1,
      rowsAffected: 1,
      error: null,
      outcome: "success",
    });
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 1));
    useUi.getState().ensureAiBoard("ws");

    useUi.getState().resetSessions({ clearWorkspaceRuntime: true });
    const nextGeneration = useUi.getState().getWorkspaceRuntime("ws")!.generation;
    useUi.getState().updateSqlDraft("ws", sqlDraft("SELECT stale"), generation);
    useUi.getState().recordSqlExecution("ws", {
      generation,
      target: { connId: "conn-mysql", dbKind: "mysql", title: "workspace" },
      sql: "SELECT stale;",
      schema: "public",
      startedAt: 3,
      endedAt: 4,
      durationMs: 1,
      rowsAffected: null,
      error: "late SQL failure",
      outcome: "error",
    });
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 2));

    expect(nextGeneration).not.toBe(generation);
    expect(useUi.getState().workspaceRuntimeEpoch).toBe(runtimeEpoch + 1);
    expect(useUi.getState().getWorkspaceRuntime("ws")).toStrictEqual({ generation: nextGeneration });
    expect(useUi.getState().aiBoards["ws"]).toBeDefined();
  });

  it("redacts quoted JSON, CLI, URL, Bearer, private-key, and dotenv secrets at both copy boundaries", () => {
    const fixture = [
      '{"password":"json-secret"}',
      "password='single secret'",
      "token=token-secret",
      "Bearer bearer-secret",
      "postgres://user:url-secret@localhost/db",
      '--password "first second"',
      "-p short-fixture",
      "cmd -pshort-attached",
      "-----BEGIN OPENSSH PRIVATE KEY-----\nprivate-secret\n-----END OPENSSH PRIVATE KEY-----",
      '.env\nORDINARY="first second"\nDOTENV=dotenv-secret',
    ].join("\n");
    const secrets = [
      "json-secret",
      "single secret",
      "token-secret",
      "bearer-secret",
      "url-secret",
      "first",
      "second",
      "short-fixture",
      "short-attached",
      "private-secret",
      "dotenv-secret",
    ];
    const sanitized = sanitizeWorkspaceProblemText(fixture);
    const copied = workspaceProblemCopyText({
      ...input(1, 1),
      id: 1,
      detail: fixture,
      detailTruncated: false,
      sqlTruncated: false,
    });

    for (const secret of secrets) {
      expect(sanitized).not.toContain(secret);
      expect(copied).not.toContain(secret);
    }
  });

  it("retains problems on close failure and clears them after a successful close", async () => {
    const generation = prepareWorkspace();
    useUi.getState().recordWorkspaceProblem("ws", input(generation, 1));
    mocks.disconnect.mockRejectedValueOnce(new Error("disconnect failed"));

    await useUi.getState().closeWorkspace("ws");
    expect(workspaceExists("ws")).toBe(true);
    expect(useUi.getState().getWorkspaceRuntime("ws")!.problems).toHaveLength(1);

    await useUi.getState().closeWorkspace("ws");
    expect(workspaceExists("ws")).toBe(false);
    expect(useUi.getState().getWorkspaceRuntime("ws")).toBeUndefined();
  });
});
