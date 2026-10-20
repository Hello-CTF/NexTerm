/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    reconnect: mocks.sessionReconnect,
    probeHostKey: mocks.probeHostKey,
  },
}));

import {
  applySessionStatusEvent,
  reconnectSessionAndWait,
  sessionReconnectStatusText,
  useUi,
  type SessionRuntimeInfo,
} from "../../app/store";

function session(
  status: SessionRuntimeInfo["status"],
  extra: Partial<SessionRuntimeInfo> = {},
): SessionRuntimeInfo {
  return {
    id: "s1",
    assetId: "a1",
    name: "web-01",
    kind: "ssh",
    status,
    tabs: [],
    createdAt: 1,
    ...extra,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.getState().resetSessions();
  useUi.setState({
    sessions: [session("connected")],
    workspaces: [],
    activeWorkspaceId: null,
    toasts: [],
    connectProgress: {},
  });
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  // A manual reconnect must observe the session engage before the connected frame counts as fresh.
  mocks.sessionList.mockResolvedValueOnce([session("reconnecting")]).mockResolvedValue([session("connected")]);
});

describe("session reconnect status", () => {
  it("carries typed code, attempt, and nextRetryAt through status events", () => {
    applySessionStatusEvent({
      sessionId: "s1",
      status: "reconnecting",
      error: "temporary network failure",
      errorCode: "ssh",
      attempt: 2,
      nextRetryAt: "2026-10-19T12:00:00Z",
      version: 1,
    });

    expect(useUi.getState().sessions[0]).toMatchObject({
      status: "reconnecting",
      error: "temporary network failure",
      errorCode: "ssh",
      attempt: 2,
      nextRetryAt: "2026-10-19T12:00:00Z",
    });
    expect(sessionReconnectStatusText(useUi.getState().sessions[0])).toContain("重连尝试 2 次");
    expect(sessionReconnectStatusText(useUi.getState().sessions[0])).toContain("下次重连");
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("keeps the typed failure code in both the session and polling error", async () => {
    applySessionStatusEvent({
      sessionId: "s1",
      status: "failed",
      error: "认证失败",
      errorCode: "auth",
      attempt: 1,
      nextRetryAt: null,
      version: 1,
    });
    expect(useUi.getState().sessions[0]).toMatchObject({ errorCode: "auth", attempt: 1 });

    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("failed", {
        error: "认证失败",
        errorCode: "auth",
        attempt: 1,
        nextRetryAt: null,
      })]);
    await expect(reconnectSessionAndWait(session("disconnected"), {
      timeoutMs: 500,
      intervalMs: 1,
    })).rejects.toMatchObject({
      message: "认证失败",
      code: "auth",
      attempt: 1,
      nextRetryAt: null,
    });
  });

  it("notifies once for an abnormal disconnect and provides a working reconnect action", async () => {
    applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 1 });
    applySessionStatusEvent({
      sessionId: "s1",
      status: "disconnected",
      error: "connection reset",
      errorCode: "disconnected",
      version: 2,
    });
    applySessionStatusEvent({
      sessionId: "s1",
      status: "disconnected",
      error: "connection reset",
      errorCode: "disconnected",
      version: 3,
    });

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].text).toContain("异常断开");
    expect(toasts[0].action?.label).toBe("重新连接");

    toasts[0].action?.onSelect();
    await vi.waitFor(() => expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1"));
    await vi.waitFor(() => expect(useUi.getState().sessions[0]?.status).toBe("connected"));
  });

  it.each(["ssh", "timeout", "disconnected"])(
    "notifies once per reconnect cycle for retryable %s failure and reconnects from the action",
    async (errorCode) => {
      const fail = (version: number) => applySessionStatusEvent({
        sessionId: "s1",
        status: "failed",
        error: "temporary failure",
        errorCode,
        attempt: 10,
        nextRetryAt: null,
        version,
      });
      applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 1 });
      fail(2);
      const firstToast = useUi.getState().toasts[0];
      expect(firstToast.text).toContain("自动重连失败");
      expect(firstToast.action?.label).toBe("重新连接");

      fail(3);
      expect(useUi.getState().toasts).toHaveLength(1);
      expect(useUi.getState().toasts[0].id).toBe(firstToast.id);

      applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 4 });
      fail(5);
      const secondToast = useUi.getState().toasts[0];
      expect(useUi.getState().toasts).toHaveLength(1);
      expect(secondToast.id).not.toBe(firstToast.id);

      secondToast.action?.onSelect();
      await vi.waitFor(() => expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1"));
      await vi.waitFor(() => expect(useUi.getState().sessions[0]?.status).toBe("connected"));
    },
  );

  it("uses a positive attempt to offer the action when the reconnecting event was missed", () => {
    applySessionStatusEvent({
      sessionId: "s1",
      status: "failed",
      error: "timeout",
      errorCode: "timeout",
      attempt: 10,
      nextRetryAt: null,
      version: 1,
    });

    expect(useUi.getState().toasts[0]?.action?.label).toBe("重新连接");
  });

  it.each([
    "auth",
    "host_key_pending",
    "host_key_changed",
    "config",
    "bad_param",
    "vault_locked",
    "vault_not_init",
  ])("keeps permanent %s failure on the plain error path", (errorCode) => {
    applySessionStatusEvent({ sessionId: "s1", status: "reconnecting", error: null, version: 1 });
    applySessionStatusEvent({
      sessionId: "s1",
      status: "failed",
      error: "permanent failure",
      errorCode,
      attempt: 1,
      nextRetryAt: null,
      version: 2,
    });

    const toasts = useUi.getState().toasts;
    expect(toasts).toHaveLength(1);
    expect(toasts[0].text).toBe("会话失败：permanent failure");
    expect(toasts[0].action).toBeUndefined();
  });

  it("does not offer an automatic retry action for the initial connection failure", () => {
    useUi.setState({ sessions: [session("connecting")] });
    applySessionStatusEvent({
      sessionId: "s1",
      status: "failed",
      error: "initial network failure",
      errorCode: "ssh",
      attempt: 0,
      nextRetryAt: null,
      version: 1,
    });

    expect(useUi.getState().toasts[0]?.text).toBe("会话失败：initial network failure");
    expect(useUi.getState().toasts[0]?.action).toBeUndefined();
  });

  it("does not toast a deliberate disconnect", () => {
    applySessionStatusEvent({
      sessionId: "s1",
      status: "disconnected",
      error: null,
      errorCode: null,
      attempt: 0,
      nextRetryAt: null,
      version: 1,
    });

    expect(useUi.getState().sessions[0]?.status).toBe("disconnected");
    expect(useUi.getState().toasts).toEqual([]);
  });
});
