/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  listLive: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    dbApi: {},
    sessionApi: { list: vi.fn().mockResolvedValue([]) },
    vaultApi: {},
    terminalApi: { listLive: mocks.listLive, closeTab: mocks.closeTab },
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
import { deferred, flush, mount, setSelectValue, waitFor, type MountedView } from "./reactTestUtils";
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
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
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

describe("BackgroundSessions lists detached terminals", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("lists detached SSH and local tabs, hiding attached and exited ones", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-local", sessionKind: "local", sessionName: "本机" }),
      liveTab({ tabId: "tab-ssh", sessionKind: "ssh", sessionName: "远程 SSH" }),
      liveTab({ tabId: "tab-attached", sessionKind: "ssh", sessionName: "有人看着", subscribers: 1 }),
      liveTab({ tabId: "tab-exited", sessionKind: "ssh", sessionName: "已结束", exited: true }),
    ]);
    let view: MountedView | null = null;
    view = mount(createElement(BackgroundSessions));
    await flush();
    await waitFor(() => {
      expect(view?.container.textContent).toContain("远程 SSH");
    });
    expect(view.container.textContent).toContain("本机");
    expect(view.container.textContent).not.toContain("有人看着");
    expect(view.container.textContent).not.toContain("已结束");
    view.unmount();
  });

  it("restores a detached local tab on takeover", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-local", sessionKind: "local", sessionId: "s-local", sessionName: "本机", title: "AI 构建" }),
    ]);
    seed([{ id: "p", tabs: [], activeTabId: null }], "local");
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => {
        expect(view?.container.textContent).toContain("AI 构建");
      });
      const button = [...view.container.querySelectorAll("button")].find(
        (candidate) => candidate.textContent?.trim() === "接管",
      );
      expect(button).toBeDefined();
      await act(async () => {
        button?.click();
        await flush();
      });
      const added = useUi
        .getState()
        .workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)
        .find((tab) => tab.tabId === "tab-local");
      expect(added?.kind).toBe("terminal");
      expect(added?.sessionId).toBe("s-local");
      expect(added?.title).toBe("AI 构建");
    } finally {
      view.unmount();
    }
  });

  it("shows a custom backend title and restores it on takeover", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "tab-titled", sessionKind: "ssh", sessionName: "远程 SSH", title: "构建机" }),
    ]);
    seed([{ id: "p", tabs: [], activeTabId: null }], "ssh");
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => {
        expect(view?.container.textContent).toContain("构建机");
      });
      const button = [...view.container.querySelectorAll("button")].find(
        (candidate) => candidate.textContent?.trim() === "接管",
      );
      expect(button).toBeDefined();
      await act(async () => {
        button?.click();
        await flush();
      });
      const added = useUi
        .getState()
        .workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)
        .find((tab) => tab.tabId === "tab-titled");
      expect(added?.title).toBe("构建机");
      expect(added?.renamedByUser).toBe(true);
    } finally {
      view.unmount();
    }
  });

  it("旧加载响应不会覆盖新一轮刷新", async () => {
    vi.useFakeTimers();
    const first = deferred<LiveTabInfo[]>();
    const second = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const view = mount(createElement(BackgroundSessions));
    try {
      expect(mocks.listLive).toHaveBeenCalledTimes(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(mocks.listLive).toHaveBeenCalledTimes(2);

      await act(async () => {
        second.resolve([liveTab({ tabId: "new", sessionName: "新会话" })]);
        await second.promise;
      });
      expect(view.container.textContent).toContain("新会话");

      await act(async () => {
        first.resolve([liveTab({ tabId: "old", sessionName: "旧会话" })]);
        await first.promise;
      });
      expect(view.container.textContent).toContain("新会话");
      expect(view.container.textContent).not.toContain("旧会话");
    } finally {
      view.unmount();
      vi.useRealTimers();
    }
  });
});

describe("BackgroundSessions 按所属工作区过滤", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  const s1 = { ...sessionInfo("ssh", "s1"), name: "主机一" };
  const s2 = { ...sessionInfo("ssh", "s2"), name: "主机二" };

  function seedScope(host: { kind: "session" | "tools"; sessionId?: string }, sessionList: SessionInfo[]) {
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

  function detachedRows() {
    return [
      liveTab({ tabId: "t1", sessionId: "s1", sessionName: "主机一", title: "构建" }),
      liveTab({ tabId: "t2", sessionId: "s2", sessionName: "主机二", title: "训练" }),
    ];
  }

  function scopeSelect(view: MountedView): HTMLSelectElement {
    const select = view.container.querySelector<HTMLSelectElement>('select[aria-label="选择会话"]');
    if (!select) throw new Error("选择会话下拉未找到");
    return select;
  }

  it("默认只列出所属工作区会话的后台终端，选择「全部」后放宽", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedScope({ kind: "session", sessionId: "s1" }, [s1, s2]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).not.toContain("训练");
      const select = scopeSelect(view);
      expect(select.value).toBe("s1");
      expect([...select.options].map((o) => o.textContent)).toEqual(["全部", "主机一", "主机二"]);

      setSelectValue(select, "");
      await flush();
      expect(view.container.textContent).toContain("训练");
    } finally {
      view.unmount();
    }
  });

  it("工具工作区或所属会话已消失时回退为显示全部", async () => {
    mocks.listLive.mockResolvedValue(detachedRows());
    seedScope({ kind: "tools" }, [s1, s2]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("构建"));
      expect(view.container.textContent).toContain("训练");
      expect(scopeSelect(view).value).toBe("");
    } finally {
      view.unmount();
    }

    seedScope({ kind: "session", sessionId: "s1" }, [s2]);
    const view2 = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view2.container.textContent).toContain("构建"));
      expect(view2.container.textContent).toContain("训练");
      expect(scopeSelect(view2).value).toBe("");
    } finally {
      view2.unmount();
    }
  });

  it("按 workspaceId 指定的工作区过滤，与当前活动工作区无关", async () => {
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
      expect(scopeSelect(view).value).toBe("s1");
    } finally {
      view.unmount();
    }
  });
});

describe("closing a local terminal ends its process", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setTerminalCloseAction("detach");
    mocks.ask.mockResolvedValue(true);
  });

  it("asks for confirmation and kills instead of detaching", async () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.ask.mock.calls[0][0]).toContain("本地");
    expect(mocks.closeTab).toHaveBeenCalledTimes(1);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-terminal", "kill");
    expect(toastText()).not.toContain("已转入后台");
  });

  it("keeps the tab when the kill confirmation is declined", async () => {
    mocks.ask.mockResolvedValue(false);
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }]);

    await requestCloseTab(tab.id);

    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toHaveLength(1);
  });

  it("hints reflect the local kill and the SSH detach distinction", () => {
    const tab = terminal();
    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], "local");
    expect(closeActionHint(tab)).toBe("结束本地进程");
    expect(closeTabHint(tab)).toBe("关闭标签（结束本地进程）");

    seed([{ id: "p", tabs: [tab], activeTabId: tab.id }], "ssh");
    expect(closeActionHint(tab)).toBe("转入后台运行");
    expect(closeTabHint(tab)).toBe("关闭标签（转入后台运行）");
  });
});
