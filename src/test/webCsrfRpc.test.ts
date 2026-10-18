import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const RPC_CSRF_FAILURE = JSON.stringify({
  ok: false,
  error: { code: "forbidden", message: "CSRF 校验失败" },
});

interface CapturedCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body?: unknown;
}

function jsonResponse(status: number, body: string) {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: async () => body,
  };
}

function useWebTransport() {
  vi.stubGlobal("window", {
    __NEXTERM_TRANSPORT__: "web",
    location: { search: "" },
  });
}

function captureFetch(handler: (url: string) => { status: number; body: string }) {
  const calls: CapturedCall[] = [];
  const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
    const target = String(url);
    calls.push({
      url: target,
      method: init.method ?? "GET",
      headers: (init.headers ?? {}) as Record<string, string>,
      ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    const { status, body } = handler(target);
    return jsonResponse(status, body);
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

async function importWeb() {
  const commands = await import("../ipc/commands");
  const authApi = await import("../ipc/authApi");
  return { commands, authApi };
}

beforeEach(() => {
  vi.resetModules();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("web /rpc CSRF transport", () => {
  it("includes X-NexTerm-CSRF on /rpc requests with an account session", async () => {
    useWebTransport();
    const calls = captureFetch(() => ({ status: 200, body: JSON.stringify({ ok: true, data: [] }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).resolves.toEqual([]);

    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/rpc");
    expect(calls[0].method).toBe("POST");
    expect(calls[0].headers["X-NexTerm-CSRF"]).toBe("csrf-1");
  });

  it("does not send an RPC started before the browser login session changed", async () => {
    useWebTransport();
    const calls = captureFetch(() => ({ status: 200, body: JSON.stringify({ ok: true, data: [] }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-before");

    const pending = commands.assetApi.list();
    authApi.setCsrfToken("csrf-after");

    await expect(pending).rejects.toMatchObject({ code: "account_changed" });
    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(0);
  });

  it("without a session (loopback/auth=off): sends /rpc without a CSRF header after the shared /auth/me prefetch fails", async () => {
    useWebTransport();
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        // auth=off: accountGuard uniformly rejects account routes with 403
        return {
          status: 403,
          body: JSON.stringify({
            ok: false,
            error: { code: "forbidden", message: "Account features are unavailable in shared workspace mode (account login is disabled)" },
          }),
        };
      }
      return { status: 200, body: JSON.stringify({ ok: true, data: [] }) };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    await expect(commands.assetApi.list()).resolves.toEqual([]);
    await expect(commands.sessionApi.list()).resolves.toEqual([]);

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(2);
    expect(rpcCalls[0].headers["X-NexTerm-CSRF"]).toBeUndefined();
    // Prefetch happens only once, the failure result is remembered, later /rpc goes out directly without prefetching again
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
  });

  it("on cold-start token miss: concurrent /rpc calls share one /auth/me request, wait, then send with the new token and no 403s", async () => {
    useWebTransport();
    let resolveMe: (value: { status: number; body: string }) => void = () => {};
    const meGate = new Promise<{ status: number; body: string }>((resolve) => {
      resolveMe = resolve;
    });
    const calls: CapturedCall[] = [];
    const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
      const target = String(url);
      calls.push({
        url: target,
        method: init.method ?? "GET",
        headers: (init.headers ?? {}) as Record<string, string>,
        ...(init.body ? { body: JSON.parse(String(init.body)) } : {}),
      });
      const { status, body } = target.includes("/auth/me")
        ? await meGate
        : { status: 200, body: JSON.stringify({ ok: true, data: null }) };
      return jsonResponse(status, body);
    });
    vi.stubGlobal("fetch", fetchMock);
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    const pending = Promise.all([
      commands.assetApi.list(),
      commands.sessionApi.list(),
      commands.vaultApi.status(),
    ]);
    // Before me returns: /auth/me fired only once, all three /rpc are waiting, none sent yet
    await vi.waitFor(() => {
      expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    });
    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(0);

    resolveMe({
      status: 200,
      body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-new" }),
    });
    await pending;

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(3);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    for (const call of rpcCalls) {
      expect(call.headers["X-NexTerm-CSRF"]).toBe("csrf-new");
    }
    expect(authApi.getCsrfToken()).toBe("csrf-new");
  });

  it("when logged out: shared prefetch 401 still dispatches SESSION_EXPIRED, requests follow the existing 403 retry path and reject, and later calls do not hang", async () => {
    const dispatchEvent = vi.fn();
    vi.stubGlobal("window", {
      __NEXTERM_TRANSPORT__: "web",
      location: { search: "" },
      dispatchEvent,
    });
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 401,
          body: JSON.stringify({ ok: false, error: { code: "forbidden", message: "Session is invalid or missing" } }),
        };
      }
      return { status: 403, body: RPC_CSRF_FAILURE };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken(null);

    await expect(commands.assetApi.list()).rejects.toMatchObject({
      code: "forbidden",
      message: "Session is invalid or missing",
    });
    expect(dispatchEvent).toHaveBeenCalled();

    // Later calls are neither suspended nor re-prefetched: /rpc goes out bare and errors the same way through the 403 retry path
    await expect(commands.sessionApi.list()).rejects.toMatchObject({
      code: "forbidden",
      message: "Session is invalid or missing",
    });
    // /auth/me fired 3 times in total: 1 shared prefetch + 1 refresh per request via the 403 retry path
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(3);
    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(2);
  });

  it("does not replay an old RPC after /auth/me reveals a different login session", async () => {
    useWebTransport();
    let rpcAttempts = 0;
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 200,
          body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-2" }),
        };
      }
      rpcAttempts += 1;
      if (rpcAttempts === 1) return { status: 403, body: RPC_CSRF_FAILURE };
      return { status: 200, body: JSON.stringify({ ok: true, data: [{ id: "a-1" }] }) };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).rejects.toMatchObject({ code: "account_changed" });

    const rpcCalls = calls.filter((call) => call.url === "/rpc");
    expect(rpcCalls).toHaveLength(1);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
    expect(rpcCalls[0].headers["X-NexTerm-CSRF"]).toBe("csrf-1");
    expect(authApi.getCsrfToken()).toBe("csrf-2");
  });

  it("does not retry again when the retry still returns CSRF 403", async () => {
    useWebTransport();
    const calls = captureFetch((url) => {
      if (url.includes("/auth/me")) {
        return {
          status: 200,
          body: JSON.stringify({ user: { id: "u-1", username: "alice" }, csrf_token: "csrf-1" }),
        };
      }
      return { status: 403, body: RPC_CSRF_FAILURE };
    });
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.list()).rejects.toEqual({
      code: "forbidden",
      message: "CSRF 校验失败",
    });

    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(2);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(1);
  });

  it("does not retry or refresh the session for non-CSRF 403 responses", async () => {
    useWebTransport();
    const error = { code: "forbidden", message: "Permission denied" };
    const calls = captureFetch(() => ({ status: 403, body: JSON.stringify({ ok: false, error }) }));
    const { commands, authApi } = await importWeb();
    authApi.setCsrfToken("csrf-1");

    await expect(commands.assetApi.groupCreate("g")).rejects.toEqual(error);

    expect(calls.filter((call) => call.url === "/rpc")).toHaveLength(1);
    expect(calls.filter((call) => call.url.includes("/auth/me"))).toHaveLength(0);
  });
});
