/** @vitest-environment jsdom */
// M165 end-to-end acceptance: known_host / AI profile sync toggles explicitly off, and collect/compare/push/apply wiring once on
// for M163 objects (revealed/masked secret handling, vault-locked warning, tombstones filtered by toggle, conflict triples kept, second sync converges).
// All through real SyncCard interaction and real encryption; remote payloads are real Go json.Marshal fixture bytes (src/test/auth-sync-fixtures.ts).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

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
let optInState: { knownHost: boolean; aiProfile: boolean; aiPermission?: boolean; aiGrant?: boolean; preferences?: boolean };

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

async function runAutoSync(wrapInAct = true) {
  const { runWebSync, WEB_SYNC_RESULT_EVENT, WEB_SYNC_ERROR_EVENT } = await import("../features/settings/SyncCard");
  const execute = async () => {
    try {
      const result = await runWebSync(DEK, { aiPermission: false, aiGrant: false, preferences: false, ...optInState });
      if (result) {
        if (wrapInAct) window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, { detail: result }));
        else await act(() => window.dispatchEvent(new CustomEvent(WEB_SYNC_RESULT_EVENT, { detail: result })));
      }
    } catch (error) {
      if (wrapInAct) window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: error }));
      else await act(() => window.dispatchEvent(new CustomEvent(WEB_SYNC_ERROR_EVENT, { detail: error })));
      throw error;
    }
  };
  if (wrapInAct) await act(execute);
  else await execute();
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

// Toggle checkboxes: the first is known_host, the second is the AI profile
function optInCheckbox(index: number): HTMLInputElement {
  const boxes = [...(mounted?.container.querySelectorAll('input[type="checkbox"]') ?? [])] as HTMLInputElement[];
  const box = boxes[index];
  if (!box) throw new Error(`checkbox ${index} not found`);
  return box;
}

// Opt-in toggles render only after kindOptInGet arrives asynchronously; collapsed by default, open the sync content section when not auto-expanded (nothing enabled)
async function flushUntilOptInRendered(): Promise<void> {
  await flushUntil(() => button("同步内容开关") !== undefined);
  if ((mounted?.container.querySelectorAll('input[type="checkbox"]').length ?? 0) < 2) {
    clickButton(mounted!.container, "同步内容开关");
  }
  await flushUntil(() => (mounted?.container.querySelectorAll('input[type="checkbox"]').length ?? 0) >= 2);
}

function openDetails(): void {
  clickButton(mounted!.container, "对比详情");
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
  optInState = { knownHost: false, aiProfile: false, aiPermission: false, preferences: false };
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
  mocks.collectAIGrants.mockResolvedValue({ grants: [], rules: [], modes: [] });
  mocks.kindOptInGet.mockImplementation(async () => ({ ...optInState }));
  mocks.kindOptInSet.mockImplementation(async (patch: Partial<typeof optInState>) => {
    optInState = { ...optInState, ...patch };
    return { ...optInState };
  });
  mocks.collectAIPermission.mockResolvedValue({ id: "global", mode: "read_write", dangerRules: [], updatedAt: 0, found: false });
  mocks.collectPreferences.mockResolvedValue([]);
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

describe("M165 opt-ins disabled by default", () => {
  it("does not collect known_host or AI profiles, apply matching remote objects, and renders both toggles unchecked", async () => {
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
    openDetails();
    // Remote known_host / AI profiles are filtered by opt-in: they enter neither the apply set nor the compare rows
    await flushUntil(() => text().includes("本机与服务端都还没有可同步的内容。"));
    expect(mocks.kindOptInGet).toHaveBeenCalled();
    expect(mocks.collectKnownHosts).not.toHaveBeenCalled();
    expect(mocks.collectAIProfiles).not.toHaveBeenCalled();
    expect(optInCheckbox(0).checked).toBe(false);
    expect(optInCheckbox(1).checked).toBe(false);
    expect(text()).toContain("当前待应用 0 项");
    expect(text()).not.toContain("10.0.0.9:22");
    expect(text()).not.toContain("生产 <档案> & more");
  });
});

describe("M165 known_host sync", () => {
  it("collects, compares, and pushes after enabling, then converges on the second sync (Go fixture bytes)", async () => {
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-2", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    act(() => optInCheckbox(0).click());
    openDetails();
    await flushUntil(() => text().includes("10.0.0.9:22"));
    expect(mocks.kindOptInSet).toHaveBeenCalledWith({ knownHost: true });
    expect(text()).toContain("已知主机");
    expect(text()).toContain("仅本机");
    expect(text()).toContain("待推送 1 项");

    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["kh-1"]);
    // The pushed plaintext is byte-identical to Go json.Marshal (knownHostObject layout)
    expect(await openPushed(objects[0], "known_host")).toBe(fixtureText("knownHostBasic"));
    expect(mocks.applyObjects).not.toHaveBeenCalled();

    // Second sync: the cloud equals the local content, both sides consistent, no more push or apply
    const wire = await remoteFixtureWire("kh-1", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 2);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "kh-1", seq: 2, blob_hash: "h2" }], head: "head-2", max_seq: 2 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-2", max_seq: 2, next_seq: 2, cursor_done: true });
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    await runAutoSync();
    await flushUntil(() => text().includes("已一致"));
    expect(text()).toContain("待推送 0 项");
    expect(text()).toContain("当前待应用 0 项");
    expect(mocks.syncPush).toHaveBeenCalledTimes(1);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
  });

  it("filters a known_host tombstone while disabled and pushes it with the conflict triple after enabling", async () => {
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });
    mocks.collectTombstones.mockResolvedValue({
      tombstones: [{ id: "kh-del", targetKind: "known_host", deletedAt: 700, host: "10.0.0.9", port: 22, keyType: "ssh-ed25519" }],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 1, applied: 1, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    openDetails();
    // Off by default: the known_host tombstone enters neither the push set nor the compare rows
    await flushUntil(() => text().includes("本机与服务端都还没有可同步的内容。"));
    expect(text()).toContain("待推送 0 项");
    expect(text()).not.toContain("kh-del");
    expect(mocks.syncPush).not.toHaveBeenCalled();

    act(() => optInCheckbox(0).click());
    await flushUntil(() => text().includes("待推送 1 项"));
    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["kh-del"]);
    // The conflict tombstone carries the loser triple as-is (M163 R4 semantics); it must not degrade into a user-deleted tombstone
    expect(await openPushed(objects[0], "tombstone")).toBe(fixtureText("tombstoneKnownHostConflict"));
  });
});

describe("M165 AI profile sync", () => {
  it("pushes plaintext keys for revealed profiles (Go fixture bytes) and warns about locked profiles without pushing them", async () => {
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
    await flushUntil(() => text().includes("AI 模型档案「locked-profile」凭据库未解锁，未进入本次对比与推送"));
    expect(optInCheckbox(1).checked).toBe(true);
    expect(mocks.collectAIProfiles).toHaveBeenCalledWith(true, undefined, 512);
    expect(text()).toContain("待推送 1 项");

    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id)).toEqual(["p-1"]);
    // The payload is byte-identical to Go json.Marshal (aiProfileObject layout, apiKey in plaintext inside, the whole thing DEK-encrypted)
    expect(await openPushed(objects[0], "ai_profile")).toBe(fixtureText("aiProfileFull"));
  });

  it("applies a remote AI profile when its toggle is enabled (aiProfileObject passed through as-is)", async () => {
    optInState = { knownHost: false, aiProfile: true };
    mocks.collectAIProfiles.mockResolvedValue({ profiles: [], hasMore: false });
    const wire = await remoteFixtureWire("p-1", "ai_profile", GO_SYNC_FIXTURES.aiProfileFull!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "p-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.applyObjects.mockResolvedValue({ applied: 1, identical: 0, skipped: 0, objects: [{ id: "p-1", kind: "ai_profile", result: "applied" }] });

    mounted = mountSyncCard();
    openDetails();
    await flushUntil(() => text().includes("生产 <档案> & more"));
    expect(text()).toContain("AI 模型档案");
    expect(text()).toContain("仅服务端");
    expect(text()).toContain("当前待应用 1 项");
    await runAutoSync();
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    const [objects] = mocks.applyObjects.mock.calls[0] as [{ id: string; kind: string; payload: Record<string, unknown> }[]];
    expect(objects[0].id).toBe("p-1");
    expect(objects[0].kind).toBe("ai_profile");
    expect(objects[0].payload.apiKey).toBe("sk-live<>&\u2028key");
    expect(objects[0].payload.updatedAt).toBe(1700000001000);
  });
});

describe("M166 AI permission and preference sync", () => {
  it("includes AI permissions, appearance, and keybindings in sync objects and pushes them", async () => {
    optInState = { knownHost: false, aiProfile: false, aiPermission: true, preferences: true };
    mocks.collectAIPermission.mockResolvedValue({ id: "global", mode: "silent", dangerRules: ["rm -rf /"], updatedAt: 100, found: true });
    mocks.collectPreferences.mockResolvedValue([
      { id: "preference:keybinding.newTerminal", key: "keybinding.newTerminal", value: "Mod+Shift+t", updatedAt: 100 },
    ]);
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 2, applied: 2, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    openDetails();
    await flushUntil(() => text().includes("全局 AI 权限") && text().includes("keybinding.newTerminal"));
    expect(text()).toContain("待推送 2 项");
    expect(text()).toContain("同步 AI 权限与拦截规则");
    expect(text()).toContain("同步外观与快捷键");

    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["global", "preference:keybinding.newTerminal"]);
    expect(await openPushed(objects.find((o) => o.id === "global")!, "ai_permission")).toBe('{"id":"global","mode":"silent","dangerRules":["rm -rf /"],"updatedAt":100}');
    expect(await openPushed(objects.find((o) => o.id === "preference:keybinding.newTerminal")!, "preference")).toBe('{"id":"preference:keybinding.newTerminal","key":"keybinding.newTerminal","value":"Mod+Shift+t","updatedAt":100}');
  });

  it("enables AI grants and permission modes by default and pushes device grants, rules, and modes", async () => {
    optInState = { knownHost: false, aiProfile: false, aiGrant: true };
    mocks.collectAIGrants.mockResolvedValue({
      grants: [{ id: "device/dev-1", deviceId: "dev-1", kinds: ["terminal_write"], updatedAt: 100 }],
      rules: [{ id: "rule/r-1", deviceId: "dev-1", action: "write_file", path: "/var/log/**", createdAt: 100, updatedAt: 100 }],
      modes: [{ id: "mode/dev-1", deviceId: "dev-1", mode: "read_only", updatedAt: 100 }],
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 3, applied: 3, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    openDetails();
    await flushUntil(() => text().includes("dev-1 write_file /var/log/**"));
    expect(text()).toContain("待推送 3 项");
    expect(text()).toContain("同步 AI 授权与权限模式");

    await runAutoSync();
    await flushUntil(() => mocks.syncPush.mock.calls.length > 0);
    const objects = mocks.syncPush.mock.calls[0]?.[1] as { id: string; blob: string }[];
    expect(objects.map((o) => o.id).sort()).toEqual(["device/dev-1", "mode/dev-1", "rule/r-1"]);
    expect(await openPushed(objects.find((o) => o.id === "device/dev-1")!, "ai_grant")).toBe('{"id":"device/dev-1","deviceId":"dev-1","kinds":["terminal_write"],"updatedAt":100}');
    expect(await openPushed(objects.find((o) => o.id === "rule/r-1")!, "ai_grant")).toBe('{"id":"rule/r-1","deviceId":"dev-1","action":"write_file","path":"/var/log/**","createdAt":100,"updatedAt":100}');
    expect(await openPushed(objects.find((o) => o.id === "mode/dev-1")!, "ai_grant")).toBe('{"id":"mode/dev-1","deviceId":"dev-1","mode":"read_only","updatedAt":100}');
  });

  it("does not collect objects or push matching tombstones after disabling their toggles", async () => {
    mocks.collectTombstones.mockResolvedValue({
      tombstones: [
        { id: "global", targetKind: "ai_permission", deletedAt: 700 },
        { id: "preference:keybinding.newTerminal", targetKind: "preference", deletedAt: 700 },
        { id: "device/dev-9", targetKind: "ai_grant", deletedAt: 700 },
      ],
      hasMore: false,
    });
    mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 0, applied: 0, skipped: 0 });

    mounted = mountSyncCard();
    await flushUntilOptInRendered();
    openDetails();
    await flushUntil(() => text().includes("本机与服务端都还没有可同步的内容。"));
    expect(mocks.collectAIPermission).not.toHaveBeenCalled();
    expect(mocks.collectAIGrants).not.toHaveBeenCalled();
    expect(mocks.collectPreferences).not.toHaveBeenCalled();
    expect(text()).not.toContain("preference:keybinding.newTerminal");
    expect(text()).not.toContain("dev-9");

    await runAutoSync();
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });
});

describe("M165 R2 opt-in toggle races", () => {
  function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((res) => {
      resolve = res;
    });
    return { promise, resolve };
  }

  const KNOWN_HOST_TOMBSTONE = { id: "kh-del", targetKind: "known_host", deletedAt: 700, host: "10.0.0.9", port: 22, keyType: "ssh-ed25519" };

  it("keeps push disabled during an on-to-off reload and never deletes the remote tombstone after disabling", async () => {
    optInState = { knownHost: true, aiProfile: false };
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });
    mocks.collectTombstones.mockResolvedValue({ tombstones: [KNOWN_HOST_TOMBSTONE], hasMore: false });
    const setGate = deferred<void>();
    mocks.kindOptInSet.mockImplementationOnce(async (patch: { knownHost?: boolean; aiProfile?: boolean }) => {
      await setGate.promise;
      optInState = { ...optInState, ...patch };
      return { ...optInState };
    });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("待推送 1 项"));

    // Toggling off: push is disabled while set is pending; still disabled after set returns until reload completes (the old snapshot is invalidated)
    const reloadGate = deferred<void>();
    mocks.collectTombstones.mockImplementationOnce(async () => {
      await reloadGate.promise;
      return { tombstones: [], hasMore: false };
    });
    act(() => optInCheckbox(0).click());
    await flushUntil(() => mocks.kindOptInSet.mock.calls.length > 0);
    setGate.resolve();
    // reload has started (second collectTombstones call) but not finished: the old snapshot is still (1), the button must stay disabled
    await flushUntil(() => mocks.collectTombstones.mock.calls.length === 2);
    reloadGate.resolve();
    await flushUntil(() => text().includes("待推送 0 项"));
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("discards a late load from the enabled state and never writes the disabled kind back into the pushable snapshot", async () => {
    optInState = { knownHost: true, aiProfile: false };
    const slowLoad = deferred<void>();
    mocks.collectKnownHosts.mockImplementation(async () => {
      await slowLoad.promise;
      return { knownHosts: [KNOWN_HOST_DTO], hasMore: false };
    });

    mounted = mountSyncCard();
    // The first load (enabled) hangs on collectKnownHosts; toggle off right after the toggles render (the load with opt-in off does not touch that mock and finishes first)
    await flushUntilOptInRendered();
    act(() => optInCheckbox(0).click());
    openDetails();
    await flushUntil(() => text().includes("本机与服务端都还没有可同步的内容。"));
    // The slow load finishes only now: the write-back must be discarded by the generation check, and the disabled kind must not appear in compare rows
    slowLoad.resolve();
    await flushUntil(() => text().includes("待推送 0 项"));
    expect(text()).not.toContain("10.0.0.9:22");
    expect(mocks.syncPush).not.toHaveBeenCalled();
  });

  it("keeps apply disabled during an on-to-off reload and no longer applies remote objects", async () => {
    optInState = { knownHost: true, aiProfile: false };
    const wire = await remoteFixtureWire("kh-1", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "kh-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("当前待应用 1 项"));

    const reloadGate = deferred<void>();
    mocks.collectTombstones.mockImplementationOnce(async () => {
      await reloadGate.promise;
      return { tombstones: [], hasMore: false };
    });
    act(() => optInCheckbox(0).click());
    await flush();
    // reload has started but not finished: the old applySet is still (1), the button must stay disabled
    await flushUntil(() => mocks.collectTombstones.mock.calls.length === 2);
    reloadGate.resolve();
    await flushUntil(() => text().includes("当前待应用 0 项"));
    expect(mocks.applyObjects).not.toHaveBeenCalled();
  });
});

describe("CompareConsole automatic sync", () => {
  function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((res) => {
      resolve = res;
    });
    return { promise, resolve };
  }

  it("does not push an empty local winner set in the same round that applies remote objects", async () => {
    optInState = { knownHost: true, aiProfile: false };
    const wire = await remoteFixtureWire("kh-1", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "kh-1", seq: 1, blob_hash: "h1" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });
    const applyGate = deferred<{ applied: number; identical: number; skipped: number; objects: [] }>();
    mocks.applyObjects.mockReturnValue(applyGate.promise);

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("当前待应用 1 项"));
    const syncPromise = runAutoSync(false);
    await flushUntil(() => mocks.applyObjects.mock.calls.length > 0);
    expect(mocks.syncPush).not.toHaveBeenCalled();

    applyGate.resolve({ applied: 1, identical: 0, skipped: 0, objects: [] });
    await syncPromise;
    expect(mocks.applyObjects).toHaveBeenCalledTimes(1);
  });

  it("does not apply or push before the snapshot has finished loading", async () => {
    optInState = { knownHost: true, aiProfile: false };
    const wire = await remoteFixtureWire("kh-2", "known_host", GO_SYNC_FIXTURES.knownHostBasic!, 1);
    mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [{ id: "kh-2", seq: 1, blob_hash: "h2" }], head: "head-1", max_seq: 1 });
    mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [wire], head: "head-1", max_seq: 1, next_seq: 1, cursor_done: true });
    mocks.collectKnownHosts.mockResolvedValue({ knownHosts: [], hasMore: false });

    mounted = mountSyncCard();
    await flushUntil(() => text().includes("当前待应用 1 项"));
    const refreshGate = deferred<void>();
    mocks.collectKnownHosts.mockImplementationOnce(async () => {
      await refreshGate.promise;
      return { knownHosts: [], hasMore: false };
    });
    const syncPromise = runAutoSync(false);
    await flushUntil(() => mocks.collectKnownHosts.mock.calls.length > 1);
    expect(mocks.applyObjects).not.toHaveBeenCalled();
    expect(mocks.syncPush).not.toHaveBeenCalled();

    refreshGate.resolve();
    await syncPromise;
    expect(mocks.applyObjects).toHaveBeenCalled();
  });
});
