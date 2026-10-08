/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { deferred, flush, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

// M209 真实 auth=on 浏览器证据: 门后应用 mounted 即发 /rpc(无 cookie)全部 401,
// 登录成功后资产树/凭据库停在登录前错误态。这里钉住 gate 转换后的重取与竞态隔离。
const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    assetList: vi.fn(),
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
  assetApi: { list: mocks.assetList, search: vi.fn() },
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

// 避免真实 argon2id(生产参数一次数秒): DEK 解包打桩, 其余保持真实。
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
vi.mock("../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { useAuth } from "../features/auth/store";

const ADMIN = {
  id: "u-admin",
  username: "root",
  display_name: "",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};
const ASSET = { id: "a-1", name: "web-01", kind: "ssh" };
const CRED = { id: "c-1", name: "db-prod", kind: "password", usedBy: [], createdAt: 1, updatedAt: 1 };
const SESSION = { id: "s-1", name: "web-01", kind: "ssh", status: "connected", assetId: "a-1" };
const UNAUTH = { code: "unauthorized", message: "访问令牌无效或缺失", status: 401 };

let authed = false;

function AssetsProbe() {
  const q = useQuery({ queryKey: ["assets"], queryFn: () => mocks.assetList() });
  return createElement(
    "div",
    { "data-testid": "assets-probe", "data-state": q.isError ? "error" : q.isSuccess ? "ready" : "pending" },
    String((q.data ?? []).length),
  );
}

function CredentialsProbe() {
  const q = useQuery({ queryKey: ["credentials"], queryFn: () => mocks.listCredentials() });
  return createElement(
    "div",
    { "data-testid": "creds-probe", "data-state": q.isError ? "error" : q.isSuccess ? "ready" : "pending" },
    String((q.data ?? []).length),
  );
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(App),
      createElement(AssetsProbe),
      createElement(CredentialsProbe),
    ),
  );
}

function probe(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
}

function vaultStatusText(): string {
  const info = document.querySelector<HTMLElement>(".nx-statusbar-info");
  for (const el of info?.querySelectorAll<HTMLElement>("span, button") ?? []) {
    if (el.textContent?.includes("凭据库")) return el.textContent;
  }
  return "";
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
  // me 按会话真实性返回: 登录成功后(含 AuthGate 挂载触发的迟到 refresh)/auth/me 必须有效,
  // 否则迟到的 401 会把 gate 打回 login, 掩盖被测行为。
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

describe("auth=on 登录成功后的数据重取", () => {
  it("gate 进 ready 后重取资产/凭据/会话/凭据库状态, 清掉登录前错误 toast", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    await flushUntil(() => probe("assets-probe")?.dataset.state === "error");
    expect(probe("creds-probe")?.dataset.state).toBe("error");
    expect(vaultStatusText()).toContain("凭据库不可用");

    // 登录前的 401 噪音: 布局同步失败类错误 toast + 一条仍相关的 info toast
    act(() => {
      useUi.getState().pushToast("error", "布局同步失败：访问令牌无效或缺失");
      useUi.getState().pushToast("info", "布局已在其他设备上更新，已刷新为最新版本");
    });

    authed = true;
    await login();
    expect(useAuth.getState().gate).toBe("ready");

    await flushUntil(() => probe("assets-probe")?.dataset.state === "ready");
    await flushUntil(() => probe("creds-probe")?.dataset.state === "ready");
    expect(probe("assets-probe")?.textContent).toBe("1");
    expect(probe("creds-probe")?.textContent).toBe("1");
    expect(mocks.assetList.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(vaultStatusText()).toContain("凭据库已解锁");
    expect(useUi.getState().sessions.map((s) => s.id)).toContain("s-1");
    const toasts = useUi.getState().toasts;
    expect(toasts.some((t) => t.kind === "error")).toBe(false);
    expect(toasts.some((t) => t.kind === "info")).toBe(true);
  });

  it("登出清空资产/凭据缓存, 重新登录再次取到数据", async () => {
    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    authed = true;
    await login();
    await flushUntil(() => probe("assets-probe")?.dataset.state === "ready");

    authed = false;
    await logout();
    expect(useAuth.getState().gate).toBe("login");
    await flushUntil(() => probe("assets-probe")?.dataset.state === "error");
    expect(probe("assets-probe")?.textContent).toBe("0");

    authed = true;
    await login();
    await flushUntil(() => probe("assets-probe")?.dataset.state === "ready");
    expect(probe("assets-probe")?.textContent).toBe("1");
  });

  it("登录前发出的在途请求迟到后不得污染新会话(查询与凭据库状态)", async () => {
    const staleAssets = deferred<never>();
    const staleVault = deferred<never>();
    mocks.assetList.mockImplementation(() => (authed ? Promise.resolve([ASSET]) : staleAssets.promise));
    mocks.vaultStatus.mockImplementation(() =>
      authed ? Promise.resolve({ initialized: true, unlocked: true }) : staleVault.promise,
    );

    mounted = mountApp();
    await flushUntil(() => useAuth.getState().gate === "login");
    // 首屏资产查询与凭据库状态请求仍在途(慢 401)
    expect(probe("assets-probe")?.dataset.state).toBe("pending");

    authed = true;
    await login();
    await flushUntil(() => probe("assets-probe")?.dataset.state === "ready");
    await flushUntil(() => vaultStatusText().includes("凭据库已解锁"));

    // 迟到的登录前响应现在才到: 查询结果被丢弃, 状态栏不被旧 401 覆盖
    await act(async () => {
      staleAssets.reject(UNAUTH);
      staleVault.reject(UNAUTH);
      await flush();
    });
    expect(probe("assets-probe")?.dataset.state).toBe("ready");
    expect(probe("assets-probe")?.textContent).toBe("1");
    expect(vaultStatusText()).toContain("凭据库已解锁");
  });
});
