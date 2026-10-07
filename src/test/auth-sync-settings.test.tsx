/** @vitest-environment jsdom */
// M165 端到端验收: known_host/AI 档案 opt-in 开关默认关、开启后 collect/compare/push/apply 接入
// M163 对象(revealed/masked 密钥处理、vault-locked 警告、墓碑按开关过滤、冲突三元组保留、二次同步收敛)。
// 全部经真实 SyncCard 交互与真实加解密; 远端载荷是真实 Go json.Marshal fixture 字节(src/test/auth-sync-fixtures.ts)。
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
    },
    adminApi: {
      users: vi.fn().mockResolvedValue({ users: [] }),
      settingsGet: vi.fn().mockResolvedValue({ registration_open: false, public_base_url: "" }),
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
import { mockInvoke } from "../demo/mock";

const DEK = new Uint8Array(32).fill(7);
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

const KNOWN_HOST_DTO = {
  id: "kh-1",
  host: "10.0.0.9",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:5Va5w2K7bh2+5Nm4EwvX0n8QV2c7CXWq3R6e3d3w4Xk",
  addedAt: 1700000000000,
};

const AI_PROFILE_DTO = {
  id: "p-1",
  name: "生产 <档案> & more",
  baseUrl: "https://api.example.com/v1",
  model: "gpt-4o",
  fallbackModel: "gpt-4o-mini",
  temperature: 0.7,
  contextWindow: 128000,
  maxTokens: 4096,
  proxy: "http://127.0.0.1:7890",
  stream: true,
  requestTimeoutSeconds: 60,
  idleTimeoutSeconds: 300,
  circuitFailureThreshold: 5,
  circuitCooldownSeconds: 30,
  updatedAt: 1700000001000,
  apiKey: "sk-live<>&\u2028key",
  apiKeySet: true,
  apiKeyState: "revealed" as const,
};

let mounted: MountedView | undefined;
let optInState: { knownHost: boolean; aiProfile: boolean };

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

async function remoteFixtureWire(id: string, kind: import("../features/auth/crypto").SyncObjectKind, fixture: string, seq: number) {
  const { sealSyncObject, bytesToBase64, base64ToBytes } = await import("../features/auth/crypto");
  return { id, seq, blob: bytesToBase64(await sealSyncObject(DEK, base64ToBytes(fixture), id, kind)) };
}

async function openPushed(object: { id: string; blob: string }, kind: import("../features/auth/crypto").SyncObjectKind): Promise<string> {
  const { openSyncObject, base64ToBytes } = await import("../features/auth/crypto");
  const plaintext = await openSyncObject(DEK, base64ToBytes(object.blob), object.id, kind);
  return new TextDecoder().decode(plaintext);
}

function text(): string {
  return mounted?.container.textContent ?? "";
}

function button(label: string): HTMLButtonElement | undefined {
  return [...(mounted?.container.querySelectorAll("button") ?? [])].find(
    (b) => b.textContent?.trim() === label,
  ) as HTMLButtonElement | undefined;
}

// 开关复选框: 第一个为 known_host, 第二个为 AI 档案
function optInCheckbox(index: number): HTMLInputElement {
  const boxes = [...(mounted?.container.querySelectorAll('input[type="checkbox"]') ?? [])] as HTMLInputElement[];
  const box = boxes[index];
  if (!box) throw new Error(`checkbox ${index} not found`);
  return box;
}

// opt-in 开关经 kindOptInGet 异步到达后才渲染
async function flushUntilOptInRendered(): Promise<void> {
  await flushUntil(() => (mounted?.container.querySelectorAll('input[type="checkbox"]').length ?? 0) >= 2);
}

function fixtureText(name: string): string {
  const bin = atob(GO_SYNC_FIXTURES[name]);
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  useUi.setState({ pushToast: mocks.toast });
  optInState = { knownHost: false, aiProfile: false };
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue({ chunks: [], nextSeq: 0, done: true, totalBytes: 0 });
  mocks.collectAssets.mockResolvedValue({ assets: [], hasMore: false });
  mocks.collectTombstones.mockResolvedValue({ tombstones: [], hasMore: false });
  mocks.collectCredentials.mockResolvedValue({ credentials: [], hasMore: false });
  mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [KNOWN_HOST_DTO], hasMore: false });
  mocks.collectAIProfiles.mockResolvedValue({ profiles: [AI_PROFILE_DTO], hasMore: false });
  mocks.kindOptInGet.mockImplementation(async () => ({ ...optInState }));
  mocks.kindOptInSet.mockImplementation(async (patch: { knownHost?: boolean; aiProfile?: boolean }) => {
    optInState = { ...optInState, ...patch };
    return { ...optInState };
  });
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

describe("M165 opt-in 默认关", () => {
  it("不收集 known_host/AI 档案, 远端对应对象也不应用, 开关渲染为未勾选", async () => {
    const khWire = await remoteFixtureWire("kh-1", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 1);
    const pWire = await remoteFixtureWire("p-1", "ai_profile", GO_SYNC_FIXTURES.aiProfileFull!, 2);
    mocks.syncIds.mockResolvedValue({
      protocol: 2,
      entries: [
        { id: "kh-1", seq: 1, blob_hash: "h1" },
        { id: "p-1", seq: 2, blob_hash: "h2" },
      ],
      head: "head-1",
      max_seq: 2,
    });
    mocks.syncPull.mockImplementation(async (_since: number, ids?: string[]) => ({
      protocol: 2,
      objects: [khWire, pWire].filter((w) => !ids || ids.includes(w.id)),
      head: "head-1",
      max_seq: 2,
      next_seq: 2,
      cursor_done: true,
    }));

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    // 远端 known_host/AI 档案被 opt-in 过滤: 不进应用集合, 也不出现在对比行
    await flushUntil(() => text().includes("本机与云端都还没有可同步的内容。"));
    expect(mocks.kindOptInGet).toHaveBeenCalled();
    expect(mocks.collectKnownHosts).not.toHaveBeenCalled();
    expect(mocks.collectAIProfiles).not.toHaveBeenCalled();
    expect(optInCheckbox(0).checked).toBe(false);
    expect(optInCheckbox(1).checked).toBe(false);
    expect(text()).toContain("拉取并应用 (0)");
    expect(button("拉取并应用 (0)")?.disabled).toBe(true);
    expect(text()).not.toContain("10.0.0.9:22");
    expect(text()).not.toContain("生产 <档案> & more");
  });
});

describe("M165 known_host 同步", () => {
  it("开启后收集/对比/推送(Go fixture 字节), 二次同步收敛", async () => {
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    optInCheckbox(0).click();
    await flushUntil(() => text().includes("10.0.0.9:22"));
    expect(mocks.kindOptInSet).toHaveBeenCalledWith({ knownHost: true });
    expect(text()).toContain("已知主机");
    expect(text()).toContain("仅本机");
    expect(text()).toContain("推送到云端 (1)");

    clickButton(mounted!.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["kh-1"]);
    // 推送明文与 Go json.Marshal 逐字节一致(knownHostObject 布局)
    expect(await openPushed(objects[0], "known_host")).toBe(fixtureText("knownHostBasic"));
    expect(mocks.applyObjects).not.toHaveBeenCalled();

    // 二次同步: 云端即本机内容, 两边一致, 不再推送也不应用
    const wire = await remoteFixtureWire("kh-1", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 2);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "kh-1", seq: 2, blob_hash: "h2" }], head: "head-2", max_seq: 2 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true });
    await flushUntil(() => text().includes("已推送 1"));
    clickButton(mounted!.container, "刷新对比");
    await flushUntil(() => text().includes("已一致"));
    expect(text()).toContain("推送到云端 (0)");
    expect(text()).toContain("拉取并应用 (0)");
    expect(mocks.syncPush).toHaveBeenCalledTimes(1);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
  });

  it("known_host 墓碑: 开关关闭时被过滤不推送; 开启后推送且保留冲突三元组", async () => {
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });
    mocks.collectTombstones.mockResolvedValue({
      tombstones: [{ id: "kh-del", targetKind: "known_host", deletedAt: 700, host: "10.0.0.9", port: 22, keyType: "ssh-ed25519" }],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    // 默认关: known_host 墓碑不进入推送集合, 也不出现在对比行
    await flushUntil(() => text().includes("本机与云端都还没有可同步的内容。"));
    expect(text()).toContain("推送到云端 (0)");
    expect(text()).not.toContain("kh-del");
    expect(mocks.syncPush).not.toHaveBeenCalled();

    optInCheckbox(0).click();
    await flushUntil(() => text().includes("推送到云端 (1)"));
    clickButton(mounted!.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["kh-del"]);
    // 冲突墓碑原样携带败者三元组(M163 R4 语义), 不得退化为用户删除墓碑
    expect(await openPushed(objects[0], "tombstone")).toBe(fixtureText("tombstoneKnownHostConflict"));
  });
});

describe("M165 AI 档案同步", () => {
  it("revealed 档案推送明文密钥(Go fixture 字节); locked 档案记 warning 不进推送", async () => {
    optInState = { knownHost: false, aiProfile: true };
    mocks.collectAIProfiles.mockResolvedValue({
      profiles: [
        AI_PROFILE_DTO,
        { id: "p-2", name: "locked-profile", baseUrl: "https://api.example.com/v1", model: "m", temperature: 0, contextWindow: 8192, proxy: null, stream: false, updatedAt: 1, apiKeySet: true, apiKeyState: "locked" },
      ],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("AI 档案「locked-profile」凭据库未解锁,未进入本次对比与推送"));
    expect(optInCheckbox(1).checked).toBe(true);
    expect(mocks.collectAIProfiles).toHaveBeenCalledWith(true, undefined, 512);
    expect(text()).toContain("推送到云端 (1)");

    clickButton(mounted!.container, "推送到云端 (1)");
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["p-1"]);
    // 载荷与 Go json.Marshal 逐字节一致(aiProfileObject 布局, apiKey 明文在其中, 整体经 DEK 加密)
    expect(await openPushed(objects[0], "ai_profile")).toBe(fixtureText("aiProfileFull"));
  });

  it("远端 AI 档案在开关开启时应用(aiProfileObject 原样下发)", async () => {
    optInState = { knownHost: false, aiProfile: true };
    mocks.collectAIProfiles.mockResolvedValue({ profiles: [], hasMore: false });
    const wire = await remoteFixtureWire("p-1", "ai_profile", GO_SYNC_FIXTURES.aiProfileFull!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "p-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockResolvedValue({ applied: 1, identical: 0, skipped: 0, objects: [{ id: "p-1", kind: "ai_profile", result: "applied" }] });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("生产 <档案> & more"));
    expect(text()).toContain("AI 档案");
    expect(text()).toContain("仅云端");
    expect(text()).toContain("拉取并应用 (1)");
    clickButton(mounted!.container, "拉取并应用 (1)");
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("p-1");
    expect(objects[0].kind).toBe("ai_profile");
    expect(objects[0].payload.apiKey).toBe("sk-live<>&\u2028key");
    expect(objects[0].payload.updatedAt).toBe(1700000001000);
  });
});

describe("M165 demo mock 命令形态", () => {
  it("sync_kind_opt_in_get 默认全 false, set 回显合并结果", async () => {
    await expect(mockInvoke("sync_kind_opt_in_get")).resolves.toEqual({ knownHost: false, aiProfile: false });
    await expect(mockInvoke("sync_kind_opt_in_set", { args: { knownHost: true } })).resolves.toEqual({ knownHost: true, aiProfile: false });
  });

  it("sync_collect_known_hosts 与生产同形(knownHosts + hasMore)", async () => {
    const result = (await mockInvoke("sync_collect_known_hosts", { args: {} })) as {
      knownHosts: { id: string; host: string; port: number; keyType: string; fingerprint: string; addedAt: number }[];
      hasMore: boolean;
    };
    expect(result.hasMore).toBe(false);
    expect(result.knownHosts.length).toBeGreaterThan(0);
    for (const host of result.knownHosts) {
      expect(host).toMatchObject({ id: expect.any(String), host: expect.any(String), port: expect.any(Number), keyType: expect.any(String), fingerprint: expect.any(String), addedAt: expect.any(Number) });
    }
  });

  it("sync_collect_ai_profiles: 未请求明文时 apiKey 扣留(withheld), 请求后按 revealed 给出", async () => {
    const withheld = (await mockInvoke("sync_collect_ai_profiles", { args: { revealSecrets: false } })) as {
      profiles: { apiKey?: string; apiKeySet: boolean; apiKeyState: string }[];
    };
    for (const profile of withheld.profiles) {
      expect(profile.apiKey).toBeUndefined();
      expect(profile.apiKeyState).toBe("withheld");
    }
    const revealed = (await mockInvoke("sync_collect_ai_profiles", { args: { revealSecrets: true } })) as {
      profiles: { apiKey?: string; apiKeySet: boolean; apiKeyState: string }[];
    };
    for (const profile of revealed.profiles) {
      expect(profile.apiKeyState).toBe("revealed");
      if (profile.apiKeySet) expect(profile.apiKey).toBeDefined();
      else expect(profile.apiKey).toBeUndefined();
    }
  });
});
