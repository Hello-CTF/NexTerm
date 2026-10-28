/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { click, clickButton, deferred, flush, flushUntil, mount, waitForRendered, type MountedView } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";
import type { AppTab, ConnectOutcome, Workspace } from "../../app/store";

const mocks = vi.hoisted(() => ({
  assetGet: vi.fn(),
  knownHostAccept: vi.fn(),
  sessionConnect: vi.fn(),
  sessionConnectQuick: vi.fn(),
  deviceConnect: vi.fn(),
  sessionConnectLocal: vi.fn(),
  sessionDisconnect: vi.fn(),
  sessionReconnect: vi.fn(),
  sessionList: vi.fn(),
  probeHostKey: vi.fn(),
  dbConnect: vi.fn(),
  dbDisconnect: vi.fn(),
  vaultStatus: vi.fn(),
  prompt: vi.fn(),
  layoutStatus: { pending: false, preview: false, count: 0 },
  finishLayoutRestore: vi.fn(),
  undoLayoutRestore: vi.fn(),
}));

vi.mock("../../app/layout", () => ({
  layoutRestoreStatus: () => mocks.layoutStatus,
  subscribeLayoutRestore: () => () => {},
  finishLayoutRestore: mocks.finishLayoutRestore,
  undoLayoutRestore: mocks.undoLayoutRestore,
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: { get: mocks.assetGet, knownHostAccept: mocks.knownHostAccept },
  sessionApi: {
    connect: mocks.sessionConnect,
    connectQuick: mocks.sessionConnectQuick,
    connectDevice: mocks.deviceConnect,
    connectLocal: mocks.sessionConnectLocal,
    disconnect: mocks.sessionDisconnect,
    reconnect: mocks.sessionReconnect,
    list: mocks.sessionList,
    probeHostKey: mocks.probeHostKey,
  },
  dbApi: { connect: mocks.dbConnect, disconnect: mocks.dbDisconnect },
  terminalApi: { listLive: vi.fn(async () => []), recordingStatus: vi.fn() },
  vaultApi: { status: mocks.vaultStatus },
}));

import {
  assetConnectionProblemKey,
  quickConnectionProblemKey,
  useUi,
} from "../../app/store";
import { registerPromptHandler } from "../../ui/dialogs";
import { reconnectRestoredWorkspaces, type ReconnectWorkspace } from "../../features/terminal/workspaceRestore";
import { WorkspaceRestoreBanner } from "../../features/terminal/WorkspaceRestoreBanner";

function connectedSession(id: string, assetId: string | null = null): SessionInfo {
  return { id, assetId, name: id, kind: "ssh", status: "connected", tabs: [], createdAt: 1 };
}

function terminal(id: string, patch: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, closable: true, restorePending: true, ...patch };
}

function hostWorkspace(id: string, patch: Partial<Workspace> = {}): Workspace {
  const tab = terminal(`${id}-tab`);
  return {
    id,
    kind: "session",
    title: id,
    assetId: `asset-${id}`,
    assetKind: "ssh",
    restorePending: true,
    panes: [{ id: `${id}-pane`, tabs: [tab], activeTabId: tab.id }],
    activePaneId: `${id}-pane`,
    closable: true,
    ...patch,
  };
}

function activeWorkspace(): Workspace {
  return {
    id: "active",
    kind: "session",
    title: "active",
    sessionId: "active-session",
    assetId: "active-asset",
    assetKind: "ssh",
    panes: [
      {
        id: "active-pane-1",
        tabs: [terminal("active-terminal", { sessionId: "active-session", tabId: "active-kernel", restorePending: undefined })],
        activeTabId: "active-terminal",
      },
      {
        id: "active-pane-2",
        tabs: [{ id: "active-file", kind: "files", title: "active file", sessionId: "active-session", path: "/tmp/a", closable: true }],
        activeTabId: "active-file",
      },
    ],
    activePaneId: "active-pane-2",
    closable: true,
  };
}

function seed(workspaces: Workspace[], sessions: SessionInfo[] = []): void {
  useUi.setState({
    workspaces: [activeWorkspace(), ...workspaces],
    activeWorkspaceId: "active",
    sessions: [connectedSession("active-session", "active-asset"), ...sessions],
    sessionResyncCompleted: true,
    layoutSyncCompleted: true,
    workspaceRestoreDismissed: false,
    workspaceRestoreResults: {},
    workspaceRuntime: {},
    connectionProblems: {},
    toasts: [],
  });
}

function expectFocusPreserved(): void {
  const state = useUi.getState();
  const active = state.workspaces.find((workspace) => workspace.id === "active");
  expect(state.activeWorkspaceId).toBe("active");
  expect(active?.activePaneId).toBe("active-pane-2");
  expect(active?.panes.find((pane) => pane.id === "active-pane-2")?.activeTabId).toBe("active-file");
}

let mounted: MountedView | undefined;

function mountBanner(): MountedView {
  mounted = mount(createElement(WorkspaceRestoreBanner));
  return mounted;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useUi.getState().resetAccountWorkspaces();
  useUi.setState({ workspaceRestoreDismissed: false, workspaceRestoreResults: {}, layoutSyncCompleted: false });
  mocks.assetGet.mockImplementation(async (id: string) => ({ id, name: id, kind: "ssh", credId: null }));
  mocks.knownHostAccept.mockResolvedValue(undefined);
  mocks.sessionConnect.mockImplementation(async (assetId: string) => connectedSession(`${assetId}-session`, assetId));
  mocks.sessionConnectQuick.mockResolvedValue(connectedSession("quick-session"));
  mocks.deviceConnect.mockResolvedValue(connectedSession("device-session", "device/device-1"));
  mocks.sessionConnectLocal.mockResolvedValue(connectedSession("local-session"));
  mocks.sessionDisconnect.mockResolvedValue(undefined);
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.sessionList.mockResolvedValue([]);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  mocks.dbConnect.mockResolvedValue({ connId: "db-connection" });
  mocks.dbDisconnect.mockResolvedValue(undefined);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.prompt.mockResolvedValue(null);
  mocks.layoutStatus = { pending: false, preview: false, count: 0 };
  mocks.finishLayoutRestore.mockResolvedValue(true);
  registerPromptHandler((message, value, options) => mocks.prompt(message, value, options));
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.getState().resetAccountWorkspaces();
});

describe("workspace restore readiness and banner", () => {
  it.each(["layout-first", "session-first"] as const)("waits for both initial completions: %s", async (order) => {
    seed([hostWorkspace("ws")]);
    useUi.setState({ layoutSyncCompleted: false, sessionResyncCompleted: false });
    const view = mountBanner();
    const banner = () => view.container.querySelector('section[aria-label="工作区恢复"]');
    expect(banner()).toBeNull();

    act(() => {
      useUi.setState(order === "layout-first" ? { layoutSyncCompleted: true } : { sessionResyncCompleted: true });
    });
    expect(banner()).toBeNull();
    act(() => {
      useUi.setState(order === "layout-first" ? { sessionResyncCompleted: true } : { layoutSyncCompleted: true });
    });
    expect(banner()?.textContent).toContain("1 个主机工作区需要重新连接");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();

    clickButton(view.container, "选择…");
    expect(view.container.querySelector('input[aria-label="选择 ws"]')).not.toBeNull();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    await flush();
  });

  it("closes itself shortly after every workspace is reconnected", async () => {
    vi.useFakeTimers();
    try {
      seed([hostWorkspace("ws")]);
      const view = mountBanner();
      const banner = () => view.container.querySelector('section[aria-label="工作区恢复"]');
      clickButton(view.container, "全部重连");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(useUi.getState().workspaceRestoreResults.ws?.status).toBe("success");
      expect(banner()?.textContent).toContain("已恢复 1 个工作区");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3500);
      });
      expect(banner()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stays open with the reason when a reconnect fails", async () => {
    seed([hostWorkspace("ws-ok"), hostWorkspace("ws-bad")]);
    mocks.sessionConnect.mockImplementation(async (assetId: string) => {
      if (assetId === "asset-ws-bad") throw new Error("refused");
      return connectedSession(`${assetId}-session`, assetId);
    });
    const view = mountBanner();
    clickButton(view.container, "全部重连");
    await flushUntil(() => (view.container.textContent ?? "").includes("1 个主机工作区连接失败"));
    expect(view.container.textContent).toContain("refused");
    expect(view.container.querySelector('input[aria-label="选择 ws-bad"]')).not.toBeNull();
  });

  it("keeps dismissal for the current session without persistent storage", async () => {
    seed([hostWorkspace("ws")]);
    const view = mountBanner();
    const banner = () => document.querySelector('section[aria-label="工作区恢复"]');
    click(view.container.querySelector('button[aria-label="关闭工作区恢复提示"]')!);
    expect(banner()).toBeNull();
    expect(localStorage.length).toBe(0);

    await act(async () => {
      await useUi.getState().resyncSessions({ silent: true });
    });
    expect(banner()).toBeNull();
    view.unmount();
    mounted = undefined;
    mountBanner();
    expect(banner()).toBeNull();
  });
});

describe("previewed account layout", () => {
  it("accepts the layout before reconnecting from the single restore bar", async () => {
    seed([hostWorkspace("ws")]);
    mocks.layoutStatus = { pending: false, preview: true, count: 2 };
    const view = mountBanner();
    expect(view.container.textContent).toContain("已载入账号中的 2 个工作区，尚未连接");
    clickButton(view.container, "恢复并全部连接");
    await flushUntil(() => useUi.getState().workspaceRestoreResults.ws?.status === "success");
    expect(mocks.finishLayoutRestore).toHaveBeenCalledTimes(1);
    expect(mocks.finishLayoutRestore.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.sessionConnect.mock.invocationCallOrder[0],
    );
  });

  it("connects nothing when the layout could not be accepted", async () => {
    seed([hostWorkspace("ws")]);
    mocks.layoutStatus = { pending: false, preview: true, count: 1 };
    mocks.finishLayoutRestore.mockResolvedValue(false);
    const view = mountBanner();
    clickButton(view.container, "恢复并全部连接");
    await flush();
    expect(mocks.finishLayoutRestore).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("keeps the layout without connecting and closes the bar", async () => {
    seed([hostWorkspace("ws")]);
    mocks.layoutStatus = { pending: false, preview: true, count: 1 };
    const view = mountBanner();
    clickButton(view.container, "仅恢复布局");
    await flushUntil(() => useUi.getState().workspaceRestoreDismissed);
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("undoes the preview", () => {
    seed([hostWorkspace("ws")]);
    mocks.layoutStatus = { pending: false, preview: true, count: 1 };
    const view = mountBanner();
    clickButton(view.container, "撤销");
    expect(mocks.undoLayoutRestore).toHaveBeenCalledTimes(1);
    expect(mocks.finishLayoutRestore).not.toHaveBeenCalled();
  });
});

describe("workspace restore queue", () => {
  it("runs five dead workspaces with at most four concurrent reconnects", async () => {
    const ids = ["ws-1", "ws-2", "ws-3", "ws-4", "ws-5"];
    seed(ids.map((id) => hostWorkspace(id)));
    const gates = new Map(ids.map((id) => [id, deferred<ConnectOutcome>()]));
    let active = 0;
    let maximum = 0;
    const reconnect: ReconnectWorkspace = vi.fn((workspaceId: string) => {
      active += 1;
      maximum = Math.max(maximum, active);
      return gates.get(workspaceId)!.promise.finally(() => {
        active -= 1;
      });
    });

    const running = reconnectRestoredWorkspaces(undefined, reconnect);
    expect(reconnect).toHaveBeenCalledTimes(4);
    expect(maximum).toBe(4);
    gates.get("ws-2")!.resolve({ ok: true });
    await flush();
    expect(reconnect).toHaveBeenCalledTimes(5);
    for (const id of ids) {
      const gate = gates.get(id)!;
      if (id !== "ws-2") gate.resolve({ ok: true });
    }
    await running;

    expect(maximum).toBe(4);
    for (const call of vi.mocked(reconnect).mock.calls) {
      expect(call[1]).toEqual({ preserveUserFocus: true });
    }
    expectFocusPreserved();
    for (const id of ids) {
      expect(useUi.getState().workspaceRestoreResults[id]?.status).toBe("success");
    }
  });

  it("keeps per-item success and failure visible and assigns only the failure to its target", async () => {
    seed([hostWorkspace("ws-a"), hostWorkspace("ws-b")]);
    mocks.sessionConnect.mockImplementation(async (assetId: string) => {
      if (assetId === "asset-ws-b") throw new Error("B unavailable");
      return connectedSession(`${assetId}-session`, assetId);
    });
    const view = mountBanner();
    clickButton(view.container, "全部重连");

    await waitForRendered(() => {
      expect(useUi.getState().workspaceRestoreResults["ws-a"]?.status).toBe("success");
      expect(useUi.getState().workspaceRestoreResults["ws-b"]?.status).toBe("failed");
    });
    expectFocusPreserved();
    const state = useUi.getState();
    expect(state.workspaceRuntime["ws-a"]?.problems ?? []).toEqual([]);
    expect(state.workspaceRuntime.active?.problems ?? []).toEqual([]);
    expect(state.workspaceRuntime["ws-b"]?.problems?.[0]).toMatchObject({
      connectionKey: assetConnectionProblemKey("asset-ws-b"),
      summary: "B unavailable",
    });
    expect(state.connectionProblems).toEqual({});
    expect(view.container.textContent).toContain("ws-a");
    expect(view.container.textContent).toContain("已连接");
    expect(view.container.textContent).toContain("ws-b");
    expect(view.container.textContent).toContain("连接失败");
    expect(view.container.textContent).toContain("B unavailable");
  });

  it("skips empty, database, deleted-asset and container-exec workspaces", async () => {
    const empty = hostWorkspace("empty", { panes: [{ id: "empty-pane", tabs: [], activeTabId: null }] });
    const database = hostWorkspace("database", {
      kind: "db",
      assetKind: "mysql",
      dbKind: "mysql",
      panes: [{
        id: "database-pane",
        tabs: [{ id: "database-tab", kind: "db", title: "SQL", dbKind: "mysql", closable: true, restorePending: true }],
        activeTabId: "database-tab",
      }],
    });
    const deleted = hostWorkspace("deleted", { deletedTarget: true });
    const exec = hostWorkspace("exec", {
      panes: [{
        id: "exec-pane",
        tabs: [terminal("exec-tab", { containerId: "container-1" })],
        activeTabId: "exec-tab",
      }],
    });
    seed([hostWorkspace("normal"), empty, database, deleted, exec]);
    const view = mountBanner();
    clickButton(view.container, "全部重连");

    await waitForRendered(() => expect(useUi.getState().workspaceRestoreResults.normal?.status).toBe("success"));
    const results = useUi.getState().workspaceRestoreResults;
    expect(results.empty).toMatchObject({ status: "skipped", detail: "空工作区无需重新连接" });
    expect(results.database).toMatchObject({ status: "skipped" });
    expect(results.deleted).toMatchObject({ status: "skipped", detail: "原资产已删除" });
    expect(results.exec).toMatchObject({ status: "skipped", detail: "容器 exec 不会自动重新执行" });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-normal");
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    expect(view.container.textContent).toContain("已恢复 1 个工作区，4 个已跳过");
    expectFocusPreserved();
  });

  it("treats quick-password cancellation as canceled without recording a problem", async () => {
    const quick = hostWorkspace("quick", {
      assetId: undefined,
      quickTarget: { id: "quick-target", host: "example.invalid", port: 22, username: "root", authKind: "password" },
    });
    seed([quick]);
    await reconnectRestoredWorkspaces();

    expect(mocks.prompt).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnectQuick).not.toHaveBeenCalled();
    expect(useUi.getState().workspaceRestoreResults.quick?.status).toBe("canceled");
    expect(useUi.getState().workspaceRuntime.quick?.problems ?? []).toEqual([]);
    expect(useUi.getState().connectionProblems[quickConnectionProblemKey(quick.quickTarget!)]).toBeUndefined();
    expectFocusPreserved();
  });

  it("reuses a disconnected existing session without changing focus", async () => {
    const restored = hostWorkspace("existing", {
      restorePending: undefined,
      sessionId: "old-session",
      panes: [{
        id: "existing-pane",
        tabs: [terminal("existing-tab", { sessionId: "old-session", tabId: "old-kernel", restorePending: undefined })],
        activeTabId: "existing-tab",
      }],
    });
    const old = { ...connectedSession("old-session", "asset-existing"), status: "disconnected" as const };
    seed([restored], [old]);
    mocks.sessionList
      .mockResolvedValueOnce([{ ...old, status: "reconnecting" }])
      .mockResolvedValue([{ ...old, status: "connected" }]);

    await reconnectRestoredWorkspaces();

    expect(mocks.sessionReconnect).toHaveBeenCalledWith("old-session");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(useUi.getState().workspaceRestoreResults.existing?.status).toBe("success");
    expect(useUi.getState().sessions.find((session) => session.id === "old-session")?.status).toBe("connected");
    expectFocusPreserved();
  });

  it("rebinds a deleted session to a fresh host connection while retaining durable tab identity", async () => {
    const restored = hostWorkspace("missing", {
      restorePending: undefined,
      sessionId: "missing-session",
      panes: [{
        id: "missing-pane",
        tabs: [terminal("missing-tab", {
          sessionId: "missing-session",
          tabId: "durable-kernel",
          restorePending: undefined,
          dead: true,
        })],
        activeTabId: "missing-tab",
      }],
    });
    seed([restored]);

    await reconnectRestoredWorkspaces();

    expect(mocks.sessionReconnect).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-missing");
    const workspace = useUi.getState().workspaces.find((item) => item.id === "missing");
    expect(workspace?.sessionId).toBe("asset-missing-session");
    expect(workspace?.panes[0].tabs[0]).toMatchObject({
      sessionId: "asset-missing-session",
      tabId: "durable-kernel",
      dead: false,
    });
    expectFocusPreserved();
  });
});
