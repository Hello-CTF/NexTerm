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
    dispatchEvent: vi.fn(),
  });
}

function captureRpc(results: Record<string, unknown> = {}) {
  const calls: RpcCall[] = [];
  vi.stubGlobal("fetch", async (...fetchArgs: unknown[]) => {
    const init = fetchArgs[1] as { body?: string } | undefined;
    const body = JSON.parse(String(init?.body)) as Omit<RpcCall, "url">;
    calls.push({ url: fetchArgs[0], ...body });
    return { text: async () => JSON.stringify({ ok: true, data: results[String(body.cmd)] ?? null }) };
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

describe("sync vertical facade IPC contracts", () => {
  it("preserves the exact envelopes for all 22 newly typed commands and both link-set facades", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");
    const { remoteLinkApi } = await import("../ipc/remoteApi");
    const bundle = { protocol: 1, origin: "desktop", exportedAt: 10, groups: [], assets: [], creds: [] };
    const linkPatch = { url: "https://sync.example.com", username: "alice", password: "login-password", insecure: false };
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "sync_digest", payload: null, invoke: () => syncApi.digest() },
      { command: "sync_export", payload: { assetIds: ["asset-1"], withCreds: true }, invoke: () => syncApi.exportAssets(["asset-1"], true) },
      { command: "sync_import", payload: { bundle, force: false }, invoke: () => syncApi.importBundle(bundle, false) },
      { command: "sync_bundle_read", payload: { path: "/tmp/sync-bundle.nex", password: "bundle-password" }, invoke: () => syncApi.readBundleFile("/tmp/sync-bundle.nex", "bundle-password") },
      { command: "sync_bundle_write", payload: { path: "/tmp/sync-bundle.nex", content: "encrypted-bundle-content", password: "bundle-password" }, invoke: () => syncApi.writeBundleFile("/tmp/sync-bundle.nex", "encrypted-bundle-content", "bundle-password") },
      { command: "sync_link_get", payload: null, invoke: () => syncApi.linkGet() },
      { command: "sync_link_set", payload: linkPatch, invoke: () => syncApi.linkSet(linkPatch) },
      { command: "sync_link_set", payload: linkPatch, invoke: () => remoteLinkApi.linkSet(linkPatch) },
      { command: "sync_status", payload: null, invoke: () => syncApi.status() },
      { command: "sync_wake", payload: null, invoke: () => syncApi.wake() },
      { command: "sync_apply_objects", payload: { objects: [{ id: "object-1", kind: "preference", payload: { nested: [1, true, null] } }] }, invoke: () => syncApi.applyObjects([{ id: "object-1", kind: "preference", payload: { nested: [1, true, null] } }]) },
      { command: "sync_collect_assets", payload: { includeDeleted: true, afterId: "asset-1", limit: 64 }, invoke: () => syncApi.collectAssets(true, "asset-1", 64) },
      { command: "sync_collect_tombstones", payload: { afterId: "tombstone-1", limit: 32 }, invoke: () => syncApi.collectTombstones("tombstone-1", 32) },
      { command: "sync_collect_credentials", payload: { revealSecrets: true, afterId: "credential-1", limit: 16 }, invoke: () => syncApi.collectCredentials(true, "credential-1", 16) },
      { command: "sync_collect_known_hosts", payload: { afterId: "host-1", limit: 8 }, invoke: () => syncApi.collectKnownHosts("host-1", 8) },
      { command: "sync_collect_ai_profiles", payload: { revealSecrets: true, afterId: "profile-1", limit: 4 }, invoke: () => syncApi.collectAIProfiles(true, "profile-1", 4) },
      { command: "sync_collect_ai_permission", payload: null, invoke: () => syncApi.collectAIPermission() },
      { command: "sync_collect_ai_grants", payload: null, invoke: () => syncApi.collectAIGrants() },
      { command: "sync_collect_preferences", payload: null, invoke: () => syncApi.collectPreferences() },
      { command: "sync_preferences_get", payload: null, invoke: () => syncApi.preferencesGet() },
      { command: "sync_preferences_set", payload: { set: { theme: "dark", "custom.nested": { enabled: true } } }, invoke: () => syncApi.preferencesUpdate({ set: { theme: "dark", "custom.nested": { enabled: true } } }) },
      { command: "sync_kind_opt_in_get", payload: null, invoke: () => syncApi.kindOptInGet() },
      { command: "sync_kind_opt_in_set", payload: { knownHost: true, aiProfile: false }, invoke: () => syncApi.kindOptInSet({ knownHost: true, aiProfile: false }) },
    ];

    expect(cases).toHaveLength(23);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(23);
  });

  it("preserves optional omission for links, bundle passwords and scoped preference updates", async () => {
    const calls = captureRpc();
    const { syncApi } = await import("../ipc/commands");
    const { remoteLinkApi } = await import("../ipc/remoteApi");

    await syncApi.linkSet({ username: "alice" });
    await remoteLinkApi.linkSet({ password: "updated-password" });
    await syncApi.readBundleFile("/tmp/sync-bundle.nex");
    await syncApi.writeBundleFile("/tmp/sync-bundle.nex", "bundle-content");
    await syncApi.preferencesUpdate({ clear: ["theme"] });
    await syncApi.kindOptInSet({ knownHost: false });

    expect(calls).toHaveLength(6);
    expectManifestEnvelope(calls[0], "sync_link_set", { username: "alice" });
    expectManifestEnvelope(calls[1], "sync_link_set", { password: "updated-password" });
    expectManifestEnvelope(calls[2], "sync_bundle_read", { path: "/tmp/sync-bundle.nex" });
    expectManifestEnvelope(calls[3], "sync_bundle_write", { path: "/tmp/sync-bundle.nex", content: "bundle-content" });
    expectManifestEnvelope(calls[4], "sync_preferences_set", { clear: ["theme"] });
    expectManifestEnvelope(calls[5], "sync_kind_opt_in_set", { knownHost: false });
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

  it("omits unset registration options and preserves owner, schedule and run result fields", async () => {
    const job = {
      id: "j-1",
      sessionId: "c-1",
      prompt: "check disk",
      schedule: "0 2 * * *",
      timezone: "UTC",
      enabled: true,
      timeout: 60_000_000_000,
      createdAt: "2030-01-01T00:00:00Z",
      updatedAt: "2030-01-01T00:00:00Z",
      revision: 1,
      nextRunAt: "2030-01-02T02:00:00Z",
      lease: { owner: "owner-1", expiresAt: "2030-01-01T00:01:00Z" },
      run: {
        id: "run-1",
        scheduledFor: "2030-01-01T02:00:00Z",
        startedAt: "2030-01-01T02:00:01Z",
        deadline: "2030-01-01T02:01:01Z",
        coalesced: true,
      },
      ownerId: "account-1",
      ownerKind: "account",
    };
    const calls = captureRpc({ cron_register: job });
    const { cronApi } = await import("../ipc/cron");

    await expect(cronApi.register({ sessionId: "c-1", prompt: "check disk", schedule: "0 2 * * *" })).resolves.toEqual(job);

    expect(calls).toHaveLength(1);
    expectManifestEnvelope(calls[0], "cron_register", {
      sessionId: "c-1",
      prompt: "check disk",
      schedule: "0 2 * * *",
    });
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

describe("logTail IPC wire contract", () => {
  it("preserves start, cancel and save envelopes and facade result shapes", async () => {
    const startResult = { runId: "run-1" };
    const cancelResult = { runId: "run-1", canceled: true };
    const saveResult = { path: "/tmp/merged.log", bytes: 13 };
    const calls = captureRpc({
      log_tail_start: startResult,
      log_tail_cancel: cancelResult,
      log_tail_save: saveResult,
    });
    const { logTailApi } = await import("../ipc/logTailApi");
    const request = {
      runId: "run-1",
      sources: [{ sourceId: "source-1", sessionId: "session-1", kind: "file" as const, value: "/var/log/app.log" }],
      maxConcurrent: 2,
    };

    await expect(logTailApi.start(request, { toJSON: () => "log-tail-1" })).resolves.toEqual(startResult);
    await expect(logTailApi.cancel("run-1")).resolves.toEqual(cancelResult);
    await expect(logTailApi.save("/tmp/merged.log", "first\nsecond\n")).resolves.toEqual(saveResult);

    expect(calls).toHaveLength(3);
    expectManifestEnvelope(calls[0], "log_tail_start", request);
    expect(calls[0]?.channel).toBe("log-tail-1");
    expectManifestEnvelope(calls[1], "log_tail_cancel", { runId: "run-1" });
    expectManifestEnvelope(calls[2], "log_tail_save", {
      path: "/tmp/merged.log",
      contentBase64: "Zmlyc3QKc2Vjb25kCg==",
    });
  });

  it("omits maxConcurrent when the caller leaves it unset", async () => {
    const calls = captureRpc();
    const { logTailApi } = await import("../ipc/logTailApi");
    const request = {
      runId: "run-1",
      sources: [{ sourceId: "source-1", sessionId: "session-1", kind: "journal" as const, value: "nginx.service" }],
    };

    await logTailApi.start(request, { toJSON: () => "log-tail-1" });

    expect(calls).toHaveLength(1);
    expectManifestEnvelope(calls[0], "log_tail_start", request);
    expect(calls[0]?.args).not.toHaveProperty("maxConcurrent");
  });
});

describe("asset, group and known-host facade IPC contracts", () => {
  it("preserves the exact envelopes for all 16 commands and successful mutation notifications", async () => {
    const calls = captureRpc();
    const { assetApi } = await import("../ipc/commands");
    const fullKey = "AAAAC3NzaC1lZDI1NTE5AAAAIFakeDemoKey";
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "asset_list", payload: null, invoke: () => assetApi.list() },
      { command: "asset_get", payload: { id: "asset-1" }, invoke: () => assetApi.get("asset-1") },
      { command: "asset_create", payload: { kind: "ssh", name: "Web", options: { nested: [1, true, null] } }, invoke: () => assetApi.create({ kind: "ssh", name: "Web", options: { nested: [1, true, null] } }) },
      { command: "asset_update", payload: { id: "asset-1", groupId: null, options: { free: "form" } }, invoke: () => assetApi.update({ id: "asset-1", groupId: null, options: { free: "form" } }) },
      { command: "asset_read_key_file", payload: { path: "/tmp/example-key" }, invoke: () => assetApi.readKeyFile("/tmp/example-key") },
      { command: "asset_delete", payload: { id: "asset-1" }, invoke: () => assetApi.delete("asset-1") },
      { command: "asset_search", payload: { q: "web" }, invoke: () => assetApi.search("web") },
      { command: "asset_search_meta", payload: { q: "web" }, invoke: () => assetApi.searchMeta("web") },
      { command: "asset_probe_batch", payload: { assetIds: ["asset-1", "asset-2"], timeoutMs: 2500, maxConcurrent: 8 }, invoke: () => assetApi.probeBatch(["asset-1", "asset-2"], 2500, 8) },
      { command: "group_list", payload: null, invoke: () => assetApi.groupList() },
      { command: "group_create", payload: { name: "Production", parentId: "group-parent" }, invoke: () => assetApi.groupCreate("Production", "group-parent") },
      { command: "group_update", payload: { id: "group-1", name: "Staging", parentId: null }, invoke: () => assetApi.groupUpdate("group-1", "Staging", null) },
      { command: "group_delete", payload: { id: "group-1" }, invoke: () => assetApi.groupDelete("group-1") },
      { command: "known_host_list", payload: null, invoke: () => assetApi.knownHostList() },
      { command: "known_host_accept", payload: { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc", key: fullKey }, invoke: () => assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc", fullKey) },
      { command: "known_host_remove", payload: { id: "known-1" }, invoke: () => assetApi.knownHostRemove("known-1") },
    ];

    expect(cases).toHaveLength(16);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(16);
    expect(window.dispatchEvent).toHaveBeenCalledTimes(6);
  });

  it("preserves search metadata and omits unset optional fields without adding trust side effects", async () => {
    const metadata = { assets: [], total: 101, truncated: true };
    const calls = captureRpc({ asset_search_meta: metadata });
    const { assetApi } = await import("../ipc/commands");

    await expect(assetApi.searchMeta("missing")).resolves.toEqual(metadata);
    await assetApi.create({ kind: "local", name: "Local" });
    await assetApi.update({ id: "asset-1", groupId: null });
    await assetApi.probeBatch(["asset-1"]);
    await assetApi.groupCreate("Root");
    await assetApi.groupUpdate("group-1", undefined, null);
    await assetApi.knownHostAccept("10.0.0.8", 22, "ssh-ed25519", "SHA256:abc");

    expect(calls).toHaveLength(7);
    expectManifestEnvelope(calls[0], "asset_search_meta", { q: "missing" });
    expectManifestEnvelope(calls[1], "asset_create", { kind: "local", name: "Local" });
    expectManifestEnvelope(calls[2], "asset_update", { id: "asset-1", groupId: null });
    expectManifestEnvelope(calls[3], "asset_probe_batch", { assetIds: ["asset-1"] });
    expectManifestEnvelope(calls[4], "group_create", { name: "Root" });
    expectManifestEnvelope(calls[5], "group_update", { id: "group-1", parentId: null });
    expectManifestEnvelope(calls[6], "known_host_accept", { host: "10.0.0.8", port: 22, keyType: "ssh-ed25519", fingerprint: "SHA256:abc" });
    expect(window.dispatchEvent).toHaveBeenCalledTimes(4);
  });
});

describe("vault and credential facade IPC contracts", () => {
  it("preserves the exact envelopes for all 13 commands, including explicit empty secrets", async () => {
    const calls = captureRpc();
    const { vaultApi } = await import("../ipc/commands");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "vault_status", payload: null, invoke: () => vaultApi.status() },
      { command: "vault_init_master", payload: { password: "" }, invoke: () => vaultApi.initMaster("") },
      { command: "vault_init_dpapi", payload: { password: "" }, invoke: () => vaultApi.initDpapi() },
      { command: "vault_unlock", payload: { password: "" }, invoke: () => vaultApi.unlock("") },
      { command: "vault_lock", payload: null, invoke: () => vaultApi.lock() },
      { command: "vault_set_autolock", payload: { minutes: 0 }, invoke: () => vaultApi.setAutoLock(0) },
      { command: "vault_change_password", payload: { oldPassword: "", newPassword: "" }, invoke: () => vaultApi.changePassword("", "") },
      {
        command: "vault_set_credential",
        payload: { id: "cred-1", name: "deploy-key", kind: "private_key", secret: "fake-private-key", source: "inline", passphrase: "" },
        invoke: () => vaultApi.setCredential("deploy-key", "private_key", "fake-private-key", { id: "cred-1", source: "inline", passphrase: "" }),
      },
      { command: "vault_list_credentials", payload: null, invoke: () => vaultApi.listCredentials() },
      { command: "vault_delete_credential", payload: { id: "cred-1" }, invoke: () => vaultApi.deleteCredential("cred-1") },
      { command: "vault_reveal_credential", payload: { id: "cred-1" }, invoke: () => vaultApi.revealCredential("cred-1") },
      {
        command: "credential_update",
        payload: { id: "cred-1", name: "renamed", secret: "", source: "file", passphrase: "" },
        invoke: () => vaultApi.updateCredential("cred-1", { name: "renamed", secret: "", source: "file", passphrase: "" }),
      },
      {
        command: "vault_generate_key",
        payload: { name: "generated-key", algorithm: "ed25519", passphrase: "" },
        invoke: () => vaultApi.generateKey("generated-key", "ed25519", ""),
      },
    ];

    expect(cases).toHaveLength(13);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(13);
  });

  it("omits unset credential fields without normalizing explicit empty values", async () => {
    const calls = captureRpc();
    const { call, vaultApi } = await import("../ipc/commands");

    await vaultApi.setCredential("database", "password", "");
    await vaultApi.updateCredential("cred-1", {});
    await vaultApi.generateKey("default-key");
    await call("credential_update", { args: { id: "cred-1", source: "" } });

    expect(calls).toHaveLength(4);
    expectManifestEnvelope(calls[0], "vault_set_credential", { name: "database", kind: "password", secret: "" });
    expectManifestEnvelope(calls[1], "credential_update", { id: "cred-1" });
    expectManifestEnvelope(calls[2], "vault_generate_key", { name: "default-key" });
    expectManifestEnvelope(calls[3], "credential_update", { id: "cred-1", source: "" });
  });

  it("passes status, usage, reveal and generated public-key results through unchanged", async () => {
    const status = { initialized: true, mode: "master", unlocked: false, autoLockMinutes: 0, passwordless: false, systemProtection: false };
    const credential = { id: "cred-1", name: "deploy", kind: "private_key", createdAt: 1, updatedAt: 2, usedBy: [{ id: "asset-1", name: "Web", kind: "ssh" }], source: "file", refPath: "/tmp/fake-key", hasPassphrase: true };
    const revealed = { kind: "private_key", value: "fake-private-key", source: "file", refPath: "/tmp/fake-key", passphrase: null };
    const generated = { id: "cred-2", name: "generated", algorithm: "ed25519", fingerprint: "SHA256:fake", publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeDemoKey" };
    const calls = captureRpc({
      vault_status: status,
      vault_list_credentials: [credential],
      vault_reveal_credential: revealed,
      vault_generate_key: generated,
    });
    const { vaultApi } = await import("../ipc/commands");

    await expect(vaultApi.status()).resolves.toEqual(status);
    await expect(vaultApi.listCredentials()).resolves.toEqual([credential]);
    await expect(vaultApi.revealCredential("cred-1")).resolves.toEqual(revealed);
    await expect(vaultApi.generateKey("generated", "ed25519")).resolves.toEqual(generated);

    expect(calls).toHaveLength(4);
    expectManifestEnvelope(calls[3], "vault_generate_key", { name: "generated", algorithm: "ed25519" });
  });
});

describe("fs, desktop local-file and mount facade IPC contracts", () => {
  it("preserves the exact envelopes for all 26 commands, including upload identity and mount secrets", async () => {
    const mountEntry = { id: "mount-1", localPoint: "Z:", remote: "\\\\nas\\share", sessionId: "session-1", createdAt: 1, managed: true };
    const calls = captureRpc({
      fs_roots: { pathStyle: "posix", roots: [], homePath: null, supported: true },
      fs_list: [],
      fs_cancel: true,
      mount_create: mountEntry,
    });
    const { fsApi, localFileApi, mountApi } = await import("../ipc/commands");
    const identity = { sessionId: "session-1", taskId: "task-1", attemptId: "attempt-1" };
    const upload = { ...identity, uploadId: "upload-1" };
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "fs_roots", payload: { sessionId: "session-1" }, invoke: () => fsApi.roots("session-1") },
      { command: "fs_list", payload: { sessionId: "session-1", path: "/remote" }, invoke: () => fsApi.list("session-1", "/remote") },
      { command: "fs_read", payload: { sessionId: "session-1", path: "/remote/a.bin", maxBytes: 1024 }, invoke: () => fsApi.read("session-1", "/remote/a.bin", 1024) },
      { command: "fs_read_range", payload: { sessionId: "session-1", path: "/remote/a.bin", offset: 32, maxBytes: 64 }, invoke: () => fsApi.readRange("session-1", "/remote/a.bin", 32, 64) },
      { command: "fs_write", payload: { sessionId: "session-1", path: "/remote/a.bin", contentBase64: "AAEC/w==", backup: false, taskId: "task-1", attemptId: "attempt-1" }, invoke: () => fsApi.write("session-1", "/remote/a.bin", "AAEC/w==", false, "task-1", "attempt-1") },
      { command: "fs_mkdir", payload: { sessionId: "session-1", path: "/remote/new" }, invoke: () => fsApi.mkdir("session-1", "/remote/new") },
      { command: "fs_rename", payload: { sessionId: "session-1", from: "/remote/old", to: "/remote/new" }, invoke: () => fsApi.rename("session-1", "/remote/old", "/remote/new") },
      { command: "fs_delete", payload: { sessionId: "session-1", path: "/remote/old", isDir: false }, invoke: () => fsApi.delete("session-1", "/remote/old", false) },
      { command: "fs_chmod", payload: { sessionId: "session-1", path: "/remote/a.bin", mode: 0o644 }, invoke: () => fsApi.chmod("session-1", "/remote/a.bin", 0o644) },
      { command: "fs_checksum", payload: { sessionId: "session-1", path: "/remote/a.bin", algo: "sha256" }, invoke: () => fsApi.checksum("session-1", "/remote/a.bin", "sha256") },
      { command: "fs_upload", payload: { sessionId: "session-1", localPath: "C:\\local\\a.bin", remotePath: "/remote/a.bin", resume: true, taskId: "task-1", attemptId: "attempt-1" }, invoke: () => fsApi.upload("session-1", "C:\\local\\a.bin", "/remote/a.bin", true, "task-1", "attempt-1") },
      { command: "fs_upload_begin", payload: { ...identity, remotePath: "/remote/a.bin", totalSize: 4, chunkSize: 1024 }, invoke: () => fsApi.uploadBegin({ ...identity, remotePath: "/remote/a.bin", totalSize: 4, chunkSize: 1024 }) },
      { command: "fs_upload_chunk", payload: { ...upload, index: 0, size: 4, sha256: "3d1f57c984978ef98a18378c8166c1cb8ede02c03eeb6aee7e2f121dfeee3e56", contentBase64: "AAEC/w==" }, invoke: () => fsApi.uploadChunk({ ...upload, index: 0, size: 4, sha256: "3d1f57c984978ef98a18378c8166c1cb8ede02c03eeb6aee7e2f121dfeee3e56", contentBase64: "AAEC/w==" }) },
      { command: "fs_upload_commit", payload: upload, invoke: () => fsApi.uploadCommit(upload) },
      { command: "fs_upload_abort", payload: upload, invoke: () => fsApi.uploadAbort(upload) },
      { command: "fs_download", payload: { sessionId: "session-1", remotePath: "/remote/a.bin", localPath: "C:\\local\\a.bin", resume: true, taskId: "task-1", attemptId: "attempt-1" }, invoke: () => fsApi.download("session-1", "/remote/a.bin", "C:\\local\\a.bin", true, "task-1", "attempt-1") },
      { command: "fs_pack_download", payload: { sessionId: "session-1", remotePath: "/remote/dir", localPath: "C:\\local\\dir.tar.gz", taskId: "task-1", attemptId: "attempt-1" }, invoke: () => fsApi.packDownload("session-1", "/remote/dir", "C:\\local\\dir.tar.gz", "task-1", "attempt-1") },
      { command: "fs_cancel", payload: { taskId: "task-1", attemptId: "attempt-1" }, invoke: () => expect(fsApi.cancel("task-1", "attempt-1")).resolves.toBe(true) },
      { command: "fs_extract", payload: { sessionId: "session-1", path: "/remote/dir.tar.gz" }, invoke: () => fsApi.extract("session-1", "/remote/dir.tar.gz") },
      { command: "local_folder_list", payload: { path: "C:\\local\\pics" }, invoke: () => localFileApi.listFolder("C:\\local\\pics") },
      { command: "local_file_read", payload: { path: "C:\\local\\a.bin", maxBytes: 1024 }, invoke: () => localFileApi.read("C:\\local\\a.bin", 1024) },
      { command: "local_file_read_range", payload: { path: "C:\\local\\a.bin", offset: 2, maxBytes: 4 }, invoke: () => localFileApi.readRange("C:\\local\\a.bin", 2, 4) },
      { command: "mount_capability", payload: null, invoke: () => mountApi.capability() },
      { command: "mount_list", payload: { sessionId: "session-1", forceRefresh: true }, invoke: () => mountApi.list({ sessionId: "session-1", forceRefresh: true }) },
      { command: "mount_create", payload: { sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:", username: "alice", password: "mount-secret" }, invoke: () => expect(mountApi.create({ sessionId: "session-1", remotePath: "\\\\nas\\share", localPoint: "Z:", username: "alice", password: "mount-secret" })).resolves.toEqual(mountEntry) },
      { command: "mount_remove", payload: { localPoint: "Z:", sessionId: "session-1" }, invoke: () => mountApi.remove("Z:", "session-1") },
    ];

    expect(cases).toHaveLength(26);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(26);
  });

  it("omits only facade-optional fields and passes base64, truncation and null results through unchanged", async () => {
    const read = { path: "/remote/a.bin", size: 4, contentBase64: "AAEC/w==" };
    const range = { path: "/remote/a.bin", offset: 16, size: 128, contentBase64: "AAEC/w==", truncated: true };
    const localRead = { path: "C:\\local\\a.bin", size: 4, contentBase64: "AAEC/w==" };
    const localRange = { path: "C:\\local\\a.bin", offset: 0, size: 4, contentBase64: "AAEC/w==", truncated: false };
    const calls = captureRpc({
      fs_read: read,
      fs_read_range: range,
      local_file_read: localRead,
      local_file_read_range: localRange,
    });
    const { fsApi, localFileApi, mountApi } = await import("../ipc/commands");

    await expect(fsApi.read("session-1", "/remote/a.bin")).resolves.toEqual(read);
    await expect(fsApi.readRange("session-1", "/remote/a.bin", 16)).resolves.toEqual(range);
    await fsApi.upload("session-1", "C:\\local\\a.bin", "/remote/a.bin");
    await fsApi.download("session-1", "/remote/a.bin", "C:\\local\\a.bin");
    await fsApi.packDownload("session-1", "/remote/dir", "C:\\local\\dir.tar.gz");
    await expect(fsApi.write("session-1", "/remote/empty", "")).resolves.toBeNull();
    await fsApi.cancel("task-1");
    await expect(localFileApi.read("C:\\local\\a.bin")).resolves.toEqual(localRead);
    await expect(localFileApi.readRange("C:\\local\\a.bin", 0)).resolves.toEqual(localRange);
    await expect(mountApi.remove("Z:")).resolves.toBeNull();

    expect(calls).toHaveLength(10);
    expectManifestEnvelope(calls[0], "fs_read", { sessionId: "session-1", path: "/remote/a.bin" });
    expectManifestEnvelope(calls[1], "fs_read_range", { sessionId: "session-1", path: "/remote/a.bin", offset: 16 });
    expectManifestEnvelope(calls[2], "fs_upload", { sessionId: "session-1", localPath: "C:\\local\\a.bin", remotePath: "/remote/a.bin" });
    expectManifestEnvelope(calls[3], "fs_download", { sessionId: "session-1", remotePath: "/remote/a.bin", localPath: "C:\\local\\a.bin" });
    expectManifestEnvelope(calls[4], "fs_pack_download", { sessionId: "session-1", remotePath: "/remote/dir", localPath: "C:\\local\\dir.tar.gz" });
    expectManifestEnvelope(calls[5], "fs_write", { sessionId: "session-1", path: "/remote/empty", contentBase64: "", backup: true });
    expectManifestEnvelope(calls[6], "fs_cancel", { taskId: "task-1" });
    expectManifestEnvelope(calls[7], "local_file_read", { path: "C:\\local\\a.bin" });
    expectManifestEnvelope(calls[8], "local_file_read_range", { path: "C:\\local\\a.bin", offset: 0 });
    expectManifestEnvelope(calls[9], "mount_remove", { localPoint: "Z:" });
  });
});

describe("docker vertical facade IPC contracts", () => {
  it("preserves the exact envelopes for all 20 commands and stable attach identity", async () => {
    const prepared = { ticket: "ticket-1", contentHash: "d".repeat(64) };
    const calls = captureRpc({
      docker_logs_attach: "logs-stream",
      docker_exec_attach: "exec-stream",
      docker_compose_logs_attach: "compose-logs-stream",
      docker_compose_redeploy_prepare: prepared,
      docker_compose_redeploy_attach: "redeploy-stream",
    });
    const { dockerApi } = await import("../ipc/commands");
    const productionConfirmation = {
      confirmed: true as const,
      targetSetHash: "a".repeat(64),
      commandHash: "b".repeat(64),
      environmentNames: ["production"],
    };
    const logsChannel = { toJSON: () => "logs-channel" };
    const execChannel = { toJSON: () => "exec-channel" };
    const composeLogsChannel = { toJSON: () => "compose-logs-channel" };
    const redeployChannel = { toJSON: () => "redeploy-channel" };
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "docker_overview", payload: { sessionId: "s1" }, invoke: () => dockerApi.overview("s1") },
      { command: "docker_ps", payload: { sessionId: "s1" }, invoke: () => dockerApi.ps("s1") },
      { command: "docker_logs_attach", payload: { sessionId: "s1", containerId: "c1", tail: 500 }, invoke: () => dockerApi.logsAttach("s1", "c1", 500, logsChannel) },
      { command: "docker_exec_attach", payload: { sessionId: "s1", containerId: "c1", cmd: "sh", cols: 120, rows: 40, clientId: expect.any(String) }, invoke: () => dockerApi.execAttach("s1", "c1", 120, 40, execChannel, "sh") },
      { command: "docker_action", payload: { sessionId: "s1", containerId: "c1", action: "rename", newName: "web-new" }, invoke: () => dockerApi.action("s1", "c1", "rename", "web-new") },
      { command: "docker_images", payload: { sessionId: "s1" }, invoke: () => dockerApi.images("s1") },
      { command: "docker_image_pull", payload: { sessionId: "s1", image: "nginx:latest" }, invoke: () => dockerApi.imagePull("s1", "nginx:latest") },
      { command: "docker_image_remove", payload: { sessionId: "s1", image: "nginx:latest", force: false }, invoke: () => dockerApi.imageRemove("s1", "nginx:latest", false) },
      { command: "docker_inspect", payload: { sessionId: "s1", containerId: "c1" }, invoke: () => dockerApi.inspect("s1", "c1") },
      { command: "docker_stats", payload: { sessionId: "s1" }, invoke: () => dockerApi.stats("s1") },
      { command: "docker_container_list_dir", payload: { sessionId: "s1", containerId: "c1", path: "/srv" }, invoke: () => dockerApi.containerListDir("s1", "c1", "/srv") },
      { command: "docker_compose_stacks", payload: { sessionId: "s1" }, invoke: () => dockerApi.composeStacks("s1") },
      { command: "docker_compose_action", payload: { sessionId: "s1", project: "shop", action: "down", productionConfirmation }, invoke: () => dockerApi.composeAction("s1", "shop", "down", productionConfirmation) },
      { command: "docker_compose_logs_attach", payload: { sessionId: "s1", project: "shop", tail: 500 }, invoke: () => dockerApi.composeLogsAttach("s1", "shop", 500, composeLogsChannel) },
      {
        command: "docker_compose_redeploy_prepare",
        payload: { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3", productionConfirmation },
        invoke: async () => expect(await dockerApi.composeRedeployPrepare({ sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3", productionConfirmation })).toEqual(prepared),
      },
      { command: "docker_compose_redeploy_attach", payload: { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", ticket: "ticket-1" }, invoke: () => dockerApi.composeRedeployAttach({ sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", ticket: "ticket-1", channel: redeployChannel }) },
      { command: "docker_disk_usage", payload: { sessionId: "s1" }, invoke: () => dockerApi.diskUsage("s1") },
      { command: "docker_prune_preview", payload: { sessionId: "s1", kinds: ["containers", "images"] }, invoke: () => dockerApi.prunePreview("s1", ["containers", "images"]) },
      { command: "docker_prune", payload: { sessionId: "s1", token: "preview-token", digest: "c".repeat(64), productionConfirmation }, invoke: () => dockerApi.prune("s1", "preview-token", "c".repeat(64), productionConfirmation) },
      { command: "docker_reclaimable", payload: { sessionId: "s1" }, invoke: () => dockerApi.reclaimable("s1") },
    ];

    expect(cases).toHaveLength(20);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(20);
    expect(calls[2]?.channel).toBe("logs-channel");
    expect(calls[3]?.channel).toBe("exec-channel");
    expect(calls[13]?.channel).toBe("compose-logs-channel");
    expect(calls[15]?.channel).toBe("redeploy-channel");
    const execClientId = (calls[3]?.args as { args: { clientId: unknown } }).args.clientId;
    const attachClientIds = [calls[2]?.clientId, execClientId, calls[13]?.clientId, calls[15]?.clientId];
    expect(attachClientIds[0]).toEqual(expect.any(String));
    for (const id of attachClientIds) expect(id).toBe(attachClientIds[0]);
    expect(calls[3]?.clientId).toBeUndefined();
  });

  it("omits only unset facade options without dropping operation identity", async () => {
    const calls = captureRpc();
    const { dockerApi } = await import("../ipc/commands");
    const channel = { toJSON: () => "exec-channel" };

    await dockerApi.execAttach("s1", "c1", 80, 24, channel);
    await dockerApi.action("s1", "c1", "restart");
    await dockerApi.imageRemove("s1", "nginx:latest");
    await dockerApi.composeAction("s1", "shop", "up");
    await dockerApi.composeRedeployPrepare({ sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3" });
    await dockerApi.prune("s1", "preview-token", "c".repeat(64));

    expect(calls).toHaveLength(6);
    expectManifestEnvelope(calls[0], "docker_exec_attach", { sessionId: "s1", containerId: "c1", cols: 80, rows: 24, clientId: expect.any(String) });
    expect(calls[0]?.args).not.toHaveProperty("args.cmd");
    expectManifestEnvelope(calls[1], "docker_action", { sessionId: "s1", containerId: "c1", action: "restart" });
    expectManifestEnvelope(calls[2], "docker_image_remove", { sessionId: "s1", image: "nginx:latest" });
    expectManifestEnvelope(calls[3], "docker_compose_action", { sessionId: "s1", project: "shop", action: "up" });
    expectManifestEnvelope(calls[4], "docker_compose_redeploy_prepare", { sessionId: "s1", project: "shop", path: "/srv/shop/compose.yml", baseContentBase64: "b2xk", contentBase64: "bmV3" });
    expectManifestEnvelope(calls[5], "docker_prune", { sessionId: "s1", token: "preview-token", digest: "c".repeat(64) });
  });
});

describe("database and Redis facade IPC contracts", () => {
  it("preserves all 10 envelopes, dynamic result shapes and null mutation results", async () => {
    const queryResult = {
      columns: ["id", "payload"],
      rows: [[1, { nested: [true, null] }]],
      rowsAffected: 3,
      durationMs: 7,
      truncated: true,
      error: "bad SQL",
    };
    const description = {
      columns: [{ name: "id", type: "BIGINT", nullable: false, key: "PRI", default: null, extra: "auto_increment" }],
      indexes: [{ name: "PRIMARY", unique: true, column: "id", seq: 1 }],
    };
    const redisView = { key: "key", keyType: "hash", ttl: -1, value: { field: [1, true, null] } };
    const calls = captureRpc({
      db_connect: { connId: "conn-1" },
      db_schemas: ["public"],
      db_tables: ["orders"],
      db_columns: description,
      db_query: queryResult,
      redis_scan: [7, ["key"]],
      redis_inspect: redisView,
      redis_command: "OK",
    });
    const { dbApi } = await import("../ipc/commands");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "db_connect", payload: { assetId: "asset-1" }, invoke: async () => expect(await dbApi.connect("asset-1")).toEqual({ connId: "conn-1" }) },
      { command: "db_disconnect", payload: { connId: "conn-1" }, invoke: () => expect(dbApi.disconnect("conn-1")).resolves.toBeNull() },
      { command: "db_schemas", payload: { connId: "conn-1" }, invoke: async () => expect(await dbApi.schemas("conn-1")).toEqual(["public"]) },
      { command: "db_tables", payload: { connId: "conn-1", schema: "" }, invoke: async () => expect(await dbApi.tables("conn-1", "")).toEqual(["orders"]) },
      { command: "db_columns", payload: { connId: "conn-1", table: "orders", schema: "" }, invoke: async () => expect(await dbApi.columns("conn-1", "orders", "")).toEqual(description) },
      { command: "db_query", payload: { connId: "conn-1", sql: "UPDATE orders SET paid = true", timeoutMs: 0 }, invoke: async () => expect(await dbApi.query("conn-1", "UPDATE orders SET paid = true", 0)).toEqual(queryResult) },
      { command: "redis_scan", payload: { connId: "conn-1", cursor: 0, pattern: "", count: 0 }, invoke: async () => expect(await dbApi.redisScan("conn-1", 0, "", 0)).toEqual([7, ["key"]]) },
      { command: "redis_inspect", payload: { connId: "conn-1", key: "" }, invoke: async () => expect(await dbApi.redisInspect("conn-1", "")).toEqual(redisView) },
      { command: "redis_command", payload: { connId: "conn-1", args: ["SET", "key", ""] }, invoke: async () => expect(await dbApi.redisCommand("conn-1", ["SET", "key", ""])).toBe("OK") },
      { command: "redis_set_ttl", payload: { connId: "conn-1", key: "", seconds: 0 }, invoke: () => expect(dbApi.redisSetTtl("conn-1", "", 0)).resolves.toBeNull() },
    ];

    expect(cases).toHaveLength(10);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(10);
  });

  it("omits unset facade options in actual web JSON while preserving direct nullable pointer fields", async () => {
    const calls = captureRpc();
    const { call, dbApi } = await import("../ipc/commands");

    await dbApi.tables("conn-1");
    await dbApi.columns("conn-1", "orders");
    await dbApi.query("conn-1", "SELECT 1");
    await dbApi.redisScan("conn-1");
    await call("db_query", { connId: "conn-1", sql: "SELECT 1", timeoutMs: null });
    await call("redis_scan", { connId: "conn-1", cursor: null, pattern: null, count: null });

    expect(calls).toHaveLength(6);
    expectManifestEnvelope(calls[0], "db_tables", { connId: "conn-1" });
    expectManifestEnvelope(calls[1], "db_columns", { connId: "conn-1", table: "orders" });
    expectManifestEnvelope(calls[2], "db_query", { connId: "conn-1", sql: "SELECT 1" });
    expectManifestEnvelope(calls[3], "redis_scan", { connId: "conn-1" });
    expectManifestEnvelope(calls[4], "db_query", { connId: "conn-1", sql: "SELECT 1", timeoutMs: null });
    expectManifestEnvelope(calls[5], "redis_scan", { connId: "conn-1", cursor: null, pattern: null, count: null });
  });
});

describe("transcript facade IPC contracts", () => {
  it("preserves all six flat envelopes, partial reads and mutation result payloads", async () => {
    const hosts = [{ assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 10 }];
    const summaries = [{
      id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
      startedAt: 10, endedAt: 20, bytes: 3, storedBytes: 2, chunks: 1, truncated: false, active: false,
      syncOptIn: true, contentOmitted: false,
    }];
    const partial = {
      chunks: [{ seq: 3, tabId: "tab-1", ts: 12, kind: 0, dataBase64: "AQID" }],
      nextSeq: 4,
      done: false,
      totalBytes: 9,
    };
    const matches = [{ seq: 3, ts: 12, preview: "match" }];
    const calls = captureRpc({
      transcript_hosts: hosts,
      transcript_list: summaries,
      transcript_read: partial,
      transcript_search: matches,
      transcript_sync_opt_in: {},
    });
    const { transcriptApi } = await import("../ipc/commands");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "transcript_hosts", payload: null, invoke: async () => expect(await transcriptApi.hosts()).toEqual(hosts) },
      { command: "transcript_list", payload: { assetId: "a1" }, invoke: async () => expect(await transcriptApi.list("a1")).toEqual(summaries) },
      { command: "transcript_read", payload: { id: "t1", afterSeq: 3, maxBytes: 1024 }, invoke: async () => expect(await transcriptApi.read("t1", 3, 1024)).toEqual(partial) },
      { command: "transcript_search", payload: { id: "t1", query: "match" }, invoke: async () => expect(await transcriptApi.search("t1", "match")).toEqual(matches) },
      { command: "transcript_delete", payload: { id: "t1" }, invoke: () => expect(transcriptApi.remove("t1")).resolves.toBeNull() },
      { command: "transcript_sync_opt_in", payload: { id: "t1", optIn: true }, invoke: () => expect(transcriptApi.syncOptIn("t1", true)).resolves.toEqual({}) },
    ];

    expect(cases).toHaveLength(6);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(6);
    expect(calls[5]?.args).not.toHaveProperty("args");
  });

  it("defaults the inclusive cursor and omits an unset maxBytes in web JSON", async () => {
    const calls = captureRpc({ transcript_read: { chunks: [], nextSeq: 0, done: true, totalBytes: 0 } });
    const { transcriptApi } = await import("../ipc/commands");

    await transcriptApi.read("t1");
    await transcriptApi.read("t1", 7);

    expect(calls).toHaveLength(2);
    expectManifestEnvelope(calls[0], "transcript_read", { id: "t1", afterSeq: 0 });
    expectManifestEnvelope(calls[1], "transcript_read", { id: "t1", afterSeq: 7 });
    expect(calls[0]?.args).not.toHaveProperty("maxBytes");
    expect(calls[1]?.args).not.toHaveProperty("maxBytes");
  });
});

describe("platform, update and biometric facade IPC contracts", () => {
  it("preserves the exact envelopes for all 11 real-consumer commands and complete results", async () => {
    const installResult = { installed: false, restarted: false, version: "", unavailableReason: "server install unavailable" };
    const restartResult = { installed: true, restarted: false, version: "1.2.3", unavailableReason: "server restart unavailable" };
    const unlockResult = { ok: true, password: "stored-master-password" };
    const calls = captureRpc({
      app_update_install: installResult,
      app_restart: restartResult,
      biometric_unlock: unlockResult,
      desktop_tray_set: { enabled: true },
    });
    const { appUpdateApi, biometricApi, desktopTrayApi, systemApi } = await import("../ipc/commands");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "app_platform", payload: null, invoke: () => systemApi.platform() },
      { command: "system_insets", payload: null, invoke: () => systemApi.insets() },
      { command: "desktop_tray_get", payload: null, invoke: () => desktopTrayApi.get() },
      { command: "desktop_tray_set", payload: { enabled: true }, invoke: () => desktopTrayApi.set(true) },
      { command: "app_update_check", payload: null, invoke: () => appUpdateApi.check() },
      { command: "app_update_install", payload: { version: null }, invoke: async () => expect(await appUpdateApi.install()).toEqual(installResult) },
      { command: "app_restart", payload: null, invoke: async () => expect(await appUpdateApi.restart()).toEqual(restartResult) },
      { command: "biometric_status", payload: null, invoke: () => biometricApi.status() },
      { command: "biometric_unlock", payload: null, invoke: async () => expect(await biometricApi.unlock()).toEqual(unlockResult) },
      { command: "biometric_enable", payload: { password: "master-password" }, invoke: async () => expect(await biometricApi.enable("master-password")).toBeNull() },
      { command: "biometric_disable", payload: null, invoke: async () => expect(await biometricApi.disable()).toBeNull() },
    ];

    expect(cases).toHaveLength(11);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(11);
  });

  it("keeps omitted, undefined, explicit and empty update versions distinct", async () => {
    const calls = captureRpc();
    const { appUpdateApi } = await import("../ipc/commands");

    await appUpdateApi.install();
    await appUpdateApi.install(undefined);
    await appUpdateApi.install("1.2.3");
    await appUpdateApi.install("");

    expect(calls).toHaveLength(4);
    expectManifestEnvelope(calls[0], "app_update_install", { version: null });
    expectManifestEnvelope(calls[1], "app_update_install", { version: null });
    expectManifestEnvelope(calls[2], "app_update_install", { version: "1.2.3" });
    expectManifestEnvelope(calls[3], "app_update_install", { version: "" });
  });

  it("passes a failed biometric unlock through without inventing password material", async () => {
    const failure = { ok: false, message: "用户取消" };
    const calls = captureRpc({ biometric_unlock: failure });
    const { biometricApi } = await import("../ipc/commands");

    const result = await biometricApi.unlock();

    expect(result).toEqual(failure);
    expect(result).not.toHaveProperty("password");
    expectManifestEnvelope(calls[0], "biometric_unlock", null);
  });
});

describe("forward runtime facade IPC contracts", () => {
  it("preserves all seven flat envelopes and existing result shapes", async () => {
    const environment = { available: true, platform: "other", listenHost: "127.0.0.1" };
    const spec = {
      id: "forward-1",
      sessionId: "session-1",
      hostId: "asset-1",
      status: "reconnecting",
      error: "reconnecting session",
      listenHost: "127.0.0.1",
      listenPort: 13306,
      targetHost: "db.internal",
      targetPort: 5432,
      kind: "local",
      createdAt: 1,
    };
    const calls = captureRpc({
      forward_env: environment,
      forward_create: spec,
      forward_create_socks: spec,
      forward_create_remote: spec,
      forward_list: [spec],
      forward_retry: spec,
    });
    const { forwardApi } = await import("../ipc/commands");
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "forward_env", payload: null, invoke: async () => expect(await forwardApi.env()).toEqual(environment) },
      {
        command: "forward_create",
        payload: { sessionId: "session-1", listenPort: 13306, targetHost: "db.internal", targetPort: 5432 },
        invoke: async () => expect(await forwardApi.create("session-1", 13306, "db.internal", 5432)).toEqual(spec),
      },
      {
        command: "forward_create_socks",
        payload: { sessionId: "session-1", listenPort: 1080, acknowledgeRisk: true },
        invoke: async () => expect(await forwardApi.createSocks("session-1", 1080, true)).toEqual(spec),
      },
      {
        command: "forward_create_remote",
        payload: { sessionId: "session-1", bindHost: "0.0.0.0", bindPort: 18080, targetHost: "127.0.0.1", targetPort: 8080, acknowledgeRisk: true },
        invoke: async () => expect(await forwardApi.createRemote("session-1", "0.0.0.0", 18080, "127.0.0.1", 8080, true)).toEqual(spec),
      },
      { command: "forward_list", payload: null, invoke: async () => expect(await forwardApi.list()).toEqual([spec]) },
      {
        command: "forward_retry",
        payload: { id: "forward-1", sessionId: "session-1" },
        invoke: async () => expect(await forwardApi.retry("forward-1", "session-1")).toEqual(spec),
      },
      {
        command: "forward_remove",
        payload: { id: "forward-1" },
        invoke: async () => expect(await forwardApi.remove("forward-1")).toBeNull(),
      },
    ];

    expect(cases).toHaveLength(7);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(7);
  });

  it("omits default and false risk acknowledgements and all empty retry session forms", async () => {
    const calls = captureRpc();
    const { forwardApi } = await import("../ipc/commands");

    await forwardApi.create("session-1", 13306, "db.internal", 5432);
    await forwardApi.createSocks("session-1", 1080);
    await forwardApi.createSocks("session-1", 1080, false);
    await forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080);
    await forwardApi.createRemote("session-1", "127.0.0.1", 18080, "127.0.0.1", 8080, false);
    await forwardApi.retry("forward-1");
    await forwardApi.retry("forward-1", undefined);
    await forwardApi.retry("forward-1", "");

    expect(calls).toHaveLength(8);
    expectManifestEnvelope(calls[0], "forward_create", {
      sessionId: "session-1",
      listenPort: 13306,
      targetHost: "db.internal",
      targetPort: 5432,
    });
    expectManifestEnvelope(calls[1], "forward_create_socks", { sessionId: "session-1", listenPort: 1080 });
    expectManifestEnvelope(calls[2], "forward_create_socks", { sessionId: "session-1", listenPort: 1080 });
    expectManifestEnvelope(calls[3], "forward_create_remote", {
      sessionId: "session-1",
      bindHost: "127.0.0.1",
      bindPort: 18080,
      targetHost: "127.0.0.1",
      targetPort: 8080,
    });
    expectManifestEnvelope(calls[4], "forward_create_remote", {
      sessionId: "session-1",
      bindHost: "127.0.0.1",
      bindPort: 18080,
      targetHost: "127.0.0.1",
      targetPort: 8080,
    });
    for (const call of calls.slice(0, 5)) {
      expect(call.args).not.toHaveProperty("acknowledgeRisk");
    }
    for (const [index, call] of calls.slice(5).entries()) {
      expect(call.args).not.toHaveProperty("sessionId");
      expectManifestEnvelope(calls[index + 5], "forward_retry", { id: "forward-1" });
    }
  });
});

describe("operations facade IPC contracts", () => {
  it("preserves all 12 envelopes, dynamic layout data, import warnings and normal conflict results", async () => {
    const layoutData = { v: 3, workspaces: [{ id: "workspace-1", nested: [true, null] }] };
    const layoutResult = { revision: 7, updatedAt: 1234, data: layoutData };
    const conflict = { saved: false, revision: 8, conflict: true };
    const preview = {
      source: "ssh-config",
      path: "/home/demo/.ssh/config",
      hosts: [{ id: "host-1", alias: "web", warnings: ["verify the path"] }],
      keys: [],
      diagnostics: [{ code: "host-pattern-skipped", source: "config:12", message: "wildcard skipped" }],
      truncated: false,
    };
    const applied = {
      assetsCreated: 1,
      assetsUpdated: 0,
      credentialsCreated: 1,
      credentialsUpdated: 0,
      skipped: 2,
      warnings: ["one entry was skipped"],
    };
    const calls = captureRpc({
      layout_get: layoutResult,
      layout_put: conflict,
      ssh_import_preview: preview,
      ssh_import_apply: applied,
    });
    const { assetApi, layoutApi, sshImportApi } = await import("../ipc/commands");
    const layoutJSON = JSON.stringify(layoutData);
    const cases: Array<{ command: IPCCommandName; payload: unknown; invoke: () => Promise<unknown> }> = [
      { command: "snippet_list", payload: null, invoke: () => assetApi.snippetList() },
      { command: "snippet_create", payload: { name: "Logs", body: "tail -f app.log", groupId: "group-1", sort: 0 }, invoke: () => assetApi.snippetCreate("Logs", "tail -f app.log", "group-1", 0) },
      { command: "snippet_update", payload: { id: "snippet-1", name: "Logs", body: "tail -f app.log", groupId: null, sort: 0 }, invoke: () => assetApi.snippetUpdate("snippet-1", "Logs", "tail -f app.log", { groupId: null, sort: 0 }) },
      { command: "snippet_delete", payload: { id: "snippet-1" }, invoke: () => assetApi.snippetDelete("snippet-1") },
      { command: "audit_query", payload: { sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect", limit: 100, offset: 200 }, invoke: () => assetApi.auditQuery({ sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect", limit: 100, offset: 200 }) },
      { command: "audit_count", payload: { sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect" }, invoke: () => assetApi.auditCount({ sessionId: "session-1", assetId: "asset-1", source: "user", kind: "connect" }) },
      { command: "command_query", payload: { sessionId: "session-1", assetId: "asset-1", userId: "user-1", limit: 100, offset: 200 }, invoke: () => assetApi.commandQuery({ sessionId: "session-1", assetId: "asset-1", userId: "user-1", limit: 100, offset: 200 }) },
      { command: "command_count", payload: { sessionId: "session-1", assetId: "asset-1", userId: "user-1" }, invoke: () => assetApi.commandCount({ sessionId: "session-1", assetId: "asset-1", userId: "user-1" }) },
      { command: "layout_get", payload: null, invoke: async () => expect(await layoutApi.get()).toEqual(layoutResult) },
      { command: "layout_put", payload: { data: layoutJSON, revision: 7 }, invoke: () => expect(layoutApi.put(layoutJSON, 7)).resolves.toEqual(conflict) },
      { command: "ssh_import_preview", payload: { source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true }, invoke: () => expect(sshImportApi.preview({ source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true })).resolves.toEqual(preview) },
      {
        command: "ssh_import_apply",
        payload: { source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true, groupId: "group-1", tags: "prod, 欧洲", hosts: [{ id: "host-1", action: "import" }], keys: [{ id: "key-1", action: "skip" }] },
        invoke: () => expect(sshImportApi.apply({ source: "ssh-config", path: "/home/demo/.ssh/config", confirmed: true, groupId: "group-1", tags: "prod, 欧洲", hosts: [{ id: "host-1", action: "import" }], keys: [{ id: "key-1", action: "skip" }] })).resolves.toEqual(applied),
      },
    ];

    expect(cases).toHaveLength(12);
    for (const [index, test] of cases.entries()) {
      await test.invoke();
      expectManifestEnvelope(calls[index], test.command, test.payload);
    }
    expect(calls).toHaveLength(12);
  });

  it("omits only unset optional fields without changing explicit clear and zero semantics", async () => {
    const calls = captureRpc();
    const { assetApi, sshImportApi } = await import("../ipc/commands");

    await assetApi.snippetCreate("Logs", "tail -f app.log");
    await assetApi.snippetUpdate("snippet-1", "Logs", "tail -f app.log");
    await assetApi.snippetUpdate("snippet-1", "Logs", "tail -f app.log", { sort: 0 });
    await assetApi.auditCount();
    await assetApi.commandCount();
    await sshImportApi.preview({ source: "ssh-home", path: undefined });
    await sshImportApi.apply({ source: "ssh-home", path: undefined, hosts: [], keys: [] });

    expect(calls).toHaveLength(7);
    expectManifestEnvelope(calls[0], "snippet_create", { name: "Logs", body: "tail -f app.log" });
    expectManifestEnvelope(calls[1], "snippet_update", { id: "snippet-1", name: "Logs", body: "tail -f app.log" });
    expectManifestEnvelope(calls[2], "snippet_update", { id: "snippet-1", name: "Logs", body: "tail -f app.log", sort: 0 });
    expectManifestEnvelope(calls[3], "audit_count", {});
    expectManifestEnvelope(calls[4], "command_count", {});
    expectManifestEnvelope(calls[5], "ssh_import_preview", { source: "ssh-home" });
    expectManifestEnvelope(calls[6], "ssh_import_apply", { source: "ssh-home", hosts: [], keys: [] });
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
