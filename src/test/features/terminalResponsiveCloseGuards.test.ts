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
  closeActionHint,
  closeTabHint,
  requestCloseTab,
  requestKillTab,
  requestKillWorkspaceTerminals,
  useUi,
} from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
}
function execTerminal(id = "exec"): AppTab {
  return { ...terminal(id), containerId: "container-1" };
}
function sessionInfo(kind: string, id = "s"): SessionInfo {
  return {
    id,
    assetId: "a",
    name: "session",
    kind,
    status: "connected",
    tabs: [],
    createdAt: 0,
  };
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
function seed(panes: Pane[], sessionKind = "ssh") {
  useUi.setState({
    workspaces: [ws(panes)],
    activeWorkspaceId: "ws",
    sessions: [sessionInfo(sessionKind)],
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
    expect(toastText()).toContain("2 个终端转入后台");
  });

  it("confirms before killing docker exec tabs on workspace close, cancel keeps everything with zero IPC", async () => {
    const tabs = [terminal("t1"), execTerminal("e1")];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个");
    expect(message).toContain("容器 exec");
    expect(message).toContain("将结束进程");
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual(tabs);

    mocks.ask.mockResolvedValueOnce(true);
    await useUi.getState().closeWorkspace("ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
    expect(toastText()).toContain("1 个进程已结束");
  });

  it("confirms before killing docker exec tabs on unsplit, cancel keeps the split with zero IPC", async () => {
    const panes = [
      { id: "p1", tabs: [terminal("keep")], activeTabId: "keep" },
      { id: "p2", tabs: [terminal("gone"), execTerminal("e1")], activeTabId: "gone" },
    ];
    seed(panes);
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("容器 exec");
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes).toHaveLength(2);

    mocks.ask.mockResolvedValueOnce(true);
    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-gone", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
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

  it("workspace close cleans up exited backend tabs without asking", async () => {
    const tabs = [terminal("t1"), { ...terminal("x1"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("unsplit cleans up exited backend tabs without asking", async () => {
    const panes = [
      { id: "p1", tabs: [terminal("keep")], activeTabId: "keep" },
      { id: "p2", tabs: [{ ...terminal("x1"), exited: true }], activeTabId: "x1" },
    ];
    seed(panes);

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", "kill");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });

  it("workspace close with only exited tabs asks nothing and still reclaims them", async () => {
    const tabs = [{ ...terminal("x1"), exited: true }, { ...terminal("x2"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "x1" }]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("workspace close combines detach, confirmed blocked kill, and exited cleanup", async () => {
    const tabs = [terminal("t1"), execTerminal("e1"), { ...terminal("x1"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
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

describe("winrm ephemeral line tabs share the no-detach path", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("close asks once and kills on confirm, never showing a background toast", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], "winrm");

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("WinRM 非交互");
    expect(message).toContain("不支持转入后台");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).not.toContain("已转入后台");
  });

  it("close cancel keeps the tab with zero IPC", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], "winrm");
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
  });

  it("workspace close counts winrm tabs in the blocked confirmation", async () => {
    const tabs = [terminal("t1", "s1"), terminal("w1", "s2")];
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs, activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("ssh", "s1"), sessionInfo("winrm", "s2")],
      toasts: [],
    });
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个");
    expect(message).toContain("WinRM 非交互");
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);

    mocks.ask.mockResolvedValueOnce(true);
    await useUi.getState().closeWorkspace("ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-w1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("closeTabHint reflects detach capability per tab kind", () => {
    seed([{ id: "p", tabs: [], activeTabId: null }], "winrm");
    expect(closeTabHint(terminal())).toBe("关闭标签（结束 WinRM 非交互进程）");
    expect(closeActionHint(terminal())).toBe("结束 WinRM 非交互进程");
    expect(closeActionHint(execTerminal())).toBe("结束容器 exec 进程");
    seed([{ id: "p", tabs: [], activeTabId: null }], "ssh");
    expect(closeTabHint(terminal())).toBe("关闭标签（转入后台运行）");
    expect(closeActionHint(terminal())).toBe("转入后台运行");
    expect(closeActionHint({ ...terminal(), exited: true })).toBeUndefined();
    expect(closeTabHint({ ...terminal(), exited: true })).toBe("关闭标签（进程已结束）");
    expect(closeTabHint({ id: "x", kind: "files", title: "x", closable: true })).toBe("关闭标签");
  });
});

describe("detach capability without session metadata", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  function seedAssetKind(panes: Pane[], assetKind?: string, sessions: SessionInfo[] = []) {
    useUi.setState({
      workspaces: [{ ...ws(panes), assetKind }],
      activeWorkspaceId: "ws",
      sessions,
      toasts: [],
    });
  }

  it("restored winrm workspace (assetKind only, sessions empty) closes via confirm+kill", async () => {
    const tab = terminal();
    seedAssetKind([{ id: "p", tabs: [tab], activeTabId: tab.id }], "winrm");

    expect(closeActionHint(tab)).toBe("结束 WinRM 非交互进程");

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("WinRM 非交互");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).not.toContain("已转入后台");
  });

  it("workspace close with missing sessions treats assetKind winrm tabs as blocked", async () => {
    const tabs = [terminal("t1", "s1"), terminal("w1", "s2")];
    useUi.setState({
      workspaces: [{ ...ws([{ id: "p", tabs, activeTabId: "t1" }]), assetKind: "winrm" }],
      activeWorkspaceId: "ws",
      sessions: [sessionInfo("ssh", "s1")],
      toasts: [],
    });
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个");
    expect(message).toContain("WinRM 非交互");
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces).toHaveLength(1);

    mocks.ask.mockResolvedValueOnce(true);
    await useUi.getState().closeWorkspace("ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-w1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("unsplit with missing sessions confirms the blocked winrm tab before any IPC", async () => {
    const panes = [
      { id: "p1", tabs: [terminal("keep")], activeTabId: "keep" },
      { id: "p2", tabs: [terminal("w1")], activeTabId: "w1" },
    ];
    useUi.setState({
      workspaces: [{ ...ws(panes), assetKind: "winrm" }],
      activeWorkspaceId: "ws",
      sessions: [],
      toasts: [],
    });
    mocks.ask.mockResolvedValueOnce(false);

    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes).toHaveLength(2);

    mocks.ask.mockResolvedValueOnce(true);
    await useUi.getState().unsplitWorkspace("p2", "ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-w1", "kill");
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
  });

  it("unknown terminal type never promises background — confirm+kill with an explicit hint", async () => {
    const tab = terminal();
    seedAssetKind([{ id: "p", tabs: [tab], activeTabId: tab.id }], undefined);

    expect(closeActionHint(tab)).toBe("结束进程");
    expect(closeTabHint(tab)).toBe("关闭标签（结束进程）");

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("其他");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(toastText()).not.toContain("已转入后台");
  });
});
