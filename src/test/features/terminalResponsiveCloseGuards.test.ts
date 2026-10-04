/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import {
  requestCloseTab,
  requestKillTab,
  requestKillWorkspaceTerminals,
  useUi,
} from "../../app/store";

function terminal(id = "terminal"): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}
function execTerminal(id = "exec"): AppTab {
  return { ...terminal(id), containerId: "container-1" };
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
function seed(panes: Pane[]) {
  useUi.setState({
    workspaces: [ws(panes)],
    activeWorkspaceId: "ws",
    toasts: [],
  });
}
function toastText(): string {
  return useUi.getState().toasts.map((t) => t.text).join("\n");
}

describe("close = detach (M111)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("detaches a running terminal on close without any dialog", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).toContain("已转入后台");
  });

  it("closes an exited terminal directly without a dialog", async () => {
    const tab = { ...terminal(), exited: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
  });

  it("closes a dead terminal without any IPC", async () => {
    const tab: AppTab = { id: "dead", kind: "terminal", title: "dead", sessionId: "s", closable: true, dead: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
  });

  it("keeps the tab and reports when the detach IPC fails", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));

    await requestCloseTab(tab.id);

    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
    expect(toastText()).toContain("终端操作失败");
  });

  it("asks before closing a docker exec tab and kills on confirm", async () => {
    const tab = execTerminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.ask.mockResolvedValueOnce(true);

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("不支持转入后台");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-exec", "kill");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
  });

  it("keeps the docker exec tab when the close confirmation is cancelled", async () => {
    const tab = execTerminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
  });

  it("detaches running terminals when closing a workspace without asking", async () => {
    const tabs = [terminal("t1"), terminal("t2")];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "detach");
    expect(useUi.getState().workspaces).toEqual([]);
    expect(toastText()).toContain("2 个终端已转入后台");
  });

  it("kills docker exec tabs during workspace close instead of detaching", async () => {
    const tabs = [terminal("t1"), execTerminal("e1")];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(toastText()).toContain("容器 exec 终端已结束");
  });

  it("detaches the pane's running terminals on unsplit", async () => {
    const panes = [
      { id: "p1", tabs: [terminal("keep")], activeTabId: "keep" },
      { id: "p2", tabs: [terminal("gone")], activeTabId: "gone" },
    ];
    seed(panes);

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-gone", "detach");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });
});

describe("kill danger actions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("requestKillTab confirms once then kills the process", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestKillTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0];
    expect(message).toContain("结束");
    expect(options.kind).toBe("warning");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).toContain("已结束");
  });

  it("requestKillTab cancel keeps the tab and the process", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.ask.mockResolvedValueOnce(false);

    await requestKillTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
  });

  it("requestKillWorkspaceTerminals confirms with the count and keeps the workspace", async () => {
    const tabs = [terminal("t1"), terminal("t2"), { ...terminal("t3"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);

    await requestKillWorkspaceTerminals("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个终端");
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill");
    const remaining = useUi.getState().workspaces[0].panes[0].tabs;
    expect(remaining).toHaveLength(3);
    expect(remaining.every((t) => t.exited === true)).toBe(true);
  });

  it("requestKillWorkspaceTerminals with nothing running only toasts", async () => {
    const tab = { ...terminal(), exited: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestKillWorkspaceTerminals("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(toastText()).toContain("没有正在运行的终端");
  });
});
