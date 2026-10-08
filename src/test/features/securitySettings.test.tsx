/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, flush, flushUntil, mount, setInputValue, type MountedView } from "./reactTestUtils";
import type { KnownHostDto } from "../../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    knownHostList: vi.fn(),
    knownHostRemove: vi.fn(),
    knownHostAccept: vi.fn(),
    sessionConnect: vi.fn(),
    assetList: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
    syncIds: vi.fn(),
    syncPull: vi.fn(),
    syncPush: vi.fn(),
    authStatus: vi.fn(),
    authMe: vi.fn(),
    authDekGet: vi.fn(),
    authDevices: vi.fn(),
    adminUsers: vi.fn(),
    adminSettingsGet: vi.fn(),
    transcriptHosts: vi.fn(),
    transcriptList: vi.fn(),
    transcriptRead: vi.fn(),
    collectAssets: vi.fn(),
    collectTombstones: vi.fn(),
    collectCredentials: vi.fn(),
    applyObjects: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", () => ({
  assetApi: {
    knownHostList: mocks.knownHostList,
    knownHostRemove: mocks.knownHostRemove,
    knownHostAccept: mocks.knownHostAccept,
    list: mocks.assetList,
    groupList: mocks.groupList,
    snippetList: mocks.snippetList,
  },
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
  },
  dbApi: {},
  sessionApi: { connect: mocks.sessionConnect },
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

vi.mock("../../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: mocks.authStatus,
      me: mocks.authMe,
      dekGet: mocks.authDekGet,
      devices: mocks.authDevices,
      logout: vi.fn().mockResolvedValue({ ok: true }),
      logoutAll: vi.fn().mockResolvedValue({ revoked: 1 }),
      deviceRevoke: vi.fn().mockResolvedValue({ ok: true }),
      enrollCode: vi.fn().mockResolvedValue({ code: "enroll-1", expires_at: 1 }),
    },
    adminApi: {
      users: mocks.adminUsers,
      settingsGet: mocks.adminSettingsGet,
      settingsPut: vi.fn().mockResolvedValue({ registration_open: true, public_base_url: "" }),
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

// 解锁路径避免真实 argon2id(生产参数一次数秒),只替换 unwrap;其余加解密保持真实。
vi.mock("../../features/auth/crypto", async (importOriginal) => {
  const original = await importOriginal<typeof import("../../features/auth/crypto")>();
  return {
    ...original,
    unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(7)),
  };
});

import { KnownHostsCard } from "../../features/settings/KnownHostsCard";
import { SyncCard } from "../../features/settings/SyncCard";
import { useAuth } from "../../features/auth/store";
import { connectAsset, useUi } from "../../app/store";

const KH1: KnownHostDto = {
  id: "kh1",
  host: "10.0.0.8",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:aaa111",
  addedAt: 1,
};
const KH2: KnownHostDto = {
  id: "kh2",
  host: "db.internal",
  port: 2222,
  keyType: "ssh-rsa",
  fingerprint: "SHA256:bbb222",
  addedAt: 2,
};

function mountSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

const DEMO_USER = {
  id: "u-1",
  username: "alice",
  display_name: "Alice",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

function seedAuthed(dek: boolean) {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: DEMO_USER,
    dek: dek ? new Uint8Array(32).fill(7) : null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

function seedLoggedOut() {
  useAuth.setState({
    status: { initialized: true, registration_open: false, auth: "on" },
    user: null,
    dek: null,
    gate: "ready",
    pendingRecoveryKey: null,
    error: null,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  useUi.setState({ pushToast: mocks.toast });
  mocks.assetList.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue({ chunks: [], nextSeq: 0, done: true, totalBytes: 0 });
  mocks.collectAssets.mockResolvedValue({ assets: [], hasMore: false });
  mocks.collectTombstones.mockResolvedValue({ tombstones: [], hasMore: false });
  mocks.collectCredentials.mockResolvedValue({ credentials: [], hasMore: false });
  mocks.applyObjects.mockResolvedValue({ applied: 0, identical: 0, skipped: 0, objects: [] });
  mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-0", max_seq: 0 });
  mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [], head: "head-0", max_seq: 0, next_seq: 0, cursor_done: true });
  mocks.authStatus.mockResolvedValue({ initialized: true, registration_open: false, auth: "on" });
  mocks.authMe.mockResolvedValue({ user: DEMO_USER, csrf_token: "csrf-1" });
  mocks.authDekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.authDevices.mockResolvedValue({ devices: [] });
  mocks.adminUsers.mockResolvedValue({ users: [DEMO_USER] });
  mocks.adminSettingsGet.mockResolvedValue({ registration_open: false, public_base_url: "" });
});

afterEach(() => {
  useAuth.setState({ user: null, dek: null, gate: "ready", pendingRecoveryKey: null, error: null, status: null });
});

describe("KnownHostsCard", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("shows loading first, then the trusted hosts with fingerprints", async () => {
    const slow = deferred<KnownHostDto[]>();
    mocks.knownHostList.mockReturnValue(slow.promise);
    mounted = mount(createElement(KnownHostsCard));
    expect(mounted.container.textContent).toContain("读取中…");

    slow.resolve([KH1, KH2]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("10.0.0.8:22");
    expect(text).toContain("SHA256:aaa111");
    expect(text).toContain("db.internal:2222");
    expect(text).toContain("2 台");
  });

  it("shows an empty hint when nothing is trusted yet", async () => {
    mocks.knownHostList.mockResolvedValue([]);
    mounted = mount(createElement(KnownHostsCard));
    await flush();
    expect(mounted.container.textContent).toContain("还没有信任过任何主机");
  });

  it("surfaces a list failure with a working retry", async () => {
    mocks.knownHostList
      .mockRejectedValueOnce(new Error("磁盘炸了"))
      .mockResolvedValueOnce([KH1]);
    mounted = mount(createElement(KnownHostsCard));
    await flush();
    expect(mounted.container.textContent).toContain("磁盘炸了");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
    expect(mocks.knownHostList).toHaveBeenCalledTimes(2);
  });

  it("drops a stale in-flight list result that lands after a newer one", async () => {
    const stale = deferred<KnownHostDto[]>();
    mocks.knownHostList.mockReturnValueOnce(stale.promise).mockResolvedValueOnce([KH2]);
    mounted = mount(createElement(KnownHostsCard));

    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("db.internal:2222");

    stale.resolve([KH1]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("db.internal:2222");
    expect(text).not.toContain("10.0.0.8:22");
  });

  it("asks before revoking and does nothing when the confirmation is declined", async () => {
    mocks.knownHostList.mockResolvedValue([KH1]);
    mocks.ask.mockResolvedValue(false);
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("10.0.0.8:22");
    expect(question).toContain("SHA256:aaa111");
    expect(mocks.knownHostRemove).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
  });

  it("revokes only after confirmation, then reloads the list", async () => {
    mocks.knownHostList.mockResolvedValueOnce([KH1, KH2]).mockResolvedValueOnce([KH2]);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostRemove.mockResolvedValue(undefined);
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.knownHostRemove).toHaveBeenCalledWith("kh1");
    expect(mounted.container.textContent).not.toContain("10.0.0.8:22");
    expect(mounted.container.textContent).toContain("db.internal:2222");
    expect(mocks.knownHostList).toHaveBeenCalledTimes(2);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已撤销"));
  });

  it("keeps the entry and reports when the revoke itself fails", async () => {
    mocks.knownHostList.mockResolvedValue([KH1]);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostRemove.mockRejectedValue(new Error("只读数据库"));
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("撤销失败"));
    expect(mocks.knownHostList).toHaveBeenCalledTimes(1);
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
  });
});

describe("SyncCard（Web 同步台）", () => {
  let mounted: MountedView | undefined;

  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("未登录时只有账号卡一个登录入口,同步台不重复渲染", async () => {
    seedLoggedOut();
    mounted = mountSyncCard();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("当前未登录");
    // 全卡只有一个登录按钮(AuthCard 账号卡);WebSyncConsole 不再渲染第二个登录入口
    const loginButtons = [...mounted.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "登录",
    );
    expect(loginButtons.length).toBe(1);
    expect(text).not.toContain("登录以启用同步");
    expect(text).not.toContain("解锁数据密钥");
  });

  it("已登录未解锁时显示解锁表单,输入密码解锁", async () => {
    seedAuthed(false);
    mounted = mountSyncCard();
    await flush();
    expect(mounted.container.textContent).toContain("解锁数据密钥");

    setInputValue(
      mounted.container.querySelector<HTMLInputElement>('input[placeholder="账号密码"]')!,
      "correct horse battery staple",
    );
    await flush();
    clickButton(mounted.container, "解锁");
    await flushUntil(() => useAuth.getState().dek !== null);
    expect(mocks.authDekGet).toHaveBeenCalled();
  });

  it("解锁后显示本机与云端的对比状态", async () => {
    seedAuthed(true);
    mocks.groupList.mockResolvedValue([
      { id: "g1", parentId: null, name: "生产环境", sort: 0, createdAt: 1, updatedAt: 100 },
    ]);
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", groupId: "g1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
        {
          id: "a2", kind: "ssh", name: "db-02", host: "10.0.0.9", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 300,
        },
      ],
      hasMore: false,
    });
    // 远端:a1 较新(修订 250 > 200),另有一个云端独有的 a3
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    const blobA1 = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a1", groupId: "g1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 250 })),
      "a1",
      "asset",
    );
    const blobA3 = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a3", kind: "ssh", name: "cache-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 400 })),
      "a3",
      "asset",
    );
    mocks.syncIds.mockResolvedValue({
      protocol: 2,
      entries: [
        { id: "a1", seq: 1, blob_hash: "h1" },
        { id: "a3", seq: 2, blob_hash: "h3" },
      ],
      head: "head-2",
      max_seq: 2,
    });
    mocks.syncPull.mockImplementation(async (_seq: number, ids?: string[]) => {
      const want = ids?.[0];
      const objects = [
        { id: "a1", seq: 1, blob: bytesToBase64(blobA1) },
        { id: "a3", seq: 2, blob: bytesToBase64(blobA3) },
      ].filter((o) => !want || o.id === want);
      return { protocol: 2, objects, head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true };
    });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01"));
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("云端较新"); // a1 云端修订更新
    expect(text).toContain("仅云端"); // a3 仅云端
    expect(text).toContain("仅本机"); // g1/db-02 仅本机
  });

  it("推送调用 /sync/v2/push 并更新本地游标", async () => {
    seedAuthed(true);
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-9", max_seq: 9, applied: 1, skipped: 0 });
    // 推送成功后服务端 head 推进到 head-9,后续 ids 应反映同一 head
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-9", max_seq: 9 });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01"));

    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    // 推送前取服务端最新 head(此处 mock 为 head-9)作为 known_head,而不是空字符串
    expect(mocks.syncPush).toHaveBeenCalledWith(
      "head-9",
      expect.arrayContaining([
        expect.objectContaining({ id: "a1", blob: expect.any(String) }),
      ]),
    );
    await flushUntil(() => window.localStorage.getItem("sync.cursor.u-1")?.includes("head-9") === true);
    expect(window.localStorage.getItem("sync.cursor.u-1")).toContain("head-9");
  });

  it("云端较新的对象不会被推送(winner 集合跳过远端胜出项)", async () => {
    seedAuthed(true);
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    // 远端 a1 修订 300 > 本机 200 → 云端较新,不应推送
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    const blob = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 300 })),
      "a1",
      "asset",
    );
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "a1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "a1", seq: 1, blob: bytesToBase64(blob) }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 0, skipped: 1 });

    mounted = mountSyncCard();
    await flushUntil(() => mounted!.container.textContent?.includes("云端较新"));
    // 按钮计数为 0(winner 集合为空),且不应触发推送
    expect(mounted.container.textContent).toContain("推送到云端 (0)");
    const pushBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("推送到云端")) as HTMLButtonElement | undefined;
    expect(pushBtn?.disabled).toBe(true);
  });

  it("平修订号按 payload hash 决胜:本地载荷 hash 较大才推送", async () => {
    seedAuthed(true);
    const { sealSyncObject, utf8Bytes, bytesToBase64, objectPayloadHash } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    const localPayload = { id: "a1", kind: "ssh", name: "aaa", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 };
    const remotePayload = { id: "a1", kind: "ssh", name: "zzz", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200 };
    const localHash = await objectPayloadHash(utf8Bytes(JSON.stringify(localPayload)));
    const remoteHash = await objectPayloadHash(utf8Bytes(JSON.stringify(remotePayload)));
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "aaa", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    const blob = await sealSyncObject(dek, utf8Bytes(JSON.stringify(remotePayload)), "a1", "asset");
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "a1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "a1", seq: 1, blob: bytesToBase64(blob) }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("aaa"));
    const expected = localHash > remoteHash ? 1 : 0;
    expect(mounted.container.textContent).toContain(`推送到云端 (${expected})`);
  });

  it("真 409(他端已更新)时重新对账并以新 head 重试一次", async () => {
    seedAuthed(true);
    const { AuthApiError } = await import("../../ipc/authApi");
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    // ids 调用序列:加载(head-0)→ 推送取新 head(head-0,与基线一致不重拉)→ 重试再取(head-1)
    mocks.syncIds
      .mockResolvedValueOnce({ protocol: 2, entries: [], head: "head-0", max_seq: 0 })
      .mockResolvedValueOnce({ protocol: 2, entries: [], head: "head-0", max_seq: 0 })
      .mockResolvedValueOnce({ protocol: 2, entries: [], head: "head-1", max_seq: 1 });
    // 第一次推送真实返回 409 异常;重试以新 head 成功
    mocks.syncPush
      .mockRejectedValueOnce(new AuthApiError("forbidden", "同步头不一致", 409))
      .mockResolvedValueOnce({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01"));
    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length >= 2);
    expect(mocks.syncPush).toHaveBeenNthCalledWith(1, "head-0", expect.any(Array));
    expect(mocks.syncPush).toHaveBeenNthCalledWith(2, "head-1", expect.any(Array));
  });

  it("head 变化且远端同 ID 更新时,重新对账后不覆盖他端新数据", async () => {
    seedAuthed(true);
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    // 本机 a1 修订 200;云端 a1 修订 300(他端更新),head 从 head-0 变 head-1
    const remotePayload = { id: "a1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 300 };
    const blob = await sealSyncObject(dek, utf8Bytes(JSON.stringify(remotePayload)), "a1", "asset");
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    mocks.syncIds
      .mockResolvedValueOnce({ protocol: 2, entries: [{ id: "a1", seq: 1, blob_hash: "h1" }], head: "head-0", max_seq: 1 })
      .mockResolvedValueOnce({ protocol: 2, entries: [{ id: "a1", seq: 2, blob_hash: "h2" }], head: "head-1", max_seq: 2 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "a1", seq: 2, blob: bytesToBase64(blob) }], head: "head-1", max_seq: 2, next_seq: 2, cursor_done: true });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 2, applied: 0, skipped: 1 });

    mounted = mountSyncCard();
    await flushUntil(() => mounted!.container.textContent?.includes("云端较新"));
    // 云端较新,winner 为空,不应推送
    expect(mounted.container.textContent).toContain("推送到云端 (0)");
    const pushBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("推送到云端")) as HTMLButtonElement | undefined;
    expect(pushBtn?.disabled).toBe(true);
  });

  it("按条 opt-in 的会话记录被收集并推送(默认不同步)", async () => {
    seedAuthed(true);
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([
      {
        id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
        startedAt: 1, endedAt: 2, bytes: 3, chunks: 1, truncated: false, active: false, syncOptIn: true, contentOmitted: false,
      },
    ]);
    mocks.transcriptRead.mockResolvedValue({
      chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }],
      nextSeq: 2,
      done: true,
      totalBytes: 3,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-9", max_seq: 9, applied: 1, skipped: 0 });
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-9", max_seq: 9 });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01 的会话记录"));
    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    expect(mocks.syncPush).toHaveBeenCalledWith(
      "head-9",
      expect.arrayContaining([
        expect.objectContaining({ id: "t1", blob: expect.any(String) }),
      ]),
    );
  });

  it("多页会话记录按 done/nextSeq 读全后再推送", async () => {
    seedAuthed(true);
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([
      {
        id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
        startedAt: 1, endedAt: 2, bytes: 6, chunks: 3, truncated: false, active: false, syncOptIn: true, contentOmitted: false,
      },
    ]);
    // 第一页 done=false,第二页 done=true;必须读全两页才推送
    mocks.transcriptRead
      .mockResolvedValueOnce({ chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }], nextSeq: 2, done: false, totalBytes: 6 })
      .mockResolvedValueOnce({ chunks: [{ seq: 2, tabId: "tab-1", ts: 2, dataBase64: "BAUG" }, { seq: 3, tabId: "tab-1", ts: 3, dataBase64: "BwgJ" }], nextSeq: 4, done: true, totalBytes: 6 });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-9", max_seq: 9, applied: 1, skipped: 0 });
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-9", max_seq: 9 });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01 的会话记录"));
    expect(mocks.transcriptRead).toHaveBeenCalledTimes(2);
    clickButton(mounted.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    // 推送的 blob 解码后应含全部 3 个 chunk
    const pushed = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    const t1 = pushed.find((o) => o.id === "t1");
    expect(t1).toBeDefined();
    const { openSyncObject, base64ToBytes } = await import("../../features/auth/crypto");
    const plaintext = await openSyncObject(new Uint8Array(32).fill(7), base64ToBytes(t1!.blob), "t1", "transcript");
    const parsed = JSON.parse(new TextDecoder().decode(plaintext)) as { content?: unknown[]; contentOmitted?: boolean };
    expect(parsed.contentOmitted).not.toBe(true);
    expect(parsed.content).toHaveLength(3);
  });

  it("相同 payload hash 的会话记录不重复推送(二次空同步)", async () => {
    seedAuthed(true);
    const { sealSyncObject, bytesToBase64, base64ToBytes } = await import("../../features/auth/crypto");
    const { GO_SYNC_FIXTURES } = await import("../auth-sync-fixtures");
    const dek = new Uint8Array(32).fill(7);
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    const summary = {
      id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
      startedAt: 1, endedAt: 2, bytes: 3, chunks: 1, truncated: false, active: false, syncOptIn: true, contentOmitted: false,
    };
    mocks.transcriptList.mockResolvedValue([summary]);
    mocks.transcriptRead.mockResolvedValue({ chunks: [{ seq: 1, tabId: "tab-1", ts: 1, dataBase64: "AQID" }], nextSeq: 2, done: true, totalBytes: 3 });
    // 远端已有相同记录,载荷是真实 Go json.Marshal 字节(fixture);本机构造与其逐字节一致
    const blob = await sealSyncObject(dek, base64ToBytes(GO_SYNC_FIXTURES.transcriptBasic!), "t1", "transcript");
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "t1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "t1", seq: 1, blob: bytesToBase64(blob) }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    await flushUntil(() => mounted!.container.textContent?.includes("web-01 的会话记录"));
    // 已一致,winner 与 applySet 均为空,不推送也不重复应用
    expect(mounted.container.textContent).toContain("推送到云端 (0)");
    expect(mounted.container.textContent).toContain("拉取并应用 (0)");
    const applyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("拉取并应用")) as HTMLButtonElement | undefined;
    expect(applyBtn?.disabled).toBe(true);
  });

  it("会话记录读取失败时显式报错而不是吞成空数组", async () => {
    seedAuthed(true);
    mocks.transcriptHosts.mockResolvedValue([
      { assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false, transcripts: 1, lastStartedAt: 1 },
    ]);
    mocks.transcriptList.mockResolvedValue([
      {
        id: "t1", sessionId: "s1", assetId: "a1", assetName: "web-01", assetKind: "ssh", assetDeleted: false,
        startedAt: 1, endedAt: 2, bytes: 3, chunks: 1, truncated: false, active: false, syncOptIn: true, contentOmitted: false,
      },
    ]);
    mocks.transcriptRead.mockRejectedValue(new Error("磁盘不可读"));

    mounted = mountSyncCard();
    await flushUntil(() => mounted!.container.textContent?.includes("磁盘不可读"));
    expect(mounted.container.textContent).not.toContain("web-01 的会话记录");
  });

  it("拉取并应用:远端胜出对象经 applyObjects 应用并重新计算对比", async () => {
    seedAuthed(true);
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    // 本机 a1 修订 200;云端 a1 修订 300(云端较新),另有一个仅云端的 a2
    mocks.collectAssets.mockResolvedValue({
      assets: [
        {
          id: "a1", kind: "ssh", name: "web-01", host: "10.0.0.8", port: 22,
          username: "root", authKind: "password", optionsJson: "{}",
          tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 200,
        },
      ],
      hasMore: false,
    });
    const blobA1 = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a1", kind: "ssh", name: "web-01", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 300 })),
      "a1",
      "asset",
    );
    const blobA2 = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "a2", kind: "ssh", name: "db-02", optionsJson: "{}", tags: "", note: "", sort: 0, createdAt: 1, updatedAt: 400 })),
      "a2",
      "asset",
    );
    mocks.syncIds.mockResolvedValue({
      protocol: 2,
      entries: [
        { id: "a1", seq: 1, blob_hash: "h1" },
        { id: "a2", seq: 2, blob_hash: "h2" },
      ],
      head: "head-2",
      max_seq: 2,
    });
    mocks.syncPull.mockImplementation(async (_seq: number, ids?: string[]) => {
      const want = ids?.[0];
      const objects = [
        { id: "a1", seq: 1, blob: bytesToBase64(blobA1) },
        { id: "a2", seq: 2, blob: bytesToBase64(blobA2) },
      ].filter((o) => !want || o.id === want);
      return { protocol: 2, objects, head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true };
    });
    mocks.applyObjects.mockResolvedValue({
      applied: 2,
      identical: 0,
      skipped: 0,
      objects: [
        { id: "a1", kind: "asset", result: "applied" },
        { id: "a2", kind: "asset", result: "applied" },
      ],
    });

    mounted = mountSyncCard();
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "拉取并应用 (2)",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "拉取并应用 (2)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.applyObjects).toHaveBeenCalledWith(
      expect.arrayContaining([
        expect.objectContaining({ id: "a1", kind: "asset" }),
        expect.objectContaining({ id: "a2", kind: "asset" }),
      ]),
    );
    await flushUntil(() => mounted!.container.textContent?.includes("已应用 2"));
  });

  it("凭据库未解锁时应用显示 skipped/warning 而不是静默成功", async () => {
    seedAuthed(true);
    const { sealSyncObject, utf8Bytes, bytesToBase64 } = await import("../../features/auth/crypto");
    const dek = new Uint8Array(32).fill(7);
    const blob = await sealSyncObject(
      dek,
      utf8Bytes(JSON.stringify({ id: "c1", name: "生产口令", kind: "password", secret: "s3cret", updatedAt: 1 })),
      "c1",
      "credential",
    );
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "c1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [{ id: "c1", seq: 1, blob: bytesToBase64(blob) }], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockResolvedValue({
      applied: 0,
      identical: 0,
      skipped: 1,
      objects: [{ id: "c1", kind: "credential", result: "skipped", warning: "凭据库已锁定, 请先解锁" }],
    });

    mounted = mountSyncCard();
    clickButton(mounted.container, "对比详情");
    // 等远端对象加载完成(凭据行出现),按钮才会变为「拉取并应用 (1)」可用
    await flushUntil(() => (mounted!.container.textContent ?? "").includes("生产口令"));
    await flushUntil(() => {
      const btn = [...mounted!.container.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "拉取并应用 (1)",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(mounted.container, "拉取并应用 (1)");
    await flushUntil(() => mounted!.container.textContent?.includes("凭据库已锁定"));
    expect(mounted.container.textContent).toContain("跳过 1");
  });
});

describe("connectAsset 主机指纹确认", () => {
  const asset = { id: "asset-1", name: "测试机", kind: "ssh" };
  const pendingDetail = {
    host: "10.0.0.8",
    port: 22,
    keyType: "ssh-ed25519",
    fingerprint: "SHA256:newfp",
    changed: false,
  };
  const sessionInfo = {
    id: "s1",
    assetId: "asset-1",
    name: "测试机",
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: 0,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    useUi.setState({ pushToast: mocks.toast });
  });

  it("首次连接时弹出指纹确认，显式接受后记录信任并重连", async () => {
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: pendingDetail,
      })
      .mockResolvedValueOnce(sessionInfo);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [question, options] = mocks.ask.mock.calls[0] as [string, Record<string, unknown>];
    expect(question).toContain("首次连接 10.0.0.8:22");
    expect(question).toContain("ssh-ed25519");
    expect(question).toContain("SHA256:newfp");
    expect(options).toMatchObject({ title: "确认主机指纹", kind: "warning" });
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("asset-1");
    expect(useUi.getState().sessions.some((s) => s.id === "s1")).toBe(true);
  });

  it("密钥变更时弹出变更警告并展示原指纹与新指纹", async () => {
    const changedDetail = {
      ...pendingDetail,
      changed: true,
      known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
    };
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "SSH host key for 10.0.0.8:22 changed",
        detail: changedDetail,
      })
      .mockResolvedValueOnce(sessionInfo);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    const [question, options] = mocks.ask.mock.calls[0] as [string, Record<string, unknown>];
    expect(question).toContain("主机密钥已变更 10.0.0.8:22");
    expect(question).toContain("SHA256:oldfp");
    expect(question).toContain("SHA256:newfp");
    expect(options).toMatchObject({ title: "主机密钥变更警告", kind: "warning" });
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("asset-1");
  });

  it("拒绝确认时不记录信任、不再重连", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: pendingDetail,
    });
    mocks.ask.mockResolvedValue(false);

    await connectAsset(asset);

    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("info", "已取消连接");
  });

  it("记录信任后重连失败时上报错误", async () => {
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: pendingDetail,
      })
      .mockRejectedValueOnce({ code: "ssh", message: "SSH handshake: broken" });
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.any(String));
  });

  it("指纹 detail 不完整时不写账本也不重连", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: { host: "10.0.0.8", port: 22 },
    });
    mocks.ask.mockResolvedValue(true);

    await connectAsset(asset);

    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.any(String));
  });
});
