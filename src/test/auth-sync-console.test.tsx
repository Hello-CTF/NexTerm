/** @vitest-environment jsdom */
// M125 R3 end-to-end acceptance: M141 collect consumption (P1-1), applySet dedup and second-round convergence (P1-2),
// transcript endedAt revision and content completion (P1-3). All through real SyncCard interaction and real encryption.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil as flushUntilBase, mount, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    groupList: vi.fn(),
    snippetList: vi.fn(),
    transcriptHosts: vi.fn(),
    transcriptList: vi.fn(),
    transcriptRead: vi.fn(),
    collectAssets: vi.fn(),
    collectTombstones: vi.fn(),
    collectCredentials: vi.fn(),
    collectKnownHosts: vi.fn(),
    collectAIProfiles: vi.fn(),
    collectAIPermission: vi.fn(),
    collectAIGrants: vi.fn(),
    collectPreferences: vi.fn(),
    kindOptInGet: vi.fn(),
    kindOptInSet: vi.fn(),
    applyObjects: vi.fn(),
    syncIds: vi.fn(),
    syncPull: vi.fn(),
    syncPush: vi.fn(),
    syncDelete: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: { list: vi.fn(), groupList: mocks.groupList, snippetList: mocks.snippetList },
  transcriptApi: {
    hosts: mocks.transcriptHosts,
    list: mocks.transcriptList,
    read: mocks.transcriptRead,
  },
  syncApi: {
    applyObjects: mocks.applyObjects,
    collectAssets: mocks.collectAssets,
    collectTombstones: mocks.collectTombstones,
    collectCredentials: mocks.collectCredentials,
    collectKnownHosts: mocks.collectKnownHosts,
    collectAIProfiles: mocks.collectAIProfiles,
    collectAIPermission: mocks.collectAIPermission,
    collectAIGrants: mocks.collectAIGrants,
    collectPreferences: mocks.collectPreferences,
    kindOptInGet: mocks.kindOptInGet,
    kindOptInSet: mocks.kindOptInSet,
  },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../ui/dialogs", () => ({ ask: mocks.ask }));

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: vi.fn().mockResolvedValue({ initialized: true, registration_open: false, auth: "on" }),
      me: vi.fn(),
      logout: vi.fn().mockResolvedValue({ ok: true }),
      logoutAll: vi.fn().mockResolvedValue({ revoked: 1 }),
      devices: vi.fn().mockResolvedValue({ devices: [] }),
      sessions: vi.fn().mockResolvedValue({ sessions: [] }),
      sessionRevoke: vi.fn().mockResolvedValue({ ok: true }),
      deviceRevoke: vi.fn().mockResolvedValue({ ok: true }),
      totpStatus: vi.fn().mockResolvedValue({ enabled: false, pending: false, mfa_required: false, recovery_codes_left: 0 }),
      totpSetup: vi.fn(),
      totpConfirm: vi.fn(),
      totpDisable: vi.fn(),
      totpLogin: vi.fn(),
    },
    adminApi: {
      users: vi.fn().mockResolvedValue({ users: [] }),
      settingsGet: vi.fn().mockResolvedValue({ registration_open: false, mfa_required: false }),
      settingsPut: vi.fn(),
      createUser: vi.fn(),
      disableUser: vi.fn(),
      resetUser: vi.fn(),
    },
    syncV2Api: {
      ids: mocks.syncIds,
      pull: mocks.syncPull,
      push: mocks.syncPush,
      deleteObject: mocks.syncDelete,
    },
  };
});

import { SyncCard } from "../features/settings/SyncCard";
import { useAuth } from "../features/auth/store";
import { useUi } from "../app/store";
import { GO_SYNC_FIXTURES } from "./auth-sync-fixtures";

const DEK = new Uint8Array(32).fill(7);
const DEMO_USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

let mounted: MountedView | undefined;

function mountSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

function seedAuthed() {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: DEMO_USER,
    dek: DEK,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

// remoteFixtureWire seals the real Go json.Marshal-produced fixture bytes directly (src/test/auth-sync-fixtures.ts),
// not replicating the layout with JS JSON.stringify, avoiding same-origin self-proof.
async function remoteFixtureWire(id: string, kind: import("../features/auth/crypto").SyncObjectKind, fixture: string, seq: number) {
  const { sealSyncObject, bytesToBase64, base64ToBytes } = await import("../features/auth/crypto");
  return { id, seq, blob: bytesToBase64(await sealSyncObject(DEK, base64ToBytes(fixture), id, kind)) };
}

async function seedUndecryptableCloudOnly(id: string): Promise<void> {
  const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
  const blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes(JSON.stringify({ id })), id, "asset"));
  mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id, seq: 1, blob_hash: "hash-1" }], head: "head-1", max_seq: 1 });
  mocks.syncPull.mockResolvedValue({
    protocol: 2, objects: [{ id, seq: 1, blob }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true,
  });
}

async function openPushed(object: { id: string; blob: string }, kind: import("../features/auth/crypto").SyncObjectKind): Promise<Record<string, unknown>> {
  const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
  const plaintext = await openSyncObject(DEK, base64ToBytes(object.blob), object.id, kind);
  return JSON.parse(new TextDecoder().decode(plaintext)) as Record<string, unknown>;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function openDetails(): void {
  clickButton(mounted!.container, "对比详情");
}

async function runAutoSync() {
  const { runWebSync, WEB_SYNC_RESULT_EVENT, WEB_SYNC_ERROR_EVENT } = await import("../features/settings/SyncCard");
  await act(async () => {
    try {
      const result = await runWebSync(DEK, { knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false });
      if (result) window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, { detail: result }));
    } catch (error) {
      window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: error }));
      throw error;
    }
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  useUi.setState({ pushToast: mocks.toast });
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue({ chunks: [], nextSeq: 0, done: true, totalBytes: 0 });
  mocks.collectAssets.mockResolvedValue({ assets: [], hasMore: false });
  mocks.collectTombstones.mockResolvedValue({ tombstones: [], hasMore: false });
  mocks.collectCredentials.mockResolvedValue({ credentials: [], hasMore: false });
  mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });
  mocks.collectAIProfiles.mockResolvedValue({ profiles: [], hasMore: false });
  mocks.collectAIPermission.mockResolvedValue({ id: "", mode: "", dangerRules: [], updatedAt: 0, found: false });
  mocks.collectAIGrants.mockResolvedValue({ grants: [], rules: [], modes: [] });
  mocks.collectPreferences.mockResolvedValue([]);
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false });
  mocks.applyObjects.mockResolvedValue({ applied: 0, identical: 0, skipped: 0, objects: [] });
  mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-0", max_seq: 0 });
  mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [], head: "head-0", max_seq: 0, next_seq: 0, cursor_done: true });
  mocks.ask.mockResolvedValue(true);
  seedAuthed();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
  vi.unstubAllGlobals();
});

describe("M141 collect consumption: complete local copies enter the push set (P1-1)", () => {
  it("pushes soft-deleted assets, both tombstone kinds, and revealed credentials through /sync/v2/push", async () => {
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a-del", kind: "ssh", name: "old-01", host: "10.0.0.1", port: 22,
          username: "root", authKind: "password", optionsJson: "{}", tags: "", note: "",
          sort: 0, createdAt: 1, updatedAt: 100, deletedAt: 150,
        },
      ],
      hasMore: false,
    });
    mocks.collectTombstones.mockResolvedValue({
      tombstones: [
        { id: "g-del", targetKind: "group", deletedAt: 300 },
        { id: "c-del", targetKind: "credential", deletedAt: 400 },
      ],
      hasMore: false,
    });
    mocks.collectCredentials.mockResolvedValue({
      credentials: [
        { id: "c1", name: "生产口令", kind: "password", updatedAt: 50, secret: "s3cret", secretState: "revealed" },
      ],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 4, applied: 4, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("old-01"));
    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);

    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["a-del", "c-del", "c1", "g-del"]);
    // Soft-deleted asset carries a deletedAt payload
    const assetPayload = await openPushed(objects.find((o) => o.id === "a-del")!, "asset");
    expect(assetPayload.deletedAt).toBe(150);
    expect(assetPayload.name).toBe("old-01");
    // Both tombstone payload kinds align with tombstoneObject
    const groupTomb = await openPushed(objects.find((o) => o.id === "g-del")!, "tombstone");
    expect(groupTomb).toEqual({ targetKind: "group", deletedAt: 300 });
    const credTomb = await openPushed(objects.find((o) => o.id === "c-del")!, "tombstone");
    expect(credTomb).toEqual({ targetKind: "credential", deletedAt: 400 });
    // The credential payload carries the plaintext secret (credentialObject shape)
    const credPayload = await openPushed(objects.find((o) => o.id === "c1")!, "credential");
    expect(credPayload).toEqual({ id: "c1", name: "生产口令", kind: "password", secret: "s3cret", updatedAt: 50 });
  });

  it("does not overwrite a newer live credential with an older tombstone", async () => {
    mocks.collectCredentials.mockResolvedValue({
      credentials: [{ id: "c1", name: "新凭据", kind: "password", updatedAt: 500, secret: "new-secret", secretState: "revealed" }],
      hasMore: false,
    });
    mocks.collectTombstones.mockResolvedValue({
      tombstones: [{ id: "c1", targetKind: "credential", deletedAt: 400 }],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });

    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["c1"]);
    expect(await openPushed(objects[0], "credential")).toEqual({ id: "c1", name: "新凭据", kind: "password", secret: "new-secret", updatedAt: 500 });
  });

  it("advances the collect cursor until every entry has been read", async () => {
    mocks.collectAssets.mockImplementation(async (_reveal: boolean, afterId?: string) => {
      if (afterId === "a1") {
        return {
          assets: [{ id: "a2", kind: "ssh", name: "db-02", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1 }],
          hasMore: false,
        };
      }
      return {
        assets: [{ id: "a1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1 }],
        hasMore: true,
        nextAfterId: "a1",
      };
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 2, applied: 2, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01") && text().includes("db-02"));
    expect(mocks.collectAssets).toHaveBeenCalledTimes(2);
    expect(mocks.collectAssets).toHaveBeenNthCalledWith(1, true, undefined, 512);
    expect(mocks.collectAssets).toHaveBeenNthCalledWith(2, true, "a1", 512);
    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["a1", "a2"]);
  });

  it("warns clearly for locked, unavailable, and failed credentials without pushing them", async () => {
    mocks.collectCredentials.mockResolvedValue({
      credentials: [
        { id: "c-locked", name: "锁定凭据", kind: "password", updatedAt: 1, secretState: "locked" },
        { id: "c-unavail", name: "不可用凭据", kind: "password", updatedAt: 1, secretState: "unavailable" },
        { id: "c-error", name: "解密失败凭据", kind: "password", updatedAt: 1, secretState: "error" },
      ],
      hasMore: false,
    });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("凭据库未解锁"));
    expect(text()).toContain("凭据「锁定凭据」凭据库未解锁，未进入本次对比与推送");
    expect(text()).toContain("凭据「不可用凭据」凭据库不可用，未进入本次对比与推送");
    expect(text()).toContain("凭据「解密失败凭据」解密失败，未进入本次对比与推送");
    // The push set is empty, no push is fired
    expect(text()).toContain("待推送 0 项");
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("keeps a same-ID local copy out of automatic sync when the cloud object cannot be decrypted", async () => {
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "",
          sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const staleWire = {
      id: "a-1",
      seq: 1,
      blob: bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"a-1","name":"stale","updatedAt":1}'), "a-1", "asset")),
    };
    mocks.syncIds.mockImplementation(async (knownHead?: string) => {
      if (knownHead === "head-1") return { protocol: 2, unchanged: true, head: "head-1", max_seq: 1 };
      return { protocol: 2, entries: [{ id: "a-1", seq: 1, blob_hash: "stale" }], head: "head-1", max_seq: 1 };
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [staleWire],
      head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true,
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("云端对象 a-1 解密失败"));
    expect(text()).toContain("已跳过自动覆盖；可在对比详情中手动确认用本机同 ID 副本修复");
    expect(text()).toContain("无法解密");
    expect(text()).toContain("云端 1 个对象");
    expect(text()).toContain("待推送 0 项");
    await runAutoSync();
    expect(mocks.syncPush).not.toHaveBeenCalled();
    expect(text()).toContain("云端对象 a-1 解密失败");
    expect(text()).toContain("无法解密");
  });

  it("does not auto-push a local winner when a fresh cloud head reveals an unreadable same-ID object", async () => {
    mocks.collectAssets.mockResolvedValue({
      assets: [{ id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 }],
      hasMore: false,
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"a-1"}'), "a-1", "asset"));
    let readCount = 0;
    mocks.syncIds.mockImplementation(async () => {
      readCount += 1;
      return readCount === 1
        ? { protocol: 2, entries: [], head: "head-0", max_seq: 0 }
        : { protocol: 2, entries: [{ id: "a-1", seq: 1, blob_hash: "corrupt" }], head: "head-1", max_seq: 1 };
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [{ id: "a-1", seq: 1, blob }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true,
    });

    const { runWebSync } = await import("../features/settings/SyncCard");
    const result = await runWebSync(new Uint8Array(DEK), { knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false });

    expect(result?.warnings).toContain("云端对象 a-1 解密失败，已跳过自动覆盖；可在对比详情中手动确认用本机同 ID 副本修复");
    expect(result?.pushed).toBe(0);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("keeps a cloud-only undecryptable object visible after a cached refresh with no local repair copy", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const staleWire = {
      id: "cloud-only", seq: 1,
      blob: bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"cloud-only"}'), "cloud-only", "asset")),
    };
    mocks.syncIds.mockImplementation(async (knownHead?: string) => knownHead === "head-1"
      ? { protocol: 2, unchanged: true, head: "head-1", max_seq: 1 }
      : { protocol: 2, entries: [{ id: "cloud-only", seq: 1, blob_hash: "stale" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [staleWire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("云端对象 cloud-only 解密失败"));
    expect(text()).toContain("本次对比没有可用的同 ID 本机副本，可在对比详情中重试读取或确认清除云端副本");
    expect(text()).toContain("无法解密");
    expect(text()).toContain("云端 1 个对象");
    await runAutoSync();
    await flushUntil(() => mocks.syncIds.mock.calls.some(([head]) => head === "head-1"));
    expect(text()).toContain("云端对象 cloud-only 解密失败");
    expect(text()).toContain("无法解密");
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("recovers an object omitted from a byte-limited ID batch and keeps its failure visible", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const wrongKey = new Uint8Array(32).fill(8);
    const first = { id: "budget-a", seq: 7, blob: bytesToBase64(await sealSyncObject(wrongKey, utf8Bytes('{"id":"budget-a"}'), "budget-a", "asset")) };
    const second = { id: "budget-b", seq: 8, blob: bytesToBase64(await sealSyncObject(wrongKey, utf8Bytes('{"id":"budget-b"}'), "budget-b", "asset")) };
    mocks.syncIds.mockResolvedValue({
      protocol: 2, head: "budget-head", max_seq: 88,
      entries: [{ id: first.id, seq: first.seq, blob_hash: "hash-a" }, { id: second.id, seq: second.seq, blob_hash: "hash-b" }],
    });
    mocks.syncPull.mockImplementation(async (_since: number, ids: string[]) => ({
      protocol: 2, objects: ids.length > 1 ? [first] : [second],
      head: "budget-head", max_seq: 88, next_seq: 88, cursor_done: true,
    }));

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("云端对象 budget-b 解密失败"));

    expect(text()).toContain("云端对象 budget-a 解密失败");
    expect(mocks.syncPull).toHaveBeenCalledWith(88, ["budget-a", "budget-b"]);
    expect(mocks.syncPull).toHaveBeenCalledWith(88, ["budget-b"]);
    expect(text()).toContain("云端 2 个对象");
  });

  it("reports an index/pull head change instead of silently omitting a cloud row", async () => {
    mocks.syncIds.mockResolvedValue({
      protocol: 2, head: "before", max_seq: 5,
      entries: [{ id: "changing", seq: 5, blob_hash: "hash-1" }],
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [], head: "after", max_seq: 6, next_seq: 6, cursor_done: true,
    });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("云端对象在读取期间发生变化"));

    expect(mocks.syncPull).toHaveBeenCalledWith(5, ["changing"]);
    expect(text()).not.toContain("本机与云端都还没有可同步的内容");
  });

  it.each([
    { id: "invalid-json", payload: "{" },
    { id: "invalid-type", payload: "null" },
  ])("does not offer cloud deletion for an authenticated but invalid payload: $id", async ({ id, payload }) => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const blob = bytesToBase64(await sealSyncObject(DEK, utf8Bytes(payload), id, "asset"));
    mocks.syncIds.mockResolvedValue({
      protocol: 2, entries: [{ id, seq: 1, blob_hash: `hash-${id}` }], head: `head-${id}`, max_seq: 1,
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [{ id, seq: 1, blob }], head: `head-${id}`, max_seq: 1, next_seq: 1, cursor_done: true,
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("同步失败："));

    expect(text()).not.toContain(`云端对象 ${id} 解密失败`);
    expect(text()).not.toContain("清除云端副本");
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("does not offer cloud deletion when Web Crypto becomes unavailable", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const blob = bytesToBase64(await sealSyncObject(DEK, utf8Bytes('{"id":"crypto-outage"}'), "crypto-outage", "asset"));
    mocks.syncIds.mockResolvedValue({
      protocol: 2, entries: [{ id: "crypto-outage", seq: 1, blob_hash: "hash-crypto-outage" }], head: "head-crypto-outage", max_seq: 1,
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [{ id: "crypto-outage", seq: 1, blob }], head: "head-crypto-outage", max_seq: 1, next_seq: 1, cursor_done: true,
    });
    vi.stubGlobal("crypto", { subtle: undefined });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("当前环境不支持 Web Crypto"));

    expect(text()).not.toContain("解密失败，已跳过");
    expect(text()).not.toContain("清除云端副本");
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("repairs an undecryptable row from a fresh same-ID local copy after confirmation", async () => {
    mocks.collectAssets.mockResolvedValue({
      assets: [{ id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 }],
      hasMore: false,
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const corruptBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"a-1"}'), "a-1", "asset"));
    let repairedBlob: string | null = null;
    mocks.syncIds.mockImplementation(async (knownHead?: string) => {
      const head = repairedBlob ? "head-2" : "head-1";
      if (knownHead === head) return { protocol: 2, unchanged: true, head, max_seq: repairedBlob ? 2 : 1 };
      return { protocol: 2, entries: [{ id: "a-1", seq: repairedBlob ? 2 : 1, blob_hash: repairedBlob ? "repaired" : "corrupt" }], head, max_seq: repairedBlob ? 2 : 1 };
    });
    mocks.syncPull.mockImplementation(async () => ({
      protocol: 2, objects: [{ id: "a-1", seq: repairedBlob ? 2 : 1, blob: repairedBlob ?? corruptBlob }],
      head: repairedBlob ? "head-2" : "head-1", max_seq: repairedBlob ? 2 : 1,
      next_seq: repairedBlob ? 2 : 1, cursor_done: true,
    }));
    mocks.syncPush.mockImplementation(async (_head: string, objects: { id: string; blob: string }[]) => {
      repairedBlob = objects[0].blob;
      return { protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 };
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("用本机副本修复"));
    clickButton(mounted.container, "用本机副本修复");
    await flushUntil(() => mocks.syncPush.mock.calls.length === 1);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush.mock.calls[0][0]).toBe("head-1");
    expect(await openPushed(mocks.syncPush.mock.calls[0][1][0], "asset")).toMatchObject({ id: "a-1", name: "web-01" });
    await flushUntil(() => text().includes("已一致"));
    expect(text()).not.toContain("无法解密");
  });

  it.each(["content", "deletion", "kind"] as const)("does not repair from a local copy whose %s changed after confirmation", async (change) => {
    const original = { id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 };
    let changed = false;
    mocks.collectAssets.mockImplementation(async () => ({
      assets: changed && change === "kind" ? [] : [{
        ...original,
        ...(changed && change === "content" ? { name: "web-02" } : {}),
        ...(changed && change === "deletion" ? { deletedAt: 300 } : {}),
      }],
      hasMore: false,
    }));
    mocks.collectTombstones.mockImplementation(async () => ({
      tombstones: changed && change === "kind" ? [{ id: "a-1", targetKind: "asset", deletedAt: 300 }] : [],
      hasMore: false,
    }));
    mocks.ask.mockImplementationOnce(async () => {
      changed = true;
      return true;
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const corruptBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"a-1"}'), "a-1", "asset"));
    mocks.syncIds.mockResolvedValue({
      protocol: 2, entries: [{ id: "a-1", seq: 1, blob_hash: "corrupt" }], head: "head-1", max_seq: 1,
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [{ id: "a-1", seq: 1, blob: corruptBlob }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true,
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("用本机副本修复"));
    clickButton(mounted.container, "用本机副本修复");
    await flushUntil(() => text().includes("本机副本在对比后发生变化"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("does not overwrite a cloud row that becomes readable before a manual repair", async () => {
    mocks.collectAssets.mockResolvedValue({
      assets: [{ id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 }],
      hasMore: false,
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const corruptBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"a-1"}'), "a-1", "asset"));
    const readableBlob = bytesToBase64(await sealSyncObject(DEK, utf8Bytes('{"id":"a-1","name":"remote-repaired","updatedAt":300}'), "a-1", "asset"));
    let currentBlob = corruptBlob;
    let currentHead = "head-1";
    mocks.syncIds.mockImplementation(async (knownHead?: string) => knownHead === currentHead
      ? { protocol: 2, unchanged: true, head: currentHead, max_seq: 1 }
      : { protocol: 2, entries: [{ id: "a-1", seq: currentHead === "head-1" ? 1 : 2, blob_hash: currentHead === "head-1" ? "corrupt" : "repaired" }], head: currentHead, max_seq: 1 });
    mocks.syncPull.mockImplementation(async () => ({
      protocol: 2, objects: [{ id: "a-1", seq: currentHead === "head-1" ? 1 : 2, blob: currentBlob }],
      head: currentHead, max_seq: 1, next_seq: 1, cursor_done: true,
    }));

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("用本机副本修复"));
    currentBlob = readableBlob;
    currentHead = "head-2";
    clickButton(mounted.container, "用本机副本修复");
    await flushUntil(() => text().includes("云端对象在对比后发生变化"));

    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("does not delete a cloud row that decrypts but has invalid payload during a fresh check", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const corruptBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"cloud-only"}'), "cloud-only", "asset"));
    const invalidBlob = bytesToBase64(await sealSyncObject(DEK, utf8Bytes("{"), "cloud-only", "asset"));
    let currentBlob = corruptBlob;
    const currentHead = "head-1";
    mocks.syncIds.mockImplementation(async (knownHead?: string) => knownHead === currentHead
      ? { protocol: 2, unchanged: true, head: currentHead, max_seq: 1 }
      : { protocol: 2, entries: [{ id: "cloud-only", seq: currentHead === "head-1" ? 1 : 2, blob_hash: currentHead }], head: currentHead, max_seq: 1 });
    mocks.syncPull.mockImplementation(async () => ({
      protocol: 2, objects: [{ id: "cloud-only", seq: currentHead === "head-1" ? 1 : 2, blob: currentBlob }],
      head: currentHead, max_seq: 1, next_seq: 1, cursor_done: true,
    }));

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));
    currentBlob = invalidBlob;
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => text().includes("操作失败："));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it.each([
    { action: "用本机副本修复", id: "a-1", hasLocalCopy: true },
    { action: "清除云端副本", id: "cloud-only", hasLocalCopy: false },
  ])("rejects $action when another device replaces the same ID with another undecryptable blob", async ({ action, id, hasLocalCopy }) => {
    if (hasLocalCopy) mocks.collectAssets.mockResolvedValue({
      assets: [{ id, kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 }],
      hasMore: false,
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const initialBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes(`{"id":"${id}"}`), id, "asset"));
    const replacementBlob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(9), utf8Bytes(`{"id":"${id}"}`), id, "asset"));
    let replaced = false;
    mocks.syncIds.mockImplementation(async () => ({
      protocol: 2,
      entries: [{ id, seq: replaced ? 2 : 1, blob_hash: replaced ? `hash-${id}-replacement` : `hash-${id}-initial` }],
      head: replaced ? "head-replacement" : "head-initial",
      max_seq: replaced ? 2 : 1,
    }));
    mocks.syncPull.mockImplementation(async () => ({
      protocol: 2, objects: [{ id, seq: replaced ? 2 : 1, blob: replaced ? replacementBlob : initialBlob }],
      head: replaced ? "head-replacement" : "head-initial", max_seq: replaced ? 2 : 1,
      next_seq: replaced ? 2 : 1, cursor_done: true,
    }));

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes(action));
    replaced = true;
    clickButton(mounted.container, action);
    await flushUntil(() => text().includes("云端对象在对比后发生变化"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it.each([
    { action: "用本机副本修复", id: "a-1", hasLocalCopy: true },
    { action: "清除云端副本", id: "cloud-only", hasLocalCopy: false },
  ])("rejects $action when sync kind settings change after confirmation", async ({ action, id, hasLocalCopy }) => {
    if (hasLocalCopy) mocks.collectAssets.mockResolvedValue({
      assets: [{ id, kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 }],
      hasMore: false,
    });
    let changed = false;
    mocks.kindOptInGet.mockImplementation(async () => ({
      knownHost: changed, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false,
    }));
    mocks.ask.mockImplementationOnce(async () => {
      changed = true;
      return true;
    });
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes(`{"id":"${id}"}`), id, "asset"));
    mocks.syncIds.mockResolvedValue({
      protocol: 2, entries: [{ id, seq: 1, blob_hash: "corrupt" }], head: "head-1", max_seq: 1,
    });
    mocks.syncPull.mockResolvedValue({
      protocol: 2, objects: [{ id, seq: 1, blob }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true,
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes(action));
    clickButton(mounted.container, action);
    await flushUntil(() => text().includes("内容类型设置已变化"));

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("retries only the undecryptable row and shows it as readable after another device repairs it", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    let blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"cloud-only"}'), "cloud-only", "asset"));
    let head = "head-1";
    mocks.syncIds.mockImplementation(async (knownHead?: string) => knownHead === head
      ? { protocol: 2, unchanged: true, head, max_seq: 1 }
      : { protocol: 2, entries: [{ id: "cloud-only", seq: head === "head-1" ? 1 : 2, blob_hash: head }], head, max_seq: 1 });
    mocks.syncPull.mockImplementation(async () => ({
      protocol: 2, objects: [{ id: "cloud-only", seq: head === "head-1" ? 1 : 2, blob }],
      head, max_seq: 1, next_seq: 1, cursor_done: true,
    }));
    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));

    blob = bytesToBase64(await sealSyncObject(DEK, utf8Bytes('{"id":"cloud-only","name":"recovered","updatedAt":2}'), "cloud-only", "asset"));
    head = "head-2";
    clickButton(mounted.container, "重试读取");
    await flushUntil(() => text().includes("recovered"));

    expect(text()).not.toContain("无法解密");
    expect(mocks.syncPush).not.toHaveBeenCalled();
    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("clears a cloud-only corrupt object only after confirmation and with head and blob hash preconditions", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"cloud-only"}'), "cloud-only", "asset"));
    let exists = true;
    mocks.syncIds.mockImplementation(async (knownHead?: string) => {
      const head = exists ? "head-1" : "head-2";
      if (knownHead === head) return { protocol: 2, unchanged: true, head, max_seq: exists ? 1 : 2 };
      return { protocol: 2, entries: exists ? [{ id: "cloud-only", seq: 1, blob_hash: "hash-1" }] : [], head, max_seq: exists ? 1 : 2 };
    });
    mocks.syncPull.mockImplementation(async (since: number) => {
      if (since !== (exists ? 1 : 2)) throw new Error("pull scanned unrelated history");
      return {
        protocol: 2, objects: exists ? [{ id: "cloud-only", seq: 1, blob }] : [],
        head: exists ? "head-1" : "head-2", max_seq: exists ? 1 : 2, next_seq: exists ? 1 : 2, cursor_done: true,
      };
    });
    mocks.syncDelete.mockImplementation(async () => {
      exists = false;
      return { head: "head-2", max_seq: 2, deleted: true };
    });
    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));

    mocks.ask.mockResolvedValueOnce(false);
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => mocks.ask.mock.calls.length === 1);
    expect(mocks.syncDelete).not.toHaveBeenCalled();

    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => mocks.syncDelete.mock.calls.length === 1);
    expect(mocks.syncDelete).toHaveBeenCalledWith("head-1", "cloud-only", "hash-1");
    await flushUntil(() => text().includes("本机与云端都还没有可同步的内容"));
  });

  it("refuses cloud deletion when a locked local credential makes collection incomplete", async () => {
    await seedUndecryptableCloudOnly("cloud-only");
    mocks.collectCredentials.mockResolvedValue({
      credentials: [{ id: "cloud-only", name: "locked", kind: "password", updatedAt: 1, secretState: "locked" }],
      hasMore: false,
    });
    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => text().includes("本机内容读取不完整"));

    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("refuses cloud deletion when an excluded local kind has the same ID", async () => {
    await seedUndecryptableCloudOnly("cloud-only");
    mocks.collectKnownHosts.mockResolvedValue({
      knownHosts: [{ id: "cloud-only", host: "example.test", port: 22, keyType: "ssh-ed25519", fingerprint: "test", addedAt: 1 }],
      hasMore: false,
    });
    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => text().includes("本机现在已有同 ID 副本"));

    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("refuses cloud deletion when a local transcript with sync disabled has the same ID", async () => {
    await seedUndecryptableCloudOnly("cloud-only");
    mocks.transcriptHosts.mockResolvedValue([{ assetId: "asset-1" }]);
    mocks.transcriptList.mockResolvedValue([{
      id: "cloud-only", assetName: "device", syncOptIn: false, active: false, endedAt: 2, startedAt: 1,
      contentOmitted: false, bytes: 0,
    }]);
    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => text().includes("本机现在已有同 ID 会话记录"));

    expect(mocks.syncDelete).not.toHaveBeenCalled();
  });

  it("keeps an undecryptable row visible when conditional cloud deletion conflicts", async () => {
    const { sealSyncObject, bytesToBase64, utf8Bytes } = await import("../features/auth/crypto");
    const { AuthApiError } = await import("../ipc/authApi");
    const blob = bytesToBase64(await sealSyncObject(new Uint8Array(32).fill(8), utf8Bytes('{"id":"cloud-only"}'), "cloud-only", "asset"));
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "cloud-only", seq: 1, blob_hash: "hash-1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "cloud-only", seq: 1, blob }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncDelete.mockRejectedValue(new AuthApiError("forbidden", "conflict", 409));

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("清除云端副本"));
    clickButton(mounted.container, "清除云端副本");
    await flushUntil(() => text().includes("云端数据已变化，请重试读取后再次操作"));

    expect(text()).toContain("无法解密");
    expect(mocks.syncDelete).toHaveBeenCalledWith("head-1", "cloud-only", "hash-1");
  });
});

describe("applySet deduplication and second-sync convergence (P1-2)", () => {
  it("skips already-identical objects with matching hashes and never calls applyObjects for them", async () => {
    // The remote is real Go fixture bytes; local collect gathers the same asset, payload byte-identical to Go -> same hash
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a-1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "",
          sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    const wire = await remoteFixtureWire("a-1", "asset", GO_SYNC_FIXTURES.assetMinimal!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "a-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("已一致"));
    expect(text()).toContain("当前待应用 0 项");
    expect(text()).toContain("待推送 0 项");
    expect(mocks.applyObjects).not.toHaveBeenCalled();
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("applies a remote-deleted group tombstone once and does not apply or push it again on the second sync", async () => {
    // Local has a live group g-del (revision 100); the cloud tombstone deletedAt 500 wins (group tombstones are the backend-supported deletion propagation path)
    let tombstoneApplied = false;
    mocks.groupList.mockImplementation(async () => {
      if (tombstoneApplied) return [];
      return [{ id: "g-del", parentId: null, name: "old-group", sort: 0, createdAt: 1, updatedAt: 100 }];
    });
    mocks.collectTombstones.mockImplementation(async () => {
      if (tombstoneApplied) return { tombstones: [{ id: "g-del", targetKind: "group", deletedAt: 500 }], hasMore: false };
      return { tombstones: [], hasMore: false };
    });
    const wire = await remoteFixtureWire("g-del", "tombstone", GO_SYNC_FIXTURES.tombstoneGroup!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "g-del", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockImplementation(async () => {
      tombstoneApplied = true;
      return { applied: 1, identical: 0, skipped: 0, objects: [{ id: "g-del", kind: "tombstone", result: "applied" }] };
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("云端已删"));
    await runAutoSync();
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.applyObjects).toHaveBeenCalledWith([
      { id: "g-del", kind: "tombstone", payload: { targetKind: "group", deletedAt: 500 } },
    ]);
    // After applying, the local side keeps an identical-content tombstone, consistent with the cloud: both sets drop to zero, no more writes are fired
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    await flushUntil(() => text().includes("当前待应用 0 项"));
    expect(text()).toContain("已一致");
    expect(text()).toContain("待推送 0 项");

    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("applies a remote soft-deleted assetObject with deletedAt once and does not apply or push it again", async () => {
    // Asset deletion goes through the soft-deleted assetObject (asset tombstones are not supported by the backend); remote deletedAt 150 > local revision 100
    const liveAsset = {
      id: "a-del", groupId: "g1", kind: "ssh", name: "old <&> 01", host: "10.0.0.8", port: 22,
      username: "root", authKind: "password", keyPath: "/home/u/.ssh/id_ed25519", credId: "c-1",
      optionsJson: "{}", tags: "x,y", note: "note <b>&", sort: 0, createdAt: 1, updatedAt: 100,
    };
    let applied = false;
    mocks.collectAssets.mockImplementation(async () => {
      if (applied) return { assets: [{ ...liveAsset, deletedAt: 150 }], hasMore: false };
      return { assets: [liveAsset], hasMore: false };
    });
    const wire = await remoteFixtureWire("a-del", "asset", GO_SYNC_FIXTURES.assetDeleted!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "a-del", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockImplementation(async () => {
      applied = true;
      return { applied: 1, identical: 0, skipped: 0, objects: [{ id: "a-del", kind: "asset", result: "applied" }] };
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("云端已删"));
    await runAutoSync();
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("a-del");
    expect(objects[0].kind).toBe("asset");
    expect(objects[0].payload.deletedAt).toBe(150);
    // After applying, the local soft-deleted asset is byte-identical to the remote: the second apply/push round is empty
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    await flushUntil(() => text().includes("当前待应用 0 项"));
    expect(text()).toContain("已一致");
    expect(text()).toContain("待推送 0 项");
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("has an empty second apply/push round after applying a revealed credential (real Go fixture bytes)", async () => {
    // The remote credential is Go fixture bytes (name/secret contain <>& and U+2028); after applying, the local revealed credential is recollected,
    // payload byte-identical to Go -> already consistent, no more apply/push
    let applied = false;
    mocks.collectCredentials.mockImplementation(async () => {
      if (applied) {
        return {
          credentials: [
            { id: "c-1", name: "生产 <口令> & more", kind: "password", updatedAt: 50, secret: "p@ss<>&\u2028w0rd", secretState: "revealed" },
          ],
          hasMore: false,
        };
      }
      return { credentials: [], hasMore: false };
    });
    const wire = await remoteFixtureWire("c-1", "credential", GO_SYNC_FIXTURES.credential!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "c-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockImplementation(async () => {
      applied = true;
      return { applied: 1, identical: 0, skipped: 0, objects: [{ id: "c-1", kind: "credential", result: "applied" }] };
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("生产 <口令> & more"));
    await runAutoSync();
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.applyObjects).toHaveBeenCalledWith([
      {
        id: "c-1",
        kind: "credential",
        payload: { id: "c-1", name: "生产 <口令> & more", kind: "password", secret: "p@ss<>&\u2028w0rd", updatedAt: 50 },
      },
    ]);
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    await flushUntil(() => text().includes("当前待应用 0 项"));
    expect(text()).toContain("已一致");
    expect(text()).toContain("待推送 0 项");
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });
});

describe("transcript endedAt revision and content completion (P1-3)", () => {
  const SUMMARY_OMITTED = {
    id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
    startedAt: 1, endedAt: 2, bytes: 3, chunks: 1, truncated: false, active: false, syncOptIn: true, contentOmitted: true,
  };
  const SUMMARY_FULL = { ...SUMMARY_OMITTED, contentOmitted: false };

  it("applies remote full content when local content is omitted instead of pushing the omitted version", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([SUMMARY_OMITTED]);
    // The remote full content is real Go json.Marshal bytes (fixture), not a JS replica
    const wire = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockResolvedValue({
      applied: 1, identical: 0, skipped: 0,
      objects: [{ id: "t1", kind: "transcript", result: "applied" }],
    });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01 的会话记录"));
    // The local omitted version must not overwrite the cloud full version: it does not enter the push set, it enters the apply set
    expect(text()).toContain("待推送 0 项");
    expect(text()).toContain("当前待应用 1 项");
    await runAutoSync();
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("t1");
    expect(objects[0].kind).toBe("transcript");
    expect(objects[0].payload.contentOmitted).not.toBe(true);
    expect(objects[0].payload.content).toEqual([{ seq: 1, tabId: "tab-1", ts: 1, data: "AQID" }]);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("pushes complete local content when the remote content is omitted", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([SUMMARY_FULL]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2, done: true, totalBytes: 3,
    });
    const wire = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptOmitted!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h-omitted" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01 的会话记录"));
    expect(text()).toContain("当前待应用 0 项");
    expect(text()).toContain("待推送 1 项");
    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["t1"]);
    // The pushed plaintext is byte-identical to the Go fixture (the local full content is the Go layout)
    const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
    const plaintext = await openSyncObject(DEK, base64ToBytes(objects[0].blob), "t1", "transcript");
    expect(new TextDecoder().decode(plaintext)).toBe(new TextDecoder().decode(base64ToBytes(GO_SYNC_FIXTURES.transcriptBasic!)));
  });

  it("uses endedAt to resolve differing content and converges both sides after the local version wins and is pushed", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([{ ...SUMMARY_FULL, endedAt: 10, startedAt: 9 }]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2, done: true, totalBytes: 3,
    });
    // The cloud has different content for the same record (endedAt 2 < local 10); the remote is real Go fixture bytes
    const wire = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01 的会话记录"));
    // The local endedAt is larger -> local is newer, push; the remote does not enter the apply set
    expect(text()).toContain("本机较新");
    expect(text()).toContain("当前待应用 0 项");
    expect(text()).toContain("待推送 1 项");
    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
    // The pushed plaintext is byte-identical to the Go fixture (transcriptEnded10)
    const pushedObjects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
    const pushedPlaintext = await openSyncObject(DEK, base64ToBytes(pushedObjects[0].blob), "t1", "transcript");
    expect(new TextDecoder().decode(pushedPlaintext)).toBe(new TextDecoder().decode(base64ToBytes(GO_SYNC_FIXTURES.transcriptEnded10!)));
    // After a successful push the cloud equals the local content (Go fixture bytes): both sides are consistent on the second sync, no more push or apply
    const wire2 = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptEnded10!, 2);
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire2], head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true });
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 2, blob_hash: "h2" }], head: "head-2", max_seq: 2 });
    await runAutoSync();
    await flushUntil(() => text().includes("已一致"));
    expect(text()).toContain("待推送 0 项");
    expect(text()).toContain("当前待应用 0 项");
    expect(mocks.syncPush).toHaveBeenCalledTimes(1);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
  });

  it("reuses an immutable transcript without rereading chunks on the second round", async () => {
    const summary = { ...SUMMARY_FULL, id: "t-cache" };
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([summary]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2, done: true, totalBytes: 3,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });
    const { runWebSync } = await import("../features/settings/SyncCard");
    const optIn = { knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false };

    await runWebSync(DEK, optIn);
    expect(mocks.transcriptRead).toHaveBeenCalledTimes(1);
    await runWebSync(DEK, optIn);
    expect(mocks.transcriptRead).toHaveBeenCalledTimes(1);
  });
});
