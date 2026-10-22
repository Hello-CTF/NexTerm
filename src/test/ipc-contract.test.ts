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

describe("session and terminal facade IPC contracts", () => {
  it("preserves the exact envelopes for the 11 consumer-priority commands", async () => {
    const calls = captureRpc();
    const { sessionApi, terminalApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "pty-1" };
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "session_connect_device", payload: { deviceId: "device-1", name: "Device" }, invoke: () => sessionApi.connectDevice("device-1", "Device") },
      { command: "session_connect_local", payload: null, invoke: () => sessionApi.connectLocal() },
      { command: "session_disconnect", payload: { sessionId: "session-1" }, invoke: () => sessionApi.disconnect("session-1") },
      { command: "session_list", payload: null, invoke: () => sessionApi.list() },
      { command: "session_open_line_tab", payload: { sessionId: "session-1", cols: 120, rows: 40 }, invoke: () => sessionApi.openLineTab("session-1", 120, 40, channel) },
      { command: "session_quick_connect_user", payload: null, invoke: () => sessionApi.quickConnectDefaultUser() },
      { command: "session_reconnect", payload: { sessionId: "session-1" }, invoke: () => sessionApi.reconnect("session-1") },
      { command: "terminal_attach", payload: { sessionId: "session-1", cols: 120, rows: 40 }, invoke: () => terminalApi.attach("session-1", 120, 40, channel) },
      { command: "terminal_broadcast_prepare", payload: { tabIds: ["tab-1", "tab-2"], data: [100, 111] }, invoke: () => terminalApi.prepareBroadcast(["tab-1", "tab-2"], new Uint8Array([100, 111])) },
      { command: "terminal_rename_tab", payload: { tabId: "tab-1", title: "Deploy" }, invoke: () => terminalApi.renameTab("tab-1", "Deploy") },
      { command: "terminal_write", payload: { tabId: "tab-1", data: [100, 111], clientId: expect.any(String) }, invoke: () => terminalApi.write("tab-1", new Uint8Array([100, 111])) },
    ];

    expect(cases).toHaveLength(11);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(11);
  });

  it("preserves explicit durable attachment and terminal-list fallback arguments", async () => {
    const calls = captureRpc();
    const { terminalApi } = await import("../ipc/commands");

    await terminalApi.attach("session-1", 80, 24, { toJSON: () => "pty-1" }, false);
    await terminalApi.listLive();
    await terminalApi.listDurable("session-1");

    expect(calls).toHaveLength(3);
    expectManifestEnvelope(calls[0], "terminal_attach", { sessionId: "session-1", cols: 80, rows: 24, durable: false });
    expectManifestEnvelope(calls[1], "terminal_list", null);
    expectManifestEnvelope(calls[2], "terminal_list", { sessionId: "session-1", durable: true });
  });
});

describe("remote desktop facade IPC contracts", () => {
  it("preserves the exact envelopes for all 28 surveyed commands", async () => {
    const calls = captureRpc();
    const {
      remoteAccountApi,
      remoteFleetApi,
      remoteHealthApi,
      remoteLinkApi,
      remotePreviewApi,
      remoteSharingApi,
      syncCompareApi,
    } = await import("../ipc/remoteApi");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "remote_account_admin_create_user", payload: { username: "alice", password: "password-1", displayName: "Alice" }, invoke: () => remoteAccountApi.adminCreateUser("alice", "password-1", "Alice") },
      { command: "remote_account_admin_set_user_state", payload: { userId: "u-1", state: "disabled" }, invoke: () => remoteAccountApi.adminSetUserState("u-1", "disabled") },
      { command: "remote_account_admin_users", payload: null, invoke: () => remoteAccountApi.adminUsers() },
      { command: "remote_account_change_password", payload: { oldPassword: "old-password", newPassword: "new-password" }, invoke: () => remoteAccountApi.changePassword("old-password", "new-password") },
      { command: "remote_account_device_revoke", payload: { deviceId: "device-1" }, invoke: () => remoteAccountApi.deviceRevoke("device-1") },
      { command: "remote_account_devices", payload: null, invoke: () => remoteAccountApi.devices() },
      { command: "remote_account_me", payload: null, invoke: () => remoteAccountApi.me() },
      { command: "remote_fleet_base_urls_get", payload: null, invoke: () => remoteFleetApi.baseUrls() },
      { command: "remote_fleet_base_urls_put", payload: { entries: [{ url: "https://sync.example.com", insecure: false }] }, invoke: () => remoteFleetApi.putBaseUrls([{ url: "https://sync.example.com", insecure: false }]) },
      { command: "remote_fleet_device_autostart", payload: { deviceId: "device-1", desired: true }, invoke: () => remoteFleetApi.setAutostart("device-1", true) },
      { command: "remote_fleet_device_metrics", payload: { deviceId: "device-1" }, invoke: () => remoteFleetApi.metrics("device-1") },
      { command: "remote_fleet_device_revoke", payload: { deviceId: "device-1" }, invoke: () => remoteFleetApi.revoke("device-1") },
      { command: "remote_fleet_devices", payload: null, invoke: () => remoteFleetApi.devices() },
      { command: "remote_fleet_enroll_code", payload: { ttlMs: 60_000 }, invoke: () => remoteFleetApi.issueEnrollCode(60_000) },
      { command: "remote_healthz", payload: null, invoke: () => remoteHealthApi.serverVersion() },
      { command: "remote_share_host_create", payload: { deviceId: "device-1", recipientUsername: "alice", write: false, ttlMs: 60_000 }, invoke: () => remoteSharingApi.createHostShare({ deviceId: "device-1", recipientUsername: "alice", write: false, ttlMs: 60_000 }) },
      { command: "remote_share_host_list", payload: null, invoke: () => remoteSharingApi.hostShares() },
      { command: "remote_share_host_revoke", payload: { id: "share-1" }, invoke: () => remoteSharingApi.revokeHostShare("share-1") },
      { command: "remote_share_link_list", payload: null, invoke: () => remoteSharingApi.links() },
      { command: "remote_share_link_revoke", payload: { id: "link-1" }, invoke: () => remoteSharingApi.revokeLink("link-1") },
      { command: "remote_share_preview_list", payload: null, invoke: () => remoteSharingApi.previewLinks() },
      { command: "remote_share_preview_revoke", payload: { id: "preview-1" }, invoke: () => remoteSharingApi.revokePreviewLink("preview-1") },
      { command: "preview_share_active", payload: { tabId: "tab-1" }, invoke: () => remotePreviewApi.active("tab-1") },
      { command: "preview_share_create", payload: { tabId: "tab-1", ttlMs: 60_000 }, invoke: () => remotePreviewApi.create("tab-1", 60_000) },
      { command: "sync_compare", payload: null, invoke: () => syncCompareApi.compare() },
      { command: "sync_link_mfa", payload: { code: "123456" }, invoke: () => remoteLinkApi.mfa("123456") },
      { command: "sync_link_reset_password", payload: { newPassword: "new-password" }, invoke: () => remoteLinkApi.resetPassword("new-password") },
      {
        command: "sync_repair_undecryptable",
        payload: { id: "asset-1", action: "restore-local", userId: "u-1", compareToken: "compare-1", remoteSeq: 7, remoteBlobHash: "remote-hash", localKind: "asset", localPayloadHash: "local-hash" },
        invoke: () => syncCompareApi.repairUndecryptable({ id: "asset-1", action: "restore-local", userId: "u-1", compareToken: "compare-1", remoteSeq: 7, remoteBlobHash: "remote-hash", localKind: "asset", localPayloadHash: "local-hash" }),
      },
    ];

    expect(cases).toHaveLength(28);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(28);
  });

  it("preserves explicitly provided optional fields and clear-cloud omissions", async () => {
    const calls = captureRpc();
    const { remoteFleetApi, syncCompareApi } = await import("../ipc/remoteApi");

    await remoteFleetApi.issueEnrollCode(60_000, "u-1");
    await remoteFleetApi.metrics("device-1", 30_000);
    await syncCompareApi.repairUndecryptable({
      id: "asset-1",
      action: "clear-cloud",
      userId: "u-1",
      compareToken: "compare-1",
      remoteSeq: 7,
      remoteBlobHash: "remote-hash",
    });

    expect(calls).toHaveLength(3);
    expectManifestEnvelope(calls[0], "remote_fleet_enroll_code", { ttlMs: 60_000, userId: "u-1" });
    expectManifestEnvelope(calls[1], "remote_fleet_device_metrics", { deviceId: "device-1", sinceMs: 30_000 });
    expectManifestEnvelope(calls[2], "sync_repair_undecryptable", {
      id: "asset-1",
      action: "clear-cloud",
      userId: "u-1",
      compareToken: "compare-1",
      remoteSeq: 7,
      remoteBlobHash: "remote-hash",
    });
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
