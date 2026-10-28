/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
  sessionList: vi.fn(),
  dbDisconnect: vi.fn(),
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember, askChoice: mocks.askChoice }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: { disconnect: mocks.dbDisconnect },
  sessionApi: { list: mocks.sessionList },
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
  mocks.sessionList.mockImplementation(async () => useUi.getState().sessions);
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [sessionInfo()], toasts: [] });
});

describe("partial failures during batch close", () => {
  it("closes successful tabs, retains failures, and marks their reasons", async () => {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [terminal("t1"), terminal("t2"), terminal("t3")], activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.closeTab.mockImplementation((tabId: string) =>
      tabId === "kernel-t2" ? Promise.reject({ code: "disconnected", message: "network unavailable" }) : Promise.resolve(),
    );

    await requestCloseTabs(["t1", "t2", "t3"]);

    expect(tabById("t1")).toBeUndefined();
    expect(tabById("t3")).toBeUndefined();
    expect(tabById("t2")?.closeError).toBe("已断开：network unavailable");
    const texts = useUi.getState().toasts.map((t) => t.text);
    expect(texts.some((t) => t.includes("终端操作失败：已断开：network unavailable"))).toBe(true);
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

describe("a regular SSH terminal can never go to the background", () => {
  const regular = (id: string): AppTab => ({
    id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true, durable: false, retainable: false,
  });

  function open(tab: AppTab) {
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });
  }

  it("offers only the close action and never sends detach, even when the remembered choice is background", async () => {
    setTerminalCloseAction("detach");
    open(regular("t1"));
    mocks.askChoice.mockResolvedValue("kill");
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTab("t1");

    const choices = (mocks.askChoice.mock.calls[0][1] as { choices: { key: string }[] }).choices;
    expect(choices.map((choice) => choice.key)).toEqual(["kill"]);
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
    expect(tabById("t1")).toBeUndefined();
  });

  it("treats a tab that reports itself as not retainable the same way before the durable flag arrives", async () => {
    setTerminalCloseAction("ask");
    open({ id: "t2", kind: "terminal", title: "t2", sessionId: "s", tabId: "kernel-t2", closable: true, retainable: false });
    mocks.askChoice.mockResolvedValue("kill");
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTab("t2");

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill", "s");
  });

  it("a durable tab keeps both choices and honours direct close", async () => {
    setTerminalCloseAction("ask");
    open({ id: "t3", kind: "terminal", title: "t3", sessionId: "s", tabId: "kernel-t3", closable: true, durable: true, retainable: true });
    mocks.askChoiceRemember.mockResolvedValue({ key: "kill", remember: false });
    mocks.closeTab.mockResolvedValue(undefined);

    await requestCloseTab("t3");

    const choices = (mocks.askChoiceRemember.mock.calls[0][1] as { choices: { key: string }[] }).choices;
    expect(choices.map((choice) => choice.key)).toEqual(["detach", "kill"]);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t3", "kill", "s");
  });
});
