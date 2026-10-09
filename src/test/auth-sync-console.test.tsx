/** @vitest-environment jsdom */
// M125 R3 端到端验收:M141 collect 消费(P1-1)、applySet 去重与二次收敛(P1-2)、
// transcript endedAt 修订与内容补全(P1-3)。全部经真实 SyncCard 交互与真实加解密。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

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
    kindOptInGet: vi.fn(),
    kindOptInSet: vi.fn(),
    applyObjects: vi.fn(),
    syncIds: vi.fn(),
    syncPull: vi.fn(),
    syncPush: vi.fn(),
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
    kindOptInGet: mocks.kindOptInGet,
    kindOptInSet: mocks.kindOptInSet,
  },
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../ui/dialogs", () => ({ ask: vi.fn() }));

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
      deviceRevoke: vi.fn().mockResolvedValue({ ok: true }),
      enrollCode: vi.fn().mockResolvedValue({ code: "e", expires_at: 1 }),
      totpStatus: vi.fn().mockResolvedValue({ enabled: false, pending: false, mfa_required: false, recovery_codes_left: 0 }),
      totpSetup: vi.fn(),
      totpConfirm: vi.fn(),
      totpDisable: vi.fn(),
      totpLogin: vi.fn(),
    },
    adminApi: {
      users: vi.fn().mockResolvedValue({ users: [] }),
      settingsGet: vi.fn().mockResolvedValue({ registration_open: false, public_base_url: "", mfa_required: false }),
      settingsPut: vi.fn(),
      createUser: vi.fn(),
      disableUser: vi.fn(),
      resetUser: vi.fn(),
    },
    syncV2Api: {
      ids: mocks.syncIds,
      pull: mocks.syncPull,
      push: mocks.syncPush,
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

// remoteFixtureWire 直接密封真实 Go json.Marshal 产出的 fixture 字节(src/test/auth-sync-fixtures.ts),
// 不用 JS JSON.stringify 复刻布局,避免同源自证。
async function remoteFixtureWire(id: string, kind: import("../features/auth/crypto").SyncObjectKind, fixture: string, seq: number) {
  const { sealSyncObject, bytesToBase64, base64ToBytes } = await import("../features/auth/crypto");
  return { id, seq, blob: bytesToBase64(await sealSyncObject(DEK, base64ToBytes(fixture), id, kind)) };
}

async function openPushed(object: { id: string; blob: string }, kind: import("../features/auth/crypto").SyncObjectKind): Promise<Record<string, unknown>> {
  const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
  const plaintext = await openSyncObject(DEK, base64ToBytes(object.blob), object.id, kind);
  return JSON.parse(new TextDecoder().decode(plaintext)) as Record<string, unknown>;
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function button(label: string): HTMLButtonElement | undefined {
  return [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === label,
  ) as HTMLButtonElement | undefined;
}

function openDetails(): void {
  clickButton(mounted!.container, "对比详情");
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
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false });
  mocks.applyObjects.mockResolvedValue({ applied: 0, identical: 0, skipped: 0, objects: [] });
  mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-0", max_seq: 0 });
  mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [], head: "head-0", max_seq: 0, next_seq: 0, cursor_done: true });
  seedAuthed();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});

describe("M141 collect 消费:完整本地副本进入推送(P1-1)", () => {
  it("软删资产、两类墓碑与 revealed 凭据真实出现在 /sync/v2/push", async () => {
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
    clickButton(mounted.container, "推送到云端 (4)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);

    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["a-del", "c-del", "c1", "g-del"]);
    // 软删资产带 deletedAt 载荷
    const assetPayload = await openPushed(objects.find((o) => o.id === "a-del")!, "asset");
    expect(assetPayload.deletedAt).toBe(150);
    expect(assetPayload.name).toBe("old-01");
    // 两类墓碑载荷与 tombstoneObject 对齐
    const groupTomb = await openPushed(objects.find((o) => o.id === "g-del")!, "tombstone");
    expect(groupTomb).toEqual({ targetKind: "group", deletedAt: 300 });
    const credTomb = await openPushed(objects.find((o) => o.id === "c-del")!, "tombstone");
    expect(credTomb).toEqual({ targetKind: "credential", deletedAt: 400 });
    // 凭据载荷携带明文 secret(credentialObject 形状)
    const credPayload = await openPushed(objects.find((o) => o.id === "c1")!, "credential");
    expect(credPayload).toEqual({ id: "c1", name: "生产口令", kind: "password", secret: "s3cret", updatedAt: 50 });
  });

  it("collect 分页游标推进读完全部条目", async () => {
    mocks.collectAssets
      .mockResolvedValueOnce({
        assets: [{ id: "a1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1 }],
        hasMore: true,
        nextAfterId: "a1",
      })
      .mockResolvedValueOnce({
        assets: [{ id: "a2", kind: "ssh", name: "db-02", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 1 }],
        hasMore: false,
      });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 2, applied: 2, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01") && text().includes("db-02"));
    expect(mocks.collectAssets).toHaveBeenCalledTimes(2);
    expect(mocks.collectAssets).toHaveBeenNthCalledWith(1, true, undefined, 512);
    expect(mocks.collectAssets).toHaveBeenNthCalledWith(2, true, "a1", 512);
    clickButton(mounted.container, "推送到云端 (2)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["a1", "a2"]);
  });

  it("locked/unavailable/error 凭据明确 warning 且不进入推送", async () => {
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
    expect(text()).toContain("凭据「锁定凭据」凭据库未解锁,未进入本次对比与推送");
    expect(text()).toContain("凭据「不可用凭据」凭据库不可用,未进入本次对比与推送");
    expect(text()).toContain("凭据「解密失败凭据」解密失败,未进入本次对比与推送");
    // 推送集合为空,不发起 push
    expect(text()).toContain("推送到云端 (0)");
    expect(button("推送到云端 (0)")?.disabled).toBe(true);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });
});

describe("applySet 去重与二次同步收敛(P1-2)", () => {
  it("已一致(同 hash)对象不进入 applySet,不重复调用 applyObjects", async () => {
    // 远端是真实 Go fixture 字节;本机 collect 收集同一资产,载荷与 Go 逐字节一致 → 同 hash
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
    expect(text()).toContain("拉取并应用 (0)");
    expect(button("拉取并应用 (0)")?.disabled).toBe(true);
    expect(text()).toContain("推送到云端 (0)");
    expect(mocks.applyObjects).not.toHaveBeenCalled();
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("remote-deleted 墓碑(group)应用一次后,二次同步不再重复应用/推送", async () => {
    // 本机有存活分组 g-del(修订 100);云端墓碑 deletedAt 500 胜出(group 墓碑是后端支持的删除传播路径)
    let tombstoneApplied = false;
    mocks.groupList.mockImplementation(async () => {
      if (tombstoneApplied) return [];
      return [{ id: "g-del", parentId: null, name: "旧分组", sort: 0, createdAt: 1, updatedAt: 100 }];
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
    clickButton(mounted.container, "拉取并应用 (1)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.applyObjects).toHaveBeenCalledWith([
      { id: "g-del", kind: "tombstone", payload: { targetKind: "group", deletedAt: 500 } },
    ]);
    // 应用后本机留下同内容墓碑,与云端一致:两个集合都归零,不再发起任何写
    await flushUntil(() => text().includes("已应用 1"));
    await flushUntil(() => button("拉取并应用 (0)") !== undefined);
    expect(text()).toContain("已一致");
    expect(text()).toContain("推送到云端 (0)");
    expect(button("拉取并应用 (0)")?.disabled).toBe(true);
    expect(button("推送到云端 (0)")?.disabled).toBe(true);
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("远端软删资产(带 deletedAt 的 assetObject)应用后,二次同步不再重复应用/推送", async () => {
    // 资产删除走软删 assetObject(asset 墓碑后端不支持);远端 deletedAt 150 > 本机修订 100
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
    clickButton(mounted.container, "拉取并应用 (1)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("a-del");
    expect(objects[0].kind).toBe("asset");
    expect(objects[0].payload.deletedAt).toBe(150);
    // 应用后本机软删资产与远端逐字节一致:第二轮 apply/push 均为空
    await flushUntil(() => text().includes("已应用 1"));
    await flushUntil(() => button("拉取并应用 (0)") !== undefined);
    expect(text()).toContain("已一致");
    expect(text()).toContain("推送到云端 (0)");
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("revealed 凭据应用成功后,第二轮 apply/push 均为空(真实 Go fixture 字节)", async () => {
    // 远端凭据是 Go fixture 字节(名称/秘密含 <>& 与 U+2028);应用后本机 revealed 凭据重新收集,
    // 载荷与 Go 逐字节一致 → 已一致,不再 apply/push
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
    clickButton(mounted.container, "拉取并应用 (1)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.applyObjects).toHaveBeenCalledWith([
      {
        id: "c-1",
        kind: "credential",
        payload: { id: "c-1", name: "生产 <口令> & more", kind: "password", secret: "p@ss<>&\u2028w0rd", updatedAt: 50 },
      },
    ]);
    await flushUntil(() => text().includes("已应用 1"));
    await flushUntil(() => button("拉取并应用 (0)") !== undefined);
    expect(text()).toContain("已一致");
    expect(text()).toContain("推送到云端 (0)");
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });
});

describe("transcript endedAt 修订与内容补全(P1-3)", () => {
  const SUMMARY_OMITTED = {
    id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
    startedAt: 1, endedAt: 2, bytes: 3, chunks: 1, truncated: false, active: false, syncOptIn: true, contentOmitted: true,
  };
  const SUMMARY_FULL = { ...SUMMARY_OMITTED, contentOmitted: false };

  it("本机 omitted、远端完整:远端胜出,走应用补全而不是推送省略版", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([SUMMARY_OMITTED]);
    // 远端完整内容是真实 Go json.Marshal 字节(fixture),不是 JS 复刻
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
    // 本机省略版不得覆盖云端完整版:不进推送集合,进应用集合
    expect(text()).toContain("推送到云端 (0)");
    expect(text()).toContain("拉取并应用 (1)");
    clickButton(mounted.container, "拉取并应用 (1)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("t1");
    expect(objects[0].kind).toBe("transcript");
    expect(objects[0].payload.contentOmitted).not.toBe(true);
    expect(objects[0].payload.content).toEqual([{ seq: 1, tabId: "tab-1", ts: 1, data: "AQID" }]);
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("本机完整、远端 omitted:本地胜出并推送完整内容", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([SUMMARY_FULL]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2, done: true, totalBytes: 3,
    });
    const wire = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptOmitted!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01 的会话记录"));
    expect(text()).toContain("拉取并应用 (0)");
    expect(text()).toContain("推送到云端 (1)");
    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["t1"]);
    // 推送明文与 Go fixture 逐字节一致(本机完整内容即 Go 布局)
    const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
    const plaintext = await openSyncObject(DEK, base64ToBytes(objects[0].blob), "t1", "transcript");
    expect(new TextDecoder().decode(plaintext)).toBe(new TextDecoder().decode(base64ToBytes(GO_SYNC_FIXTURES.transcriptBasic!)));
  });

  it("不同内容按 endedAt 裁决;本机胜出推送后二次同步两边一致", async () => {
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([{ ...SUMMARY_FULL, endedAt: 10, startedAt: 9 }]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2, done: true, totalBytes: 3,
    });
    // 云端是同记录的不同内容(endedAt 2 < 本机 10),远端为真实 Go fixture 字节
    const wire = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("web-01 的会话记录"));
    // 本机 endedAt 更大 → 本机较新,推送;远端不进入应用集合
    expect(text()).toContain("本机较新");
    expect(text()).toContain("拉取并应用 (0)");
    expect(text()).toContain("推送到云端 (1)");
    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
    // 推送明文与 Go fixture(transcriptEnded10)逐字节一致
    const pushedObjects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
    const pushedPlaintext = await openSyncObject(DEK, base64ToBytes(pushedObjects[0].blob), "t1", "transcript");
    expect(new TextDecoder().decode(pushedPlaintext)).toBe(new TextDecoder().decode(base64ToBytes(GO_SYNC_FIXTURES.transcriptEnded10!)));
    // 等推送流程(含推送后重载)完全结束:推送按钮恢复可点
    await flushUntil(() => text().includes("已推送 1"));
    await flushUntil(() => {
      const b = button("推送到云端 (1)");
      return b !== undefined && !b.disabled;
    });

    // 推送成功后云端即本机内容(Go fixture 字节):二次同步两边一致,不再推送也不应用
    const wire2 = await remoteFixtureWire("t1", "transcript", GO_SYNC_FIXTURES.transcriptEnded10!, 2);
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire2], head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true });
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 2, blob_hash: "h2" }], head: "head-2", max_seq: 2 });
    clickButton(mounted.container, "刷新对比");
    await flushUntil(() => text().includes("已一致"));
    expect(text()).toContain("推送到云端 (0)");
    expect(text()).toContain("拉取并应用 (0)");
    expect(mocks.syncPush).toHaveBeenCalledTimes(1);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
  });
});
