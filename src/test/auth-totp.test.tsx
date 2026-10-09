/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, flushUntil, mount, setInputValue, clickButton, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    status: vi.fn(),
    me: vi.fn(),
    login: vi.fn(),
    totpLogin: vi.fn(),
    logout: vi.fn(),
    dekGet: vi.fn(),
    dekUpload: vi.fn(),
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
      totpLogin: mocks.totpLogin,
      logout: mocks.logout,
      dekGet: mocks.dekGet,
      dekUpload: mocks.dekUpload,
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
import { useAuth } from "../features/auth/store";
import { getCsrfToken } from "../ipc/authApi";
import { demoAuthRequest } from "../demo/auth";

const ADMIN = {
  id: "u-admin",
  username: "root",
  display_name: "",
  role: "superadmin" as const,
  state: "active" as const,
  must_change_password: false,
  mfa_enabled: true,
  created_at: 1,
  updated_at: 1,
  last_login_at: 1,
};

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
  mocks.login.mockResolvedValue({ mfa_required: true, ticket: "ticket-1", expires_at: 1 });
  mocks.totpLogin.mockResolvedValue({ user: ADMIN, csrf_token: "csrf-mfa" });
  mocks.logout.mockResolvedValue({ ok: true });
  mocks.dekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.dekUpload.mockResolvedValue({ ok: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetStore();
});

describe("TOTP 登录第二步(store)", () => {
  it("密码通过后拿到 MFA 挑战: 挂起票据而不是建立会话", async () => {
    useAuth.setState({ gate: "login" });
    await useAuth.getState().login("root", "pw-123456");
    const state = useAuth.getState();
    expect(state.pendingMfa).toMatchObject({ ticket: "ticket-1", username: "root", password: "pw-123456" });
    expect(state.user).toBeNull();
    expect(state.dek).toBeNull();
    expect(state.gate).toBe("login");
    expect(getCsrfToken()).toBeNull();
  });

  it("verifyMfa 成功后完成登录: 记 CSRF、解 DEK、进门", async () => {
    await useAuth.getState().login("root", "pw-123456");
    await useAuth.getState().verifyMfa("123456");
    const state = useAuth.getState();
    expect(mocks.totpLogin).toHaveBeenCalledWith("ticket-1", "123456");
    expect(state.pendingMfa).toBeNull();
    expect(state.user?.username).toBe("root");
    expect(state.dek).not.toBeNull();
    expect(state.gate).toBe("ready");
    expect(getCsrfToken()).toBe("csrf-mfa");
  });

  it("verifyMfa 失败保留票据并记错误, cancelMfa 清除", async () => {
    mocks.totpLogin.mockRejectedValue({ code: "forbidden", message: "验证码错误" });
    await useAuth.getState().login("root", "pw-123456");
    await expect(useAuth.getState().verifyMfa("000000")).rejects.toMatchObject({ code: "forbidden" });
    expect(useAuth.getState().pendingMfa?.ticket).toBe("ticket-1");
    expect(useAuth.getState().error?.message).toBe("验证码错误");

    useAuth.getState().cancelMfa();
    expect(useAuth.getState().pendingMfa).toBeNull();
    expect(useAuth.getState().error).toBeNull();
  });
});

describe("AuthGate TOTP 挑战表单", () => {
  it("pendingMfa 时渲染两步验证表单,提交走 verifyMfa", async () => {
    useAuth.setState({ gate: "login", pendingMfa: { ticket: "ticket-1", username: "root", password: "pw" } });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.textContent?.includes("两步验证"));

    const input = mounted.container.querySelector("input")!;
    setInputValue(input, "123456");
    clickButton(mounted.container, "验证并登录");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(mocks.totpLogin).toHaveBeenCalledWith("ticket-1", "123456");
    expect(useAuth.getState().pendingMfa).toBeNull();
  });

  it("返回按钮取消 MFA 挑战,回到密码登录", async () => {
    useAuth.setState({ gate: "login", pendingMfa: { ticket: "ticket-1", username: "root", password: "pw" } });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.textContent?.includes("两步验证"));

    clickButton(mounted.container, "返回重新登录");
    await flush();
    expect(useAuth.getState().pendingMfa).toBeNull();
    await flushUntil(() => mounted!.container.querySelectorAll("input").length >= 2);
  });
});

describe("demo 假后端 TOTP 契约", () => {
  it("开启后登录走 MFA 挑战, 动态码/恢复码完成第二步", async () => {
    const call = <T,>(method: string, path: string, body?: unknown) => demoAuthRequest<T>(method, path, body);
    const setup = await call<{ secret: string; otpauth_uri: string }>("POST", "/auth/totp/setup", {});
    expect(setup.secret).toHaveLength(32);
    expect(setup.otpauth_uri).toContain("otpauth://totp/");

    const confirm = await call<{ recovery_codes: string[] }>("POST", "/auth/totp/confirm", { code: "123456" });
    expect(confirm.recovery_codes).toHaveLength(8);

    const status = await call<{ enabled: boolean; recovery_codes_left: number }>("GET", "/auth/totp");
    expect(status.enabled).toBe(true);
    expect(status.recovery_codes_left).toBe(8);

    const challenge = await call<{ mfa_required: boolean; ticket: string }>("POST", "/auth/login", {
      username: "demo",
      password: "任意密码",
    });
    expect(challenge.mfa_required).toBe(true);

    const bad = await call("POST", "/auth/totp/login", { ticket: challenge.ticket, code: "abc" }).catch((e) => e);
    expect(bad).toMatchObject({ code: "forbidden" });

    const session = await call<{ user: { mfa_enabled: boolean }; csrf_token: string }>("POST", "/auth/totp/login", {
      ticket: challenge.ticket,
      code: confirm.recovery_codes[0],
    });
    expect(session.user.mfa_enabled).toBe(true);

    // 恢复码一次性: 再次挑战时同一枚恢复码不再可用
    const challenge2 = await call<{ ticket: string }>("POST", "/auth/login", { username: "demo", password: "x" });
    const reuse = await call("POST", "/auth/totp/login", {
      ticket: challenge2.ticket,
      code: confirm.recovery_codes[0],
    }).catch((e) => e);
    expect(reuse).toMatchObject({ code: "forbidden" });
    await call("POST", "/auth/totp/login", { ticket: challenge2.ticket, code: "654321" });

    // 关闭后回到密码直登
    await call("DELETE", "/auth/totp", { code: "654321" });
    const direct = await call<{ csrf_token: string }>("POST", "/auth/login", { username: "demo", password: "x" });
    expect(direct.csrf_token).toBeTruthy();
  });
});
