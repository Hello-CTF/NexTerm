/** @vitest-environment jsdom */
// SHARE158 live contract: sharingApi paths/methods/CSRF/snake_case JSON fields align with
// the real routes in internal/fleet/server/sharing_http.go; 401 reuses the existing session-expired event;
// legacy public links have only list and revoke; new anonymous access uses the separate preview-link API.
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

describe("sharingApi live contract", () => {
  it("lists host shares via GET /share/host-shares without a CSRF header", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await sharingApi.hostShares();

    expect(calls[0]).toMatchObject({ url: "/share/host-shares", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("creates host shares via POST /share/host-shares with snake_case fields and CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await sharingApi.createHostShare({ deviceId: "d-1", recipientUsername: "alice", write: true, ttlMs: 3_600_000 });

    expect(calls[0]).toMatchObject({
      url: "/share/host-shares",
      method: "POST",
      body: { device_id: "d-1", recipient_username: "alice", write: true, ttl_ms: 3_600_000 },
    });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("revokes host shares via POST /share/host-shares/{id}/revoke with an encoded path and CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await sharingApi.revokeHostShare("s/1");

    expect(calls[0]).toMatchObject({ url: "/share/host-shares/s%2F1/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("no longer exposes legacy public-link list, revoke or create on browser or desktop clients", async () => {
    const { remoteSharingApi } = await import("../../ipc/remoteApi");
    for (const api of [sharingApi, remoteSharingApi] as unknown as Record<string, unknown>[]) {
      expect(api.links).toBeUndefined();
      expect(api.revokeLink).toBeUndefined();
      expect(api.createLink).toBeUndefined();
    }
  });

  it("maps snake_case response fields into DTOs and omits tokens from lists", async () => {
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

  it("dispatches a session-expired event and normalizes errors on 401", async () => {
    mocks.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => JSON.stringify({ error: { code: "forbidden", message: "session is invalid or missing" } }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);

    await expect(sharingApi.hostShares()).rejects.toMatchObject({
      code: "forbidden",
      message: "session is invalid or missing",
      status: 401,
    });
    expect(expired).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
