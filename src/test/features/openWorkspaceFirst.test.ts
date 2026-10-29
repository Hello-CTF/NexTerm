/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    sessionConnect: vi.fn(),
    sessionDisconnect: vi.fn(),
    prepareDurable: vi.fn(),
    assetGet: vi.fn(),
    dbConnect: vi.fn(),
    vaultStatus: vi.fn(),
    listDurable: vi.fn(),
    listLive: vi.fn(),
    attachTab: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { connect: mocks.sessionConnect, disconnect: mocks.sessionDisconnect, prepareDurable: mocks.prepareDurable },
    assetApi: { get: mocks.assetGet },
    dbApi: { connect: mocks.dbConnect },
    vaultApi: { status: mocks.vaultStatus },
    terminalApi: { listDurable: mocks.listDurable, listLive: mocks.listLive, attachTab: mocks.attachTab },
  });
});

import { READINESS_QUERY_TIMEOUT_MS, connectAsset, forgetDurableReadiness, noteHelperProgress, openTerminalTab, useUi, type AppTab, type Workspace } from "../../app/store";
import { setTerminalDefaultMode } from "../../app/preferences";
import { tabTooltip } from "../../app/workspace/tabStrip";
import type { SessionInfo } from "../../ipc/commands";

const SSH_ASSET = { id: "asset-1", name: "web-01", kind: "ssh" };
const SSH_SESSION: SessionInfo = {
  id: "s1", assetId: "asset-1", name: "web-01", kind: "ssh", status: "connected", tabs: [], createdAt: 0,
};
const LOCAL_SESSION: SessionInfo = {
  id: "local-1", assetId: "local", name: "当前设备", kind: "local", status: "connected", tabs: [], createdAt: 0,
};

function allTabs(): AppTab[] {
  return useUi.getState().workspaces.flatMap((workspace) => workspace.panes.flatMap((pane) => pane.tabs));
}

function terminalTabs(): AppTab[] {
  return allTabs().filter((tab) => tab.kind === "terminal");
}

beforeEach(async () => {
  vi.clearAllMocks();
  localStorage.clear();
  await setTerminalDefaultMode("durable");
  forgetDurableReadiness();
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
    connectionProblems: {},
    connectingAssetIds: [],
    connectFocusRevision: 0,
    pushToast: mocks.toast,
  });
  mocks.sessionConnect.mockResolvedValue(SSH_SESSION);
  mocks.prepareDurable.mockResolvedValue({ state: "preparing" });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.assetGet.mockResolvedValue({ id: "asset-1", name: "web-01", kind: "ssh", credId: null });
  mocks.listDurable.mockResolvedValue([{ tabId: "durable-old", sessionId: "s1", durable: true }]);
  mocks.listLive.mockResolvedValue([]);
});

afterEach(() => {
  useUi.setState({ workspaces: [], activeWorkspaceId: null, sessions: [] });
});

describe("clicking a host asset opens its workspace", () => {
  it("opens the SSH workspace without a terminal and prepares durable terminals in the background", async () => {
    expect(await connectAsset(SSH_ASSET)).toEqual({ ok: true });
    await flush();

    const state = useUi.getState();
    const workspace = state.workspaces.find((item) => item.sessionId === "s1");
    expect(workspace).toBeDefined();
    expect(state.activeWorkspaceId).toBe(workspace?.id);
    expect(terminalTabs()).toEqual([]);
    expect(mocks.prepareDurable).toHaveBeenCalledWith("s1");
  });

  it("leaves existing durable terminals on the host in the background", async () => {
    await connectAsset(SSH_ASSET);
    await flush();

    expect(terminalTabs()).toEqual([]);
    expect(mocks.listDurable).not.toHaveBeenCalled();
    expect(mocks.attachTab).not.toHaveBeenCalled();
  });

  it("switches to the open workspace when the asset is clicked again", async () => {
    await connectAsset(SSH_ASSET);
    const first = useUi.getState().activeWorkspaceId;
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
    expect(useUi.getState().activeWorkspaceId).not.toBe(first);

    expect(await connectAsset(SSH_ASSET)).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().activeWorkspaceId).toBe(first);
    expect(terminalTabs()).toEqual([]);
  });

  it("opens the local device as a workspace without a terminal and only reads the local helper readiness", async () => {
    mocks.sessionConnect.mockResolvedValue(LOCAL_SESSION);
    expect(await connectAsset({ id: "local", name: "当前设备", kind: "local" })).toEqual({ ok: true });
    await flush();

    expect(useUi.getState().workspaces.some((item) => item.sessionId === "local-1")).toBe(true);
    expect(terminalTabs()).toEqual([]);
    expect(mocks.prepareDurable).toHaveBeenCalledWith("local-1");
  });

  it("does not prepare durable terminals when the default terminal mode is regular", async () => {
    await setTerminalDefaultMode("regular");
    await connectAsset(SSH_ASSET);
    await flush();

    expect(terminalTabs()).toEqual([]);
    expect(mocks.prepareDurable).not.toHaveBeenCalled();
  });

  it("keeps opening database assets in the database workbench", async () => {
    mocks.dbConnect.mockResolvedValue({ connId: "conn-1" });
    expect(await connectAsset({ id: "db-1", name: "orders", kind: "mysql" })).toEqual({ ok: true });
    await flush();

    expect(allTabs().map((tab) => tab.kind)).toEqual(["db"]);
    expect(mocks.prepareDurable).not.toHaveBeenCalled();
  });

  it("reconnects a restored workspace without creating a terminal and prepares durable terminals", async () => {
    const restored: Workspace = {
      id: "ws-restored", kind: "session", title: "web-01", assetId: "asset-1", assetKind: "ssh", restorePending: true,
      panes: [{ id: "pane-1", tabs: [], activeTabId: null }], activePaneId: "pane-1", closable: true,
    };
    useUi.setState({ workspaces: [restored], activeWorkspaceId: null });

    expect(await connectAsset(SSH_ASSET)).toMatchObject({ ok: true });
    await flush();

    const workspace = useUi.getState().workspaces.find((item) => item.id === "ws-restored");
    expect(workspace?.sessionId).toBe("s1");
    expect(workspace?.restorePending).toBeUndefined();
    expect(terminalTabs()).toEqual([]);
    expect(mocks.prepareDurable).toHaveBeenCalledWith("s1");
  });
});

describe("explicitly opening a terminal", () => {
  it("opens a durable terminal when the helper is ready", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    await openTerminalTab(SSH_SESSION);

    const [tab] = terminalTabs();
    expect(tab?.sessionId).toBe("s1");
    expect(tab?.regularReason).toBeUndefined();
  });

  it("opens a regular terminal at once while the helper is preparing and says so", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "preparing" });
    await openTerminalTab(SSH_SESSION);

    expect(terminalTabs()[0]?.regularReason).toBe("持久组件准备中");
  });

  it("opens a regular terminal with the failure reason when the helper is unavailable", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "unavailable", reason: "目标主机不支持持久终端组件" });
    await openTerminalTab(SSH_SESSION);

    expect(terminalTabs()[0]?.regularReason).toBe("持久组件不可用：目标主机不支持持久终端组件");
  });

  it("reports a failed readiness query as unavailable instead of degrading silently", async () => {
    mocks.prepareDurable.mockRejectedValue(new Error("network down"));
    await openTerminalTab(SSH_SESSION);

    expect(terminalTabs()[0]?.regularReason).toMatch(/^持久组件不可用：.*network down/);
  });

  it("opens regular terminals without asking the helper when the default mode is regular", async () => {
    await setTerminalDefaultMode("regular");
    await openTerminalTab(SSH_SESSION);

    expect(mocks.prepareDurable).not.toHaveBeenCalled();
    expect(terminalTabs()[0]?.regularReason).toBeUndefined();
  });

  it("opens local terminals durable once the local helper is ready", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "ready" });
    await openTerminalTab(LOCAL_SESSION);

    expect(mocks.prepareDurable).toHaveBeenCalledWith("local-1");
    expect(terminalTabs()[0]?.regularReason).toBeUndefined();
  });

  it("names why a local terminal opens regular while the local helper is unavailable", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "unavailable", reason: "本机持久组件为旧版本（协议 v2），有 2 个会话在运行" });
    await openTerminalTab(LOCAL_SESSION);

    expect(terminalTabs()[0]?.regularReason).toBe("持久组件不可用：本机持久组件为旧版本（协议 v2），有 2 个会话在运行");
  });

  it("opens local terminals regular without a reason when this device keeps them regular", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "disabled" });
    await openTerminalTab(LOCAL_SESSION);

    expect(mocks.prepareDurable).toHaveBeenCalledWith("local-1");
    expect(terminalTabs()[0]?.regularReason).toBeUndefined();
  });

  it("names the regular terminal reason in the tab tooltip", async () => {
    mocks.prepareDurable.mockResolvedValue({ state: "preparing" });
    await openTerminalTab(SSH_SESSION);

    const [tab] = terminalTabs();
    expect(tab && tabTooltip(tab, false)).toBe("终端 1（临时终端：持久组件准备中）");
  });

  it("does not wait longer than the readiness timeout and opens a regular terminal meanwhile", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      mocks.prepareDurable.mockReturnValue(new Promise(() => {}));
      const opening = openTerminalTab(SSH_SESSION);
      await vi.advanceTimersByTimeAsync(READINESS_QUERY_TIMEOUT_MS);
      await opening;

      expect(terminalTabs()).toHaveLength(1);
      expect(terminalTabs()[0]?.regularReason).toBe("持久组件准备中");
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives terminals opened in quick succession distinct titles", async () => {
    let answer: (value: unknown) => void = () => {};
    mocks.prepareDurable.mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    const first = openTerminalTab(SSH_SESSION);
    const second = openTerminalTab(SSH_SESSION);
    answer({ state: "ready" });
    await Promise.all([first, second]);

    expect(terminalTabs().map((tab) => tab.title).sort()).toEqual(["终端 1", "终端 2"]);
  });

  it("uses a settled answer from opening the workspace without waiting again", async () => {
    mocks.prepareDurable.mockResolvedValueOnce({ state: "unavailable", reason: "目标主机不支持持久终端组件" });
    await connectAsset(SSH_ASSET);
    await flush();
    mocks.prepareDurable.mockReturnValue(new Promise(() => {}));

    await openTerminalTab(SSH_SESSION);

    expect(terminalTabs()[0]?.regularReason).toBe("持久组件不可用：目标主机不支持持久终端组件");
  });

  it("opens durable terminals once a helper progress event reports the helper ready", async () => {
    await connectAsset(SSH_ASSET);
    await flush();
    noteHelperProgress({
      sessionId: "s1", assetId: "asset-1", step: 6, steps: 6, stage: "ready", transferred: 0, total: 0, percent: 100, done: true,
    });
    mocks.prepareDurable.mockReturnValue(new Promise(() => {}));

    await openTerminalTab(SSH_SESSION);

    expect(terminalTabs()[0]?.regularReason).toBeUndefined();
  });
});
