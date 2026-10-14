/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab },
}));

import { getTerminalPrefs, setTerminalCloseAction } from "../../app/preferences";
import {
  closeActionHint,
  closeTabHint,
  requestCloseTab,
  requestCloseTabs,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
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
function tabsRemaining(): AppTab[] {
  return useUi.getState().workspaces[0].panes[0].tabs;
}

describe("terminal close preference dispatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    setTerminalCloseAction("ask");
  });

  it("shows choices for ask with background primary first, close directly as danger last, and a remember checkbox", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "detach", remember: false });

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.askChoiceRemember.mock.calls[0];
    expect(message).toContain("「terminal」");
    expect(message).toContain("后台会话");
    expect(options.rememberLabel).toBe("以后都按此选择");
    expect(options.choices.map((c: { key: string }) => c.key)).toEqual(["detach", "kill"]);
    expect(options.choices[0].primary).toBe(true);
    expect(options.choices[1].danger).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(tabsRemaining()).toEqual([]);
    expect(toastText()).toContain("已转入后台");
    expect(getTerminalPrefs().closeAction).toBe("ask");
  });

  it("kills the process when close directly is chosen in the ask dialog", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("「terminal」");
    expect(mocks.ask.mock.calls[0][0]).toContain("无法恢复");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(tabsRemaining()).toEqual([]);
    expect(toastText()).not.toContain("已转入后台");
  });

  it("keeps the tab and does not save the preference when the second confirmation is cancelled after choosing close directly", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: true });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([tab]);
    expect(getTerminalPrefs().closeAction).toBe("ask");
  });

  it("keeps the tab without any IPC when the ask dialog is cancelled", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([tab]);
  });

  it("saves the selected action when remember this choice is checked and skips future prompts", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: true });

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(getTerminalPrefs().closeAction).toBe("kill");
    expect(localStorage.getItem("nexterm.terminalClose.v1")).toBe('{"closeAction":"kill"}');

    const next = terminal("t2");
    seed([{ id: "p", tabs: [next], activeTabId: next.id }]);
    await requestCloseTab(next.id);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill");
  });

  it("ends the process directly without dialogs for the kill preference", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("kill");

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(tabsRemaining()).toEqual([]);
  });

  it("moves to the background directly without dialogs for the detach preference", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("detach");

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(toastText()).toContain("已转入后台");
  });

  it("keeps the existing confirmation branch for types that cannot detach regardless of preference", async () => {
    const tab = execTerminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("kill");

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("不支持转入后台");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-exec", "kill");
  });
});

describe("consolidated batch close dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    setTerminalCloseAction("ask");
  });

  it("shows one choice without a remember checkbox for multiple running SSH terminals", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "Files", closable: true };
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2"), terminal("t3"), files], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });

    await requestCloseTabs(["t1", "t2", "t3", "f1"]);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.askChoiceRemember.mock.calls[0];
    expect(message).toContain("3 个终端");
    expect(message).toContain("后台会话");
    expect(options.rememberLabel).toBeUndefined();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("3 个终端");
    expect(mocks.closeTab).toHaveBeenCalledTimes(3);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t3", "kill");
    expect(tabsRemaining()).toEqual([]);
    expect(getTerminalPrefs().closeAction).toBe("ask");
  });

  it("keeps terminals, closes non-terminal tabs normally, and reports no failure when the batch dialog is cancelled", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "Files", closable: true };
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2"), files], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTabs(["t1", "t2", "f1"]);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining().map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(toastText()).not.toContain("未能关闭");
  });

  it("keeps terminals and closes non-terminal tabs when the second confirmation is cancelled after choosing close directly in batch", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "Files", closable: true };
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2"), files], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTabs(["t1", "t2", "f1"]);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining().map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(toastText()).not.toContain("未能关闭");
  });

  it("does not show a dialog for batches containing only non-terminal tabs", async () => {
    const f1: AppTab = { id: "f1", kind: "files", title: "Files", closable: true };
    const f2: AppTab = { id: "f2", kind: "files", title: "Files2", closable: true };
    seed([{ id: "p", tabs: [f1, f2], activeTabId: "f1" }]);

    await requestCloseTabs(["f1", "f2"]);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([]);
  });

  it("keeps separate confirmation branches for blocked terminals in a batch", async () => {
    seed([{ id: "p", tabs: [terminal("t1"), execTerminal("e1")], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "detach", remember: false });

    await requestCloseTabs(["t1", "e1"]);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.askChoiceRemember.mock.calls[0][0]).toContain("1 个终端");
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("容器 exec");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(tabsRemaining()).toEqual([]);
  });

  it("ends all processes directly without dialogs for a kill-preference batch close", async () => {
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2")], activeTabId: "t1" }]);
    setTerminalCloseAction("kill");

    await requestCloseTabs(["t1", "t2"]);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "kill");
    expect(tabsRemaining()).toEqual([]);
  });
});

describe("close button tooltip follows preference", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setTerminalCloseAction("ask");
    seed([{ id: "p", tabs: [], activeTabId: null }]);
  });

  it("shows tooltip text for ask, kill, and detach preferences", () => {
    const tab = terminal();
    expect(closeActionHint(tab)).toBe("转入后台运行或结束进程");
    expect(closeTabHint(tab)).toBe("关闭标签（转入后台运行或结束进程）");

    setTerminalCloseAction("kill");
    expect(closeActionHint(tab)).toBe("结束进程");
    expect(closeTabHint(tab)).toBe("关闭标签（结束进程）");

    setTerminalCloseAction("detach");
    expect(closeActionHint(tab)).toBe("转入后台运行");
    expect(closeTabHint(tab)).toBe("关闭标签（转入后台运行）");
  });
});
