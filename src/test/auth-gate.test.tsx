/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { flush, flushUntil, mount, setInputValue, clickButton, type MountedView } from "./features/reactTestUtils";

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

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  resetStore();
});

function mountGate(): MountedView {
  return mount(createElement(AuthGate));
}

describe("AuthGate 首次初始化", () => {
  it("未初始化时显示设置表单,提交初始化码+账号+密码后进入恢复密钥展示", async () => {
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

    // 恢复密钥只显示一次,复制后才能进入
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥(只显示这一次)"));
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制"));
    expect(copyBtn).toBeTruthy();
    expect(mounted.container.textContent).toContain("我已安全保存");
  });

  it("两次密码不一致时不提交", async () => {
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

describe("AuthGate 恢复密钥全屏门", () => {
  it("恢复密钥页在同一全屏门内,复制前不能进入", async () => {
    useAuth.setState({
      status: { initialized: true, registration_open: false, auth: "on" },
      user: { id: "u-admin", username: "root", display_name: "", role: "superadmin", state: "active", must_change_password: false, created_at: 1, updated_at: 1, last_login_at: 1 },
      dek: new Uint8Array(32).fill(9),
      gate: "ready",
      pendingRecoveryKey: { canonical: "X3QSWQP4PY4PVVAYPH7RNDV2CUAIQUKU", formatted: "X3QS-WQP4-PY4P-VVAY-PH7R-NDV2-CUAI-QUKU" },
      error: null,
    });
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥(只显示这一次)"));

    // 全屏门容器(fixed inset-0)存在,底层应用被遮挡
    const gate = mounted.container.querySelector(".fixed.inset-0");
    expect(gate).not.toBeNull();

    // 复制前「我已安全保存」不可用
    const doneBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("我已安全保存")) as HTMLButtonElement | undefined;
    expect(doneBtn?.disabled).toBe(true);

    // 复制后可用
    const copyBtn = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("复制")) as HTMLButtonElement | undefined;
    copyBtn?.click();
    await flush();
    const doneBtn2 = [...mounted.container.querySelectorAll("button")].find((b) => b.textContent?.includes("我已安全保存")) as HTMLButtonElement | undefined;
    expect(doneBtn2?.disabled).toBe(false);
  });
});

describe("AuthGate 登录", () => {
  it("已初始化未登录时显示登录表单,登录成功进入应用", async () => {
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    const inputs = mounted.container.querySelectorAll("input");
    setInputValue(inputs[0], "root");
    setInputValue(inputs[1], "pw-123456");
    await flush();
    clickButton(mounted.container, "登录");

    await flushUntil(() => mocks.login.mock.calls.length > 0);
    expect(mocks.login).toHaveBeenCalledWith("root", "pw-123456");
    await flushUntil(() => useAuth.getState().user?.id === "u-admin");
    await flushUntil(() => useAuth.getState().gate === "ready");
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("管理员创建的用户首登无信封:本地生成并上传,展示恢复密钥", async () => {
    // GET /auth/dek 返回 not_found(管理员创建的用户尚无 DEK 信封)
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
    // 上传成功后进入恢复密钥一次性展示
    await flushUntil(() => mounted!.container.textContent?.includes("恢复密钥(只显示这一次)"));
    expect(useAuth.getState().gate).toBe("ready");
  });

  it("登录失败时显示服务端错误", async () => {
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

  it("注册关闭时不显示注册入口,开放时显示并可注册", async () => {
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

describe("AuthGate reset_required 强制改密", () => {
  it("reset_required 状态强制先设置新密码", async () => {
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
    const resetInputs = mounted.container.querySelectorAll("input");
    setInputValue(resetInputs[0], "temp-pw-123");
    setInputValue(resetInputs[1], "new-pw-123");
    setInputValue(resetInputs[2], "new-pw-123");
    await flush();
    clickButton(mounted.container, "设置新密码");

    await flushUntil(() => mocks.changePassword.mock.calls.length > 0);
    // changePassword 在事务内 upsert 信封并校验临时密码;不再先 dekUpload(insert-only 会在重试时 409 卡死)
    expect(mocks.dekUpload).not.toHaveBeenCalled();
    expect(mocks.changePassword).toHaveBeenCalledWith(
      expect.objectContaining({ oldPassword: "temp-pw-123", newPassword: "new-pw-123" }),
    );
  });

  it("临时密码错误后,正确重试能成功(不因信封已存在 409 卡死)", async () => {
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

    // 重试:正确的临时密码应能走到 changePassword(不先在 dekUpload 处 409)
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

describe("AuthGate 恢复密钥重置", () => {
  it("忘记密码走恢复密钥重置并重新登录", async () => {
    mounted = mountGate();
    await flushUntil(() => mounted!.container.textContent?.includes("登录"));

    clickButton(mounted.container, "忘记密码?用恢复密钥重置");
    await flushUntil(() => mounted!.container.textContent?.includes("用恢复密钥重置"));
    expect(mounted.container.textContent).toContain("输入你保存的最新恢复密钥");

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
    expect(mocks.login).toHaveBeenCalledWith("root", "new-pw-123");
  });
});
