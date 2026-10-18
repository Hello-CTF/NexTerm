/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  sessionConnectDevice: vi.fn(),
  sessionReconnect: vi.fn(),
  sessionList: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: {
      connectDevice: mocks.sessionConnectDevice,
      reconnect: mocks.sessionReconnect,
      list: mocks.sessionList,
    },
    dbApi: {},
    vaultApi: {},
  };
});

import { connectDevice, deviceAssetId, useUi, type ConnectOutcome } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

const DEVICE = { id: "d-1", name: "build-box" };

const DEVICE_SESSION: SessionInfo = {
  id: "s-dev-1",
  assetId: "device/d-1",
  name: "build-box",
  kind: "device",
  status: "connected",
  tabs: [],
  createdAt: 0,
};

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    leftMode: "assets",
    pushToast: mocks.toast,
  });
  mocks.sessionConnectDevice.mockResolvedValue(DEVICE_SESSION);
});

afterEach(() => {
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [], leftMode: "assets" });
});

describe("connectDevice opens device workspaces", () => {
  it("a fresh connection creates a session and a separate workspace with a terminal tab", async () => {
    const visibleStates: boolean[] = [];
    const unsubscribe = useUi.subscribe((state) => {
      const active = state.workspaces.find((workspace) => workspace.id === state.activeWorkspaceId);
      if (active?.assetId === "device/d-1") {
        visibleStates.push(active.panes.some((pane) => pane.tabs.some((tab) => tab.kind === "terminal")));
      }
    });
    const outcome = (await connectDevice(DEVICE)) as ConnectOutcome;
    unsubscribe();
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(mocks.sessionConnectDevice).toHaveBeenCalledWith("d-1", "build-box");

    const { workspaces, sessions, activeWorkspaceId } = useUi.getState();
    const ws = workspaces.find((w) => w.assetId === "device/d-1");
    expect(ws).toBeDefined();
    expect(ws?.kind).toBe("session");
    expect(ws?.assetKind).toBe("device");
    expect(ws?.sessionId).toBe("s-dev-1");
    expect(activeWorkspaceId).toBe(ws?.id);
    expect(sessions.some((s) => s.id === "s-dev-1" && s.kind === "device")).toBe(true);
    const tabs = ws?.panes.flatMap((p) => p.tabs) ?? [];
    expect(tabs.some((t) => t.kind === "terminal" && t.sessionId === "s-dev-1")).toBe(true);
    expect(visibleStates.length).toBeGreaterThan(0);
    expect(visibleStates.every(Boolean)).toBe(true);
  });

  it("automatically switches to the files panel after connecting a device workspace, consistent with SSH sessions", async () => {
    useUi.setState({ leftMode: "assets" });
    await connectDevice(DEVICE);
    expect(useUi.getState().leftMode).toBe("files");
    expect(useUi.getState().leftOpen).toBe(true);
  });

  it("duplicate calls for the same in-flight device start only one connection", async () => {
    const gate = deferred<SessionInfo>();
    mocks.sessionConnectDevice.mockReturnValueOnce(gate.promise);

    const first = connectDevice(DEVICE);
    const second = connectDevice(DEVICE);
    await flush();
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    expect(useUi.getState().connectingAssetIds).toContain("device/d-1");

    gate.resolve(DEVICE_SESSION);
    const [a, b] = (await Promise.all([first, second])) as ConnectOutcome[];
    expect(a).toEqual({ ok: true });
    expect(b).toEqual({ ok: true });
    expect(useUi.getState().connectingAssetIds).not.toContain("device/d-1");
  });

  it("returns the error and shows a toast on connection failure, then allows a retry after the guard clears", async () => {
    mocks.sessionConnectDevice.mockRejectedValueOnce({ code: "forbidden", message: "Terminal access is disabled for this device" });
    const failed = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(failed).toEqual({ ok: false, error: "Terminal access is disabled for this device" });
    expect(mocks.toast).toHaveBeenCalledWith("error", "Terminal access is disabled for this device");
    expect(useUi.getState().workspaces.every((w) => w.assetId !== "device/d-1")).toBe(true);

    const retried = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(retried).toEqual({ ok: true });
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(2);
  });

  it("deviceAssetId matches the backend pseudo-asset id", () => {
    expect(deviceAssetId("d-1")).toBe("device/d-1");
  });
});

describe("connectDevice workspace deduplication", () => {
  function seedDeviceWorkspace(sessionStatus: SessionInfo["status"], withTerminalTab = false) {
    const tabs = withTerminalTab
      ? [{ id: "t1", kind: "terminal" as const, title: "Terminal 1", sessionId: "s-dev-1", closable: true }]
      : [];
    useUi.setState({
      sessions: [{ ...DEVICE_SESSION, status: sessionStatus }],
      workspaces: [
        {
          id: "ws-dev",
          kind: "session" as const,
          sessionId: "s-dev-1",
          title: "build-box",
          assetId: "device/d-1",
          assetKind: "device",
          panes: [{ id: "p1", tabs, activeTabId: tabs[0]?.id ?? null }],
          activePaneId: "p1",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: null,
    });
  }

  it("reopening with an existing live-session workspace jumps to it without starting a new connection", async () => {
    seedDeviceWorkspace("connected", true);
    const outcome = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    expect(useUi.getState().activeWorkspaceId).toBe("ws-dev");
  });

  it("reopens a terminal tab when the workspace remains but its terminal tab was closed", async () => {
    seedDeviceWorkspace("connected");
    const outcome = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    const ws = useUi.getState().workspaces.find((w) => w.id === "ws-dev");
    expect(ws?.panes.flatMap((p) => p.tabs).some((t) => t.kind === "terminal" && t.sessionId === "s-dev-1")).toBe(true);
  });

  it("reconnects directly when the session is disconnected without starting a new connection", async () => {
    seedDeviceWorkspace("disconnected");
    mocks.sessionReconnect.mockResolvedValue(true);
    mocks.sessionList.mockResolvedValue([{ ...DEVICE_SESSION, status: "connected" }]);

    const outcome = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionReconnect).toHaveBeenCalledWith("s-dev-1");
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    expect(useUi.getState().activeWorkspaceId).toBe("ws-dev");
  });

  it("preserves the original terminal when reconnect fails", async () => {
    seedDeviceWorkspace("disconnected", true);
    useUi.getState().updateTab("t1", { tabId: "original-shell" });
    mocks.sessionReconnect.mockRejectedValue(new Error("device unavailable"));

    const outcome = await connectDevice(DEVICE);

    expect(outcome).toEqual({ ok: false, error: "device unavailable" });
    expect(mocks.sessionConnectDevice).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]).toMatchObject({ id: "t1", tabId: "original-shell" });
  });

  it.each(["missing", "deleted"] as const)("preserves restored device panes and shell identities with a %s session", async (state) => {
    seedDeviceWorkspace("disconnected", true);
    const workspace = useUi.getState().workspaces[0];
    useUi.setState({
      sessions: state === "missing" ? [] : useUi.getState().sessions,
      workspaces: [{ ...workspace, activePaneId: "p2", splitRatio: 0.4, panes: [
        { id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", title: "one", sessionId: "s-dev-1", tabId: "shell-one", closable: true }] },
        { id: "p2", activeTabId: "t2", tabs: [{ id: "t2", kind: "terminal", title: "two", sessionId: "s-dev-1", tabId: "shell-two", closable: true }] },
      ] }],
    });
    mocks.sessionReconnect.mockRejectedValue({ code: "not_found", message: "Session not found" });
    mocks.sessionConnectDevice.mockResolvedValue({ ...DEVICE_SESSION, id: "s-new" });

    expect(await connectDevice(DEVICE)).toEqual({ ok: true });

    const restored = useUi.getState().workspaces[0];
    expect(restored).toMatchObject({ id: "ws-dev", sessionId: "s-new", activePaneId: "p2", splitRatio: 0.4 });
    expect(restored.panes.map((pane) => ({ id: pane.id, activeTabId: pane.activeTabId, tabs: pane.tabs.map((tab) => [tab.id, tab.tabId, tab.sessionId]) }))).toEqual([
      { id: "p1", activeTabId: "t1", tabs: [["t1", "shell-one", "s-new"]] },
      { id: "p2", activeTabId: "t2", tabs: [["t2", "shell-two", "s-new"]] },
    ]);
  });

  it("rebinds the existing workspace when the previous session was deleted", async () => {
    seedDeviceWorkspace("disconnected");
    mocks.sessionReconnect.mockRejectedValue({ code: "not_found", message: "Session not found" });

    const outcome = (await connectDevice(DEVICE)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnectDevice).toHaveBeenCalledTimes(1);
    const { workspaces } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(workspaces[0]?.id).toBe("ws-dev");
    expect(workspaces[0]?.assetId).toBe("device/d-1");
  });
});
