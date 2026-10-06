/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";

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
import {
  closeActionHint,
  closeTabHint,
  requestCloseTab,
  useUi,
  type AppTab,
  type Pane,
  type Workspace,
} from "../../app/store";
import { flush, mount, waitFor, type MountedView } from "./reactTestUtils";
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

describe("BackgroundSessions hides local terminals", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
  });

  it("lists detached SSH tabs but never local ones", async () => {
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
    expect(view.container.textContent).not.toContain("本机");
    expect(view.container.textContent).not.toContain("有人看着");
    expect(view.container.textContent).not.toContain("已结束");
    view.unmount();
  });
});

describe("closing a local terminal ends its process", () => {
  beforeEach(() => {
    vi.clearAllMocks();
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
