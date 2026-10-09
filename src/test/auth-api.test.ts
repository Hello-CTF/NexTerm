/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    fetch: vi.fn(),
  };
});

interface CapturedRequest {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

function capture(): CapturedRequest[] {
  const calls: CapturedRequest[] = [];
  mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
    calls.push({
      url: String(url),
      method: init.method ?? "GET",
      headers: (init.headers ?? {}) as Record<string, string>,
      ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    return {
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ ok: true, user: { id: "u-1" }, csrf_token: "csrf-1" }),
    };
  });
  return calls;
}

import { authApi, adminApi, syncV2Api, setCsrfToken, getCsrfToken, isMfaChallenge, AuthApiError, SESSION_EXPIRED_EVENT } from "../ipc/authApi";

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("authApi 线上契约", () => {
  it("status/me 走 GET,login 走 POST 并返回会话负载", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.status();
    const result = await authApi.login("alice", "secret");
    if (isMfaChallenge(result)) throw new Error("unexpected MFA challenge");
    expect(result.csrf_token).toBe("csrf-1");

    expect(calls[0]).toMatchObject({ url: "/auth/status", method: "GET" });
    expect(calls[1]).toMatchObject({ url: "/auth/login", method: "POST", body: { username: "alice", password: "secret" } });
  });

  it("totpLogin 走 /auth/totp/login 并记住 CSRF 令牌", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.totpLogin("ticket-1", "123456");
    expect(getCsrfToken()).toBe("csrf-1");
    expect(calls[0]).toMatchObject({ url: "/auth/totp/login", method: "POST", body: { ticket: "ticket-1", code: "123456" } });
  });

  it("init 携带初始化码、账号与 DEK 信封五元组", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.init({
      code: "init-code-1",
      username: "root",
      password: "pw-123456",
      dekEnvelope: new Uint8Array([1, 2, 3]),
      kdfSalt: new Uint8Array([4, 5, 6]),
      kdfParams: '{"t":3,"m":65536,"p":4}',
      recoveryEnvelope: new Uint8Array([7, 8, 9]),
      recoveryHash: "hash-1",
    });

    expect(calls[0]?.body).toEqual({
      code: "init-code-1",
      username: "root",
      password: "pw-123456",
      dek_envelope: "AQID",
      kdf_salt: "BAUG",
      kdf_params: '{"t":3,"m":65536,"p":4}',
      recovery_envelope: "BwgJ",
      recovery_hash: "hash-1",
    });
  });

  it("状态变更请求携带 CSRF 头,查询不携带", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await authApi.logout();
    await authApi.changePassword({
      oldPassword: "old-pw-123",
      newPassword: "new-pw-123",
      dekEnvelope: new Uint8Array(60),
      kdfSalt: new Uint8Array(16),
      kdfParams: '{"t":3,"m":65536,"p":4}',
      recoveryEnvelope: new Uint8Array(60),
      recoveryHash: "hash-2",
    });
    await authApi.me();

    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]?.body).toMatchObject({ old_password: "old-pw-123", new_password: "new-pw-123" });
    expect(calls[2]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("deviceRevoke 走 DELETE 并编码路径", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await authApi.deviceRevoke("d/1");
    expect(calls[0]).toMatchObject({ url: "/auth/devices/d%2F1", method: "DELETE" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("login 携带 device_id 时绑定设备,缺省不带该字段", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.login("alice", "secret", "d-1");
    await authApi.login("alice", "secret");
    expect(calls[0]?.body).toEqual({ username: "alice", password: "secret", device_id: "d-1" });
    expect(calls[1]?.body).toEqual({ username: "alice", password: "secret" });
  });

  it("enroll 走公共 POST /auth/devices/enroll,不携带 CSRF 头", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await authApi.enroll("pair-code-1", "这台浏览器", "web");
    expect(calls[0]).toMatchObject({
      url: "/auth/devices/enroll",
      method: "POST",
      body: { code: "pair-code-1", name: "这台浏览器", kind: "web" },
    });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("enroll 的无效/过期码与网络错误如实上抛", async () => {
    mocks.fetch.mockResolvedValue({
      ok: false,
      status: 403,
      text: async () => JSON.stringify({ error: { code: "forbidden", message: "设备注册码无效或已过期" } }),
    });
    vi.stubGlobal("fetch", mocks.fetch);
    await expect(authApi.enroll("bad-code", "n", "web")).rejects.toMatchObject({
      code: "forbidden",
      message: "设备注册码无效或已过期",
      status: 403,
    });

    mocks.fetch.mockRejectedValue(new Error("offline"));
    await expect(authApi.enroll("bad-code", "n", "web")).rejects.toMatchObject({ code: "disconnected" });
  });

  it("错误规整为 code+message,401 时派发会话过期事件", async () => {
    mocks.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => JSON.stringify({ error: { code: "forbidden", message: "会话无效或缺失" } }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);

    await expect(authApi.me()).rejects.toMatchObject({ code: "forbidden", message: "会话无效或缺失", status: 401 });
    expect(expired).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });

  it("网络失败规整为 disconnected", async () => {
    mocks.fetch.mockRejectedValue(new Error("offline"));
    vi.stubGlobal("fetch", mocks.fetch);
    await expect(authApi.status()).rejects.toMatchObject({ code: "disconnected" });
    await expect(authApi.status()).rejects.toBeInstanceOf(AuthApiError);
  });
});

describe("adminApi 线上契约", () => {
  it("用户管理与注册开关的路径和方法", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await adminApi.users();
    await adminApi.createUser("bob", "bob-pw-123", "Bob");
    await adminApi.disableUser("u-2");
    await adminApi.resetUser("u-2");
    await adminApi.settingsGet();
    await adminApi.settingsPut({ registrationOpen: true, publicBaseUrl: "https://nexterm.example.com" });

    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "GET /admin/users",
      "POST /admin/users",
      "POST /admin/users/u-2/disable",
      "POST /admin/users/u-2/reset",
      "GET /admin/settings",
      "PUT /admin/settings",
    ]);
    expect(calls[1]?.body).toEqual({ username: "bob", password: "bob-pw-123", display_name: "Bob" });
    expect(calls[5]?.body).toEqual({ registration_open: true, public_base_url: "https://nexterm.example.com" });
    expect(calls[2]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });
});

describe("syncV2Api 线上契约", () => {
  it("ids/pull/push 的路径与协议字段", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await syncV2Api.ids();
    await syncV2Api.pull(42, ["a1", "a2"], 1024);
    await syncV2Api.push("head-1", [{ id: "a1", blob: "blob-1" }]);

    expect(calls[0]).toMatchObject({ url: "/sync/v2/ids", body: { protocol: 2 } });
    expect(calls[1]).toMatchObject({ url: "/sync/v2/pull", body: { protocol: 2, since_seq: 42, ids: ["a1", "a2"], max_bytes: 1024 } });
    expect(calls[2]).toMatchObject({ url: "/sync/v2/push", body: { protocol: 2, known_head: "head-1", objects: [{ id: "a1", blob: "blob-1" }] } });
    expect(calls[2]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });
});
