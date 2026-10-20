/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { flush, flushUntil as flushUntilBase, mount, setInputValue, clickButton, type MountedView } from "./features/reactTestUtils";

async function flushUntil(predicate: () => boolean, maxTicks = 60): Promise<void> {
  await flush();
  await flushUntilBase(predicate, maxTicks);
  await flush();
}

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    status: vi.fn(),
    me: vi.fn(),
    init: vi.fn(),
    login: vi.fn(),
    logout: vi.fn(),
    dekGet: vi.fn(),
    dekUpload: vi.fn(),
    changePassword: vi.fn(),
    recoveryReset: vi.fn(),
    writeText: vi.fn(),
  };
});

vi.mock("../ipc/authApi", async (importOriginal) => {
  const original = await importOriginal<typeof import("../ipc/authApi")>();
  return {
    ...original,
    authApi: {
      status: mocks.status,
      me: mocks.me,
      init: mocks.init,
      login: mocks.login,
      register: mocks.login,
      logout: mocks.logout,
      dekGet: mocks.dekGet,
      dekUpload: mocks.dekUpload,
      changePassword: mocks.changePassword,
      recoveryReset: mocks.recoveryReset,
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
import { useAuth } from "../features/auth/store";
import { AuthApiError } from "../ipc/authApi";

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

let mounted: MountedView | undefined;

function resetStore() {
  useAuth.setState({
    status: null,
    user: null,
    dek: null,
    gate: "loading",
    pendingRecoveryKey: null,
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
  mocks.init.mockResolvedValue({ user: ADMIN, csrf_token: "csrf-1" });
  mocks.login.mockResolvedValue({ user: ADMIN, csrf_token: "csrf-1" });
  mocks.logout.mockResolvedValue({ ok: true });
  mocks.dekGet.mockResolvedValue({
    dek_envelope: "",
    kdf_salt: "",
    kdf_params: '{"t":3,"m":65536,"p":4}',
    recovery_envelope: "",
    recovery_hash: "",
  });
  mocks.dekUpload.mockResolvedValue({ ok: true });
  mocks.changePassword.mockResolvedValue({ ok: true });
  mocks.recoveryReset.mockResolvedValue({ ok: true });
});

afterEach(async () => {
  await flush();
  mounted?.unmount();
  mounted = undefined;
  await flush();
  resetStore();
});

function mountGate(): MountedView {
  return mount(createElement(AuthGate));
}

describe("AuthGate initial setup", () => {
  it("shows the setup form when uninitialized and displays the recovery key after submitting the initialization code, account, and password", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "on" });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "console-init-code");
    setInputValue(inputs[1], "root");
    setInputValue(inputs[2], "pw-123456");
    setInputValue(inputs[3], "pw-123456");
    await flush();
    clickButton(mounted.container, "创建超级管理员");

    await flushUntil(() => mocks.init.mock.calls.length > 0);
    expect(mocks.init).toHaveBeenCalledWith(
      expect.objectContaining({
        code: "console-init-code",
        username: "root",
        password: "pw-123456",
        dekEnvelope: expect.any(Uint8Array),
        recoveryEnvelope: expect.any(Uint8Array),
        recoveryHash: "hash-1",
      }),
    );

    // Recovery key is shown only once; entry is allowed after copying
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥（仅显示一次）"));
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制"));
    expect(copyBtn).toBeTruthy();
    expect(mounted.container.textContent).toContain("已保存，进入应用");
  });

  it("does not submit when the two passwords differ", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "on" });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "console-init-code");
    setInputValue(inputs[1], "root");
    setInputValue(inputs[2], "pw-123456");
    setInputValue(inputs[3], "pw-different");
    await flush();
    clickButton(mounted.container, "创建超级管理员");
    await flush();
    expect(mocks.init).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("两次输入的密码不一致");
  });
});

describe("AuthGate loopback login bypass", () => {
  it("enters the app directly for an uninitialized loopback instance without showing the setup gate", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "loopback" });
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().gate).toBe("ready");
    expect(mounted.container.querySelector(".fixed.inset-0")).toBeNull();
    expect(mounted.container.textContent ?? "").not.toContain("初始化 NexTerm");
    expect(mocks.me).not.toHaveBeenCalled();
  });

  it("becomes anonymously ready for an initialized signed-out loopback instance without showing the login gate", async () => {
    mocks.status.mockResolvedValue({ initialized: true, registration_open: false, auth: "loopback" });
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().gate).toBe("ready");
    expect(mounted.container.querySelector(".fixed.inset-0")).toBeNull();
  });

  it("opens setup from settings for an uninitialized loopback instance and returns to anonymous use", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "loopback" });
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");

    // "Initialize account" entry on the settings page account card
    act(() => useAuth.setState({ gate: "setup" }));
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));
    clickButton(mounted.container, "暂不初始化，匿名使用");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(mounted.container.querySelector(".fixed.inset-0")).toBeNull();
  });

  it("offers no anonymous exit from the setup gate for an uninitialized auth=on instance", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "on" });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));
    const skip = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("暂不初始化"));
    expect(skip).toBeUndefined();
  });

  it("sets status.initialized to true after successful loopback initialization", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "loopback" });
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");

    act(() => useAuth.setState({ gate: "setup" }));
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));
    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "console-init-code");
    setInputValue(inputs[1], "root");
    setInputValue(inputs[2], "pw-123456");
    setInputValue(inputs[3], "pw-123456");
    await flush();
    clickButton(mounted.container, "创建超级管理员");

    await flushUntil(() => mocks.init.mock.calls.length > 0);
    await flushUntil(() => useAuth.getState().status?.initialized === true);
    expect(useAuth.getState().gate).toBe("ready");
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥（仅显示一次）"));
  });
});

describe("AuthGate auth=off", () => {
  it("normalizes an auth=off 403 during refresh to anonymous ready with no gate or error", async () => {
    mocks.status.mockRejectedValue(new AuthApiError("forbidden", "--auth=off 下账号功能已关闭", 403));
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().status?.auth).toBe("off");
    expect(useAuth.getState().error).toBeNull();
    expect(mounted.container.querySelector(".fixed.inset-0")).toBeNull();
    expect(mocks.me).not.toHaveBeenCalled();
  });

  it("does not normalize other 403 errors to auth=off and preserves the error as-is", async () => {
    mocks.status.mockRejectedValue(new AuthApiError("forbidden", "用户名或密码错误", 403));
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().status).toBeNull();
    expect(useAuth.getState().error?.message).toBe("用户名或密码错误");
  });

  it("offers an exit back to the app when setup is opened while auth=off", async () => {
    mocks.status.mockResolvedValue({ initialized: false, registration_open: false, auth: "off" });
    mounted = mountGate();
    await flushUntil(() => useAuth.getState().gate === "ready");

    act(() => useAuth.setState({ gate: "setup" }));
    await flushUntil(() => mounted!.container.textContent?.includes("初始化 NexTerm"));
    clickButton(mounted.container, "账号功能已关闭，返回应用");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(mounted.container.querySelector(".fixed.inset-0")).toBeNull();
  });
});

describe("AuthGate fullscreen recovery-key gate", () => {
  it("keeps recovery in the same fullscreen gate and blocks entry until the key is copied", async () => {
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: { id: "u-admin", username: "root", display_name: "", role: "superadmin", state: "active", must_change_password: false, mfa_enabled: false, created_at: 1, updated_at: 1, last_login_at: 1 },
      dek: new Uint8Array(32).fill(9),
      gate: "ready",
      pendingRecoveryKey: { canonical: "X3QSWQP4PY4PVVAYPH7RNDV2CUAIQUKU", formatted: "X3QS-WQP4-PY4P-VVAY-PH7R-NDV2-CUAI-QUKU" },
      error: null,
    });
    mounted = mountGate();
    await flush();
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥（仅显示一次）"));

    // Fullscreen gate container (fixed inset-0) is present, the underlying app is covered
    const gate = mounted.container.querySelector(".fixed.inset-0");
    expect(gate).not.toBeNull();

    // "Saved manually" is unavailable before copying
    const doneBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入应用")) as HTMLButtonElement | undefined;
    expect(doneBtn?.disabled).toBe(true);

    // Available after copying
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制")) as HTMLButtonElement | undefined;
    act(() => copyBtn?.click());
    await flush();
    const doneBtn2 = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入应用")) as HTMLButtonElement | undefined;
    expect(doneBtn2?.disabled).toBe(false);
  });

  it("shows a manual-copy path when recovery-key copying fails and still allows entry after confirmation", async () => {
    const formatted = "X3QS-WQP4-PY4P-VVAY-PH7R-NDV2-CUAI-QUKU";
    mocks.writeText.mockRejectedValue(new Error("denied"));
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: ADMIN,
      dek: new Uint8Array(32).fill(9),
      gate: "ready",
      pendingRecoveryKey: { canonical: "X3QSWQP4PY4PVVAYPH7RNDV2CUAIQUKU", formatted },
      error: null,
    });
    mounted = mountGate();
    await flush();
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥（仅显示一次）"));

    clickButton(mounted.container, "复制");
    await flushUntil(() => mounted!.container.textContent?.includes("复制失败，请手动复制恢复密钥"));
    const doneBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("已保存，进入应用")) as HTMLButtonElement;
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.trim() === "复制") as HTMLButtonElement;
    expect(mocks.writeText).toHaveBeenCalledWith(formatted);
    expect(doneBtn.disabled).toBe(false);
    expect(copyBtn.textContent).not.toContain("已复制");
    expect(mounted.container.textContent).toContain(formatted);
  });
});

describe("AuthGate login", () => {
  it("shows the login form for an initialized signed-out instance and enters the app after login", async () => {
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "pw-123456");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mocks.login.mock.calls.length > 0);
    expect(mocks.login).toHaveBeenCalledWith("root", "pw-123456", undefined);
    await flushUntil(() => useAuth.getState().user?.id === "u-admin");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("shows only the in-progress login state while login is pending", async () => {
    let resolveLogin!: (value: { user: typeof ADMIN; csrf_token: string }) => void;
    mocks.login.mockImplementation(
      () => new Promise<{ user: typeof ADMIN; csrf_token: string }>((resolve) => { resolveLogin = resolve; }),
    );
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "pw-123456");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mounted!.container.textContent?.includes("登录中…"));
    expect(mounted.container.textContent).not.toContain("解锁");
    expect(mounted.container.textContent).not.toContain("解密");
    resolveLogin({ user: ADMIN, csrf_token: "csrf-1" });
    await flushUntil(() => useAuth.getState().gate === "ready");
  });

  it("generates and uploads an envelope locally for an admin-created user's first login, then shows the recovery key", async () => {
    // GET /auth/dek returns not_found (users created by an admin have no DEK envelope yet)
    mocks.dekGet.mockRejectedValue({ code: "not_found", message: "未找到: DEK 信封", status: 404 });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "alice");
    setInputValue(inputs[1], "pw-123456");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mocks.dekUpload.mock.calls.length > 0);
    expect(mocks.dekUpload).toHaveBeenCalledWith(
      expect.objectContaining({
        dekEnvelope: expect.any(Uint8Array),
        recoveryEnvelope: expect.any(Uint8Array),
        recoveryHash: "hash-1",
      }),
    );
    // After a successful upload, enter the one-time recovery key display
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥（仅显示一次）"));
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("shows the server error when login fails", async () => {
    mocks.login.mockRejectedValue({ code: "forbidden", message: "用户名或密码错误", status: 403 });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "wrong-pw");
    await flush();
    clickButton(mounted.container, "登录");
    await flushUntil(() => mounted!.container.textContent?.includes("用户名或密码错误"));
    expect(useAuth.getState().user).toBeNull();
  });

  it("hides registration while closed and shows a working registration entry while open", async () => {
    mocks.status.mockResolvedValue({ initialized: true, registration_open: true, auth: "on" });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));
    expect(mounted.container.textContent).toContain("注册新账号");

    clickButton(mounted.container, "注册新账号");
    await flushUntil(() => mounted!.container.textContent?.includes("注册"));
    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "alice");
    setInputValue(inputs[2], "pw-123456");
    setInputValue(inputs[3], "pw-123456");
    await flush();
    clickButton(mounted.container, "注册并登录");
    await flushUntil(() => useAuth.getState().user?.id === "u-admin");
    expect(mocks.init).not.toHaveBeenCalled();
  });
});

describe("AuthGate reset_required forced password change", () => {
  it("forces a new password before allowing a reset_required session", async () => {
    mocks.login.mockResolvedValue({
      user: { ...ADMIN, state: "reset_required", must_change_password: true },
      csrf_token: "csrf-1",
    });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "temp-pw-123");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mounted!.container.textContent?.includes("必须先设置新密码"));
    const resetText = mounted.container.textContent ?? "";
    expect(resetText).toContain("当前临时密码仅用于本次验证");
    expect(resetText).toContain("数据密钥、所有会话和两步验证绑定已清除");
    expect(resetText).toContain("云端密文需从仍持有数据的设备重新同步");
    expect(resetText).not.toContain("旧密码与加密数据已被清除");
    const resetInputs = mounted.container.querySelectorAll("input");
    setInputValue(resetInputs[0], "temp-pw-123");
    setInputValue(resetInputs[1], "new-pw-123");
    setInputValue(resetInputs[2], "new-pw-123");
    await flush();
    clickButton(mounted.container, "设置新密码");

    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);
    // changePassword upserts the envelope in a transaction and verifies the temp password; dekUpload no longer runs first (insert-only would 409 forever on retry)
    expect(mocks.dekUpload).not.toHaveBeenCalled();
    expect(mocks.changePassword).toHaveBeenCalledWith(
      expect.objectContaining({ oldPassword: "temp-pw-123", newPassword: "new-pw-123" }),
    );
  });

  it("succeeds after retrying with the correct temporary password instead of getting stuck on an existing-envelope 409", async () => {
    mocks.login.mockResolvedValue({
      user: { ...ADMIN, state: "reset_required", must_change_password: true },
      csrf_token: "csrf-1",
    });
    mocks.changePassword.mockRejectedValueOnce({ code: "forbidden", message: "原密码错误", status: 403 });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "temp-pw-123");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mounted!.container.textContent?.includes("必须先设置新密码"));
    const resetInputs = mounted.container.querySelectorAll("input");
    setInputValue(resetInputs[0], "wrong-temp-pw");
    setInputValue(resetInputs[1], "new-pw-123");
    setInputValue(resetInputs[2], "new-pw-123");
    await flush();
    clickButton(mounted.container, "设置新密码");
    await flushUntil(() => mounted!.container.textContent?.includes("原密码错误"));

    // Retry: the correct temp password should reach changePassword (not 409 at dekUpload first)
    const retryInputs = mounted.container.querySelectorAll("input");
    setInputValue(retryInputs[0], "temp-pw-123");
    await flush();
    clickButton(mounted.container, "设置新密码");
    await flushUntil(() => mocks.changePassword.mock.calls.length >= 2);
    expect(mocks.changePassword).toHaveBeenLastCalledWith(
      expect.objectContaining({ oldPassword: "temp-pw-123", newPassword: "new-pw-123" }),
    );
    expect(mocks.dekUpload).not.toHaveBeenCalled();
  });
});

describe("AuthGate recovery-key reset", () => {
  it("resets a forgotten password with the recovery key and logs in again", async () => {
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    clickButton(mounted.container, "使用恢复密钥重置密码");
    await flushUntil(() => mounted!.container.textContent?.includes("用恢复密钥重置"));
    expect(mounted.container.textContent).toContain("重置后，旧密码、数据密钥、所有会话和两步验证绑定将失效");

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "X3QSWQP4PY4PVVAYPH7RNDV2CUAIQUKU");
    setInputValue(inputs[2], "new-pw-123");
    setInputValue(inputs[3], "new-pw-123");
    await flush();
    clickButton(mounted.container, "重置密码并登录");

    await flushUntil(() => mocks.recoveryReset.mock.calls.length > 0);
    expect(mocks.recoveryReset).toHaveBeenCalledWith(
      expect.objectContaining({
        username: "root",
        recoveryKey: "X3QSWQP4PY4PVVAYPH7RNDV2CUAIQUKU",
        newPassword: "new-pw-123",
      }),
    );
    await flushUntil(() => mocks.login.mock.calls.length > 0);
    expect(mocks.login).toHaveBeenCalledWith("root", "new-pw-123", undefined);
  });
});
