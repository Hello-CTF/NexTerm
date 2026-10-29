/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  reconnect: vi.fn(),
  confirm: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: { probeLiveness: vi.fn().mockResolvedValue(undefined) },
}));

vi.mock("../../app/store/workspaceReconnect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/store/workspaceReconnect")>()),
  reconnectWorkspaceConnection: mocks.reconnect,
  confirmSuspectReconnect: mocks.confirm,
}));

import {
  AUTO_RECONNECT_MAX_FAILURES,
  AUTO_RECONNECT_MIN_INTERVAL_MS,
  reconnectDroppedWorkspaces,
  resetAutoReconnectState,
  useUi,
  type SessionRuntimeInfo,
  type Workspace,
} from "../../app/store";

function session(status: SessionRuntimeInfo["status"], kind = "ssh"): SessionRuntimeInfo {
  return { id: "s1", assetId: "a1", name: "s1", kind, status, tabs: [], createdAt: 1 } as SessionRuntimeInfo;
}

function setup(status: SessionRuntimeInfo["status"], kind = "ssh") {
  const ws = { id: "w1", kind: "session", title: "w1", sessionId: "s1", panes: [], activePaneId: "p", closable: true } as unknown as Workspace;
  useUi.setState({ sessions: [session(status, kind)], workspaces: [ws], activeWorkspaceId: "w1" });
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(1_000_000);
  resetAutoReconnectState();
  mocks.reconnect.mockReset().mockResolvedValue(true);
  mocks.confirm.mockReset().mockResolvedValue(true);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("foreground reconnect of dropped sessions", () => {
  it.each([["failed"], ["disconnected"]] as const)("reconnects a %s ssh session", async (status) => {
    setup(status);
    expect(await reconnectDroppedWorkspaces()).toBe(true);
    expect(mocks.reconnect).toHaveBeenCalledWith("w1");
  });

  it("reconnects a device session", async () => {
    setup("disconnected", "device");
    await reconnectDroppedWorkspaces();
    expect(mocks.reconnect).toHaveBeenCalledTimes(1);
  });

  it("leaves connected, reconnecting and local sessions alone", async () => {
    setup("connected");
    expect(await reconnectDroppedWorkspaces()).toBe(false);
    setup("reconnecting");
    expect(await reconnectDroppedWorkspaces()).toBe(false);
    setup("disconnected", "local");
    expect(await reconnectDroppedWorkspaces()).toBe(false);
    expect(mocks.reconnect).not.toHaveBeenCalled();
  });

  it("throttles attempts per session", async () => {
    setup("disconnected");
    await reconnectDroppedWorkspaces();
    vi.setSystemTime(1_000_000 + AUTO_RECONNECT_MIN_INTERVAL_MS - 1);
    expect(await reconnectDroppedWorkspaces()).toBe(false);
    vi.setSystemTime(1_000_000 + AUTO_RECONNECT_MIN_INTERVAL_MS);
    expect(await reconnectDroppedWorkspaces()).toBe(true);
    expect(mocks.reconnect).toHaveBeenCalledTimes(2);
  });

  it("backs off after failures and stops after the failure cap", async () => {
    setup("failed");
    mocks.reconnect.mockResolvedValue(false);
    let t = 1_000_000;
    vi.setSystemTime(t);
    expect(await reconnectDroppedWorkspaces()).toBe(true);
    for (let failures = 1; failures < AUTO_RECONNECT_MAX_FAILURES; failures += 1) {
      const interval = AUTO_RECONNECT_MIN_INTERVAL_MS * 2 ** failures;
      vi.setSystemTime(t + interval - 1);
      expect(await reconnectDroppedWorkspaces()).toBe(false);
      t += interval;
      vi.setSystemTime(t);
      expect(await reconnectDroppedWorkspaces()).toBe(true);
    }
    vi.setSystemTime(t + 60 * 60_000);
    expect(await reconnectDroppedWorkspaces()).toBe(false);
    expect(mocks.reconnect).toHaveBeenCalledTimes(AUTO_RECONNECT_MAX_FAILURES);
  });

  it("does not reconnect when the user declines the ordinary-terminal confirmation", async () => {
    setup("disconnected");
    mocks.confirm.mockResolvedValue(false);
    await reconnectDroppedWorkspaces();
    expect(mocks.reconnect).not.toHaveBeenCalled();
  });
});
