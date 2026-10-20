/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    call: vi.fn(),
    dispose: vi.fn(),
    handler: null as ((raw: unknown) => void) | null,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return { ...actual, call: mocks.call };
});

vi.mock("../../ipc/events", () => ({
  createAiChannel: (handler: (raw: unknown) => void) => {
    mocks.handler = handler;
    return { toJSON: () => "stream-1" };
  },
  channelIdOf: () => "stream-1",
  disposeChannel: mocks.dispose,
}));

import { createHostShareConnection } from "../../ipc/hostShareApi";

function recorded() {
  const ready: unknown[] = [];
  const output: Uint8Array[] = [];
  const errors: unknown[] = [];
  const closes: unknown[] = [];
  const connection = createHostShareConnection("device-1", {
    onReady: (value) => ready.push(value),
    onOutput: (value) => output.push(value),
    onError: (value) => errors.push(value),
    onClose: (value) => closes.push(value),
  });
  return { connection, ready, output, errors, closes };
}

beforeEach(() => {
  mocks.handler = null;
  mocks.call.mockResolvedValue({ ok: true });
});

afterEach(() => vi.clearAllMocks());

describe("desktop host share IPC wire", () => {
  it("opens a nested command with a stream channel and forwards read_write input in order", async () => {
    const { connection, ready } = recorded();
    expect(mocks.call).toHaveBeenCalledWith("host_share_terminal_open", {
      args: { deviceId: "device-1", streamId: "stream-1" },
      channel: { toJSON: expect.any(Function) },
    });
    mocks.handler!({ type: "ready", session_id: "s-1", permission: "read_write", expires_at: 10 });
    connection.sendInput("ls\n");
    connection.sendInput("pwd\n");
    await vi.waitFor(() => expect(mocks.call.mock.calls.filter(([name]) => name === "host_share_terminal_input")).toHaveLength(2));
    const inputs = mocks.call.mock.calls.filter(([name]) => name === "host_share_terminal_input");
    expect(new TextDecoder().decode(new Uint8Array(inputs[0][1].args.data))).toBe("ls\n");
    expect(new TextDecoder().decode(new Uint8Array(inputs[1][1].args.data))).toBe("pwd\n");
    expect(ready).toHaveLength(1);
  });

  it("applies a repeated ready frame and blocks shrunk input", async () => {
    const { connection } = recorded();
    mocks.handler!({ type: "ready", session_id: "s-1", permission: "read_write", expires_at: 10 });
    connection.sendInput("before-shrink");
    await vi.waitFor(() => expect(mocks.call.mock.calls.filter(([name]) => name === "host_share_terminal_input")).toHaveLength(1));
    mocks.handler!({ type: "ready", session_id: "s-1", permission: "read", expires_at: 10 });
    connection.sendInput("after-shrink");
    await Promise.resolve();
    expect(mocks.call.mock.calls.filter(([name]) => name === "host_share_terminal_input")).toHaveLength(1);
  });

  it("drops read-only input and decodes streamed output frames", async () => {
    const { connection, output } = recorded();
    mocks.handler!({ type: "ready", session_id: "s-1", permission: "read", expires_at: 10 });
    connection.sendInput("not-allowed");
    await Promise.resolve();
    expect(mocks.call.mock.calls.some(([name]) => name === "host_share_terminal_input")).toBe(false);
    mocks.handler!({ type: "output", data: btoa("terminal output") });
    expect(new TextDecoder().decode(output[0])).toBe("terminal output");
  });

  it("closes both directions and disposes the channel", async () => {
    const { connection } = recorded();
    connection.close();
    await vi.waitFor(() => expect(mocks.call.mock.calls.some(([name]) => name === "host_share_terminal_close")).toBe(true));
    expect(mocks.call).toHaveBeenCalledWith("host_share_terminal_close", { args: { streamId: "stream-1" } });
    expect(mocks.dispose).toHaveBeenCalled();
  });

  it("surfaces a 401/open failure and prevents later input", async () => {
    mocks.call.mockImplementation((name: string) => name === "host_share_terminal_open"
      ? Promise.reject({ code: "forbidden", message: "401 unauthorized" })
      : Promise.resolve({ ok: true }));
    const { connection, errors, closes } = recorded();
    await vi.waitFor(() => expect(errors).toEqual([{ code: "forbidden", message: "401 unauthorized" }]));
    expect(closes).toHaveLength(1);
    connection.sendInput("no");
    expect(mocks.call.mock.calls.some(([name]) => name === "host_share_terminal_input")).toBe(false);
  });

  it("forwards revocation and remote close without allowing more input", async () => {
    const { connection, errors, closes } = recorded();
    mocks.handler!({ type: "ready", session_id: "s-1", permission: "read_write", expires_at: 10 });
    mocks.handler!({ type: "error", code: "forbidden", message: "主机分享已吊销" });
    mocks.handler!({ type: "close", code: 1008, reason: "share terminated", wasClean: true });
    connection.sendInput("no");
    await Promise.resolve();
    expect(errors).toHaveLength(1);
    expect(closes).toHaveLength(1);
    expect(mocks.call.mock.calls.some(([name]) => name === "host_share_terminal_input")).toBe(false);
  });
});
