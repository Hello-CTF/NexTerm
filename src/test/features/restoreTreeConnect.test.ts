/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  assetGet: vi.fn(),
  sessionConnect: vi.fn(),
  sessionDisconnect: vi.fn(),
  deviceConnect: vi.fn(),
  dbConnect: vi.fn(),
  vaultStatus: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, get: mocks.assetGet },
    sessionApi: { ...actual.sessionApi, connect: mocks.sessionConnect, disconnect: mocks.sessionDisconnect, connectDevice: mocks.deviceConnect },
    dbApi: { ...actual.dbApi, connect: mocks.dbConnect },
    vaultApi: { ...actual.vaultApi, status: mocks.vaultStatus },
  };
});

import { connectAsset, connectDevice, useUi, type Workspace } from "../../app/store";

function pendingWorkspace(kind: "ssh" | "device" | "mysql"): Workspace {
  const db = kind === "mysql";
  const assetId = kind === "ssh" ? "asset-ssh" : kind === "device" ? "device/device-1" : "asset-db";
  return {
    id: `workspace-${kind}`,
    kind: db ? "db" : "session",
    title: `saved ${kind}`,
    assetId,
    assetKind: kind,
    ...(db ? { dbKind: "mysql" as const } : {}),
    restorePending: true,
    panes: [
      { id: "pane-1", activeTabId: "saved-tab", tabs: [
        { id: "saved-tab", kind: db ? "db" : "terminal", title: "saved editor", closable: true, restorePending: true,
          ...(db ? { dbKind: "mysql" as const } : {}) },
      ] },
      { id: "pane-2", activeTabId: "saved-files", tabs: [
        { id: "saved-files", kind: "files", title: "saved files", closable: true, restorePending: true },
      ] },
    ],
    activePaneId: "pane-1",
    splitRatio: 0.37,
    closable: true,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    workspaceRuntime: {},
  });
  mocks.assetGet.mockImplementation(async (id: string) => ({ id, name: id, credId: null }));
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.sessionConnect.mockResolvedValue({
    id: "ssh-session", assetId: "asset-ssh", name: "saved ssh", kind: "ssh", status: "connected", tabs: [], createdAt: 1,
  });
  mocks.deviceConnect.mockResolvedValue({
    id: "device-session", assetId: "device/device-1", name: "saved device", kind: "device", status: "connected", tabs: [], createdAt: 1,
  });
  mocks.dbConnect.mockResolvedValue({ connId: "db-connection" });
});

describe("asset and device tree entry for restored workspace previews", () => {
  it("connects a saved SSH workspace and keeps its tabs and pane layout", async () => {
    useUi.setState({ workspaces: [pendingWorkspace("ssh")] });

    expect(await connectAsset({ id: "asset-ssh", name: "saved ssh", kind: "ssh" })).toEqual({ ok: true });

    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-ssh");
    const state = useUi.getState();
    const workspace = state.workspaces[0];
    expect(state.workspaces).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe("workspace-ssh");
    expect(workspace).toMatchObject({ id: "workspace-ssh", sessionId: "ssh-session", restorePending: undefined, activePaneId: "pane-1", splitRatio: 0.37 });
    expect(workspace?.panes.map((pane) => pane.tabs.map((tab) => [tab.id, tab.title]))).toEqual([
      [["saved-tab", "saved editor"]], [["saved-files", "saved files"]],
    ]);
    expect(workspace?.panes[0].tabs[0]).toMatchObject({ sessionId: "ssh-session", restorePending: true });
  });

  it("connects a saved managed device workspace through the device API", async () => {
    useUi.setState({ workspaces: [pendingWorkspace("device")] });

    expect(await connectDevice({ id: "device-1", name: "saved device" })).toEqual({ ok: true });

    expect(mocks.deviceConnect).toHaveBeenCalledWith("device-1", "saved device");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe("workspace-device");
    expect(state.workspaces[0]).toMatchObject({ sessionId: "device-session", restorePending: undefined });
    expect(state.workspaces[0]?.panes[0].tabs[0]).toMatchObject({ id: "saved-tab", title: "saved editor", sessionId: "device-session" });
    expect(state.workspaces[0]?.panes[1].tabs[0]).toMatchObject({ id: "saved-files", title: "saved files" });
  });

  it("opens a saved database connection instead of treating the preview as live", async () => {
    useUi.setState({ workspaces: [pendingWorkspace("mysql")] });

    expect(await connectAsset({ id: "asset-db", name: "saved mysql", kind: "mysql" })).toEqual({ ok: true });

    expect(mocks.dbConnect).toHaveBeenCalledWith("asset-db");
    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe("workspace-mysql");
    expect(state.workspaces[0]).toMatchObject({ connId: "db-connection", restorePending: undefined, activePaneId: "pane-1", splitRatio: 0.37 });
    expect(state.workspaces[0]?.panes[0].tabs[0]).toMatchObject({ id: "saved-tab", title: "saved editor", connId: "db-connection", restorePending: undefined });
    expect(state.workspaces[0]?.panes[1].tabs[0]).toMatchObject({ id: "saved-files", title: "saved files" });
  });

  it("keeps the saved database preview pending when its connection fails", async () => {
    useUi.setState({ workspaces: [pendingWorkspace("mysql")] });
    mocks.dbConnect.mockRejectedValueOnce(new Error("database unavailable"));

    expect(await connectAsset({ id: "asset-db", name: "saved mysql", kind: "mysql" })).toEqual({
      ok: false, error: "database unavailable",
    });
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(useUi.getState().workspaces[0]).toMatchObject({ id: "workspace-mysql", restorePending: true });
    expect(useUi.getState().workspaces[0]?.panes[0].tabs[0]).toMatchObject({ id: "saved-tab", restorePending: true });
  });

  it("starts a new connection when a preview is withdrawn and reapplied with the same workspace ID", async () => {
    const stale = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(stale.promise).mockResolvedValueOnce({
      id: "new-session", assetId: "asset-ssh", name: "saved ssh", kind: "ssh", status: "connected", tabs: [], createdAt: 2,
    });
    useUi.setState({ workspaces: [pendingWorkspace("ssh")] });
    const first = connectAsset({ id: "asset-ssh", name: "saved ssh", kind: "ssh" });
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);

    useUi.setState({ workspaces: [] });
    useUi.setState({ workspaces: [pendingWorkspace("ssh")] });
    const second = connectAsset({ id: "asset-ssh", name: "saved ssh", kind: "ssh" });
    expect(await second).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);

    stale.resolve({ id: "old-session", assetId: "asset-ssh", name: "saved ssh", kind: "ssh", status: "connected", tabs: [], createdAt: 1 });
    expect(await first).toEqual({ ok: false, canceled: true });
    expect(useUi.getState().workspaces[0]).toMatchObject({ id: "workspace-ssh", sessionId: "new-session", restorePending: undefined });
    expect(useUi.getState().sessions.map((session) => session.id)).toEqual(["new-session"]);
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("old-session");
  });
});
