/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: { list: vi.fn(), reconnect: vi.fn(), probeHostKey: vi.fn() },
}));

import { applySessionStatusEvent, useUi } from "../../app/store";

const pendingMessage = "主机 jump.example.com:2222 还没有确认过指纹，请先在弹出的确认窗口中核对并信任该主机";

beforeEach(() => {
  useUi.getState().resetSessions();
  useUi.setState({ sessions: [], toasts: [], connectProgress: {} });
});

describe("host key status events", () => {
  it("does not toast a first-connect failure that the confirmation dialog answers", () => {
    applySessionStatusEvent({ sessionId: "s1", status: "failed", error: pendingMessage, errorCode: "host_key_pending", version: 1 });
    applySessionStatusEvent({ sessionId: "s2", status: "failed", error: "changed", errorCode: "host_key_changed", version: 1 });
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("still reports other first-connect failures", () => {
    applySessionStatusEvent({ sessionId: "s1", status: "failed", error: "认证失败", errorCode: "auth", version: 1 });
    expect(useUi.getState().toasts.map((toast) => toast.text)).toEqual(["会话失败：认证失败"]);
  });

  it("keeps the toast when an established session is blocked by a host key", () => {
    useUi.setState({
      sessions: [{ id: "s1", assetId: "a1", name: "jump", kind: "ssh", status: "reconnecting", tabs: [], createdAt: 1 }],
    });
    applySessionStatusEvent({ sessionId: "s1", status: "failed", error: pendingMessage, errorCode: "host_key_pending", version: 2 });
    expect(useUi.getState().toasts.map((toast) => toast.text)).toEqual([`会话失败：${pendingMessage}`]);
  });
});
