/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  listSessions: vi.fn(),
  closeTab: vi.fn(),
  resyncSessions: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dbApi: {},
    sessionApi: { list: mocks.listSessions },
    vaultApi: {},
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, closeTab: mocks.closeTab },
  };
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { setTerminalCloseAction } from "../../app/preferences";
import {
  closeActionHint,
  closeTabHint,
  requestCloseTab,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../../app/store";
import { deferred, flush, mount, waitFor, type MountedView } from "./reactTestUtils";
import type { LiveTabInfo, SessionInfo } from "../../ipc/commands";

function liveTab(extra: Partial<LiveTabInfo> & { tabId: string }): LiveTabInfo {
  return {
    sessionId: "s",
    sessionName: "session",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 0,
    viewers: 0,
    exited: false,
    lastOutputMsAgo: 1000,
    ...extra,
  };
}

function terminal(id = "terminal", sessionId = "s"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true, retainable: true };
}

function sessionInfo(kind: string, id = "s"): SessionInfo {
  return { id, assetId: "a", name: "session", kind, status: "connected", tabs: [], createdAt: 0 };
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

function seed(panes: Pane[], sessionKind = "local") {
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

describe("BackgroundSessions host-scoped view", () => {
  const s1 = { ...sessionInfo("ssh", "s1"), name: "主机一" };
  const s2 = { ...sessionInfo("ssh", "s2"), name: "主机二" };

  beforeEach(() => {
    vi.resetAllMocks();
    mocks.ask.mockResolvedValue(true);
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listSessions.mockResolvedValue([s1, s2]);
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.resyncSessions.mockResolvedValue(undefined);
  });

  function seedScope(sessionId = "s1", activeSessionId = sessionId) {
    const host = {
      id: "ws-host",
      kind: "session" as const,
      title: "host",
      sessionId,
      panes: [{ id: "host-pane", tabs: [], activeTabId: null }],
      activePaneId: "host-pane",
      splitRatio: 0.5,
      closable: true,
    };
    const active = {
      id: "ws-active",
      kind: "session" as const,
      title: "active",
      sessionId: activeSessionId,
      panes: [{ id: "active-pane", tabs: [], activeTabId: null }],
      activePaneId: "active-pane",
      splitRatio: 0.5,
      closable: true,
    };
    useUi.setState({
      workspaces: activeSessionId === sessionId ? [host] : [active, host],
      activeWorkspaceId: activeSessionId === sessionId ? host.id : active.id,
      sessions: [s1, s2],
      toasts: [],
      resyncSessions: mocks.resyncSessions,
    });
  }

  function mountHost(sessionId = "s1"): MountedView {
    return mount(createElement(BackgroundSessions, { workspaceId: "ws-host", sessionId }));
  }

  function actionButton(view: MountedView, label: string): HTMLButtonElement {
    const button = [...view.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (candidate) => candidate.textContent?.trim() === label,
    );
    if (!button) throw new Error(`${label}按钮未找到`);
    return button;
  }

  it("shows only the owning host's detached terminals without a scope selector", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-local", sessionId: "s1", sessionKind: "local", sessionName: "主机一", title: "本地构建" }),
      liveTab({ tabId: "tab-ssh", sessionId: "s1", sessionName: "主机一", title: "远程构建" }),
      liveTab({ tabId: "tab-other", sessionId: "s2", sessionName: "主机二", title: "其他主机" }),
      liveTab({ tabId: "tab-attached", sessionId: "s1", sessionName: "主机一", title: "有人看着", subscribers: 1 }),
      liveTab({ tabId: "tab-exited", sessionId: "s1", sessionName: "主机一", title: "已结束", exited: true }),
    ]);
    seedScope("s1", "s2");
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("本地构建"));
      expect(view.container.textContent).toContain("远程构建");
      expect(view.container.textContent).not.toContain("其他主机");
      expect(view.container.textContent).not.toContain("有人看着");
      expect(view.container.textContent).not.toContain("已结束");
      expect(view.container.querySelector('select[aria-label="选择会话"]')).toBeNull();
      expect(view.container.querySelector('[data-testid="background-host"]')?.textContent).toContain("主机一");
      expect(view.container.querySelector('[data-background-view="local"]')).not.toBeNull();
    } finally {
      view.unmount();
    }
  });

  it("keeps device background tabs without requesting unsupported remote discovery", async () => {
    seedScope();
    useUi.setState({ sessions: [{ ...s1, kind: "device" }, s2] });
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "device-tab", sessionId: "s1", sessionKind: "device", title: "设备任务" })]);
    const view = mountHost();
    try {
      await flush();
      expect(view.container.textContent).toContain("设备任务");
      expect(mocks.listDurable).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("discovers remote shells from another client and takes over the original identity", async () => {
    mocks.listDurable.mockResolvedValue([{
      id: "remote-shell", incarnation: "i1", createdAt: 10, metadataRevision: 1,
      cols: 100, rows: 30, dead: false, exitCode: null, finishedAt: null, title: "远端训练",
    }]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("远端训练"));
      expect(mocks.listDurable).toHaveBeenCalledWith("s1");
      await act(async () => {
        actionButton(view, "接管").click();
        await flush();
      });
      const tab = useUi.getState().workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs).find((candidate) => candidate.tabId === "remote-shell");
      expect(tab).toMatchObject({ sessionId: "s1", tabId: "remote-shell", kind: "terminal", title: "远端训练" });
      expect(mocks.closeTab).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("does not duplicate local tabs or advertise attached and exited shells as remote background rows", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "known", sessionId: "s1", title: "已有后台" }),
      liveTab({ tabId: "attached", sessionId: "s1", subscribers: 1, title: "正在观看" }),
    ]);
    mocks.listDurable.mockResolvedValue([
      { id: "known", cols: 80, rows: 24, dead: false, title: "重复远端" },
      { id: "attached", cols: 80, rows: 24, dead: false, title: "重复观看" },
      { id: "finished", cols: 80, rows: 24, dead: true, title: "已退出远端" },
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("已有后台"));
      expect(view.container.textContent).not.toContain("重复远端");
      expect(view.container.textContent).not.toContain("重复观看");
      expect(view.container.textContent).not.toContain("已退出远端");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);
    } finally {
      view.unmount();
    }
  });

  it("passes the owning SSH connection when ending a discovered remote shell", async () => {
    mocks.listDurable.mockResolvedValue([{ id: "remote-kill", cols: 80, rows: 24, dead: false, title: "远端任务" }]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("远端任务"));
      await act(async () => {
        actionButton(view, "结束进程").click();
        await flush();
      });
      expect(mocks.ask).toHaveBeenCalled();
      expect(mocks.closeTab).toHaveBeenCalledWith("remote-kill", "kill", "s1");
      expect(mocks.resyncSessions).toHaveBeenCalledWith({ silent: true });
    } finally {
      view.unmount();
    }
  });

  it("retains local results while reporting remote discovery failure", async () => {
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "known", sessionId: "s1", title: "已有后台" })]);
    mocks.listDurable.mockRejectedValue(new Error("SSH disconnected"));
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("SSH disconnected"));
      expect(view.container.textContent).toContain("已有后台");
      expect(view.container.textContent).not.toContain("当前主机没有在后台运行的终端");
    } finally {
      view.unmount();
    }
  });

  it("does not fall back to the active workspace when the owner is missing", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
    ]);
    seedScope();
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws-missing" }));
    try {
      await flush();
      expect(mocks.listLive).not.toHaveBeenCalled();
      expect(view.container.textContent).toContain("无法确定后台会话所属主机");
      expect(view.container.textContent).not.toContain("构建");
    } finally {
      view.unmount();
    }
  });

  it("keeps loading and empty states distinct", async () => {
    const pending = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValue(pending.promise);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.querySelectorAll('tr[aria-hidden="true"]').length).toBeGreaterThan(0));
      expect(view.container.textContent).toContain("加载中");
      expect(view.container.textContent).not.toContain("当前主机没有在后台运行的终端");

      await act(async () => {
        pending.resolve([]);
        await pending.promise;
      });
      expect(view.container.textContent).toContain("当前主机没有在后台运行的终端");
      expect(view.container.querySelector('tr[aria-hidden="true"]')).toBeNull();
      expect(view.container.textContent).not.toContain("读取后台会话失败");
    } finally {
      view.unmount();
    }
  });

  it("does not present a list error as an empty result", async () => {
    mocks.listLive.mockRejectedValue(new Error("database unavailable"));
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("读取后台会话失败"));
      expect(view.container.textContent).toContain("database unavailable");
      expect(view.container.textContent).not.toContain("当前主机没有在后台运行的终端");
    } finally {
      view.unmount();
    }
  });

  it("restores a detached tab with its backend title on takeover", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-local", sessionId: "s1", sessionKind: "local", sessionName: "主机一", title: "AI 构建" }),
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("AI 构建"));
      await act(async () => {
        actionButton(view, "接管").click();
        await flush();
      });
      const added = useUi
        .getState()
        .workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)
        .find((tab) => tab.tabId === "tab-local");
      expect(added).toMatchObject({
        kind: "terminal",
        sessionId: "s1",
        title: "AI 构建",
        renamedByUser: true,
      });
    } finally {
      view.unmount();
    }
  });

  it("restores a background row and resynchronizes sessions after kill fails", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-local", sessionKind: "local", sessionName: "本机", title: "构建" }),
    ]);
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));
    seed([{ id: "p", tabs: [], activeTabId: null }], "local");
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      const button = [...view.container.querySelectorAll("button")].find(
        (candidate) => candidate.textContent?.trim() === "结束进程",
      );
      await act(async () => {
        button?.click();
        await flush();
      });
      expect(mocks.resyncSessions).toHaveBeenCalledWith({ silent: true });
      expect(view.container.textContent).toContain("构建");
      expect(toastText()).toContain("结束进程失败：boom");
    } finally {
      view.unmount();
    }
  });

  it("ignores stale load responses after a newer refresh", async () => {
    vi.useFakeTimers();
    const first = deferred<LiveTabInfo[]>();
    const second = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    seedScope();
    const view = mountHost();
    try {
      expect(mocks.listLive).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(mocks.listLive).toHaveBeenCalledTimes(2);

      await act(async () => {
        second.resolve([liveTab({ tabId: "new", sessionId: "s1", sessionName: "主机一", title: "新结果" })]);
        await second.promise;
      });
      expect(view.container.textContent).toContain("新结果");

      await act(async () => {
        first.resolve([liveTab({ tabId: "old", sessionId: "s1", sessionName: "主机一", title: "旧结果" })]);
        await first.promise;
      });
      expect(view.container.textContent).toContain("新结果");
      expect(view.container.textContent).not.toContain("旧结果");
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });

  it("reloads after reconnect and never lets the old connection response win", async () => {
    const oldRequest = deferred<LiveTabInfo[]>();
    const newRequest = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValueOnce(oldRequest.promise).mockReturnValueOnce(newRequest.promise);
    seedScope();
    const view = mountHost("s1");
    try {
      expect(mocks.listLive).toHaveBeenCalledTimes(1);
      await act(async () => {
        useUi.getState().updateWorkspaceSession("ws-host", "s2");
        view.root.render(createElement(BackgroundSessions, { workspaceId: "ws-host", sessionId: "s2" }));
        await flush();
      });
      await waitFor(() => expect(mocks.listLive).toHaveBeenCalledTimes(2));

      await act(async () => {
        newRequest.resolve([liveTab({ tabId: "new", sessionId: "s2", sessionName: "主机二", title: "新连接" })]);
        await newRequest.promise;
      });
      expect(view.container.textContent).toContain("新连接");
      await act(async () => {
        oldRequest.resolve([liveTab({ tabId: "old", sessionId: "s1", sessionName: "主机一", title: "旧连接" })]);
        await oldRequest.promise;
      });

      expect(view.container.textContent).toContain("新连接");
      expect(view.container.textContent).not.toContain("旧连接");
      expect(view.container.querySelector('[data-testid="background-host"]')?.textContent).toContain("主机二");
    } finally {
      view.unmount();
    }
  });

  function detachedRows() {
    return [
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
      liveTab({ tabId: "t2", sessionId: "s2", sessionName: "主机二", title: "训练" }),
    ];
  }

  function expectNoAssetSelector(view: MountedView) {
    expect(view.container.querySelector('select[aria-label="选择会话"]')).toBeNull();
  }

  function seedWorkspaceScope(host: { kind: "session" | "tools"; sessionId?: string }, sessionList: SessionInfo[]) {
    useUi.setState({
      workspaces: [
        {
          id: "ws",
          kind: host.kind,
          title: "workspace",
          sessionId: host.sessionId,
          panes: [{ id: "p", tabs: [], activeTabId: null }],
          activePaneId: "p",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: "ws",
      sessions: sessionList,
      toasts: [],
    });
  }

  it("lists only the owning workspace's detached terminals without an asset selector", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedWorkspaceScope({ kind: "session", sessionId: "s1" }, [s1, s2]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).not.toContain("训练");
      expectNoAssetSelector(view);
    } finally {
      view.unmount();
    }
  });

  it("shows all terminals for tools workspaces without an asset selector", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedWorkspaceScope({ kind: "tools" }, [s1, s2]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).toContain("训练");
      expectNoAssetSelector(view);
    } finally {
      view.unmount();
    }
  });

  it("keeps the owning filter when the session is missing from the current session list", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedWorkspaceScope({ kind: "session", sessionId: "s1" }, [s2]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).not.toContain("训练");
      expectNoAssetSelector(view);
    } finally {
      view.unmount();
    }
  });

  it("filters by the workspace identified by workspaceId, not the active workspace", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    useUi.setState({
      workspaces: [
        {
          id: "ws-active",
          kind: "session",
          title: "active",
          sessionId: "s2",
          panes: [{ id: "p1", tabs: [], activeTabId: null }],
          activePaneId: "p1",
          splitRatio: 0.5,
          closable: true,
        },
        {
          id: "ws-host",
          kind: "session",
          title: "host",
          sessionId: "s1",
          panes: [{ id: "p2", tabs: [], activeTabId: null }],
          activePaneId: "p2",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: "ws-active",
      sessions: [s1, s2],
      toasts: [],
    });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws-host" }));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).not.toContain("训练");
      expectNoAssetSelector(view);
    } finally {
      view.unmount();
    }
  });

  it("uses sessionId directly when the source workspace is gone", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedWorkspaceScope({ kind: "tools" }, [s1, s2]);
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws", sessionId: "s1" }));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).not.toContain("训练");
      expectNoAssetSelector(view);
    } finally {
      view.unmount();
    }
  });

  it("asks before killing and resyncs sessions after success", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      await act(async () => {
        actionButton(view, "结束进程").click();
        await flush();
      });
      await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill"));
      expect(mocks.ask).toHaveBeenCalledWith(
        expect.stringContaining("无法恢复"),
        { title: "结束进程", kind: "warning" },
      );
      expect(mocks.resyncSessions).toHaveBeenCalledWith({ silent: true });
    } finally {
      view.unmount();
    }
  });

  it("restores the row and reports the error when kill fails", async () => {
    mocks.closeTab.mockRejectedValue(new Error("kill failed"));
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      await act(async () => {
        actionButton(view, "结束进程").click();
        await flush();
      });
      await waitFor(() => expect(toastText()).toContain("结束进程失败：kill failed"));
      expect(view.container.textContent).toContain("构建");
    } finally {
      view.unmount();
    }
  });

  it("does not kill when confirmation is declined", async () => {
    mocks.ask.mockResolvedValue(false);
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      await act(async () => {
        actionButton(view, "结束进程").click();
        await flush();
      });
      expect(mocks.closeTab).not.toHaveBeenCalled();
      expect(view.container.textContent).toContain("构建");
    } finally {
      view.unmount();
    }
  });
});

describe("closing a local terminal follows explicit capability and preference", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    setTerminalCloseAction("detach");
    mocks.ask.mockResolvedValue(true);
    mocks.listSessions.mockResolvedValue([]);
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "kernel-terminal" })]);
    mocks.closeTab.mockResolvedValue(undefined);
    mocks.resyncSessions.mockResolvedValue(undefined);
  });

  it("detaches a retainable local terminal without a destructive prompt and ignores a repeated close", async () => {
    const tab = { ...terminal(), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);
    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "detach");
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([]);
    expect(toastText()).toContain("已转入后台");
  });

  it("kills a retainable local terminal directly for the saved kill preference", async () => {
    const tab = { ...terminal(), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("kill");

    await requestCloseTab(tab.id);

    expect(mocks.ask).not.toHaveBeenCalled();
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(toastText()).not.toContain("已转入后台");
  });

  it("offers background, kill, and cancel to a retainable local terminal for ask", async () => {
    const tab = { ...terminal(), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    setTerminalCloseAction("ask");
    mocks.askChoiceRemember.mockResolvedValueOnce(null);

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).toHaveBeenCalledTimes(1);
    expect(mocks.askChoiceRemember.mock.calls[0][1].choices.map((c: { key: string }) => c.key)).toEqual(["detach", "kill"]);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
  });

  it("does not infer background capability from the local kind", async () => {
    const tab = { ...terminal(), retainable: undefined };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("不支持转入后台");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(toastText()).not.toContain("已转入后台");
  });

  it("keeps a retainable local tab with closeError and no success toast when detach fails", async () => {
    const tab = { ...terminal(), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));

    await requestCloseTab(tab.id);

    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([{ ...tab, closeError: "boom" }]);
    expect(toastText()).toContain("终端操作失败");
    expect(toastText()).not.toContain("已转入后台");
  });

  it("hints use the explicit local capability instead of only the session kind", () => {
    const tab = { ...terminal(), retainable: true };
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], "local");
    expect(closeActionHint(tab)).toBe("转入后台运行");
    expect(closeTabHint(tab)).toBe("关闭标签（转入后台运行）");

    const unknown = { ...terminal("unknown"), retainable: undefined };
    seed([{ id: "p", tabs: [unknown], activeTabId: unknown.id }], "local");
    expect(closeActionHint(unknown)).toBe("结束本地进程");
    expect(closeTabHint(unknown)).toBe("关闭标签（结束本地进程）");
  });
});
