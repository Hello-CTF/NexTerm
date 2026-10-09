/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    status: vi.fn(),
    me: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dekGet: vi.fn(),
    dekUpload: vi.fn(),
    devices: vi.fn(),
    totpStatus: vi.fn(),
    totpSetup: vi.fn(),
    totpConfirm: vi.fn(),
  };
});

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
      dekUpload: mocks.dekUpload,
      devices: mocks.devices,
      totpStatus: mocks.totpStatus,
      totpSetup: mocks.totpSetup,
      totpConfirm: mocks.totpConfirm,
    },
  };
});

// 避免真实 argon2id(生产参数一次数秒):信封生成/解包全部打桩,其余保持真实。
vi.mock("../features/auth/crypto", async (importOriginal) => {
  const original = await importOriginal<typeof import("../features/auth/crypto")>();
  return {
    ...original,
    generateDEK: vi.fn(() => new Uint8Array(32).fill(9)),
    wrapDEKWithPassword: vi.fn().mockResolvedValue({
      envelope: new Uint8Array(60).fill(1),
      salt: new Uint8Array(16).fill(2),
      params: '{"t":3,"m":65536,"p":4}',
    }),
    wrapDEKWithRecovery: vi.fn().mockResolvedValue(new Uint8Array(60).fill(3)),
    recoveryKeyHash: vi.fn().mockResolvedValue("hash-1"),
    unwrapDEKWithPassword: vi.fn().mockResolvedValue(new Uint8Array(32).fill(9)),
  };
});

import { AuthGate } from "../features/auth/AuthGate";
import { AuthCard } from "../features/settings/AuthCard";
import { useAuth } from "../features/auth/store";

const UNBOUND_USER = {
  id: "u-1",
  username: "bob",
  display_name: "",
  role: "user" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: false,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

const BOUND_USER = { ...UNBOUND_USER, mfa_enabled: true };

let mounted: MountedView | undefined;

function resetStore() {
  useAuth.setState({
    status: null,
    user: null,
    dek: null,
    gate: "loading",
    pendingRecoveryKey: null,
    pendingMfa: null,
    mfaEnrollmentPassword: null,
    error: null,
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  resetStore();
  mocks.status.mockResolvedValue({ initialized: true, registration_open: false, auth: "on" });
  mocks.me.mockRejectedValue({ code: "forbidden", message: "会话无效或缺失", status: 401 });
  mocks.login.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
  mocks.logout.mockResolvedValue({ ok: true });
  mocks.dekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.dekUpload.mockResolvedValue({ ok: true });
  mocks.devices.mockResolvedValue({ devices: [] });
  mocks.totpStatus.mockResolvedValue({ enabled: false, pending: false, mfa_required: false, recovery_codes_left: 0 });
  mocks.totpSetup.mockResolvedValue({ secret: "SECRETSECRETSECRETSECRETSECRETSEC", otpauth_uri: "otpauth://totp/NexTerm:bob?secret=SECRET&issuer=NexTerm" });
  mocks.totpConfirm.mockResolvedValue({ recovery_codes: ["AAAA-BBBB-CCCC-DDDD", "EEEE-FFFF-GGGG-HHHH"] });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetStore();
});

describe("mfa_required 强制绑定门(store)", () => {
  it("登录会话未绑定且 mfa_required: 进强制绑定门并暂存密码, 不拉 DEK", async () => {
    await useAuth.getState().login("bob", "pw-123456");
    const state = useAuth.getState();
    expect(state.gate).toBe("mfa_enroll");
    expect(state.mfaEnrollmentPassword).toBe("pw-123456");
    expect(state.user?.username).toBe("bob");
    expect(state.dek).toBeNull();
    expect(mocks.dekGet).not.toHaveBeenCalled();
  });

  it("绑定完成后 finishMfaEnrollment 用暂存密码解 DEK 进入应用", async () => {
    await useAuth.getState().login("bob", "pw-123456");
    mocks.me.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-2", mfa_required: true });
    await useAuth.getState().finishMfaEnrollment();
    const state = useAuth.getState();
    expect(state.gate).toBe("ready");
    expect(state.dek).not.toBeNull();
    expect(state.mfaEnrollmentPassword).toBeNull();
  });

  it("刷新恢复的门没有暂存密码: 绑定完成后直接进门, DEK 留待解锁", async () => {
    mocks.me.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    await useAuth.getState().refresh();
    expect(useAuth.getState().gate).toBe("mfa_enroll");
    expect(useAuth.getState().mfaEnrollmentPassword).toBeNull();

    mocks.me.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    await useAuth.getState().finishMfaEnrollment();
    const state = useAuth.getState();
    expect(state.gate).toBe("ready");
    expect(state.dek).toBeNull();
  });

  it("已绑定或未强制策略的会话不进强制绑定门", async () => {
    mocks.login.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    await useAuth.getState().login("bob", "pw-123456");
    expect(useAuth.getState().gate).toBe("ready");

    resetStore();
    mocks.login.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: false });
    await useAuth.getState().login("bob", "pw-123456");
    expect(useAuth.getState().gate).toBe("ready");
  });
});

describe("mfa_required 强制绑定门(AuthGate 表单)", () => {
  it("自动开始绑定: 确认动态码 → 展示恢复码 → 保存后进入应用", async () => {
    useAuth.setState({ gate: "mfa_enroll", user: UNBOUND_USER });
    // AuthGate 挂载时会 refresh(): me() 返回未绑定 + mfa_required 会话, 门停在强制绑定页。
    mocks.me.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    mocks.totpSetup.mockResolvedValue({ secret: "SECRETSECRETSECRETSECRETSECRETSEC", otpauth_uri: "otpauth://totp/NexTerm:bob?secret=SECRET&issuer=NexTerm" });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.querySelector('input[autocomplete="one-time-code"]') !== null);
    expect(mounted.container.textContent).toContain("必须启用两步验证");

    const input = mounted.container.querySelector<HTMLInputElement>('input[autocomplete="one-time-code"]')!;
    setInputValue(input, "123456");
    clickButton(mounted.container, "确认绑定");
    await flushUntil(() => mounted!.container.textContent?.includes("保存恢复码"));
    expect(mocks.totpConfirm).toHaveBeenCalledWith("123456");

    clickButton(mounted.container, "复制全部");
    await flush();
    mocks.me.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-2", mfa_required: true });
    clickButton(mounted.container, "我已安全保存,进入应用");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().mfaEnrollmentPassword).toBeNull();
  });

  it("setup 失败显示错误并可重试", async () => {
    useAuth.setState({ gate: "mfa_enroll", user: UNBOUND_USER });
    mocks.me.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    mocks.totpSetup.mockRejectedValueOnce({ code: "forbidden", message: "尝试过于频繁，请稍后再试" });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.textContent?.includes("尝试过于频繁"));

    mocks.totpSetup.mockResolvedValueOnce({ secret: "SECRETSECRETSECRETSECRETSECRETSEC", otpauth_uri: "otpauth://totp/NexTerm:bob?secret=SECRET&issuer=NexTerm" });
    clickButton(mounted.container, "重试");
    await flushUntil(() => mounted!.container.querySelector("input") !== null);
    expect(mounted.container.textContent).toContain("otpauth://totp/");
  });
});

describe("TotpCard 换绑", () => {
  function mountCard(): MountedView {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(createElement(QueryClientProvider, { client }, createElement(AuthCard)));
  }

  it("已绑定用户换绑必须提供当前凭据, setup 携带 reverify", async () => {
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: BOUND_USER,
      dek: null,
      gate: "ready",
      pendingRecoveryKey: null,
      pendingMfa: null,
      mfaEnrollmentPassword: null,
      error: null,
    });
    mocks.totpStatus.mockResolvedValue({ enabled: true, pending: false, mfa_required: false, recovery_codes_left: 8 });
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("已开启"));

    clickButton(mounted.container, "换绑认证器");
    await flush();
    const input = mounted.container.querySelector<HTMLInputElement>('input[placeholder="当前动态码、恢复码或登录密码"]')!;
    setInputValue(input, "current-password");
    clickButton(mounted.container, "验证并换绑");
    await flushUntil(() => mocks.totpSetup.mock.calls.length > 0);
    expect(mocks.totpSetup).toHaveBeenCalledWith("current-password");
    await flushUntil(
      () => mounted!.container.querySelector('input[placeholder="输入 6 位动态码完成绑定"]') !== null,
    );
  });
});

describe("mfa_enrollment_required 全局事件", () => {
  it("任意 API 收到 mfa_enrollment_required 时, 已登录未绑定会话切入强制绑定门", async () => {
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: UNBOUND_USER,
      dek: null,
      gate: "ready",
      pendingRecoveryKey: null,
      pendingMfa: null,
      mfaEnrollmentPassword: null,
      error: null,
    });
    const { request } = await import("../ipc/authApi");
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(
        async () =>
          new Response(JSON.stringify({ error: { code: "mfa_enrollment_required", message: "管理员已要求启用两步验证" } }), {
            status: 403,
            headers: { "Content-Type": "application/json" },
          }),
      ),
    );
    await expect(request("GET", "/auth/dek")).rejects.toMatchObject({ code: "mfa_enrollment_required" });
    await flushUntil(() => useAuth.getState().gate === "mfa_enroll");
    // 已绑定会话不误伤
    useAuth.setState({ gate: "ready", user: BOUND_USER });
    await expect(request("GET", "/auth/dek")).rejects.toMatchObject({ code: "mfa_enrollment_required" });
    expect(useAuth.getState().gate).toBe("ready");
    vi.unstubAllGlobals();
  });
});
