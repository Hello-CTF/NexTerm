/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

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
    writeText: vi.fn(),
    toast: vi.fn(),
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

// Avoid real argon2id (production params take seconds per run): stub all envelope generation/unwrapping, keep the rest real.
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
  mocks.writeText.mockResolvedValue(undefined);
  Object.assign(navigator, { clipboard: { writeText: mocks.writeText } });
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

describe("mfa_required forced-enrollment gate (store)", () => {
  it("enters forced enrollment and stashes the password for an unbound mfa_required login without fetching the DEK", async () => {
    await useAuth.getState().login("bob", "pw-123456");
    const state = useAuth.getState();
    expect(state.gate).toBe("mfa_enroll");
    expect(state.mfaEnrollmentPassword).toBe("pw-123456");
    expect(state.user?.username).toBe("bob");
    expect(state.dek).toBeNull();
    expect(mocks.dekGet).not.toHaveBeenCalled();
  });

  it("unwraps the DEK with the stashed password and enters the app after finishMfaEnrollment", async () => {
    await useAuth.getState().login("bob", "pw-123456");
    mocks.me.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-2", mfa_required: true });
    await useAuth.getState().finishMfaEnrollment();
    const state = useAuth.getState();
    expect(state.gate).toBe("ready");
    expect(state.dek).not.toBeNull();
    expect(state.mfaEnrollmentPassword).toBeNull();
  });

  it("enters the app directly after enrollment when a refreshed gate has no stashed password and leaves DEK unlocking for later", async () => {
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

  it("does not enter forced enrollment for already-bound sessions or sessions without an enforced policy", async () => {
    mocks.login.mockResolvedValue({ user: BOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    await useAuth.getState().login("bob", "pw-123456");
    expect(useAuth.getState().gate).toBe("ready");

    resetStore();
    mocks.login.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: false });
    await useAuth.getState().login("bob", "pw-123456");
    expect(useAuth.getState().gate).toBe("ready");
  });
});

describe("mfa_required forced-enrollment gate (AuthGate form)", () => {
  it("starts enrollment automatically, confirms the code, shows recovery codes, and enters the app after saving", async () => {
    useAuth.setState({ gate: "mfa_enroll", user: UNBOUND_USER });
    // AuthGate runs refresh() on mount: me() returns an unbound + mfa_required session, so the gate stays on the forced-binding page.
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
    clickButton(mounted.container, "已保存，进入应用");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().mfaEnrollmentPassword).toBeNull();
  });

  it("keeps a manual-copy path when recovery-code copying fails and still lets the user enter after confirmation", async () => {
    useAuth.setState({ gate: "mfa_enroll", user: UNBOUND_USER });
    mocks.me.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.querySelector('input[autocomplete="one-time-code"]') !== null);
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[autocomplete="one-time-code"]')!, "123456");
    clickButton(mounted.container, "确认绑定");
    await flushUntil(() => mounted!.container.textContent?.includes("保存恢复码"));

    mocks.writeText.mockRejectedValue(new Error("denied"));
    clickButton(mounted.container, "复制全部");
    await flushUntil(() => mounted!.container.textContent?.includes("复制失败，请手动复制恢复码"));
    const doneBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入应用")) as HTMLButtonElement;
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制全部")) as HTMLButtonElement;
    expect(doneBtn.disabled).toBe(false);
    expect(copyBtn.textContent).not.toContain("已复制");
    expect(mounted.container.textContent).toContain("AAAA-BBBB-CCCC-DDDD");
  });

  it("shows setup failures and allows retrying", async () => {
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

  it("copies the link and secret through the clipboard and suggests manual copying on failure", async () => {
    useAuth.setState({ gate: "mfa_enroll", user: UNBOUND_USER });
    mocks.me.mockResolvedValue({ user: UNBOUND_USER, csrf_token: "csrf-1", mfa_required: true });
    mounted = mount(createElement(AuthGate));
    await flushUntil(() => mounted!.container.querySelector('input[autocomplete="one-time-code"]') !== null);

    const copyButtons = [...mounted.container.querySelectorAll("button")].filter(
      (b) => b.textContent?.trim() === "复制",
    );
    expect(copyButtons.length).toBe(2);
    clickButton(mounted.container, "复制");
    await flushUntil(() =>
      mocks.writeText.mock.calls.some(
        (c) => c[0] === "otpauth://totp/NexTerm:bob?secret=SECRET&issuer=NexTerm",
      ),
    );
    click(copyButtons[1]!);
    await flushUntil(() =>
      mocks.writeText.mock.calls.some((c) => c[0] === "SECRETSECRETSECRETSECRETSECRETSEC"),
    );
    expect(mounted.container.textContent).not.toContain("复制失败");

    mocks.writeText.mockRejectedValue(new Error("denied"));
    click(copyButtons[0]!);
    await flushUntil(() => mounted!.container.textContent?.includes("复制失败，请手动复制") ?? false);
  });
});

describe("TotpCard re-enrollment", () => {
  function mountCard(): MountedView {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return mount(createElement(QueryClientProvider, { client }, createElement(AuthCard)));
  }

  it("requires the current credential for re-enrollment and passes reverify to setup", async () => {
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

  it("shows a clear recovery-code copy failure and lets the user close the one-time page after confirmation", async () => {
    const { useUi } = await import("../app/store");
    useUi.setState({ pushToast: mocks.toast });
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
    mounted = mountCard();
    await flushUntil(() => mounted!.container.textContent?.includes("开启两步验证"));
    clickButton(mounted.container, "开启两步验证");
    await flushUntil(() => mounted!.container.querySelector('input[placeholder="输入 6 位动态码完成绑定"]') !== null);
    setInputValue(mounted.container.querySelector<HTMLInputElement>('input[placeholder="输入 6 位动态码完成绑定"]')!, "123456");
    clickButton(mounted.container, "确认绑定");
    await flushUntil(() => mounted!.container.textContent?.includes("恢复码仅显示一次"));

    mocks.writeText.mockRejectedValue(new Error("denied"));
    clickButton(mounted.container, "复制全部");
    await flushUntil(() => mocks.toast.mock.calls.some((call) => call[0] === "error"));
    const doneBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "已手动保存") as HTMLButtonElement;
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制全部")) as HTMLButtonElement;
    expect(mocks.toast).toHaveBeenCalledWith("error", "复制失败，请手动复制");
    expect(doneBtn.disabled).toBe(false);
    expect(copyBtn.textContent).not.toContain("已复制");
    expect(mounted.container.textContent).toContain("AAAA-BBBB-CCCC-DDDD");
  });

  it("explains in the card that the TOTP binding lives on the server account and is reused on other devices", async () => {
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
    expect(mounted.container.textContent).toContain("两步验证绑定在服务端账号上，换设备登录仍使用同一认证器，无需重新绑定");
  });
});

describe("mfa_enrollment_required global event", () => {
  it("moves an authenticated unbound session into forced enrollment when any API returns mfa_enrollment_required", async () => {
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
    // Already-bound sessions are not affected
    useAuth.setState({ gate: "ready", user: BOUND_USER });
    await expect(request("GET", "/auth/dek")).rejects.toMatchObject({ code: "mfa_enrollment_required" });
    expect(useAuth.getState().gate).toBe("ready");
    vi.unstubAllGlobals();
  });
});
