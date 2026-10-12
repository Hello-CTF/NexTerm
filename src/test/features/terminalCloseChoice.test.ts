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

describe("终端关闭偏好分派", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    setTerminalCloseAction("ask");
  });

  it("ask 偏好弹出选择：转到后台 primary 在前、直接关闭 danger 在后，带记住勾选", async () => {
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

  it("ask 弹窗选直接关闭则杀进程", async () => {
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

  it("ask 弹窗选直接关闭但二次确认取消则保留标签，不写偏好", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: true });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([tab]);
    expect(getTerminalPrefs().closeAction).toBe("ask");
  });

  it("ask 弹窗取消则保留标签，无任何 IPC", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([tab]);
  });

  it("勾选「以后都按此选择」把所选写入偏好，之后不再询问", async () => {
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

  it("kill 偏好直接结束进程，不弹窗", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("kill");

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(tabsRemaining()).toEqual([]);
  });

  it("detach 偏好直接转入后台，不弹窗", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("detach");

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(toastText()).toContain("已转入后台");
  });

  it("不可 detach 的类型保持现有确认分支，不受偏好影响", async () => {
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

describe("批量关闭合并弹窗", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    setTerminalCloseAction("ask");
  });

  it("多个运行中 SSH 终端只弹一次选择，不带记住勾选", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "文件", closable: true };
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

  it("批量弹窗取消：终端保留，非终端标签照常关闭，不算失败", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "文件", closable: true };
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2"), files], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTabs(["t1", "t2", "f1"]);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining().map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(toastText()).not.toContain("未能关闭");
  });

  it("批量选直接关闭但二次确认取消：终端保留，非终端标签照常关闭", async () => {
    const files: AppTab = { id: "f1", kind: "files", title: "文件", closable: true };
    seed([{ id: "p", tabs: [terminal("t1"), terminal("t2"), files], activeTabId: "t1" }]);
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "kill", remember: false });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTabs(["t1", "t2", "f1"]);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabsRemaining().map((t) => t.id)).toEqual(["t1", "t2"]);
    expect(toastText()).not.toContain("未能关闭");
  });

  it("批量只含非终端标签时不弹窗", async () => {
    const f1: AppTab = { id: "f1", kind: "files", title: "文件", closable: true };
    const f2: AppTab = { id: "f2", kind: "files", title: "文件2", closable: true };
    seed([{ id: "p", tabs: [f1, f2], activeTabId: "f1" }]);

    await requestCloseTabs(["f1", "f2"]);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(tabsRemaining()).toEqual([]);
  });

  it("批量中的 blocked 终端保持各自的确认分支", async () => {
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

  it("kill 偏好下批量关闭不弹窗，全部直接结束", async () => {
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

describe("关闭按钮 tooltip 随偏好联动", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setTerminalCloseAction("ask");
    seed([{ id: "p", tabs: [], activeTabId: null }]);
  });

  it("ask/kill/detach 三种偏好的提示文案", () => {
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
