import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ipcContractManifest, type IPCCommandName } from "../ipc/generated/manifest";

declare const callForContract: typeof import("../ipc/commands").call;
if (false) {
  // @ts-expect-error nested legacy commands reject flat arguments
  void callForContract("session_connect", { assetId: "asset-1" });
  void callForContract("session_connect", { args: { assetId: "asset-1" } });
  void callForContract("redis_command", { connId: "conn-1", args: ["PING"] });
  void callForContract("terminal_close_tab", { tabId: "tab-1" });
}

type RpcCall = {
  url: unknown;
  cmd?: unknown;
  args?: unknown;
  channel?: unknown;
  clientId?: unknown;
};

function installWebEnv() {
  const storage = new Map<string, string>();
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "", protocol: "http:", host: "127.0.0.1:9" },
    localStorage: {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => void storage.set(key, value),
    },
  });
}

function captureRpc() {
  const calls: RpcCall[] = [];
  vi.stubGlobal("fetch", async (...fetchArgs: unknown[]) => {
    const init = fetchArgs[1] as { body?: string } | undefined;
    const body = JSON.parse(String(init?.body)) as Omit<RpcCall, "url">;
    calls.push({ url: fetchArgs[0], ...body });
    return { text: async () => JSON.stringify({ ok: true, data: null }) };
  });
  return calls;
}

function expectManifestEnvelope(call: RpcCall | undefined, command: IPCCommandName, payload: unknown) {
  if (!call) throw new Error(`missing RPC call for ${command}`);
  const contract = ipcContractManifest.commands[command];
  expect(call.url).toBe("/rpc");
  expect(call.cmd).toBe(command);
  expect(call.args).toEqual(contract.nested ? { args: payload } : payload);
  if (contract.channel) {
    expect(call.channel).toBeDefined();
  } else {
    expect(call.channel).toBeUndefined();
  }
}

beforeEach(() => {
  vi.resetModules();
  installWebEnv();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("generated IPC envelope contract", () => {
  it("matches per-command nested and channel metadata on real facade calls", async () => {
    const calls = captureRpc();
    const { terminalApi } = await import("../ipc/commands");
    const { remoteHostShareApi } = await import("../ipc/remoteApi");

    await terminalApi.write("tab-1", new Uint8Array([1, 2]));
    await terminalApi.prepareBroadcast(["tab-1", "tab-2"], new Uint8Array([100, 111]));
    await terminalApi.attach("session-1", 80, 24, { toJSON: () => "pty-1" });
    await remoteHostShareApi.open("device-1", "stream-1", { toJSON: () => "host-1" });

    expect(calls).toHaveLength(4);
    expectManifestEnvelope(calls[0], "terminal_write", {
      tabId: "tab-1",
      data: [1, 2],
      clientId: expect.any(String),
    });
    expectManifestEnvelope(calls[1], "terminal_broadcast_prepare", {
      tabIds: ["tab-1", "tab-2"],
      data: [100, 111],
    });
    expectManifestEnvelope(calls[2], "terminal_attach", { sessionId: "session-1", cols: 80, rows: 24 });
    expect(calls[2]?.channel).toBe("pty-1");
    expectManifestEnvelope(calls[3], "host_share_terminal_open", { deviceId: "device-1", streamId: "stream-1" });
    expect(calls[3]?.channel).toBe("host-1");
  });
});

describe("cron IPC wire contract", () => {
  it("uses the generated flat envelope for cron_register", async () => {
    const calls = captureRpc();
    const { cronApi } = await import("../ipc/cron");
    const registration = {
      sessionId: "c-1",
      name: "nightly",
      prompt: "do the thing",
      schedule: "0 0 1 1 *",
      timezone: "UTC",
      timeoutMs: 60_000,
    };

    await cronApi.register(registration);

    expect(calls).toHaveLength(1);
    expectManifestEnvelope(calls[0], "cron_register", registration);
  });

  it("keeps cron_list/get/set_enabled/unregister production args flat", async () => {
    const calls = captureRpc();
    const { cronApi } = await import("../ipc/cron");

    await cronApi.list("c-1");
    await cronApi.get("c-1", "j-1");
    await cronApi.setEnabled("c-1", "j-1", false);
    await cronApi.unregister("c-1", "j-1");

    expect(calls).toHaveLength(4);
    expectManifestEnvelope(calls[0], "cron_list", { sessionId: "c-1" });
    expectManifestEnvelope(calls[1], "cron_get", { sessionId: "c-1", jobId: "j-1" });
    expectManifestEnvelope(calls[2], "cron_set_enabled", { sessionId: "c-1", jobId: "j-1", enabled: false });
    expectManifestEnvelope(calls[3], "cron_unregister", { sessionId: "c-1", jobId: "j-1" });
  });
});

describe("ai_edit_resend wire contract", () => {
  it("keeps ai_edit_resend production args flat with exact fields", async () => {
    const calls = captureRpc();
    const { aiApi } = await import("../ipc/commands");

    await aiApi.editResend("conv-1", "msg-3");

    expect(calls).toHaveLength(1);
    expectManifestEnvelope(calls[0], "ai_edit_resend", { conversationId: "conv-1", messageId: "msg-3" });
  });
});

describe("ai_circuit_status wire contract", () => {
  it("keeps ai_circuit_status production args flat with exact fields", async () => {
    const calls = captureRpc();
    const { modelApi } = await import("../ipc/commands");

    await modelApi.circuitStatus("m-1");

    expect(calls).toHaveLength(1);
    expectManifestEnvelope(calls[0], "ai_circuit_status", { id: "m-1" });
  });
});
