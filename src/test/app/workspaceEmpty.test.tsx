/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, deferred, flush, mount, waitForRendered, type MountedView } from "../features/reactTestUtils";
import type { Workspace } from "../../app/store";

const mocks = vi.hoisted(() => ({
  reopen: vi.fn(),
  connect: vi.fn(),
  assetGet: vi.fn(),
  listLive: vi.fn(),
  listDurable: vi.fn(),
  sessionList: vi.fn(),
}));

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, reopenRemoteDesktopTab: mocks.reopen, connectAsset: mocks.connect };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    terminalApi: { ...actual.terminalApi, listLive: mocks.listLive, listDurable: mocks.listDurable },
    sessionApi: { ...actual.sessionApi, list: mocks.sessionList },
    assetApi: { ...actual.assetApi, get: mocks.assetGet },
  };
});

import { useUi } from "../../app/store";
import { WorkspaceEmpty } from "../../app/workspace/EmptyStates";

let view: MountedView | null = null;

function workspace(patch: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "prod",
    panes: [{ id: "p1", tabs: [], activeTabId: null }],
    activePaneId: "p1",
    closable: true,
    ...patch,
  };
}

function render(onNew = vi.fn()) {
  view = mount(createElement(WorkspaceEmpty, { onNew, workspaceId: "ws", paneId: "p1" }));
  return onNew;
}

function buttons(): string[] {
  return [...view!.container.querySelectorAll("button")].map((b) => b.textContent ?? "");
}

function text(): string {
  return view!.container.textContent ?? "";
}

beforeEach(() => {
  mocks.reopen.mockReset().mockResolvedValue({ ok: true });
  mocks.connect.mockReset().mockResolvedValue({ ok: true });
  mocks.assetGet.mockReset().mockResolvedValue({ id: "db-asset", name: "orders", kind: "postgres" });
  mocks.listLive.mockReset().mockResolvedValue([]);
  mocks.listDurable.mockReset().mockResolvedValue([]);
  mocks.sessionList.mockReset().mockResolvedValue([]);
  useUi.setState({ toasts: [], sessions: [] });
});

afterEach(() => {
  view?.unmount();
  view = null;
});

describe("WorkspaceEmpty", () => {
  it("keeps the terminal prompt for a session workspace", () => {
    useUi.setState({ workspaces: [workspace()] });
    const onNew = render();
    expect(text()).toContain("暂无终端");
    click(view!.container.querySelector("button")!);
    expect(onNew).toHaveBeenCalledTimes(1);
    expect(mocks.reopen).not.toHaveBeenCalled();
  });

  it("lists the host's background 持久终端 next to 新建终端 and takes one over into this pane", async () => {
    const session = { id: "s1", assetId: "a1", name: "prod", kind: "ssh", status: "connected" as const, tabs: [], createdAt: 0 };
    useUi.setState({
      sessions: [session],
      workspaces: [
        workspace({
          sessionId: "s1",
          assetId: "a1",
          assetKind: "ssh",
          panes: [
            { id: "p0", tabs: [], activeTabId: null },
            { id: "p1", tabs: [], activeTabId: null },
          ],
          activePaneId: "p0",
        }),
      ],
      activeWorkspaceId: "ws",
    });
    mocks.listDurable.mockResolvedValue([
      { id: "d1", title: "deploy", windowTitle: "", cols: 80, rows: 24, dead: false, createdAt: 1 },
      { id: "d2", title: "", windowTitle: "", cols: 80, rows: 24, dead: true, createdAt: 1 },
    ]);
    mocks.sessionList.mockResolvedValue([session]);
    render();
    await waitForRendered(() => expect(buttons()).toEqual(["新建终端", "接管"]));
    expect(text()).toContain("后台运行的持久终端");
    expect(text()).toContain("deploy");
    expect(mocks.listDurable).toHaveBeenCalledWith("s1");

    click(view!.container.querySelector<HTMLButtonElement>('button[aria-label="接管 deploy"]')!);
    await waitForRendered(() =>
      expect(useUi.getState().workspaces[0]?.panes.find((pane) => pane.id === "p1")?.tabs).toEqual([
        expect.objectContaining({ kind: "terminal", sessionId: "s1", tabId: "d1", title: "deploy" }),
      ]),
    );
    expect(useUi.getState().workspaces[0]?.panes.find((pane) => pane.id === "p0")?.tabs).toEqual([]);
  });

  it("picks up background terminals the host lists only after its helper started", async () => {
    vi.useFakeTimers();
    try {
      useUi.setState({
        sessions: [{ id: "s1", assetId: "a1", name: "prod", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
        workspaces: [workspace({ sessionId: "s1", assetId: "a1", assetKind: "ssh" })],
      });
      render();
      await flush();
      expect(buttons()).toEqual(["新建终端"]);

      mocks.listDurable.mockResolvedValue([{ id: "d1", title: "deploy", windowTitle: "", cols: 80, rows: 24, dead: false, createdAt: 1 }]);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(buttons()).toEqual(["新建终端", "接管"]);
    } finally {
      vi.useRealTimers();
    }
  });

  describe("taking over while the workspace changes", () => {
    const session = { id: "s1", assetId: "a1", name: "prod", kind: "ssh", status: "connected" as const, tabs: [], createdAt: 0 };
    const other: Workspace = {
      id: "ws-b", kind: "session", title: "other", sessionId: "s2",
      panes: [{ id: "pb", tabs: [], activeTabId: null }], activePaneId: "pb", closable: true,
    };

    async function listed() {
      useUi.setState({
        sessions: [session],
        workspaces: [workspace({ sessionId: "s1", assetId: "a1", assetKind: "ssh" }), other],
        activeWorkspaceId: "ws",
      });
      mocks.listDurable.mockResolvedValue([{ id: "d1", title: "deploy", windowTitle: "", cols: 80, rows: 24, dead: false, createdAt: 1 }]);
      const list = deferred<unknown[]>();
      mocks.sessionList.mockReturnValue(list.promise);
      render();
      await waitForRendered(() => expect(buttons()).toEqual(["新建终端", "接管"]));
      click(view!.container.querySelector<HTMLButtonElement>('button[aria-label="接管 deploy"]')!);
      return list;
    }

    function terminalTabs() {
      return useUi.getState().workspaces.flatMap((item) => item.panes.flatMap((pane) => pane.tabs));
    }

    it("drops the takeover when the user switched to another workspace meanwhile", async () => {
      const list = await listed();
      act(() => useUi.getState().setActiveWorkspace("ws-b"));
      await act(async () => {
        list.resolve([session]);
        await list.promise;
      });
      await flush();
      expect(useUi.getState().activeWorkspaceId).toBe("ws-b");
      expect(terminalTabs()).toEqual([]);
    });

    it("refuses a listed terminal whose session the workspace no longer holds", async () => {
      const list = await listed();
      act(() =>
        useUi.setState((state) => ({
          workspaces: state.workspaces.map((item) => (item.id === "ws" ? { ...item, sessionId: "s9" } : item)),
        })),
      );
      await act(async () => {
        list.resolve([session, { ...session, id: "s9" }]);
        await list.promise;
      });
      await flush();
      expect(terminalTabs()).toEqual([]);
      expect(useUi.getState().toasts.map((toast) => toast.text).join("\n")).toContain("不属于当前主机");
    });
  });

  it("shows only 新建终端 when the host has no background terminal", async () => {
    useUi.setState({
      sessions: [{ id: "s1", assetId: "a1", name: "prod", kind: "ssh", status: "connected", tabs: [], createdAt: 0 }],
      workspaces: [workspace({ sessionId: "s1", assetId: "a1", assetKind: "ssh" })],
    });
    render();
    await waitForRendered(() => expect(mocks.listDurable).toHaveBeenCalledWith("s1"));
    await flush();
    expect(buttons()).toEqual(["新建终端"]);
  });

  it("offers reconnect, not a terminal, in a closed remote desktop workspace", async () => {
    useUi.setState({ workspaces: [workspace({ kind: "remote", assetId: "a1", assetKind: "rdp" })] });
    const onNew = render();
    expect(text()).toContain("远程桌面已关闭");
    expect(text()).not.toContain("终端");
    expect(text()).not.toContain("文件树");
    expect(buttons()).toEqual(["重新连接"]);
    click(view!.container.querySelector("button")!);
    await flush();
    expect(mocks.reopen).toHaveBeenCalledWith("ws", "p1");
    expect(onNew).not.toHaveBeenCalled();
  });

  it("shows no reconnect button when the asset was deleted", () => {
    useUi.setState({ workspaces: [workspace({ kind: "remote", assetId: "a1", deletedTarget: true })] });
    render();
    expect(text()).toContain("已删除");
    expect(buttons()).toEqual([]);
  });

  it("shows no reconnect when the desktop is open in another pane", () => {
    useUi.setState({
      workspaces: [
        workspace({
          kind: "remote",
          assetId: "a1",
          panes: [
            { id: "p1", tabs: [], activeTabId: null },
            { id: "p2", tabs: [{ id: "r1", kind: "remote", title: "远程桌面", closable: true }], activeTabId: "r1" },
          ],
        }),
      ],
    });
    render();
    expect(buttons()).toEqual([]);
  });

  describe("database workspace", () => {
    const dbWorkspace = (patch: Partial<Workspace> = {}) =>
      workspace({
        kind: "db",
        assetId: "db-asset",
        panes: [
          { id: "p0", tabs: [], activeTabId: null },
          { id: "p1", tabs: [], activeTabId: null },
        ],
        activePaneId: "p0",
        ...patch,
      });

    it("reopens the database into this pane instead of offering a terminal", async () => {
      let focusedPane: string | undefined;
      mocks.connect.mockImplementation(async () => {
        focusedPane = useUi.getState().workspaces[0]?.activePaneId;
        return { ok: true };
      });
      useUi.setState({ workspaces: [dbWorkspace()] });
      const onNew = render();
      expect(text()).not.toContain("终端");
      expect(buttons()).toEqual(["重新打开数据库"]);

      click(view!.container.querySelector("button")!);
      await flush();

      expect(mocks.assetGet).toHaveBeenCalledWith("db-asset");
      expect(mocks.connect).toHaveBeenCalledWith(expect.objectContaining({ id: "db-asset" }));
      expect(focusedPane).toBe("p1");
      expect(onNew).not.toHaveBeenCalled();
    });

    it("offers nothing to reopen while the database is open in another pane", () => {
      useUi.setState({
        workspaces: [
          dbWorkspace({
            panes: [
              { id: "p0", tabs: [{ id: "d1", kind: "db", title: "PostgreSQL", connId: "c1", closable: true }], activeTabId: "d1" },
              { id: "p1", tabs: [], activeTabId: null },
            ],
          }),
        ],
      });
      render();
      expect(buttons()).toEqual([]);
    });

    it("offers nothing to reopen when the asset was deleted", () => {
      useUi.setState({ workspaces: [dbWorkspace({ deletedTarget: true })] });
      render();
      expect(text()).toContain("已删除");
      expect(buttons()).toEqual([]);
    });
  });

  it("offers no terminal in a tools workspace", () => {
    useUi.setState({ workspaces: [workspace({ kind: "tools" })] });
    const onNew = render();
    expect(text()).toContain("此窗格暂无内容");
    expect(buttons()).toEqual([]);
    expect(onNew).not.toHaveBeenCalled();
  });
});
