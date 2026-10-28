/** @vitest-environment jsdom */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

// M209 real auth=on browser evidence: once the gated app mounts it fires /rpc (no cookie) and gets 401 across the board,
// and after login the asset tree / credential vault stay in the pre-login error state. Uses a real AssetTree
// to pin the refetch after the gate transition (groups / credentials / snippets) and in-flight race isolation; toasts are not all cleared on ready.
const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    assetList: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
    listCredentials: vi.fn(),
    sessionList: vi.fn(),
    vaultStatus: vi.fn(),
    status: vi.fn(),
    me: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dekGet: vi.fn(),
    transcriptHosts: vi.fn(),
    transcriptList: vi.fn(),
    transcriptRead: vi.fn(),
    collectAssets: vi.fn(),
    collectTombstones: vi.fn(),
    collectCredentials: vi.fn(),
    kindOptInGet: vi.fn(),
    applyObjects: vi.fn(),
    syncIds: vi.fn(),
    syncPull: vi.fn(),
    syncPush: vi.fn(),
    onRemoteChange: vi.fn(),
    advanceLayoutAccount: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    search: vi.fn(),
    groupList: mocks.groupList,
    snippetList: mocks.snippetList,
  },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: mocks.listCredentials },
  transcriptApi: {
    hosts: mocks.transcriptHosts,
    list: mocks.transcriptList,
    read: mocks.transcriptRead,
  },
  syncApi: {
    linkGet: vi.fn(),
    collectAssets: mocks.collectAssets,
    collectTombstones: mocks.collectTombstones,
    collectCredentials: mocks.collectCredentials,
    kindOptInGet: mocks.kindOptInGet,
    applyObjects: mocks.applyObjects,
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: mocks.status,
      me: mocks.me,
      login: mocks.login,
      logout: mocks.logout,
      dekGet: mocks.dekGet,
      dekUpload: vi.fn(),
      changePassword: vi.fn(),
      recoveryReset: vi.fn(),
      init: vi.fn(),
    },
    syncV2Api: {
      ids: mocks.syncIds,
      pull: mocks.syncPull,
      push: mocks.syncPush,
    },
  };
});

// Avoid real argon2id (production params take seconds per run): stub DEK unwrap, keep everything else real.
vi.mock("../features/auth/crypto", async (importOriginal) => {
  const original = await importOriginal<typeof import("../features/auth/crypto")>();
  return {
    ...original,
    unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(9)),
  };
});

vi.mock("../app/layout", () => ({
  layoutBootstrapped: Promise.resolve(),
  startLayoutSync: vi.fn(),
  onRemoteChange: mocks.onRemoteChange,
  advanceLayoutAccount: mocks.advanceLayoutAccount,
  layoutRestoreStatus: () => ({ pending: false, preview: false, count: 0 }),
  subscribeLayoutRestore: () => () => {},
  reviewPendingLayoutRestore: vi.fn(),
  finishLayoutRestore: vi.fn(),
  undoLayoutRestore: vi.fn(),
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { useAuth } from "../features/auth/store";
import { SESSION_IDENTITY_CHANGED_EVENT } from "../ipc/authApi";
import type { Asset } from "../ipc/commands";

const ADMIN = {
  id: "u-admin",
  username: "root",
  display_name: "",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};
const GROUP = { id: "g1", parentId: null, name: "生产", sort: 0, createdAt: 1, updatedAt: 1 };
const ASSET: Asset = {
  id: "a-1",
  groupId: "g1",
  name: "web-01",
  kind: "ssh",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "password",
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};
const CRED = { id: "c-1", name: "db-prod", kind: "password", usedBy: [], createdAt: 1, updatedAt: 1 };
const SNIPPET = { id: "sn-1", name: "uptime", command: "uptime", createdAt: 1, updatedAt: 1 };
const SESSION = { id: "s-1", name: "web-01", kind: "ssh", status: "connected", assetId: "a-1", tabs: [], createdAt: 0 };
const UNAUTH = { code: "unauthorized", message: "访问令牌无效或缺失", status: 401 };

let authed = false;

let mounted: MountedView | undefined;

function mountApp(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })): MountedView {
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(App),
    ),
  );
}

function text(): string {
  return document.body.textContent ?? "";
}

function vaultStatusText(): string {
  const info = document.querySelector<HTMLElement>(".nx-statusbar-info");
  for (const el of info?.querySelectorAll<HTMLElement>("span, button") ?? []) {
    if (el.textContent?.includes("凭据库")) return el.textContent;
  }
  return "";
}

function assetTreeFooterCount(title: string): string {
  const btn = [...document.querySelectorAll<HTMLElement>("button")].find(
    (b) => b.getAttribute("title") === title,
  );
  return btn?.textContent ?? "";
}

async function login(): Promise<void> {
  await act(async () => {
    await useAuth.getState().login("root", "pw-123456");
  });
}

async function logout(): Promise<void> {
  await act(async () => {
    await useAuth.getState().logout();
  });
}

// The account gate and the asset tree load through dynamic imports. A cold import transforms the module
// in real time, which no tick budget bounds under machine load, so warm them before flushUntil counts ticks.
beforeAll(async () => {
  await Promise.all([import("../features/auth/AuthGate"), import("../features/explorer/AssetTree")]);
});

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  authed = false;
  mocks.status.mockResolvedValue({ initialized: true, registration_open: false, auth: "on" });
  // me follows session reality: after login (including the late refresh triggered by AuthGate mounting) /auth/me must be valid,
  // otherwise the late 401 knocks the gate back to login and masks the behavior under test.
  mocks.me.mockImplementation(() =>
    authed
      ? Promise.resolve({ user: ADMIN, csrf_token: "csrf-1" })
      : Promise.reject({ code: "forbidden", message: "会话无效或缺失", status: 401 }),
  );
  mocks.login.mockResolvedValue({ user: ADMIN, csrf_token: "csrf-1" });
  mocks.logout.mockResolvedValue({ ok: true });
  mocks.dekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.transcriptHosts.mockResolvedValue([]);
  mocks.transcriptList.mockResolvedValue([]);
  mocks.transcriptRead.mockResolvedValue({ chunks: [], nextSeq: 0, done: true, totalBytes: 0 });
  mocks.collectAssets.mockResolvedValue({ assets: [], hasMore: false });
  mocks.collectTombstones.mockResolvedValue({ tombstones: [], hasMore: false });
  mocks.collectCredentials.mockResolvedValue({ credentials: [], hasMore: false });
  mocks.kindOptInGet.mockResolvedValue({ knownHost: false, aiProfile: false, aiPermission: false, aiGrant: false, preferences: false, stateRules: false, runbookTemplates: false, skills: false, memory: false, cron: false });
  mocks.applyObjects.mockResolvedValue({ applied: 0, identical: 0, skipped: 0, objects: [] });
  mocks.syncIds.mockResolvedValue({ protocol: 2, entries: [], head: "head-0", max_seq: 0 });
  mocks.syncPull.mockResolvedValue({ protocol: 2, objects: [], head: "head-0", max_seq: 0, next_seq: 0, cursor_done: true });
  mocks.syncPush.mockResolvedValue({ protocol: 2, head: "head-1", max_seq: 2, applied: 2, skipped: 0 });
  mocks.assetList.mockImplementation(() => (authed ? Promise.resolve([ASSET]) : Promise.reject(UNAUTH)));
  mocks.groupList.mockImplementation(() => (authed ? Promise.resolve([GROUP]) : Promise.reject(UNAUTH)));
  mocks.snippetList.mockImplementation(() => (authed ? Promise.resolve([SNIPPET]) : Promise.reject(UNAUTH)));
  mocks.listCredentials.mockImplementation(() => (authed ? Promise.resolve([CRED]) : Promise.reject(UNAUTH)));
  mocks.sessionList.mockImplementation(() => (authed ? Promise.resolve([SESSION]) : Promise.reject(UNAUTH)));
  mocks.vaultStatus.mockImplementation(() =>
    authed ? Promise.resolve({ initialized: true, unlocked: true }) : Promise.reject(UNAUTH),
  );
  useUi.setState({
    toasts: [],
    leftOpen: true,
    rightOpen: false,
    leftMode: "assets",
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
    aiBoards: {},
    aiAccountEpoch: 0,
  });
  useAuth.setState({
    status: null,
    user: null,
    dek: null,
    gate: "loading",
    pendingRecoveryKey: null,
    error: null,
  });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
});

describe("data refetch after successful auth=on login (real AssetTree)", () => {
  it("restores the asset tree, groups, credentials, snippets, and vault state after the gate becomes ready without clearing every toast", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    // Before login: asset tree sits in the 401 error state, status bar vault unavailable
    await flushUntil(() => text().includes("资产列表加载失败"));
    expect(text()).toContain("访问令牌无效或缺失");
    expect(vaultStatusText()).toContain("凭据库状态未知");
    expect(text()).not.toContain("web-01");

    // Toasts left over from before login: one auth-context 401, one non-401 layout error, one non-auth terminal error
    act(() => {
      useUi.getState().pushToast("error", "布局同步失败：访问令牌无效或缺失");
      useUi.getState().pushToast("error", "布局同步失败：daemon 未响应");
      useUi.getState().pushToast("error", "会话失败：connection reset");
      useUi.getState().pushToast("info", "布局已在其他设备上更新，已刷新为最新版本");
    });

    authed = true;
    await login();
    expect(useAuth.getState().gate).toBe("ready");

    // Groups and assets inside groups all come back after login (guard against group assets being hidden), credential/snippet counts recover
    await flushUntil(() => text().includes("web-01"));
    expect(text()).toContain("生产");
    expect(assetTreeFooterCount("凭据库（左栏查看）")).toBe("");
    expect(assetTreeFooterCount("打开命令片段：保存常用命令，插入或在当前终端执行")).toContain("1");
    expect(vaultStatusText()).toBe("");
    expect(useUi.getState().sessions.map((s) => s.id)).toContain("s-1");

    // Toasts are not all cleared on ready: non-auth errors and non-401 layout errors remain valid feedback after login, all kept
    const toasts = useUi.getState().toasts;
    expect(toasts.filter((t) => t.kind === "error").map((t) => t.text)).toEqual([
      "布局同步失败：访问令牌无效或缺失",
      "布局同步失败：daemon 未响应",
      "会话失败：connection reset",
    ]);
    expect(toasts.some((t) => t.kind === "info")).toBe(true);
  });

  it("clears account-scoped caches, SQL runtime, and session/vault state on logout and refetches after another login", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    act(() => {
      useUi.setState({
        workspaces: [
          {
            id: "ws-account",
            kind: "session",
            title: "account workspace",
            panes: [{ id: "pane-account", tabs: [], activeTabId: null }],
            activePaneId: "pane-account",
            closable: true,
          },
        ],
        activeWorkspaceId: "ws-account",
        workspaceRuntime: {},
      });
      useUi.getState().updateSqlDraft("ws-account", {
        text: "SELECT old_account_secret;",
        selection: { from: 0, to: 0 },
        schema: "public",
        connId: "conn-old",
        dbKind: "postgres",
      });
    });
    expect(useUi.getState().getWorkspaceRuntime("ws-account")?.sqlDraft?.text).toBe(
      "SELECT old_account_secret;",
    );

    authed = false;
    await logout();
    expect(useAuth.getState().gate).toBe("login");
    // Account-switch direction: no asset/session/vault/SQL state of the old account may linger
    await flushUntil(() => text().includes("资产列表加载失败"));
    expect(text()).not.toContain("web-01");
    expect(useUi.getState().sessions).toEqual([]);
    expect(vaultStatusText()).not.toContain("凭据库已锁定");
    expect(useUi.getState().getWorkspaceRuntime("ws-account")?.sqlDraft).toBeUndefined();

    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(text()).toContain("生产");
    expect(useUi.getState().getWorkspaceRuntime("ws-account")?.sqlDraft).toBeUndefined();
  });

  it("delegates layout account generation through two-stage login and ready identity changes", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(mocks.advanceLayoutAccount).toHaveBeenCalledTimes(1);
    expect(mocks.onRemoteChange).toHaveBeenCalledTimes(1);
    expect(mocks.advanceLayoutAccount.mock.invocationCallOrder[0]).toBeLessThan(
      mocks.onRemoteChange.mock.invocationCallOrder[0],
    );

    act(() => {
      useAuth.setState({ user: { ...ADMIN, id: "u-b", username: "account-b" } });
    });
    await flush();
    expect(mocks.advanceLayoutAccount).toHaveBeenCalledTimes(2);
    expect(mocks.onRemoteChange).toHaveBeenCalledTimes(2);
  });

  it("clears account-scoped query caches when the user changes without leaving the ready gate", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mountApp(client);
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    client.setQueryData(["forwards"], [{ id: "old-account-forward" }]);
    client.setQueryData(["forwardEnv"], { available: true, listenHost: "old-account" });
    client.setQueryData(["exec-batch-history", "account-a", 0], { items: [{ id: "old-account-batch" }] });
    client.setQueryData(["exec-batch-history-detail", "account-a", "shared-id", "self"], { batch: { id: "shared-id" } });

    act(() => useAuth.setState({ user: { ...ADMIN, id: "u-b", username: "account-b" } }));
    await flush();
    expect(client.getQueryData(["forwards"])).toBeUndefined();
    expect(client.getQueryData(["forwardEnv"])).toBeUndefined();
    expect(client.getQueryData(["exec-batch-history", "account-a", 0])).toBeUndefined();
    expect(client.getQueryData(["exec-batch-history-detail", "account-a", "shared-id", "self"])).toBeUndefined();
  });

  it("synchronizes a cross-tab session identity response into the auth store", async () => {
    useAuth.setState({ user: { ...ADMIN, id: "u-a", username: "account-a" }, gate: "ready", dek: new Uint8Array([1, 2, 3]) });
    const next = { ...ADMIN, id: "u-b", username: "account-b" };
    act(() => {
      window.dispatchEvent(new CustomEvent(SESSION_IDENTITY_CHANGED_EVENT, {
        detail: { user: next, csrf_token: "csrf-b", mfa_required: false },
      }));
    });
    expect(useAuth.getState().user?.id).toBe("u-b");
    expect(useAuth.getState().dek).toBeNull();
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("clears AI transient state on logout and direct user identity changes", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    const seedDraft = (draft: string) => {
      act(() => {
        useUi.setState({
          aiBoards: {
            account: {
              tabs: [{ id: "ai-shared", title: "History", conversationId: "conv-history", draft }],
              activeTabId: "ai-shared",
            },
          },
          aiBusy: true,
          takeover: { tabId: "old-tab", token: "old-token", task: "old task", allowWrite: true, startedAt: 1 },
        });
      });
    };
    mocks.onRemoteChange.mockImplementation(() => {
      useUi.setState({
        aiBoards: {
          account: {
            tabs: [{ id: "ai-shared", title: "History", conversationId: "conv-history" }],
            activeTabId: "ai-shared",
          },
        },
      });
    });

    seedDraft("account A secret");
    const firstEpoch = useUi.getState().aiAccountEpoch;
    authed = false;
    await logout();
    expect(useUi.getState().aiBoards).toEqual({});
    expect(useUi.getState().aiAccountEpoch).toBe(firstEpoch + 1);
    expect(useUi.getState().aiBusy).toBe(false);
    expect(useUi.getState().takeover).toBeNull();

    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(mocks.onRemoteChange).toHaveBeenCalled();
    expect(useUi.getState().aiBoards.account.tabs[0].conversationId).toBe("conv-history");
    expect(useUi.getState().aiBoards.account.tabs[0].draft).toBeUndefined();

    seedDraft("account B secret");
    const secondEpoch = useUi.getState().aiAccountEpoch;
    act(() => {
      useAuth.setState({ user: { ...ADMIN, id: "u-b", username: "account-b" } });
    });
    await flush();
    expect(useUi.getState().aiAccountEpoch).toBe(secondEpoch + 1);
    expect(useUi.getState().aiBoards.account.tabs[0].conversationId).toBe("conv-history");
    expect(useUi.getState().aiBoards.account.tabs[0].draft).toBeUndefined();
  });

  it("does not write back late session-list or vault responses from the old account after logout", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(useUi.getState().sessions.map((s) => s.id)).toContain("s-1");
    expect(vaultStatusText()).toBe("");

    // The old account's slow request goes out via the header refresh button and stays in flight, then logout happens
    const staleSessions = deferred<(typeof SESSION)[]>();
    const staleVault = deferred<{ initialized: boolean; unlocked: boolean }>();
    mocks.sessionList.mockImplementation(() => staleSessions.promise);
    // Only the one request fired by the refresh button stays in flight; the refetch triggered by resetQueries after logout
    // belongs to the new session - the real server would answer 401, so it must not hang on the old account's response either.
    let staleVaultTaken = false;
    mocks.vaultStatus.mockImplementation(() => {
      if (!staleVaultTaken) {
        staleVaultTaken = true;
        return staleVault.promise;
      }
      return authed ? Promise.resolve({ initialized: true, unlocked: true }) : Promise.reject(UNAUTH);
    });
    act(() => {
      void useUi.getState().resyncSessions();
      void mocks.vaultStatus();
    });
    await flush();

    authed = false;
    await logout();
    expect(useAuth.getState().gate).toBe("login");
    expect(useUi.getState().sessions).toEqual([]);

    // The late old-account responses arrive only now: the session list seq has already advanced, and the in-flight
    // vault-status fetch was cancelled by resetQueries; both write-backs must be discarded
    await act(async () => {
      staleSessions.resolve([SESSION]);
      staleVault.resolve({ initialized: true, unlocked: false });
      await flush();
    });
    expect(useUi.getState().sessions).toEqual([]);
    expect(vaultStatusText()).not.toContain("凭据库已锁定");
  });

  it("does not let in-flight pre-login requests pollute the new session (asset queries and vault state)", async () => {
    const staleAssets = deferred<never>();
    const staleVault = deferred<never>();
    mocks.assetList.mockImplementation(() => (authed ? Promise.resolve([ASSET]) : staleAssets.promise));
    mocks.vaultStatus.mockImplementation(() =>
      authed ? Promise.resolve({ initialized: true, unlocked: true }) : staleVault.promise,
    );

    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    // First-screen asset query and vault status request still in flight (slow 401): the tree is still on the loading skeleton, the status bar still shows the placeholder
    expect(text()).not.toContain("web-01");
    expect(
      [...document.querySelectorAll('[role="alert"]')].some((el) =>
        el.textContent?.includes("加载失败"),
      ),
    ).toBe(false);
    expect(vaultStatusText()).not.toContain("凭据库状态未知");

    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    await flush();

    // The late pre-login responses arrive only now: query results are discarded, the status bar is not overwritten by the old 401
    await act(async () => {
      staleAssets.reject(UNAUTH);
      staleVault.reject(UNAUTH);
      await flush();
    });
    expect(text()).toContain("web-01");
    expect(vaultStatusText()).toBe("");
  });
});

describe("toast lifecycle", () => {
  it("expires errors after 8s and info toasts after 3.5s without context-cleanup fallbacks", async () => {
    vi.useFakeTimers();
    try {
      useUi.getState().pushToast("error", "布局同步失败：访问令牌无效或缺失");
      useUi.getState().pushToast("info", "布局已在其他设备上更新");
      await vi.advanceTimersByTimeAsync(3500);
      expect(useUi.getState().toasts.map((t) => t.kind)).toEqual(["error"]);
      await vi.advanceTimersByTimeAsync(4500);
      expect(useUi.getState().toasts).toEqual([]);
    } finally {
      vi.useRealTimers();
    }
  });
});
