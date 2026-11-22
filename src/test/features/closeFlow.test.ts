/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  sessionList: vi.fn(),
  disconnect: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  vaultApi: {},
  sessionApi: { list: mocks.sessionList, disconnect: mocks.disconnect },
  terminalApi: { closeTab: mocks.closeTab, listLive: mocks.listLive, listDurable: mocks.listDurable },
}));

import { getTerminalPrefs, setTerminalCloseAction } from "../../app/preferences";
import {
  applyPreconfirmedClose,
  closeWorkspaces,
  collectWorkspaceClose,
  pushWorkspaceCloseToast,
  requestCloseTab,
  useUi,
  type AppTab,
  type Workspace,
} from "../../app/store";
import type { LiveTabInfo, SessionInfo } from "../../ipc/commands";

function terminal(id: string, extra: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true, retainable: true, ...extra };
}

function audit(id: string): AppTab {
  return { id, kind: "audit", title: id, closable: true };
}

function session(): SessionInfo {
  return { id: "s", assetId: "a", name: "session", kind: "ssh", status: "connected", tabs: [], createdAt: 0 };
}

function workspace(id: string, tabs: AppTab[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId: "s",
    assetKind: "ssh",
    panes: [{ id: `${id}-p`, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: `${id}-p`,
    closable: true,
  };
}

function liveTabs(): LiveTabInfo[] {
  return useUi.getState().workspaces.flatMap((w) =>
    w.panes.flatMap((p) =>
      p.tabs
        .filter((tab) => tab.kind === "terminal" && tab.tabId)
        .map((tab) => ({
          tabId: tab.tabId as string,
          sessionId: "s",
          sessionName: "session",
          sessionKind: "ssh",
          cols: 80,
          rows: 24,
          sizeOwner: null,
          subscribers: 1,
          viewers: 1,
          exited: false,
          lastOutputMsAgo: 0,
        })),
    ),
  );
}

function workspaceIds(): string[] {
  return useUi.getState().workspaces.map((w) => w.id);
}

beforeEach(() => {
  vi.resetAllMocks();
  mocks.listLive.mockImplementation(async () => liveTabs());
  mocks.listDurable.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([session()]);
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.closeTab.mockResolvedValue(undefined);
  setTerminalCloseAction("ask");
  useUi.setState({ sessions: [session()], workspaces: [], activeWorkspaceId: null, toasts: [], workspaceRuntime: {} });
});

afterEach(() => {
  setTerminalCloseAction("ask");
});

describe("closing a running terminal from a key chord", () => {
  it("asks once before a kill preference ends the process, with 取消 as the safe answer", async () => {
    setTerminalCloseAction("kill");
    useUi.setState({ workspaces: [workspace("w1", [terminal("t1")])], activeWorkspaceId: "w1" });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab("t1", { viaShortcut: true });

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][1]).toMatchObject({ kind: "warning", confirmLabel: "结束进程" });
    expect(mocks.closeTab).not.toHaveBeenCalled();

    mocks.ask.mockResolvedValueOnce(true);
    await requestCloseTab("t1", { viaShortcut: true });
    expect(mocks.ask).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
  });

  it("still ends the process without a dialog when the kill preference is used with the mouse", async () => {
    setTerminalCloseAction("kill");
    useUi.setState({ workspaces: [workspace("w1", [terminal("t1")])], activeWorkspaceId: "w1" });

    await requestCloseTab("t1");

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "kill", "s");
  });

  it("focuses 取消 in the background-or-end choice it opens", async () => {
    useUi.setState({ workspaces: [workspace("w1", [terminal("t1")])], activeWorkspaceId: "w1" });
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTab("t1", { viaShortcut: true });
    expect(mocks.askChoiceRemember.mock.calls[0][1]).toMatchObject({ focusCancel: true });

    mocks.askChoiceRemember.mockResolvedValueOnce(null);
    await requestCloseTab("t1");
    expect(mocks.askChoiceRemember.mock.calls[1][1]).toMatchObject({ focusCancel: false });
    expect(mocks.closeTab).not.toHaveBeenCalled();
  });

  it("asks before ending a terminal that cannot go to the background even with the kill preference", async () => {
    setTerminalCloseAction("kill");
    useUi.setState({ workspaces: [workspace("w1", [terminal("e1", { containerId: "c1" })])], activeWorkspaceId: "w1" });
    mocks.ask.mockResolvedValueOnce(false);

    await requestCloseTab("e1", { viaShortcut: true });

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).not.toHaveBeenCalled();
  });
});

describe("closing a workspace", () => {
  it("asks once, with the remember option, when it would also end a terminal that cannot go to the background", async () => {
    useUi.setState({
      workspaces: [workspace("w1", [terminal("t1"), terminal("e1", { containerId: "c1" })])],
      activeWorkspaceId: "w1",
    });
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "detach", remember: true });

    expect(await useUi.getState().closeWorkspace("w1")).toBe(true);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.askChoiceRemember.mock.calls[0];
    expect(message).toContain("容器 exec");
    expect(options.rememberLabel).toBe("以后都按此选择");
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-e1", "kill");
    expect(getTerminalPrefs().closeAction).toBe("detach");
  });

  it("activates the right neighbour of the closed active workspace, or the left one at the end", async () => {
    useUi.setState({
      workspaces: [workspace("w1", [audit("a1")]), workspace("w2", [audit("a2")]), workspace("w3", [audit("a3")]), workspace("w4", [audit("a4")])],
      activeWorkspaceId: "w2",
    });
    expect(await useUi.getState().closeWorkspace("w2")).toBe(true);
    expect(useUi.getState().activeWorkspaceId).toBe("w3");
    useUi.setState({ activeWorkspaceId: "w4" });
    expect(await useUi.getState().closeWorkspace("w4")).toBe(true);
    expect(useUi.getState().activeWorkspaceId).toBe("w3");
  });
});

describe("closing all workspaces", () => {
  function seedTwo() {
    useUi.setState({
      workspaces: [workspace("w1", [terminal("t1"), audit("a1")]), workspace("w2", [terminal("t2")])],
      activeWorkspaceId: "w1",
    });
  }

  it("asks once for every workspace, and Cancel closes nothing", async () => {
    seedTwo();
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await closeWorkspaces(["w2", "w1"]);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.askChoiceRemember.mock.calls[0][0]).toContain("2 个终端");
    expect(mocks.askChoiceRemember.mock.calls[0][1]).toMatchObject({ title: "关闭 2 个工作区" });
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(workspaceIds()).toEqual(["w1", "w2"]);
  });

  it("closes every workspace under the one decision", async () => {
    seedTwo();
    mocks.askChoiceRemember.mockResolvedValueOnce({ key: "detach", remember: false });

    await closeWorkspaces(["w2", "w1"]);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach", "s");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "detach", "s");
    expect(workspaceIds()).toEqual([]);
  });

  it("confirms a workspace again when it would end something the batch dialog did not name", () => {
    const tabs = [terminal("t1")];
    const confirmed = collectWorkspaceClose(tabs, "ssh", "session", "w1");
    const same = collectWorkspaceClose(tabs, "ssh", "session", "w1");
    expect(applyPreconfirmedClose(same, { action: "kill", confirmed })).toBe(true);
    expect(same.preferredKills.map((tab) => tab.id)).toEqual(["t1"]);

    const grown = collectWorkspaceClose([...tabs, terminal("t9")], "ssh", "session", "w1");
    expect(applyPreconfirmedClose(grown, { action: "kill", confirmed })).toBe(false);
    expect(grown.preferredKills).toEqual([]);
  });
});

describe("closing a tab", () => {
  it("activates the right neighbour of the closed active tab, or the left one at the end", async () => {
    useUi.setState({ workspaces: [workspace("w1", [audit("a1"), audit("a2"), audit("a3"), audit("a4")])], activeWorkspaceId: "w1" });
    useUi.getState().setActiveTab("a2");
    await requestCloseTab("a2");
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("a3");
    useUi.getState().setActiveTab("a4");
    await requestCloseTab("a4");
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("a3");
  });
});

describe("notices about processes left running", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("keeps a skipped remote kill on screen until dismissed, also when newer notices arrive", () => {
    vi.useFakeTimers();
    pushWorkspaceCloseToast("w1", {
      detached: 0,
      killed: 0,
      killUnreachable: 1,
      failed: 0,
      dbFailed: 0,
      succeededTabIds: new Set(),
      succeededDbConnIds: new Set(),
    }, true);
    const notice = useUi.getState().toasts.find((toast) => toast.text.includes("远端进程未能结束"));
    expect(notice?.persistent).toBe(true);
    for (let i = 0; i < 6; i++) useUi.getState().pushToast("info", `notice ${i}`);
    vi.advanceTimersByTime(60_000);
    expect(useUi.getState().toasts.map((toast) => toast.id)).toEqual([notice?.id]);
    useUi.getState().dismissToast(notice!.id);
    expect(useUi.getState().toasts).toEqual([]);
  });
});
