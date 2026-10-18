/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
  dbDisconnect: vi.fn(),
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: mocks.dbDisconnect },
  sessionApi: {},
  terminalApi: { closeTab: mocks.closeTab },
  vaultApi: {},
}));

import {
  requestCloseTab,
  requestCloseTabs,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../app/store";
import { setTerminalCloseAction } from "../app/preferences";
import type { SessionInfo } from "../ipc/commands";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true, retainable: true };
}

function sessionInfo(): SessionInfo {
  return { id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId: "s",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function tabById(id: string): AppTab | undefined {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes)
    .flatMap((p) => p.tabs)
    .find((t) => t.id === id);
}

beforeEach(() => {
  vi.clearAllMocks();
  setTerminalCloseAction("detach");
  mocks.ask.mockResolvedValue(true);
  mocks.dbDisconnect.mockResolvedValue(undefined);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [sessionInfo()], toasts: [] });
});

describe("partial failures during batch close", () => {
  it("closes successful tabs, retains failures, and marks their reasons", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2"), terminal("t3")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockImplementation((tabId: string) =>
      tabId === "kernel-t2" ? Promise.reject({ code: "not_found", message: "tab not found" }) : Promise.resolve(),
    );

    await requestCloseTabs(["t1", "t2", "t3"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t3")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBe("资源不存在：tab not found");
    const texts = useUi.getState().toasts.map((t) => t.text);
    expect(texts.some((t) => t.includes("终端操作失败：资源不存在：tab not found"))).toBe(true);
    expect(texts).toContain("1 个标签未能关闭，已保留并在标签上标出原因");
  });

  it("does not count canceled confirmations as failures or include them in the summary", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
      sessions: [{ ...sessionInfo(), kind: "winrm" }],
    });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTabs(["t1", "t2"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBeUndefined();
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });

  it("excludes a tab with an old failure marker when this round is canceled", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), { ...terminal("t2"), closeError: "previous failure" }], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
      sessions: [{ ...sessionInfo(), kind: "winrm" }],
    });
    mocks.ask.mockResolvedValueOnce(true).mockResolvedValueOnce(false);
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTabs(["t1", "t2"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBeUndefined();
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });

  it("marks a single-tab close failure and removes the tab after a successful retry", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));

    await requestCloseTab("t1");

    expect(tabById("t1")?.closeError).toBe("boom");
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );

    mocks.closeTab.mockResolvedValueOnce(undefined);
    await requestCloseTab("t1");
    expect(tabById("t1")).toBeUndefined();
  });

  it("retains a database tab and marks the reason when disconnection fails", async () => {
    const dbTab: AppTab = { id: "db1", kind: "db", title: "SQL", connId: "c1", dbKind: "mysql", closable: true };
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), dbTab], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.dbDisconnect.mockRejectedValueOnce({ code: "io", message: "connection reset" });

    await requestCloseTabs(["t1", "db1"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("db1")?.closeError).toBe("IO 错误：connection reset");
    expect(useUi.getState().toasts.map((t) => t.text)).toContain(
      "1 个标签未能关闭，已保留并在标签上标出原因",
    );
  });

  it("closes all terminals after one direct-close batch choice", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    setTerminalCloseAction("ask");
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });
    await requestCloseTabs(["t1", "t2"]);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill", "s");
    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t2")).toBeUndefined();
    expect(useUi.getState().toasts.map((t) => t.text)).not.toContain(
      "2 个标签未能关闭，已保留并在标签上标出原因",
    );
  });
});
