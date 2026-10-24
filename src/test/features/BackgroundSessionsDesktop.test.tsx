/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { deferred, flush, flushUntil, mount } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    status: vi.fn(),
    linkGet: vi.fn(),
    listLive: vi.fn(),
    listDurable: vi.fn(),
    syncHandlers: [] as Array<() => void>,
    registrationGate: null as Promise<void> | null,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    syncApi: { status: mocks.status, linkGet: mocks.linkGet },
    terminalApi: { listLive: mocks.listLive, listDurable: mocks.listDurable },
  });
});
vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn(async (event: string, handler: () => void) => {
      await mocks.registrationGate;
      if (event === "sync://status") mocks.syncHandlers.push(handler);
      return () => {
        const index = mocks.syncHandlers.indexOf(handler);
        if (index >= 0) mocks.syncHandlers.splice(index, 1);
      };
    }),
  };
});

import { BackgroundSessions } from "../../features/terminal/BackgroundSessions";
import { useUi } from "../../app/store";

describe("desktop background durable account scope", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    mocks.syncHandlers.length = 0;
    mocks.registrationGate = null;
    mocks.listLive.mockResolvedValue([]);
    mocks.listDurable.mockResolvedValue([]);
  });

  it("refreshes the account scope on sync status and discards the previous account's late discovery", async () => {
    let account = "account-a";
    mocks.status.mockImplementation(async () => ({ loggedIn: true, userId: account, username: account }));
    mocks.linkGet.mockImplementation(async () => ({ url: "https://sync.invalid", username: account, insecure: false }));
    mocks.listDurable.mockResolvedValue([{ id: "private-tab", cols: 80, rows: 24, dead: false, title: "账号 A 的任务" }]);
    useUi.setState({
      sessions: [{ id: "session-a", assetId: "asset-a", name: "测试主机", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
      workspaces: [{
        id: "workspace-a", kind: "session", title: "测试主机", sessionId: "session-a", assetId: "asset-a", assetKind: "ssh",
        panes: [{ id: "pane-a", tabs: [], activeTabId: null }], activePaneId: "pane-a", splitRatio: 0.5, closable: true,
      }],
      activeWorkspaceId: "workspace-a",
    });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "workspace-a" }));
    try {
      await flushUntil(() => view.container.textContent?.includes("账号 A 的任务") ?? false);
      expect([...Array(window.localStorage.length)].some((_, index) =>
        window.localStorage.getItem(window.localStorage.key(index) ?? "")?.includes("账号 A 的任务") ?? false)).toBe(false);

      const late = deferred<unknown>();
      mocks.listDurable.mockImplementation(async () => account === "account-a" ? late.promise : []);
      await act(async () => {
        const refresh = [...view.container.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.includes("刷新"));
        refresh?.click();
        await flush();
      });
      const beforeSwitch = mocks.status.mock.calls.length;
      account = "account-b";
      const pendingStatus = deferred<unknown>();
      mocks.status.mockImplementationOnce(() => pendingStatus.promise);
      act(() => {
        for (const handler of [...mocks.syncHandlers]) handler();
      });
      expect(view.container.textContent).not.toContain("账号 A 的任务");
      await act(async () => {
        pendingStatus.resolve({ loggedIn: true, userId: account, username: account });
        await flush();
      });
      await flushUntil(() => mocks.status.mock.calls.length > beforeSwitch);
      await flushUntil(() => !view.container.textContent?.includes("账号 A 的任务"));
      await act(async () => {
        late.resolve([{ id: "private-tab", cols: 80, rows: 24, dead: false, title: "账号 A 的任务" }]);
        await flush();
      });
      expect(view.container.textContent).not.toContain("账号 A 的任务");
      expect(mocks.syncHandlers).toHaveLength(1);
    } finally {
      view.unmount();
    }
    expect(mocks.syncHandlers).toHaveLength(0);
  });

  it("reads identity after the desktop event subscription becomes active", async () => {
    let account = "account-a";
    const registration = deferred<void>();
    mocks.registrationGate = registration.promise;
    mocks.status.mockImplementation(async () => ({ loggedIn: true, userId: account, username: account }));
    mocks.linkGet.mockImplementation(async () => ({ url: "https://sync.invalid", username: account, insecure: false }));
    useUi.setState({
      sessions: [],
      workspaces: [{ id: "tools", kind: "tools", title: "工具", panes: [{ id: "pane", tabs: [], activeTabId: null }], activePaneId: "pane", splitRatio: 0.5, closable: true }],
      activeWorkspaceId: "tools",
    });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "tools" }));
    try {
      expect(mocks.status).not.toHaveBeenCalled();
      account = "account-b";
      await act(async () => {
        registration.resolve();
        await flush();
      });
      await flushUntil(() => mocks.status.mock.calls.length === 1);
      expect(mocks.syncHandlers).toHaveLength(1);
    } finally {
      view.unmount();
    }
  });

  it("hides in-memory terminal titles across login, account switch, and logout", async () => {
    let account: { id: string; username: string } | null = null;
    mocks.status.mockImplementation(async () => ({ loggedIn: account !== null, userId: account?.id ?? "", username: account?.username ?? "" }));
    mocks.linkGet.mockImplementation(async () => ({ url: "https://sync.invalid", username: account?.username ?? "", insecure: false }));
    mocks.listDurable.mockImplementation(async () => [{
      id: "private-tab", cols: 80, rows: 24, dead: false,
      title: account?.id === "a" ? "账号 A 的私有任务" : "账号 B 的私有任务",
    }]);
    const connected = { id: "session-a", assetId: "asset-a", name: "测试主机", kind: "ssh", status: "connected" as const, tabs: [], createdAt: 0 };
    useUi.setState({
      sessions: [],
      workspaces: [{
        id: "workspace-a", kind: "session", title: "测试主机", sessionId: "session-a", assetId: "asset-a", assetKind: "ssh",
        panes: [{ id: "pane-a", tabs: [], activeTabId: null }], activePaneId: "pane-a", splitRatio: 0.5, closable: true,
      }],
      activeWorkspaceId: "workspace-a",
    });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "workspace-a" }));
    try {
      await flushUntil(() => mocks.syncHandlers.length === 1);
      await flush();
      expect(view.container.textContent).toContain("尚未查找的终端");

      account = { id: "a", username: "alice" };
      act(() => { for (const handler of [...mocks.syncHandlers]) handler(); });
      await flush();
      act(() => useUi.setState({ sessions: [connected] }));
      await flushUntil(() => view.container.textContent?.includes("账号 A 的私有任务") ?? false);
      act(() => useUi.setState({ sessions: [{ ...connected, status: "disconnected" }] }));
      expect(view.container.textContent).toContain("账号 A 的私有任务");

      account = { id: "b", username: "bob" };
      act(() => { for (const handler of [...mocks.syncHandlers]) handler(); });
      expect(view.container.textContent).not.toContain("账号 A 的私有任务");
      await flush();
      expect(view.container.textContent).toContain("尚未查找的终端");
      act(() => useUi.setState({ sessions: [connected] }));
      await flushUntil(() => view.container.textContent?.includes("账号 B 的私有任务") ?? false);
      act(() => useUi.setState({ sessions: [{ ...connected, status: "disconnected" }] }));

      account = null;
      act(() => { for (const handler of [...mocks.syncHandlers]) handler(); });
      expect(view.container.textContent).not.toContain("账号 B 的私有任务");
      await flush();
      expect(view.container.textContent).toContain("尚未查找的终端");
      expect([...Array(window.localStorage.length)].some((_, index) =>
        window.localStorage.getItem(window.localStorage.key(index) ?? "")?.includes("私有任务") ?? false)).toBe(false);
    } finally {
      view.unmount();
    }
  });

  it("restores the current account's in-memory hint after an unchanged status event", async () => {
    const status = { loggedIn: true, userId: "account-a", username: "alice" };
    mocks.status.mockResolvedValue(status);
    mocks.linkGet.mockResolvedValue({ url: "https://sync.invalid", username: "alice", insecure: false });
    mocks.listDurable.mockResolvedValue([{ id: "known-tab", cols: 80, rows: 24, dead: false, title: "当前账号的任务" }]);
    const connected = { id: "session-a", assetId: "asset-a", name: "测试主机", kind: "ssh", status: "connected" as const, tabs: [], createdAt: 0 };
    useUi.setState({
      sessions: [connected],
      workspaces: [{
        id: "workspace-a", kind: "session", title: "测试主机", sessionId: "session-a", assetId: "asset-a", assetKind: "ssh",
        panes: [{ id: "pane-a", tabs: [], activeTabId: null }], activePaneId: "pane-a", splitRatio: 0.5, closable: true,
      }],
      activeWorkspaceId: "workspace-a",
    });
    const view = mount(createElement(BackgroundSessions, { workspaceId: "workspace-a" }));
    try {
      await flushUntil(() => view.container.textContent?.includes("当前账号的任务") ?? false);
      act(() => useUi.setState({ sessions: [{ ...connected, status: "disconnected" }] }));
      const pending = deferred<unknown>();
      mocks.status.mockImplementationOnce(() => pending.promise);
      act(() => { for (const handler of [...mocks.syncHandlers]) handler(); });
      expect(view.container.textContent).not.toContain("当前账号的任务");
      await act(async () => {
        pending.resolve(status);
        await flush();
      });
      expect(view.container.textContent).toContain("当前账号的任务");
    } finally {
      view.unmount();
    }
  });
});
