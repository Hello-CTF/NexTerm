/** @vitest-environment jsdom */
// FLEET149 live contract: fleetApi paths/methods/CSRF/snake_case JSON fields align with
// the real routes in internal/fleet/server/http.go; 401 reuses the existing session-expired event.
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

import { fleetApi } from "../../ipc/fleetApi";
import { setCsrfToken, SESSION_EXPIRED_EVENT } from "../../ipc/authApi";

beforeEach(() => {
  vi.clearAllMocks();
  setCsrfToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fleetApi live contract", () => {
  it("lists devices with GET /fleet/devices without a CSRF header", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await fleetApi.devices();

    expect(calls[0]).toMatchObject({ url: "/fleet/devices", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();
  });

  it("issues enroll codes with POST /device/enroll-codes, snake_case ttl_ms, and CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await fleetApi.issueEnrollCode(900_000);
    await fleetApi.issueEnrollCode(300_000, "u-2");

    expect(calls[0]).toMatchObject({ url: "/device/enroll-codes", method: "POST", body: { ttl_ms: 900_000 } });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]?.body).toEqual({ ttl_ms: 300_000, user_id: "u-2" });
  });

  it("revokes devices and sets autostart with POST /fleet/devices/{id}/..., encoded paths, and CSRF", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await fleetApi.revoke("d/1");
    await fleetApi.setAutostart("d 2", true);

    expect(calls[0]).toMatchObject({ url: "/fleet/devices/d%2F1/revoke", method: "POST" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
    expect(calls[1]).toMatchObject({
      url: "/fleet/devices/d%202/autostart",
      method: "POST",
      body: { desired: true },
    });
  });

  it("gets metrics with GET /fleet/devices/{id}/metrics and an optional since_ms query parameter", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);

    await fleetApi.metrics("d-1");
    await fleetApi.metrics("d-1", 1735689600000);

    expect(calls[0]).toMatchObject({ url: "/fleet/devices/d-1/metrics", method: "GET" });
    expect(calls[1]?.url).toBe("/fleet/devices/d-1/metrics?since_ms=1735689600000");
  });

  it("uses GET/PUT /fleet/base-urls for base URLs, with a snake_case array and CSRF on PUT", async () => {
    const calls = capture();
    vi.stubGlobal("fetch", mocks.fetch);
    setCsrfToken("csrf-9");

    await fleetApi.baseUrls();
    await fleetApi.putBaseUrls([{ url: "https://a.example.com" }, { url: "http://10.0.0.8", insecure: true }]);

    expect(calls[0]).toMatchObject({ url: "/fleet/base-urls", method: "GET" });
    expect(calls[1]).toMatchObject({
      url: "/fleet/base-urls",
      method: "PUT",
      body: { base_urls: [{ url: "https://a.example.com" }, { url: "http://10.0.0.8", insecure: true }] },
    });
    expect(calls[1]?.headers["X-NexTerm-CSRF"]).toBe("csrf-9");
  });

  it("maps snake_case response fields directly into the DTO", async () => {
    mocks.fetch.mockResolvedValue({
      ok: true,
      status: 200,
      text: async () =>
        JSON.stringify({
          devices: [
            {
              id: "d-1",
              name: "web-01",
              kind: "agent",
              created_at: 1,
              last_seen_at: 2,
              revoked_at: 0,
              owner: { id: "u-1", username: "alice" },
              agent: {
                platform: "linux",
                app_version: "0.2.2",
                desired_autostart: true,
                terminal_enabled: true,
                current_url: "https://a.example.com",
                service_state: { installed: true, enabled: true, active: true, last_reconcile_at: 3 },
                last_seen_at: 4,
              },
            },
          ],
        }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const r = await fleetApi.devices();

    expect(r.devices[0]?.agent?.service_state.active).toBe(true);
    expect(r.devices[0]?.agent?.desired_autostart).toBe(true);
    expect(r.devices[0]?.owner?.username).toBe("alice");
    expect(r.devices[0]?.last_seen_at).toBe(2);
  });

  it("gets serverVersion with GET /healthz without a CSRF header and normalizes an empty version to an empty string", async () => {
    const calls: CapturedRequest[] = [];
    let served = 0;
    mocks.fetch.mockImplementation(async (url: string, init: RequestInit) => {
      calls.push({
        url: String(url),
        method: init.method ?? "GET",
        headers: (init.headers ?? {}) as Record<string, string>,
      });
      served += 1;
      const payload = served === 1 ? { ok: true, version: "0.2.2" } : { ok: true };
      return { ok: true, status: 200, text: async () => JSON.stringify(payload) };
    });
    vi.stubGlobal("fetch", mocks.fetch);

    await expect(fleetApi.serverVersion()).resolves.toBe("0.2.2");
    expect(calls[0]).toMatchObject({ url: "/healthz", method: "GET" });
    expect(calls[0]?.headers["X-NexTerm-CSRF"]).toBeUndefined();

    await expect(fleetApi.serverVersion()).resolves.toBe("");
  });

  it("dispatches the session-expired event and normalizes the error on 401", async () => {
    mocks.fetch.mockResolvedValue({
      ok: false,
      status: 401,
      text: async () => JSON.stringify({ error: { code: "forbidden", message: "invalid or missing session" } }),
    });
    vi.stubGlobal("fetch", mocks.fetch);

    const expired = vi.fn();
    window.addEventListener(SESSION_EXPIRED_EVENT, expired);

    await expect(fleetApi.devices()).rejects.toMatchObject({
      code: "forbidden",
      message: "invalid or missing session",
      status: 401,
    });
    expect(expired).toHaveBeenCalledTimes(1);
    window.removeEventListener(SESSION_EXPIRED_EVENT, expired);
  });
});
