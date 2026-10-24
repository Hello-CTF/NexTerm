/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  listSessions: vi.fn(),
  reconnectSession: vi.fn(),
  connectSession: vi.fn(),
  connectDevice: vi.fn(),
  probeHostKey: vi.fn(),
  closeTab: vi.fn(),
  resyncSessions: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoiceRemember: mocks.askChoiceRemember }));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: {
      list: mocks.listSessions,
      reconnect: mocks.reconnectSession,
      connect: mocks.connectSession,
      connectDevice: mocks.connectDevice,
      probeHostKey: mocks.probeHostKey,
    },
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable, closeTab: mocks.closeTab },
  });
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { useAuth } from "../../features/auth/store";
import { setTerminalCloseAction } from "../../app/preferences";
import {
  assetConnectionProblemKey,
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
import type { AccountUser } from "../../ipc/authApi";

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
    sessionId: "s",
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

function storageContents(): string {
  return Array.from({ length: window.localStorage.length }, (_, index) => {
    const key = window.localStorage.key(index) ?? "";
    return `${key} ${window.localStorage.getItem(key) ?? ""}`;
  }).join("\n");
}

describe("BackgroundSessions host-scoped view", () => {
  const s1 = { ...sessionInfo("ssh", "s1"), name: "主机一" };
  const s2 = { ...sessionInfo("ssh", "s2"), name: "主机二" };

  beforeEach(() => {
    vi.resetAllMocks();
    window.localStorage.clear();
    useAuth.setState({ user: null });
    mocks.ask.mockResolvedValue(true);
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
    mocks.listSessions.mockResolvedValue([s1, s2]);
    mocks.reconnectSession.mockResolvedValue(true);
    mocks.connectSession.mockResolvedValue(s1);
    mocks.connectDevice.mockResolvedValue({ ...s1, kind: "device", assetId: "device/dev-1" });
    mocks.probeHostKey.mockResolvedValue({ state: "known" });
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

  it("explains how to find existing durable tabs when an SSH asset is in ordinary mode", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) =>
        workspace.id === "ws-host"
          ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
          : workspace,
      ),
    }));
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("这台主机上没有在后台运行的终端"));
      expect(view.container.textContent).toContain("持久终端关闭标签后会继续在主机上运行并出现在这里；普通模式的终端关闭后会结束");
    } finally {
      view.unmount();
    }
  });

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

  it("discovers and takes over durable device tabs only from the owning device", async () => {
    seedScope();
    const device = { ...s1, kind: "device", assetId: "device/dev-1", name: "设备一" };
    const other = { ...s2, kind: "device", assetId: "device/dev-2", name: "设备二" };
    useUi.setState((state) => ({
      sessions: [device, other],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: device.assetId, assetKind: "device" }
        : workspace),
    }));
    mocks.listSessions.mockResolvedValue([device, other]);
    mocks.listDurable.mockImplementation(async (id: string) => id === "s1"
      ? [{ id: "device-tab", cols: 80, rows: 24, dead: false, title: "设备任务" }]
      : [{ id: "other-tab", cols: 80, rows: 24, dead: false, title: "其他设备" }]);
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("设备任务"));
      expect(view.container.textContent).not.toContain("其他设备");
      expect(mocks.listDurable).toHaveBeenCalledWith("s1");
      expect(mocks.listDurable).not.toHaveBeenCalledWith("s2");
      await act(async () => {
        actionButton(view, "接管").click();
        await flush();
      });
      expect(useUi.getState().workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)).toContainEqual(expect.objectContaining({ sessionId: "s1", tabId: "device-tab" }));
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
      expect(view.container.textContent).not.toContain("这台主机上没有在后台运行的终端");
    } finally {
      view.unmount();
    }
  });

  it("offers scoped recovery for a disconnected SSH host and lists durable tabs only after reconnect", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one", status: "disconnected" }, s2],
      workspaces: state.workspaces.map((workspace) =>
        workspace.id === "ws-host"
          ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
          : workspace,
      ),
    }));
    mocks.listSessions.mockResolvedValue([{ ...s1, assetId: "asset-host-one" }, s2]);
    mocks.listDurable.mockResolvedValue([
      { id: "resumed-shell", cols: 80, rows: 24, dead: false, title: "远端构建" },
    ]);
    const view = mountHost();
    try {
      await flush();
      expect(view.container.textContent).toContain("主机一");
      expect(view.container.textContent).toContain("连接主机后才能确认是否仍在运行");
      expect(mocks.listDurable).not.toHaveBeenCalled();

      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.reconnectSession).toHaveBeenCalledWith("s1"));
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("s1"));
      await waitFor(() => expect(view.container.textContent).toContain("远端构建"));
      expect(mocks.listDurable).not.toHaveBeenCalledWith("s2");
    } finally {
      view.unmount();
    }
  });

  it("records scoped current-session recovery failure and clears it after retry", async () => {
    useUi.setState({ workspaceRuntime: {}, connectionProblems: {} });
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one", status: "disconnected" }, s2],
      workspaces: state.workspaces.map((workspace) =>
        workspace.id === "ws-host"
          ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
          : workspace,
      ),
    }));
    mocks.listSessions.mockResolvedValue([{ ...s1, assetId: "asset-host-one" }, s2]);
    mocks.reconnectSession.mockResolvedValueOnce(false).mockResolvedValue(true);
    const view = mountHost();
    try {
      await flush();
      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(toastText()).toContain("重新连接失败：重连未能启动"));
      expect(useUi.getState().workspaceRuntime["ws-host"]?.problems?.[0]).toMatchObject({
        connectionKey: assetConnectionProblemKey("asset-host-one"),
        summary: "重连未能启动",
      });

      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("s1"));
      expect(useUi.getState().workspaceRuntime["ws-host"]?.problems ?? []).toEqual([]);
    } finally {
      view.unmount();
    }
  });

  it("keeps a known durable tab visible but inactive after a host disconnects", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "known-shell", cols: 80, rows: 24, dead: false, title: "持续编译" }]);
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("持续编译"));
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => session.id === "s1"
          ? { ...session, status: "disconnected" as const } : session) }));
        await flush();
      });
      const row = [...view.container.querySelectorAll("tbody tr")].find((candidate) => candidate.textContent?.includes("持续编译"));
      expect(row?.textContent).toContain("待核实");
      expect(row?.textContent).toContain("连接并查找");
      expect(row?.textContent).not.toContain("接管");
      expect(row?.textContent).not.toContain("结束进程");
      expect(mocks.closeTab).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("uses the initial quick workspace session binding without reconnecting by name", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: null, name: "与工作区名称不同" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? {
          ...workspace,
          assetId: "",
          assetKind: "ssh",
          quickTarget: { id: "quick-one", host: "example.invalid", port: 22, username: "tester", authKind: "agent" as const },
        }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "quick-shell", cols: 80, rows: 24, dead: false, title: "快速连接的终端" }]);
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("快速连接的终端"));
      expect(mocks.listDurable).toHaveBeenCalledWith("s1");
      expect(view.container.textContent).not.toContain("连接并查找");
      expect(mocks.connectSession).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("shows a restorable layout terminal before the old host session exists", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? {
          ...workspace, sessionId: undefined, assetId: "asset-host-one", assetKind: "ssh",
          panes: [{ id: "host-pane", tabs: [{ ...terminal("layout-task", "s1"), title: "布局里的任务", restorePending: true }], activeTabId: "layout-task" }],
        }
        : workspace),
    }));
    const view = mount(createElement(BackgroundSessions, { workspaceId: "ws-host" }));
    try {
      await flush();
      const row = [...view.container.querySelectorAll("tbody tr")].find((candidate) => candidate.textContent?.includes("布局里的任务"));
      expect(row?.textContent).toContain("待核实");
      expect(row?.textContent).toContain("连接并查找");
      expect(row?.textContent).not.toContain("接管");
      expect(mocks.listDurable).not.toHaveBeenCalled();
    } finally {
      view.unmount();
    }
  });

  it("shows a generic restore hint after restart and verifies the terminal on its owning host", async () => {
    useAuth.setState({ user: { id: "account-a" } as AccountUser });
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "saved-shell", cols: 80, rows: 24, dead: false, title: "远端任务" }]);
    const first = mountHost();
    try {
      await flush();
      await waitFor(() => expect(first.container.textContent).toContain("远端任务"));
    } finally {
      first.unmount();
    }
    expect(storageContents()).not.toContain("远端任务");
    expect(storageContents()).not.toContain("saved-shell");

    useUi.setState((state) => ({ sessions: [s2], workspaces: state.workspaces.map((workspace) =>
      workspace.id === "ws-host" ? { ...workspace, sessionId: undefined } : workspace) }));
    mocks.listDurable.mockClear();
    const second = mountHost();
    try {
      const row = [...second.container.querySelectorAll("tbody tr")].find((candidate) => candidate.textContent?.includes("尚未查找的终端"));
      expect(row?.textContent).toContain("待核实");
      expect(row?.textContent).not.toContain("接管");
      expect(second.container.textContent).not.toContain("远端任务");
      expect(mocks.listDurable).not.toHaveBeenCalled();

      mocks.connectSession.mockResolvedValue({ ...s1, id: "fresh-host", assetId: "asset-host-one" });
      mocks.listDurable.mockResolvedValue([{ id: "saved-shell", cols: 80, rows: 24, dead: false, title: "远端任务" }]);
      await act(async () => {
        actionButton(second, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("fresh-host"));
      expect(mocks.listDurable).not.toHaveBeenCalledWith("s1");
      expect(second.container.textContent).toContain("接管");
    } finally {
      second.unmount();
    }
  });

  it("does not expose another account's in-memory terminal or enumerate a mismatched session", async () => {
    useAuth.setState({ user: { id: "account-a" } as AccountUser });
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "private-shell", cols: 80, rows: 24, dead: false, title: "仅账号 A 可见" }]);
    const first = mountHost();
    try {
      await flush();
      await waitFor(() => expect(first.container.textContent).toContain("仅账号 A 可见"));
    } finally {
      first.unmount();
    }

    useAuth.setState({ user: { id: "account-b" } as AccountUser });
    useUi.setState((state) => ({ sessions: [{ ...s1, assetId: "another-asset" }, s2], workspaces: state.workspaces.map((workspace) =>
      workspace.id === "ws-host" ? { ...workspace, sessionId: "s1" } : workspace) }));
    mocks.listDurable.mockClear();
    const second = mountHost();
    try {
      await flush();
      expect(second.container.textContent).not.toContain("仅账号 A 可见");
      expect(second.container.textContent).toContain("连接并查找");
      expect(mocks.listDurable).not.toHaveBeenCalled();
      mocks.connectSession.mockResolvedValue({ ...s1, id: "fresh-a", assetId: "asset-host-one" });
      mocks.listDurable.mockResolvedValue([]);
      await act(async () => {
        actionButton(second, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("fresh-a"));
      expect(mocks.listDurable).not.toHaveBeenCalledWith("s1");
    } finally {
      second.unmount();
    }
  });

  it("hides previously discovered terminal names immediately when the account changes", async () => {
    useAuth.setState({ user: { id: "account-a" } as AccountUser });
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "private-shell", cols: 80, rows: 24, dead: false, title: "账号 A 的任务" }]);
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("账号 A 的任务"));
      act(() => {
        useAuth.setState({ user: { id: "account-b" } as AccountUser });
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => session.id === "s1"
          ? { ...session, status: "disconnected" as const } : session) }));
      });
      expect(view.container.textContent).not.toContain("账号 A 的任务");
      expect(view.container.textContent).toContain("尚未查找的终端");
    } finally {
      view.unmount();
    }
  });

  it("does not retain host or tab titles without a restored workspace after account switch and restart", async () => {
    useAuth.setState({ user: { id: "account-a" } as AccountUser });
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-host-one" }, s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, title: "缓存主机", assetId: "asset-host-one", assetKind: "ssh" }
        : workspace),
    }));
    mocks.listDurable.mockResolvedValue([{ id: "saved-only", cols: 80, rows: 24, dead: false, title: "私有任务" }]);
    const first = mountHost();
    try {
      await flush();
      await waitFor(() => expect(first.container.textContent).toContain("私有任务"));
    } finally {
      first.unmount();
    }

    useAuth.setState({ user: { id: "account-b" } as AccountUser });

    useUi.setState({
      sessions: [],
      workspaces: [{ id: "tools", kind: "tools", title: "工具", panes: [{ id: "p", tabs: [], activeTabId: null }], activePaneId: "p", splitRatio: 0.5, closable: true }],
      activeWorkspaceId: "tools",
    });
    mocks.listDurable.mockClear();
    const second = mount(createElement(BackgroundSessions, { workspaceId: "tools" }));
    try {
      await flush();
      expect(second.container.textContent).not.toContain("私有任务");
      expect(second.container.textContent).not.toContain("缓存主机");
      expect(storageContents()).not.toContain("私有任务");
      expect(storageContents()).not.toContain("saved-only");
      expect(mocks.listDurable).not.toHaveBeenCalled();
    } finally {
      second.unmount();
    }
  });

  it("reconnects a lost SSH session through its owning asset and rebinds before discovery", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [s2],
      workspaces: state.workspaces.map((workspace) =>
        workspace.id === "ws-host"
          ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
          : workspace,
      ),
    }));
    mocks.connectSession.mockResolvedValue({ ...s1, id: "fresh-s1", assetId: "asset-host-one" });
    mocks.listDurable.mockResolvedValue([
      { id: "restored-shell", cols: 80, rows: 24, dead: false, title: "持续任务" },
    ]);
    const view = mountHost();
    try {
      await flush();
      expect(mocks.listDurable).not.toHaveBeenCalled();
      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.connectSession).toHaveBeenCalledWith("asset-host-one"));
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("fresh-s1"));
      expect(useUi.getState().workspaces.find((workspace) => workspace.id === "ws-host")?.sessionId).toBe("fresh-s1");
      expect(mocks.listDurable).not.toHaveBeenCalledWith("s1");
      expect(view.container.textContent).toContain("持续任务");
    } finally {
      view.unmount();
    }
  });

  it("reconnects a lost device session through the device identity before discovery", async () => {
    seedScope();
    const device = { ...s1, id: "fresh-device", kind: "device", assetId: "device/dev-1", name: "设备一" };
    useUi.setState((state) => ({
      sessions: [s2],
      workspaces: state.workspaces.map((workspace) => workspace.id === "ws-host"
        ? { ...workspace, assetId: device.assetId, assetKind: "device", title: device.name }
        : workspace),
    }));
    mocks.connectDevice.mockResolvedValue(device);
    mocks.listDurable.mockResolvedValue([
      { id: "device-shell", cols: 80, rows: 24, dead: false, title: "持续设备任务" },
    ]);
    const view = mountHost();
    try {
      await flush();
      expect(mocks.listDurable).not.toHaveBeenCalled();
      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      await waitFor(() => expect(mocks.connectDevice).toHaveBeenCalledWith("dev-1", "设备一"));
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("fresh-device"));
      expect(useUi.getState().workspaces.find((workspace) => workspace.id === "ws-host")?.sessionId).toBe("fresh-device");
      expect(view.container.textContent).toContain("持续设备任务");
    } finally {
      view.unmount();
    }
  });

  it("never enumerates or attaches a session whose ID points at another asset", async () => {
    seedScope();
    useUi.setState((state) => ({
      sessions: [{ ...s1, assetId: "asset-other" }, s2],
      workspaces: state.workspaces.map((workspace) =>
        workspace.id === "ws-host"
          ? { ...workspace, assetId: "asset-host-one", assetKind: "ssh" }
          : workspace,
      ),
    }));
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "wrong-shell", sessionId: "s1", sessionName: "其他主机", title: "不能接管" }),
    ]);
    mocks.connectSession.mockResolvedValue({ ...s1, id: "fresh-s1", assetId: "asset-host-one" });
    mocks.listDurable.mockResolvedValue([
      { id: "right-shell", cols: 80, rows: 24, dead: false, title: "本主机任务" },
    ]);
    const view = mountHost();
    try {
      await flush();
      expect(view.container.textContent).not.toContain("不能接管");
      expect(mocks.listDurable).not.toHaveBeenCalled();
      await act(async () => {
        actionButton(view, "连接并查找").click();
        await flush();
      });
      expect(mocks.reconnectSession).not.toHaveBeenCalled();
      expect(mocks.connectSession).toHaveBeenCalledWith("asset-host-one");
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("fresh-s1"));
      expect(view.container.textContent).toContain("本主机任务");
    } finally {
      view.unmount();
    }
  });

  it("does not offer recovery or attach a tab when no SSH asset owns the disconnected session", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "old-shell", sessionId: "s1", sessionName: "主机一", title: "旧任务" }),
    ]);
    seedScope();
    useUi.setState({ sessions: [{ ...s1, status: "disconnected" }, s2] });
    mocks.listSessions.mockResolvedValue([{ ...s1, status: "disconnected" }, s2]);
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("旧任务"));
      expect(view.container.textContent).not.toContain("连接并查找");
      expect(mocks.listDurable).not.toHaveBeenCalled();
      await act(async () => {
        actionButton(view, "接管").click();
        await flush();
      });
      expect(view.container.textContent).toContain("旧任务");
      expect(toastText()).toContain("主机未连接，请先重新连接再接管");
      expect(useUi.getState().workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs).some((tab) => tab.tabId === "old-shell")).toBe(false);
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
      expect(view.container.textContent).toContain("不知道该查看哪台主机");
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
      expect(view.container.textContent).not.toContain("这台主机上没有在后台运行的终端");

      await act(async () => {
        pending.resolve([]);
        await pending.promise;
      });
      expect(view.container.textContent).toContain("这台主机上没有在后台运行的终端");
      expect(view.container.querySelector('tr[aria-hidden="true"]')).toBeNull();
      expect(view.container.textContent).not.toContain("无法读取后台会话");
    } finally {
      view.unmount();
    }
  });

  it("shows an explicit empty state for a tab without output", async () => {
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "empty", sessionId: "s1", sessionName: "主机一", title: "空终端", lastOutputAt: 0, lastOutputMsAgo: 0 }),
    ]);
    seedScope();
    const view = mountHost();
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("暂无输出"));
      expect(view.container.textContent).toContain("空终端");
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
      await waitFor(() => expect(view.container.textContent).toContain("无法读取后台会话"));
      expect(view.container.textContent).toContain("database unavailable");
      expect(view.container.textContent).not.toContain("这台主机上没有在后台运行的终端");
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

  function seedSharedHostWorkspaces(firstStatus: SessionInfo["status"], secondStatus: SessionInfo["status"]) {
    seedWorkspaceScope({ kind: "tools" }, []);
    const first: Workspace = {
      ...ws([{ id: "first-pane", tabs: [{ ...terminal("first-task", "s1"), title: "第一个工作区的任务", restorePending: true }], activeTabId: "first-task" }]),
      id: "first-host", title: "同一主机", sessionId: "s1", assetId: "shared-asset", assetKind: "ssh",
    };
    const second: Workspace = {
      ...ws([{ id: "second-pane", tabs: [
        { ...terminal("second-task", "s2"), title: "第二个工作区的任务", restorePending: true },
        { ...terminal("duplicate-first", "s2"), tabId: "kernel-first-task", title: "第一个工作区的任务", restorePending: true },
      ], activeTabId: "second-task" }]),
      id: "second-host", title: "同一主机", sessionId: "s2", assetId: "shared-asset", assetKind: "ssh",
    };
    useUi.setState((state) => ({
      workspaces: [...state.workspaces, first, second],
      sessions: [{ ...s1, assetId: "shared-asset", status: firstStatus }, { ...s2, assetId: "shared-asset", status: secondStatus }],
    }));
  }

  it("shows pending terminals from every workspace of the same host", async () => {
    seedSharedHostWorkspaces("disconnected", "disconnected");
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.textContent).toContain("第一个工作区的任务");
      expect(view.container.textContent).toContain("第二个工作区的任务");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(2);
    } finally {
      view.unmount();
    }
  });

  it("does not show a pending host when its second workspace is connected", async () => {
    seedSharedHostWorkspaces("disconnected", "connected");
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.textContent).not.toContain("尚未查找的终端");
      expect(view.container.textContent).not.toContain("第一个工作区的任务");
      expect(view.container.textContent).not.toContain("连接并查找");
    } finally {
      view.unmount();
    }
  });

  it("lists one takeable terminal when two sessions discover the same host tab", async () => {
    seedSharedHostWorkspaces("connected", "connected");
    mocks.listSessions.mockResolvedValue(useUi.getState().sessions);
    mocks.listDurable.mockResolvedValue([{ id: "shared-tab", cols: 80, rows: 24, dead: false, title: "共享任务" }]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(mocks.listDurable).toHaveBeenCalledWith("s2"));
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);
      expect(view.container.textContent).toContain("共享任务");
      await act(async () => {
        actionButton(view, "接管").click();
        await flush();
      });
      expect(useUi.getState().workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)).toContainEqual(expect.objectContaining({ sessionId: "s1", tabId: "shared-tab" }));
    } finally {
      view.unmount();
    }
  });

  it("deduplicates a live tab against another session's durable inventory", async () => {
    seedSharedHostWorkspaces("connected", "connected");
    mocks.listLive.mockResolvedValue([liveTab({ tabId: "shared-tab", sessionId: "s1", sessionName: "主机一", title: "现场任务" })]);
    mocks.listDurable.mockResolvedValue([{ id: "shared-tab", cols: 80, rows: 24, dead: false, title: "持久任务" }]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);
      expect(view.container.textContent).toContain("现场任务");
      expect(view.container.textContent).not.toContain("持久任务");
    } finally {
      view.unmount();
    }
  });

  it("does not let an exited live tab hide another session's durable tab", async () => {
    seedSharedHostWorkspaces("connected", "connected");
    mocks.listLive.mockResolvedValue([liveTab({
      tabId: "shared-tab", sessionId: "s1", sessionName: "主机一", title: "已退出任务", exited: true,
    })]);
    mocks.listDurable.mockImplementation(async (sessionId: string) => sessionId === "s2"
      ? [{ id: "shared-tab", cols: 80, rows: 24, dead: false, title: "仍在运行的任务" }]
      : []);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);
      expect(view.container.textContent).toContain("仍在运行的任务");
      expect(view.container.textContent).not.toContain("已退出任务");
    } finally {
      view.unmount();
    }
  });

  it("remembers the union of durable tabs discovered by sessions on one host", async () => {
    seedSharedHostWorkspaces("connected", "connected");
    useUi.setState((state) => ({ workspaces: state.workspaces.map((workspace) => workspace.kind === "session"
      ? { ...workspace, panes: workspace.panes.map((pane) => ({ ...pane, tabs: [], activeTabId: null })) }
      : workspace) }));
    mocks.listDurable.mockImplementation(async (sessionId: string) => [{
      id: sessionId === "s1" ? "first-durable" : "second-durable",
      cols: 80, rows: 24, dead: false,
      title: sessionId === "s1" ? "第一个持久任务" : "第二个持久任务",
    }]);
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.textContent).toContain("第一个持久任务");
      expect(view.container.textContent).toContain("第二个持久任务");
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "disconnected" as const })) }));
        await flush();
      });
      expect(view.container.textContent).toContain("第一个持久任务");
      expect(view.container.textContent).toContain("第二个持久任务");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(2);
    } finally {
      view.unmount();
    }
  });

  it("keeps prior host inventory when one session's refresh fails", async () => {
    seedSharedHostWorkspaces("connected", "connected");
    useUi.setState((state) => ({ workspaces: state.workspaces.map((workspace) => workspace.kind === "session"
      ? { ...workspace, panes: workspace.panes.map((pane) => ({ ...pane, tabs: [], activeTabId: null })) }
      : workspace) }));
    let inventory: "initial" | "partial" | "complete" | "empty" = "initial";
    mocks.listDurable.mockImplementation(async (sessionId: string) => {
      if (inventory === "partial" && sessionId === "s1") throw new Error("inventory unavailable");
      if (inventory === "empty" || (inventory === "complete" && sessionId === "s1")) return [];
      const id = inventory === "initial" ? sessionId === "s1" ? "first-durable" : "second-durable" : "new-durable";
      const title = inventory === "initial" ? sessionId === "s1" ? "第一个持久任务" : "第二个持久任务" : "新发现任务";
      return [{ id, cols: 80, rows: 24, dead: false, title }];
    });
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      expect(view.container.textContent).toContain("第一个持久任务");
      expect(view.container.textContent).toContain("第二个持久任务");
      inventory = "partial";
      await act(async () => {
        actionButton(view, "刷新").click();
        await flush();
      });
      expect(view.container.textContent).toContain("新发现任务");
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "disconnected" as const })) }));
        await flush();
      });
      expect(view.container.textContent).toContain("第一个持久任务");
      expect(view.container.textContent).toContain("第二个持久任务");
      expect(view.container.textContent).toContain("新发现任务");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(3);

      inventory = "complete";
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
        await flush();
      });
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "disconnected" as const })) }));
        await flush();
      });
      expect(view.container.textContent).not.toContain("第一个持久任务");
      expect(view.container.textContent).not.toContain("第二个持久任务");
      expect(view.container.textContent).toContain("新发现任务");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);

      inventory = "empty";
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "connected" as const })) }));
        await flush();
      });
      await act(async () => {
        useUi.setState((state) => ({ sessions: state.sessions.map((session) => ({ ...session, status: "disconnected" as const })) }));
        await flush();
      });
      expect(view.container.textContent).not.toContain("新发现任务");
      expect(view.container.textContent).toContain("尚未查找的终端");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(1);
    } finally {
      view.unmount();
    }
  });

  it("keeps matching terminal IDs from different hosts separate", async () => {
    const firstHost = { ...s1, assetId: "first-asset" };
    const secondHost = { ...s2, assetId: "second-asset" };
    mocks.listLive.mockResolvedValue([
      liveTab({ tabId: "shared", sessionId: "s1", sessionName: "主机一", title: "主机一任务" }),
    ]);
    mocks.listDurable.mockImplementation(async (sessionId: string) =>
      sessionId === "s2"
        ? [{ id: "shared", cols: 80, rows: 24, dead: false, title: "主机二任务" }]
        : [],
    );
    seedWorkspaceScope({ kind: "tools" }, [firstHost, secondHost]);
    mocks.listSessions.mockResolvedValue([firstHost, secondHost]);
    useUi.setState({ resyncSessions: mocks.resyncSessions });
    const view = mount(createElement(BackgroundSessions));
    try {
      await flush();
      await waitFor(() => expect(view.container.textContent).toContain("主机二任务"));
      expect(view.container.textContent).toContain("主机一任务");
      expect(view.container.querySelectorAll("tbody tr")).toHaveLength(2);

      const firstRow = [...view.container.querySelectorAll("tbody tr")].find((row) => row.textContent?.includes("主机一任务"));
      const killFirst = [...(firstRow?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.includes("结束进程"));
      if (!killFirst) throw new Error("主机一的结束按钮未找到");
      await act(async () => {
        killFirst.click();
        await flush();
      });
      await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("shared", "kill", "s1"));
      expect(view.container.textContent).toContain("主机二任务");
      expect(view.container.textContent).not.toContain("主机一任务");

      const secondRow = [...view.container.querySelectorAll("tbody tr")].find((row) => row.textContent?.includes("主机二任务"));
      const takeOverSecond = [...(secondRow?.querySelectorAll("button") ?? [])].find((button) => button.textContent?.includes("接管"));
      if (!takeOverSecond) throw new Error("主机二的接管按钮未找到");
      await act(async () => {
        takeOverSecond.click();
        await flush();
      });
      expect(useUi.getState().workspaces.flatMap((workspace) => workspace.panes)
        .flatMap((pane) => pane.tabs)).toContainEqual(expect.objectContaining({ sessionId: "s2", tabId: "shared" }));
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
      await waitFor(() => expect(mocks.closeTab).toHaveBeenCalledWith("t1", "kill", "s1"));
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
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([{ ...tab, exited: false }]);
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

    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([{ ...tab, exited: false, closeError: "boom" }]);
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
