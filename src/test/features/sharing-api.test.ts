/** @vitest-environment jsdom */
// SHARE158 线上契约: sharingApi 的路径/方法/CSRF/蛇形 JSON 字段对齐
// internal/fleet/server/sharing_http.go 的真实路由, 401 复用既有会话过期事件;
// 公开链接只有列表与吊销 — 不存在任何创建方法 (创建入口归实时会话切片)。
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
      text: async () => JSON.stringify({ ok: true }),
    };
  });
  return calls;
}

import { sharingApi } from "../../ipc/sharingApi";
import { setCsrfToken, SESSION_EXPIRED_EVENT } from "../../ipc/authApi";

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("sharingApi 线上契约", () => {
  it("主机分享列表走 GET /share/host-shares 且不带 CSRF 头", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await sharingApi.hostShares();

    expect(calls[0]).toMatchObject({ url: "/share/host-shares", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("创建主机分享走 POST /share/host-shares, 蛇形字段 + CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await sharingApi.createHostShare({ deviceId: "d-1", recipientId: "u-2", write: true, ttlMs: 3_600_000 });

    expect(calls[0]).toMatchObject({
      url: "/share/host-shares",
      method: "POST",
      body: { device_id: "d-1", recipient_id: "u-2", write: true, ttl_ms: 3_600_000 },
    });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("吊销主机分享走 POST /share/host-shares/{id}/revoke, 路径编码且带 CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await sharingApi.revokeHostShare("s/1");

    expect(calls[0]).toMatchObject({ url: "/share/host-shares/s%2F1/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("公开链接列表走 GET /share/links 且不带 CSRF 头", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await sharingApi.links();

    expect(calls[0]).toMatchObject({ url: "/share/links", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("吊销公开链接走 POST /share/links/{id}/revoke, 路径编码且带 CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await sharingApi.revokeLink("l 1");

    expect(calls[0]).toMatchObject({ url: "/share/links/l%201/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("没有公开链接创建方法: 本切片不提供创建入口, 更不会伪造 session_id", () => {
    expect((sharingApi as unknown as Record<string, unknown>).createLink).toBeUndefined();
    expect((sharingApi as unknown as Record<string, unknown>).linksCreate).toBeUndefined();
  });

  it("响应蛇形字段原样映射进 DTO (列表不含 token)", async () => {
    mocks.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({
          shares: [
            {
              id: "hs-1",
              owner_id: "u-1",
              owner_username: "root",
              device_id: "d-1",
              recipient_id: "u-2",
              recipient_username: "alice",
              permission: "read_write",
              created_at: 1,
              expires_at: 2,
              revoked_at: 3,
            },
          ],
        }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const r = await sharingApi.hostShares();

    expect(r.shares[0]?.recipient_username).toBe("alice");
    expect(r.shares[0]?.permission).toBe("read_write");
    expect(r.shares[0]?.revoked_at).toBe(3);
    expect(r.shares[0]).not.toHaveProperty("token");
  });

  it("401 派发会话过期事件并规整错误", async () => {
    mocks.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => JSON.stringify({ error: { code: "forbidden", message: "会话无效或缺失" } }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);

    await expect(sharingApi.links()).rejects.toMatchObject({
      code: "forbidden",
      message: "会话无效或缺失",
      status: 401,
    });
    expect(expired).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
