import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../ipc/env", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/env")>()),
  WEB: false,
}));
vi.mock("../ipc/wails", () => ({
  disposeWailsChannel: vi.fn(),
  listenWailsEvent: vi.fn(),
  newWailsChannel: vi.fn(),
}));

import { INPUT_FAILURE_THROTTLE_MS, isConnectionLostError, reportInputFailure, setInputFailureHandler } from "../ipc/inputFailure";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(1_000_000);
});
afterEach(() => {
  setInputFailureHandler(null);
  vi.useRealTimers();
});

describe("terminal input failures", () => {
  it("recognizes errors that mean the link or tab is gone", () => {
    expect(isConnectionLostError({ code: "disconnected", message: "x" })).toBe(true);
    expect(isConnectionLostError({ code: "internal", message: "终端标签页已关闭" })).toBe(true);
    expect(isConnectionLostError({ code: "internal", message: "内部错误" })).toBe(false);
    expect(isConnectionLostError(new Error("boom"))).toBe(false);
  });

  it("reports a burst of failures once per window and a connection loss separately", () => {
    const handler = vi.fn();
    setInputFailureHandler(handler);
    reportInputFailure("t", new Error("a"));
    reportInputFailure("t", new Error("b"));
    expect(handler).toHaveBeenCalledTimes(1);
    reportInputFailure("t", { code: "disconnected", message: "断开" });
    expect(handler).toHaveBeenCalledTimes(2);
    expect(handler.mock.calls[1][0]).toMatchObject({ tabId: "t", connectionLost: true });
    reportInputFailure("t", { code: "disconnected", message: "断开" });
    expect(handler).toHaveBeenCalledTimes(2);
    vi.setSystemTime(1_000_000 + INPUT_FAILURE_THROTTLE_MS);
    reportInputFailure("t", { code: "disconnected", message: "断开" });
    expect(handler).toHaveBeenCalledTimes(3);
  });

  it("surfaces a failed desktop input write instead of swallowing it", async () => {
    const { sendChannelInput } = await import("../ipc/events");
    const handler = vi.fn();
    setInputFailureHandler(handler);
    const error = { code: "disconnected", message: "会话已断开" };
    sendChannelInput(null, "tab-9", new TextEncoder().encode("a"), () => Promise.reject(error));
    await vi.waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(handler.mock.calls[0][0]).toEqual({ tabId: "tab-9", error, connectionLost: true });
  });

  it("surfaces a failed web input write when the channel is gone", async () => {
    const { sendChannelInput } = await import("../ipc/webTransport");
    const handler = vi.fn();
    setInputFailureHandler(handler);
    sendChannelInput("no-such-channel", "tab-w", new TextEncoder().encode("a"), () => Promise.reject(new Error("net")));
    await vi.waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(handler.mock.calls[0][0]).toMatchObject({ tabId: "tab-w", connectionLost: false });
  });
});
