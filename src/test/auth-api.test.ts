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

describe("authApi production contract", () => {
  it("uses GET for status/me and POST for login, returning the session payload", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.status();
    const result = await authApi.login("alice", "secret");
    if (isMfaChallenge(result)) throw new Error("unexpected MFA challenge");
    expect(result.csrf_token).toBe("csrf-1");

    expect(calls[0]).toMatchObject({ url: "/auth/status", method: "GET" });
    expect(calls[1]).toMatchObject({ url: "/auth/login", method: "POST", body: { username: "alice", password: "secret" } });
    expect(calls[1]?.headers["X-NexTerm-Client"]).toBe("browser");
  });

  it("uses /auth/totp/login for totpLogin and remembers the CSRF token", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.totpLogin("ticket-1", "123456");
    expect(getCsrfToken()).toBe("csrf-1");
    expect(calls[0]).toMatchObject({ url: "/auth/totp/login", method: "POST", body: { ticket: "ticket-1", code: "123456" } });
    expect(calls[0]?.headers["X-NexTerm-Client"]).toBe("browser");
  });

  it("sends the initialization code, account, and five DEK envelope fields in init", async () => {
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
    expect(calls[0]?.headers["X-NexTerm-Client"]).toBe("browser");
  });

  it("sends the CSRF header on mutations but not queries", async () => {
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

  it("uses DELETE and encodes the path in deviceRevoke", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await authApi.deviceRevoke("d/1");
    expect(calls[0]).toMatchObject({ url: "/auth/devices/d%2F1", method: "DELETE" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("lists active login sessions and revokes one session with CSRF protection", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await authApi.sessions();
    await authApi.sessionRevoke("s/1");
    expect(calls.map((call) => `${call.method} ${call.url}`)).toEqual(["GET /auth/sessions", "DELETE /auth/sessions/s%2F1"]);
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
    expect(calls[1]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("binds a device when login receives device_id and omits the field otherwise", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await authApi.login("alice", "secret", "d-1");
    await authApi.login("alice", "secret");
    expect(calls[0]?.body).toEqual({ username: "alice", password: "secret", device_id: "d-1" });
    expect(calls[1]?.body).toEqual({ username: "alice", password: "secret" });
  });

  it("normalizes errors to code+message and emits the session-expired event on 401", async () => {
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

  it("normalizes network failures to disconnected", async () => {
    mocks.fetch.mockRejectedValue(new Error("offline"));
    vi.stubGlobal("fetch", mocks.fetch);
    await expect(authApi.status()).rejects.toMatchObject({ code: "disconnected" });
    await expect(authApi.status()).rejects.toBeInstanceOf(AuthApiError);
  });
});

describe("adminApi production contract", () => {
  it("uses the expected paths and methods for user management and the registration toggle", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await adminApi.users();
    await adminApi.createUser("bob", "bob-pw-123", "Bob");
    await adminApi.disableUser("u-2");
    await adminApi.resetUser("u-2");
    await adminApi.settingsGet();
    await adminApi.settingsPut({ registrationOpen: true });

    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      "GET /admin/users",
      "POST /admin/users",
      "POST /admin/users/u-2/disable",
      "POST /admin/users/u-2/reset",
      "GET /admin/settings",
      "PUT /admin/settings",
    ]);
    expect(calls[1]?.body).toEqual({ username: "bob", password: "bob-pw-123", display_name: "Bob" });
    expect(calls[5]?.body).toEqual({ registration_open: true });
    expect(calls[2]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });
});

describe("syncV2Api production contract", () => {
  it("uses the expected ids/pull/push/delete paths and protocol fields", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await syncV2Api.ids();
    await syncV2Api.pull(42, ["a1", "a2"], 1024);
    await syncV2Api.push("head-1", [{ id: "a1", blob: "blob-1" }]);
    await syncV2Api.deleteObject("head-2", "a1", "hash-1");

    expect(calls[0]).toMatchObject({ url: "/sync/v2/ids", body: { protocol: 3 } });
    expect(calls[1]).toMatchObject({ url: "/sync/v2/pull", body: { protocol: 3, since_seq: 42, ids: ["a1", "a2"], max_bytes: 1024 } });
    expect(calls[2]).toMatchObject({ url: "/sync/v2/push", body: { protocol: 3, known_head: "head-1", objects: [{ id: "a1", blob: "blob-1" }] } });
    expect(calls[3]).toMatchObject({ url: "/sync/v2/delete", body: { protocol: 3, known_head: "head-2", id: "a1", blob_hash: "hash-1" } });
    expect(calls[2]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[3]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });
});
