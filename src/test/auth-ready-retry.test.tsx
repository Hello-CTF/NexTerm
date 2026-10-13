/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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
  syncApi: { linkGet: vi.fn() },
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
      enroll: vi.fn(),
      init: vi.fn(),
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
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { useAuth } from "../features/auth/store";
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

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
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

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("auth=on 登录成功后的数据重取(真实 AssetTree)", () => {
  it("gate 进 ready 后资产树(含分组)/凭据/片段/凭据库状态全部恢复, toast 不做全清", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    // Before login: asset tree sits in the 401 error state, status bar vault unavailable
    await flushUntil(() => text().includes("加载失败："));
    expect(text()).toContain("访问令牌无效或缺失");
    expect(vaultStatusText()).toContain("凭据库不可用");
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
    expect(assetTreeFooterCount("命令片段（插入当前终端）")).toContain("1");
    expect(vaultStatusText()).toContain("凭据库已解锁");
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

  it("登出清空账号级缓存与会话/凭据库状态, 重新登录再次取到数据", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));

    authed = false;
    await logout();
    expect(useAuth.getState().gate).toBe("login");
    // Account-switch direction: no asset/session/vault state of the old account may linger
    await flushUntil(() => text().includes("加载失败："));
    expect(text()).not.toContain("web-01");
    expect(useUi.getState().sessions).toEqual([]);
    expect(vaultStatusText()).not.toContain("凭据库已解锁");

    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(text()).toContain("生产");
  });

  it("登出后迟到的旧账号会话列表与凭据库状态不得回写", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    expect(useUi.getState().sessions.map((s) => s.id)).toContain("s-1");
    expect(vaultStatusText()).toContain("凭据库已解锁");

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
      staleVault.resolve({ initialized: true, unlocked: true });
      await flush();
    });
    expect(useUi.getState().sessions).toEqual([]);
    expect(vaultStatusText()).not.toContain("凭据库已解锁");
  });

  it("登录前发出的在途请求迟到后不得污染新会话(资产查询与凭据库状态)", async () => {
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
    expect(vaultStatusText()).not.toContain("凭据库不可用");

    authed = true;
    await login();
    await flushUntil(() => text().includes("web-01"));
    await flushUntil(() => vaultStatusText().includes("凭据库已解锁"));

    // The late pre-login responses arrive only now: query results are discarded, the status bar is not overwritten by the old 401
    await act(async () => {
      staleAssets.reject(UNAUTH);
      staleVault.reject(UNAUTH);
      await flush();
    });
    expect(text()).toContain("web-01");
    expect(vaultStatusText()).toContain("凭据库已解锁");
  });
});

describe("toast 生命周期", () => {
  it("error 8s / info 3.5s 自动消失, 无需上下文清理兜底", async () => {
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
