/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
  listLive: vi.fn().mockResolvedValue([]),
  listDurable: vi.fn().mockResolvedValue([]),
}));
vi.mock("../../ui/dialogs", () => ({
  ask: mocks.ask,
  askChoice: mocks.askChoice,
  askChoiceRemember: mocks.askChoiceRemember,
}));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab, listLive: mocks.listLive, listDurable: mocks.listDurable },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { setTerminalCloseAction } from "../../app/preferences";
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
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true, retainable: true };
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
function liveTab(tabId: string, exited = false) {
  return {
    tabId,
    sessionId: "s",
    sessionName: "session",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 1,
    viewers: 1,
    exited,
    lastOutputMsAgo: 0,
  };
}
function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
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
    setTerminalCloseAction("detach");
    mocks.ask.mockResolvedValue(true);
  });

  it("detaches a running terminal on close without any dialog", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoice).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach", "s");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).toContain("已转入后台");
  });

  it.each(["ssh", "device"] as const)("keeps missing %s backend state unknown and never defaults to kill", async (kind) => {
    const tab = { ...terminal(), exited: false };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], kind);
    if (kind === "ssh") mocks.listDurable.mockRejectedValueOnce(new Error("durable unavailable"));
    mocks.closeTab.mockRejectedValueOnce(new Error("offline"));

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach", "s");
    const remaining = useUi.getState().workspaces[0]?.panes[0]?.tabs[0];
    expect(remaining?.dead).not.toBe(true);
    expect(remaining?.exited).toBeUndefined();
    expect(remaining?.closeError).toBe("offline");
  });

  it("uses default cleanup for a currently exited terminal without a dialog", async () => {
    const tab = { ...terminal(), exited: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.listLive.mockResolvedValueOnce([liveTab("kernel-terminal", true)]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", undefined, "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-terminal", "kill", "s");
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

    const remaining = useUi.getState().workspaces[0].panes[0].tabs;
    expect(remaining).toHaveLength(1);
    expect(remaining[0]).toMatchObject({ ...tab, closeError: "boom" });
    expect(remaining[0].exited).toBeUndefined();
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
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "detach", "s");
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

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
    expect(toastText()).toContain("1 个进程已结束");
  });

  it.each([
    { name: "ssh, docker exec and exited", assetKind: undefined as string | undefined, close: "detach" as const },
    { name: "winrm", assetKind: "winrm", close: "ask" as const },
  ])("closing a pane keeps its $name terminals running with no dialog or close IPC", async ({ assetKind, close }) => {
    const panes = [
      { id: "p1", tabs: [terminal("keep")], activeTabId: "keep" },
      {
        id: "p2",
        tabs: [terminal("gone"), execTerminal("e1"), { ...terminal("x1"), exited: true }],
        activeTabId: "e1",
      },
    ];
    useUi.setState({
      workspaces: [{ ...ws(panes), assetKind }],
      activeWorkspaceId: "ws",
      toasts: [],
    });
    setTerminalCloseAction(close);

    useUi.getState().closePane("p2", "ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    const [merged] = useUi.getState().workspaces[0].panes;
    expect(useUi.getState().workspaces[0].panes).toHaveLength(1);
    expect(merged.tabs.map((tab) => tab.id)).toEqual(["keep", "gone", "e1", "x1"]);
    expect(merged.activeTabId).toBe("e1");
  });

  it("workspace close uses default cleanup for a currently exited backend tab", async () => {
    const tabs = [terminal("t1"), { ...terminal("x1"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);
    mocks.listLive.mockResolvedValueOnce([liveTab("kernel-t1"), liveTab("kernel-x1", true)]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", undefined, "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-x1", "kill", "s");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("workspace close with only currently exited tabs asks nothing and uses default cleanup", async () => {
    const tabs = [{ ...terminal("x1"), exited: true }, { ...terminal("x2"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "x1" }]);
    mocks.listLive.mockResolvedValueOnce([liveTab("kernel-x1", true), liveTab("kernel-x2", true)]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", undefined, "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x2", undefined, "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-x1", "kill", "s");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("workspace close separates detach, confirmed blocked kill, and default exited cleanup", async () => {
    const tabs = [terminal("t1"), execTerminal("e1"), { ...terminal("x1"), exited: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);
    mocks.listLive.mockResolvedValueOnce([liveTab("kernel-t1"), liveTab("kernel-x1", true)]);

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-x1", undefined, "s");
    expect(mocks.closeTab).not.toHaveBeenCalledWith("kernel-x1", "kill", "s");
    expect(useUi.getState().workspaces).toEqual([]);
  });
});

describe("workspace and pane reclaim follow terminal close preferences", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("uses saved kill without dialogs for retainable local tabs on workspace close", async () => {
    const tabs = [{ ...terminal("t1"), retainable: true }, { ...terminal("t2"), retainable: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }], "local");
    setTerminalCloseAction("kill");

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("uses saved kill without an extra warning for a mixed workspace", async () => {
    const tabs = [{ ...terminal("t1"), retainable: true }, execTerminal("e1")];
    seed([{ id: "p", tabs, activeTabId: "t1" }], "local");
    setTerminalCloseAction("kill");

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("uses one ask choice to detach retainable local tabs on workspace close", async () => {
    const tabs = [{ ...terminal("t1"), retainable: true }, { ...terminal("t2"), retainable: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }], "local");
    setTerminalCloseAction("ask");
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "detach", remember: false });

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "detach");
    expect(toastText()).toContain("2 个终端转入后台");
  });

  it("kills workspace terminals after one direct-close choice", async () => {
    const tab = { ...terminal("t1"), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: "t1" }], "local");
    setTerminalCloseAction("ask");
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(toastText()).not.toContain("转入后台");
  });

  it("reports a failed workspace close under the current workspace title", async () => {
    const tab = { ...terminal("t1"), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: "t1" }], "local");
    setTerminalCloseAction("detach");
    mocks.closeTab.mockImplementationOnce(async () => {
      useUi.setState({ workspaces: [{ ...useUi.getState().workspaces[0], title: "renamed" }] });
      throw new Error("boom");
    });

    await useUi.getState().closeWorkspace("ws");

    expect(toastText()).toContain("关闭「renamed」");
    expect(toastText()).not.toContain("关闭「workspace」");
  });

  it("keeps failed workspace tabs with closeError and removes only successful detach operations", async () => {
    const tabs = [{ ...terminal("t1"), retainable: true }, { ...terminal("t2"), retainable: true }];
    seed([{ id: "p", tabs, activeTabId: "t1" }], "local");
    setTerminalCloseAction("detach");
    mocks.closeTab.mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("boom"));

    await useUi.getState().closeWorkspace("ws");

    const remaining = useUi.getState().workspaces[0]?.panes[0]?.tabs;
    expect(remaining).toHaveLength(1);
    expect(remaining?.[0]).toMatchObject({ id: "t2", closeError: "boom" });
    expect(toastText()).not.toContain("转入后台");
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
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill", "s");
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
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill", "s");
    const remaining = useUi.getState().workspaces[0].panes[0].tabs;
    expect(remaining).toHaveLength(3);
    expect(remaining.every((t) => t.exited === true)).toBe(true);
  });

  it("requestKillWorkspaceTerminals reads sessions after the confirmation, not before the dialog", async () => {
    const tab = terminal("t1");
    seed([{ id: "p", tabs: [tab], activeTabId: "t1" }], "local");
    mocks.ask.mockImplementationOnce(async () => {
      useUi.setState({ sessions: [sessionInfo("ssh")] });
      return true;
    });

    await requestKillWorkspaceTerminals("ws");

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
  });

  it("requestKillWorkspaceTerminals keeps failures with closeError and no success toast", async () => {
    const tabs = [terminal("t1"), terminal("t2")];
    seed([{ id: "p", tabs, activeTabId: "t1" }]);
    mocks.closeTab.mockResolvedValueOnce(undefined).mockRejectedValueOnce(new Error("boom"));

    await requestKillWorkspaceTerminals("ws");

    const remaining = useUi.getState().workspaces[0].panes[0].tabs;
    expect(remaining[0]).toMatchObject({ id: "t1", exited: true });
    expect(remaining[1]).toMatchObject({ id: "t2", closeError: "boom" });
    expect(remaining[1].exited).not.toBe(true);
    expect(toastText()).toContain("1/2 个终端结束失败");
    expect(toastText()).not.toContain("已结束 2 个终端进程");
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
    setTerminalCloseAction("detach");
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

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s1");
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

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s1");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-w1", "kill");
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("unknown terminal type never promises background — confirm+kill with an explicit hint", async () => {
    const tab = { ...terminal(), retainable: undefined };
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
